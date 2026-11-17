// Package dberr is the shared core of the dberr library: the SafeError type
// returned to clients, the vocabulary of stable client-facing codes, their
// canonical HTTP statuses, and the default-deny resolution logic used by every
// source package.
//
// The contract is one line: a database error becomes a stable Code plus a
// generic message for the client, while the original error — including your own
// wrapping context — goes to the log. Nothing from the raw error reaches the
// response.
//
// # Split by source
//
// Each database source lives in its own subpackage so that importing one
// driver does not pull in the others:
//
//	dberr/postgres   pgx v5 and lib/pq        (keyed by SQLSTATE, e.g. "23505")
//	dberr/mysql      go-sql-driver/mysql      (keyed by error number, e.g. "1062")
//	dberr/mongo      mongo-driver             (keyed by error code, e.g. "11000")
//	dberr/opensearch raw HTTP response body   (keyed by exception type string)
//	dberr/sqlerr     database/sql sentinels   (sql.ErrNoRows, context errors)
//
// This package itself imports no database driver. Import
// github.com/fonteeboa/go-errshield/postgres only if you use Postgres, and nothing
// from this core needs to change when you add or remove a source.
//
// dberr/sqlerr is worth importing regardless of which driver you use: it
// handles sql.ErrNoRows and the context sentinels, which are not driver errors
// and therefore cannot be matched by any driver extractor.
//
// # Design principle: default-deny
//
// Each source subpackage owns an allowlist of codes, and only codes explicitly
// present in that allowlist get a specific message. Anything else — an unmapped
// code, or an error that does not match the source's driver error type at all —
// falls back to CodeInternal. Nothing about the raw error (message,
// constraint/table/collection/index/field name) is ever included in the
// returned SafeError.
//
// A SafeError deliberately does not unwrap to the error it was translated
// from: exposing the original through the error chain would let any caller walk
// back to the driver's message and render it. The original is in the log,
// correlated by TraceID.
//
// # Classification
//
// Every Code has a sentinel, so callers branch with errors.Is rather than
// comparing strings:
//
//	if errors.Is(safe, dberr.ErrDuplicateItem) {
//	    // 409
//	}
//
// # Example
//
//	safe := postgres.Translate(ctx, err, traceID)
//	if safe != nil {
//	    c.JSON(safe.HTTPStatus(), safe)
//	}
package dberr

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// SafeError is safe to marshal directly into an HTTP response body. It
// implements error so it can be returned like any other error value.
type SafeError struct {
	Code    string `json:"code"`
	Message string `json:"error"`
	TraceID string `json:"trace_id,omitempty"`
}

func (e *SafeError) Error() string { return e.Message }

// Stable, client-facing codes. These are independent of the source that
// produced the underlying error: a duplicate key from Postgres, MySQL, Mongo
// and OpenSearch all translate to CodeDuplicateItem, so route-level handling
// can switch on Code without knowing which database was involved.
const (
	CodeInternal             = "INTERNAL_ERROR"
	CodeDuplicateItem        = "DUPLICATE_ITEM"
	CodeReferenceNotFound    = "REFERENCE_NOT_FOUND"
	CodeReferenceInUse       = "REFERENCE_IN_USE"
	CodeMissingRequiredField = "MISSING_REQUIRED_FIELD"
	CodeInvalidValue         = "INVALID_VALUE"
	CodeValueTooLong         = "VALUE_TOO_LONG"
	CodeValueOutOfRange      = "VALUE_OUT_OF_RANGE"
	CodeInvalidInput         = "INVALID_INPUT"
	CodeConcurrentUpdate     = "CONCURRENT_UPDATE"
	CodeDeadlock             = "DEADLOCK"
	CodeLockTimeout          = "LOCK_TIMEOUT"
	CodeTimeout              = "TIMEOUT"
	CodeTooManyConnections   = "TOO_MANY_CONNECTIONS"
	CodeUnavailable          = "UNAVAILABLE"
	CodeAuthFailed           = "AUTH_FAILED"
	CodePermissionDenied     = "PERMISSION_DENIED"
	CodeMethodNotAllowed     = "METHOD_NOT_ALLOWED"
	CodeNotFound             = "NOT_FOUND"

	// CodeColumnNotFound means the query referenced a column that does not
	// exist. That is either a bad field name from the client or a bug in a
	// hardcoded query, and only the application knows which — so it gets its
	// own code and a 400 default, and routes that never interpolate client
	// input into SQL should remap it to 500.
	CodeColumnNotFound = "COLUMN_NOT_FOUND"

	// CodeSchemaError means the database rejected the query itself: unknown
	// table, unknown function, malformed SQL. This is always a defect in the
	// deployed code, so it is 500 and deliberately distinct from
	// CodeNotFound. Reporting it as "resource not found" would tell a client
	// that their request failed on missing data when the service is in fact
	// broken.
	CodeSchemaError = "SCHEMA_ERROR"
)

