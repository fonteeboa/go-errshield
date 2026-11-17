// Package opensearch translates failed OpenSearch HTTP responses into safe,
// client-facing messages.
//
// Unlike the other dberr sources, this one takes no Go error type: the
// opensearch-go client does not return a typed error for an API-level failure,
// it returns an *Response whose body you read and parse yourself. This package
// takes the raw response bytes instead, which also decouples it from whichever
// client version (v2/v3/v4) and API surface you use — the server's wire format
// is stable across all of them.
//
// This package has no external dependencies.
//
//	res, err := client.Index(indexName, body)
//	if err != nil {
//	    // transport/network failure — not what TranslateResponse handles
//	    return dberr.Message(err, traceID)
//	}
//	defer res.Body.Close()
//	if res.IsError() {
//	    raw, _ := io.ReadAll(res.Body)
//	    safe := opensearch.TranslateResponse(ctx, raw, res.StatusCode, traceID)
//	}
package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/fonteeboa/go-errshield"
)

const source = "opensearch"

// registry holds the allowlist of Elasticsearch/OpenSearch exception types
// considered safe to describe externally.
//
// There is no closed official list of `type` values; the entries here are the
// exception types most commonly observed on the REST API, which are stable
// across releases. Anything not listed falls back to dberr.CodeInternal.
var registry = dberr.NewRegistry(source, map[string]dberr.SafeError{
	// Optimistic concurrency control
	"version_conflict_engine_exception": {Code: dberr.CodeConcurrentUpdate, Message: "please try again"},
	"snapshot_in_progress_exception":    {Code: dberr.CodeConcurrentUpdate, Message: "please try again"},

	// Conflicts and missing documents
	"resource_already_exists_exception": {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},
	"document_already_exists_exception": {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},
	"document_missing_exception":        {Code: dberr.CodeNotFound, Message: "resource not found"},
	"index_not_found_exception":         {Code: dberr.CodeNotFound, Message: "resource not found"},
	"index_closed_exception":            {Code: dberr.CodeNotFound, Message: "resource not found"},

	// Malformed requests
	"mapper_parsing_exception":         {Code: dberr.CodeInvalidInput, Message: "invalid input format"},
	"parsing_exception":                {Code: dberr.CodeInvalidInput, Message: "invalid input format"},
	"data_exception":                   {Code: dberr.CodeInvalidInput, Message: "invalid value"},
	"no_mapping_definition_exception":  {Code: dberr.CodeInvalidInput, Message: "invalid input format"},
	"strict_dynamic_mapping_exception": {Code: dberr.CodeInvalidInput, Message: "invalid input format"},

	// Rejected values
	"illegal_argument_exception":   {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},
	"validation_exception":         {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},
	"invalid_index_name_exception": {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},

	// Saturation and timeouts
	"es_rejected_execution_exception":         {Code: dberr.CodeTooManyConnections, Message: "service temporarily unavailable"},
	"circuit_breaking_exception":              {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"process_cluster_event_timeout_exception": {Code: dberr.CodeTimeout, Message: "operation timed out"},
	"search_phase_execution_exception":        {Code: dberr.CodeTimeout, Message: "operation timed out"},

	// Cluster availability
	"cluster_block_exception":                     {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"unavailable_shards_exception":                {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"no_shard_available_action_exception":         {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"index_primary_shard_not_allocated_exception": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},

	// Access control
	"security_exception":      {Code: dberr.CodeAuthFailed, Message: "authentication failed"},
	"token_expired_exception": {Code: dberr.CodeAuthFailed, Message: "authentication failed"},

	// Routing mistakes — safe to describe, and the client can act on them
	"incorrect_http_method_exception": {Code: dberr.CodeMethodNotAllowed, Message: "method not allowed for this resource"},
})

// errorBody captures the "error" field before deciding its shape.
type errorBody struct {
	Error json.RawMessage `json:"error"`
}

// structuredError is the shape most endpoints send:
// {"error":{"type":"...","reason":"..."},"status":N}
type structuredError struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// responseError adapts a raw response body to the error interface so it can be
// handed to the shared logging path. It never reaches a response body: only its
// string form is logged.
type responseError struct{ body []byte }

func (e responseError) Error() string { return string(e.body) }

// Registry returns this source's registry, so you can register log redaction
// markers during setup:
//
//	opensearch.Registry().Redact("topsecret")
func Registry() *dberr.Registry { return registry }

// MaxBodySize bounds how much of a response body TranslateResponse will parse
// and log. It exists because this is the only code path in dberr that handles
// attacker-influenced bytes: OpenSearch error bodies carry the document that
// failed to parse, and a document can be arbitrarily large.
//
// A body over this limit is not parsed and not logged; the result is the
// default-deny fallback. 1 MiB is far larger than any legitimate OpenSearch
// error and small enough that a hostile cluster cannot make your process
// allocate without bound.
//
// Read the body with a limit so you never buffer more than this in the first
// place:
//
//	raw, _ := io.ReadAll(io.LimitReader(res.Body, opensearch.MaxBodySize+1))
const MaxBodySize = 1 << 20 // 1 MiB

// DefaultMinStatusCode is the default minimum valid HTTP status code for
// OpenSearch responses.
//
// Change it with SetMinStatusCode.
const DefaultMinStatusCode = 100

// DefaultMaxStatusCode is the default maximum valid HTTP status code for
// OpenSearch responses.
//
// Change it with SetMaxStatusCode.
const DefaultMaxStatusCode = 599

// minStatusCode and maxStatusCode hold the accepted HTTP status range. They
// are atomic so reading them from a request goroutine is safe even if another
// goroutine reconfigures them.
var (
	minStatusCode atomic.Int64
	maxStatusCode atomic.Int64
)

func init() {
	minStatusCode.Store(DefaultMinStatusCode)
	maxStatusCode.Store(DefaultMaxStatusCode)
}

// MinStatusCode returns the current minimum accepted HTTP status code.
func MinStatusCode() int { return int(minStatusCode.Load()) }

// MaxStatusCode returns the current maximum accepted HTTP status code.
func MaxStatusCode() int { return int(maxStatusCode.Load()) }

// SetMinStatusCode sets the minimum accepted HTTP status code.
//
// A status code below it is replaced with 500 in the log record.
//
//	opensearch.SetMinStatusCode(200)
//
// Safe to call concurrently with TranslateResponse.
func SetMinStatusCode(n int) {
	if n < 0 {
		n = 0
	}
	minStatusCode.Store(int64(n))
}

// SetMaxStatusCode sets the maximum accepted HTTP status code.
//
// A status code above it is replaced with 500 in the log record.
//
//	opensearch.SetMaxStatusCode(499)
//
// Safe to call concurrently with TranslateResponse.
func SetMaxStatusCode(n int) {
	if n < 0 {
		n = 0
	}
	maxStatusCode.Store(int64(n))
}

// TranslateResponse parses the raw JSON body of a failed OpenSearch response
// (HTTP status >= 300, i.e. where resp.IsError() returns true) and returns a
// SafeError.
//
// OpenSearch's format is not fully consistent: most endpoints send a structured
// {"type","reason"} object, but a few (e.g. 405 Method Not Allowed) send a
// plain string instead. Both are handled; the plain-string shape has no
// structured `type` to map and always falls back to dberr.CodeInternal.
//
// A body that is not valid JSON, is over MaxBodySize, or carries an exception
// type outside the allowlist, also falls back to dberr.CodeInternal. In every
// case the raw reason and index/document names are logged, never returned.
func TranslateResponse(ctx context.Context, body []byte, statusCode int, traceID string) *dberr.SafeError {
	statusCode = sanitizeStatusCode(statusCode)
	if len(body) > MaxBodySize {
		return tooLarge(ctx, statusCode, len(body), traceID)
	}

	var parsed errorBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return dberr.Unrecognized(ctx, err, traceID, "status", statusCode)
	}

	var structured structuredError
	if err := json.Unmarshal(parsed.Error, &structured); err == nil && structured.Type != "" {
		// Log the whole body, not just the reason: the body also carries the
		// index, shard and document identifiers needed to debug.
		return registry.Resolve(ctx, structured.Type, responseError{body: body}, traceID)
	}

	// Plain-string shape: log it and fall back to the default-deny message.
	var plain string
	if err := json.Unmarshal(parsed.Error, &plain); err != nil {
		return dberr.Unrecognized(ctx, err, traceID, "status", statusCode, "body", body)
	}
	return dberr.Unrecognized(ctx, errors.New(plain), traceID, "status", statusCode, "body", body)
}

// sanitizeStatusCode clamps the HTTP status code to the accepted range.
// Invalid codes are replaced with 500.
func sanitizeStatusCode(statusCode int) int {
	if statusCode < MinStatusCode() || statusCode > MaxStatusCode() {
		return 500
	}
	return statusCode
}

// tooLarge handles an oversized body without parsing or logging it. Logging
// the size rather than the content is deliberate: the content is exactly what
// the size limit exists to avoid handling.
func tooLarge(ctx context.Context, statusCode, size int, traceID string) *dberr.SafeError {
	dberr.LogUnrecognized(ctx,
		fmt.Errorf("response body of %d bytes exceeds MaxBodySize (%d)", size, MaxBodySize),
		traceID, "status", statusCode)
	return &dberr.SafeError{
		Code:    dberr.CodeInternal,
		Message: dberr.DefaultMessage,
		TraceID: traceID,
	}
}

// MessageResponse is a convenience wrapper for the common case: you only want
// the ready-to-return string.
//
//	c.JSON(http.StatusConflict, gin.H{"error": opensearch.MessageResponse(ctx, raw, res.StatusCode, traceID)})
func MessageResponse(ctx context.Context, body []byte, statusCode int, traceID string) string {
	// TranslateResponse always returns a non-nil SafeError, so there is no nil
	// case to handle here — unlike the driver packages, there is no nil error
	// to map.
	return TranslateResponse(ctx, body, statusCode, traceID).Message
}