// DefaultMessage is the message behind CodeInternal. It is intentionally
// vague: it must not hint at the cause of the failure.
const DefaultMessage = "internal server error"

// DefaultMaxTraceIDLength is the default maximum length for a traceID.
//
// A traceID usually comes from a request header, so it is attacker-influenced.
// Without a limit, a single request can put megabytes into the log record and
// into the response body.
//
// Change it with SetMaxTraceIDLength, or per-Instance with
// WithMaxTraceIDLength.
const DefaultMaxTraceIDLength = 256

// DefaultMaxRedactMarkers is the default maximum number of redaction markers
// a Registry keeps.
//
// Redact is meant to be called during setup. The cap exists so that a
// misconfigured program that calls it per request cannot grow the marker list
// without bound.
//
// Change it with SetMaxRedactMarkers, or per-Instance with
// WithMaxRedactMarkers.
const DefaultMaxRedactMarkers = 1000

// maxTraceIDLength and maxRedactMarkers hold the process-wide limits. They are
// atomic so that reading them from a request goroutine is safe even if
// another goroutine reconfigures them.
var (
	maxTraceIDLength atomic.Int64
	maxRedactMarkers atomic.Int64
)

func init() {
	maxTraceIDLength.Store(DefaultMaxTraceIDLength)
	maxRedactMarkers.Store(DefaultMaxRedactMarkers)
}

// MaxTraceIDLength returns the current maximum traceID length.
func MaxTraceIDLength() int { return int(maxTraceIDLength.Load()) }

// SetMaxTraceIDLength sets the maximum traceID length for the whole process.
//
// A value of 0 or less disables truncation entirely — only do that if the
// traceID source is trusted, because an unbounded traceID is a memory
// exhaustion vector.
//
//	dberr.SetMaxTraceIDLength(128)
//
// Safe to call concurrently with Translate.
func SetMaxTraceIDLength(n int) {
	if n < 0 {
		n = 0
	}
	maxTraceIDLength.Store(int64(n))
}

// MaxRedactMarkers returns the current maximum number of redaction markers
// per Registry.
func MaxRedactMarkers() int { return int(maxRedactMarkers.Load()) }

// SetMaxRedactMarkers sets the maximum number of redaction markers a
// Registry keeps, for the whole process.
//
// A value of 0 or less disables the cap.
//
//	dberr.SetMaxRedactMarkers(500)
//
// Safe to call concurrently with Translate.
func SetMaxRedactMarkers(n int) {
	if n < 0 {
		n = 0
	}
	maxRedactMarkers.Store(int64(n))
}

// allCodes is the canonical set of client-facing codes. The const block above
// cannot be enumerated directly, so it is mirrored here — and
// TestAllCodesCoversVocabulary fails if the two drift apart.
var allCodes = []string{
	CodeInternal,
	CodeDuplicateItem,
	CodeReferenceNotFound,
	CodeReferenceInUse,
	CodeMissingRequiredField,
	CodeInvalidValue,
	CodeValueTooLong,
	CodeValueOutOfRange,
	CodeInvalidInput,
	CodeConcurrentUpdate,
	CodeDeadlock,
	CodeLockTimeout,
	CodeTimeout,
	CodeTooManyConnections,
	CodeUnavailable,
	CodeAuthFailed,
	CodePermissionDenied,
	CodeMethodNotAllowed,
	CodeNotFound,
	CodeColumnNotFound,
	CodeSchemaError,
}

// AllCodes returns every client-facing code in the library. It exists so tests
// and tooling can iterate the whole vocabulary — for instance to assert that
// every code has an HTTP status.
func AllCodes() []string {
	out := make([]string, len(allCodes))
	copy(out, allCodes)
	return out
}

// logger is stored atomically so SetLogger is safe to call while other
// goroutines are translating errors.
//
// Deprecated: this mirrors Default's logger. Use an Instance for per-policy
// configuration.
var logger atomic.Pointer[slog.Logger]

func init() { logger.Store(slog.Default()) }

// SetLogger redirects dberr's internal logging. Call it during process setup,
// before serving traffic. A nil argument restores the default slog logger.
//
// dberr logs the raw, untranslated error — including constraint, collection
// and index names — so point this at a sink that is not reachable from the
// client side.
// logger is retained so the package-level helpers stay cheap. It mirrors
// Default's logger, which SetLogger updates.
//
// Deprecated in favour of Instance: a program needing more than one logger
// should create its own Instances rather than mutate this global.
func SetLogger(l *slog.Logger) {
	Default.SetLogger(l)
}

func log(ctx context.Context, level slog.Level, msg string, args ...any) {
	Default.log(ctx, level, msg, args...)
}

// fallback builds the default-deny SafeError.
func fallback(traceID string) *SafeError {
	return &SafeError{Code: CodeInternal, Message: DefaultMessage, TraceID: sanitizeTraceID(traceID)}
}

// sanitizeTraceID truncates the traceID to the process-wide limit.
//
// A limit of 0 or less disables truncation.
func sanitizeTraceID(traceID string) string {
	limit := MaxTraceIDLength()
	if limit > 0 && len(traceID) > limit {
		return traceID[:limit]
	}
	return traceID
}

// Unrecognized returns the default-deny fallback for an error that did not
// match the source's driver error type at all, logging the raw error first.
//
// Source subpackages call this when their extractor finds nothing. It is
// exported so that code combining several sources (try postgres.Translate,
// then mongo.Translate, ...) can use the same fallback. Extra attrs are
// appended to the log record, e.g. the HTTP status in the OpenSearch path.
func Unrecognized(ctx context.Context, err error, traceID string, attrs ...any) *SafeError {
	LogUnrecognized(ctx, err, traceID, attrs...)
	return fallback(traceID)
}

// LogUnrecognized writes the ERROR-level record for an unrecognized error and
// notifies the unmapped hook, without building the fallback SafeError.
//
// Source packages use it when they need to control the fallback itself — the
// OpenSearch path, for instance, reports a body over MaxBodySize this way so it
// can log the size instead of the content it is refusing to handle.
func LogUnrecognized(ctx context.Context, err error, traceID string, attrs ...any) {
	traceID = sanitizeTraceID(traceID)
	base := []any{
		"trace_id", traceID,
		"error", errorString(err),
	}
	log(ctx, slog.LevelError, "dberr: unrecognized error type", append(base, attrs...)...)

	// An unrecognized error type is a different failure from an unmapped code —
	// it means no extractor matched at all — but it is the same actionable
	// signal: dberr was not taught about something a real database produces.
	// It is reported with a sentinel Source so a hook can tell the two apart.
	emitUnmapped(ctx, "unknown", "", traceID, err)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A Registry holds one source's allowlist: the set of that source's own codes
// that are considered safe to describe to a client, and the message each one
// maps to.
//
// Every source uses a different code space, so registries are never merged
// and never share keys. A Registry is created once at package init and is
// read-only afterwards, making it safe for concurrent use.
type Registry struct {
	source string
	codes  map[string]SafeError

	// inst is where this registry logs and reports unmapped codes. It is never
	// nil: NewRegistry substitutes Default.
	inst *Instance

	// redact matches substrings to strip from the logged error chain. Empty by
	// default; extended at setup via Redact, which is safe to call concurrently.
	redactMu sync.RWMutex
	redact   []string

	// limitMu guards maxMarkersOverride and maxTraceIDOverride.
	limitMu sync.RWMutex

	// maxMarkersOverride caps this registry's marker list. A negative value
	// means "use the process-wide cap".
	maxMarkersOverride int

	// maxTraceIDOverride caps the traceID length for this registry. A negative
	// value means "use the process-wide cap".
	maxTraceIDOverride int
}

// NewRegistryWith is NewRegistry bound to a specific Instance, so the registry's
// log records and unmapped reports go to that instance rather than to Default.
//
// It is the building block for per-tenant or per-service policy:
//
//	tenantA := dberr.NewRegistry("postgres", codes, dberr.New(dberr.WithUnmappedHook(hookA)))
func NewRegistryWith(inst *Instance, source string, codes map[string]SafeError) *Registry {
	if inst == nil {
		inst = Default
	}
	copied := make(map[string]SafeError, len(codes))
	for code, safe := range codes {
		safe.TraceID = ""
		copied[code] = safe
	}
	return &Registry{
		source:             source,
		codes:              copied,
		inst:               inst,
		maxMarkersOverride: -1,
		maxTraceIDOverride: -1,
	}
}

// SetMaxMarkers caps how many redaction markers this registry keeps.
//
// A value of 0 or less disables the cap. A negative value restores the
// process-wide default (dberr.SetMaxRedactMarkers).
//
//	postgres.Registry().SetMaxMarkers(50)
//
// Safe to call concurrently with Translate.
func (r *Registry) SetMaxMarkers(n int) {
	r.limitMu.Lock()
	defer r.limitMu.Unlock()
	r.maxMarkersOverride = n
}

// SetMaxTraceIDLength caps the traceID length for this registry.
//
// A value of 0 or less disables truncation. A negative value restores the
// process-wide default (dberr.SetMaxTraceIDLength).
//
//	postgres.Registry().SetMaxTraceIDLength(64)
//
// Safe to call concurrently with Translate.
func (r *Registry) SetMaxTraceIDLength(n int) {
	r.limitMu.Lock()
	defer r.limitMu.Unlock()
	r.maxTraceIDOverride = n
}

// traceIDLimit returns the traceID cap that applies to this registry: the
// registry's own limit when set, otherwise the process-wide one.
func (r *Registry) traceIDLimit() int {
	r.limitMu.RLock()
	limit := r.maxTraceIDOverride
	r.limitMu.RUnlock()

	if limit >= 0 {
		return limit
	}
	return MaxTraceIDLength()
}

// sanitizeTraceID truncates the traceID to this registry's limit.
//
// A limit of 0 or less disables truncation.
func (r *Registry) sanitizeTraceID(traceID string) string {
	limit := r.traceIDLimit()
	if limit > 0 && len(traceID) > limit {
		return traceID[:limit]
	}
	return traceID
}

// NewRegistry returns a Registry for the named source. The codes map is
// copied, so the caller may reuse or mutate it afterwards.
//
// Only add entries you have consciously decided are safe to describe
// externally; everything omitted falls back to CodeInternal.
func NewRegistry(source string, codes map[string]SafeError) *Registry {
	return NewRegistryWith(nil, source, codes)
}

// Source returns the registry's source name, as used in log records.
func (r *Registry) Source() string { return r.source }

// Lookup returns the SafeError registered for one of the source's codes,
// without logging anything and without applying the default-deny fallback. The
// boolean reports whether the code is in the allowlist.
//
// Use it when you need to inspect codes before deciding what to return — for
// instance when one error carries several candidate codes and you must pick the
// most specific one.
func (r *Registry) Lookup(code string) (SafeError, bool) {
	safe, ok := r.codes[code]
	return safe, ok
}

// Redact adds substrings to strip from this registry's log records, replacing
// each with "***". Empty markers are ignored.
//
// The reason this exists: dberr logs the full error chain so you can debug, but
// your own wrapping text is part of that chain, and that text routinely carries
// the very identifiers dberr exists to keep out of responses — a repository
// that writes
//
//	fmt.Errorf("repo.InsertUser(email=%s): %w", email, err)
//
// puts the user's email into the log for every failure. That is a deliberate
// choice you may be happy with, or it may be exactly what you must not log. If
// it is the latter:
//
//	postgres.Registry().Redact("admin@example.com")
//
// The marker must match the sensitive value, not the label: "email=" would
// remove the label and leave the address intact.
//
// Redaction is substring-based, not structural. It cannot know that an email
// address is sensitive while a constraint name is not, so choose markers that
// only appear in your own wrapping text, and prefer specific values — a
// one-character marker will redact that character everywhere in the log.
// Values logged by the driver itself (constraint, table and column names) are
// not redacted: those are what you need to debug, and they never reach a
// response.
//
// Redact is safe to call concurrently with Translate, so calling it from a
// request handler will not race. Calling it per request is still a bad idea:
// the marker list grows without bound.
func (r *Registry) Redact(markers ...string) {
	r.redactMu.Lock()
	defer r.redactMu.Unlock()

	limit := r.maxMarkers()
	for _, m := range markers {
		if m == "" {
			continue
		}
		if limit > 0 && len(r.redact) >= limit {
			return
		}
		r.redact = append(r.redact, m)
	}
}

// maxMarkers returns the marker cap that applies to this registry: the
// registry's own limit when set, otherwise the process-wide one.
func (r *Registry) maxMarkers() int {
	r.limitMu.RLock()
	limit := r.maxMarkersOverride
	r.limitMu.RUnlock()

	if limit >= 0 {
		return limit
	}
	return MaxRedactMarkers()
}

// Codes returns every code in the allowlist, in unspecified order. It exists so
// tests can iterate the whole allowlist — see the leak-injection tests, which
// assert that no entry can leak the raw error into the response.
func (r *Registry) Codes() []string {
	codes := make([]string, 0, len(r.codes))
	for code := range r.codes {
		codes = append(codes, code)
	}
	return codes
}

// Resolve maps one of the source's raw codes to a SafeError, logging the
// original error before it is discarded. Unknown codes fall back to
// CodeInternal at Error level, which is the signal that the allowlist is
// missing an entry.
//
// code is the unsafe value produced by a source's driver, and err is the
// original error as the caller received it. Both appear in the log record only.
// Passing err rather than just the driver's own message keeps the caller's
// wrapping context — "repo.InsertUser: ..." — which is usually what tells you
// which call site failed.
//
// Redaction markers registered with Redact are applied to the logged chain.
func (r *Registry) Resolve(ctx context.Context, code string, err error, traceID string) *SafeError {
	traceID = r.sanitizeTraceID(traceID)
	mapped, known := r.codes[code]

	level := slog.LevelWarn
	if !known {
		level = slog.LevelError
	}
	r.inst.log(ctx, level, "dberr: db error translated",
		"trace_id", traceID,
		"source", r.source,
		"code", code,
		"error", r.scrub(err),
		"known", known,
	)

	if !known {
		r.redactMu.RLock()
		markers := make([]string, len(r.redact))
		copy(markers, r.redact)
		r.redactMu.RUnlock()

		r.inst.emitUnmappedWithMarkers(ctx, r.source, code, traceID, err, markers)
		return fallback(traceID)
	}

	mapped.TraceID = traceID
	return &mapped
}

// scrub renders err for logging, replacing each registered marker with "***".
func (r *Registry) scrub(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()

	r.redactMu.RLock()
	markers := r.redact
	r.redactMu.RUnlock()

	if len(markers) == 0 {
		return raw
	}
	for _, marker := range markers {
		raw = strings.ReplaceAll(raw, marker, "***")
	}
	return raw
}
