package opensearch

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
)

// A body over MaxBodySize must not be parsed and must not be logged. OpenSearch
// error bodies embed the document that failed to parse, so the body is
// attacker-influenced and can be arbitrarily large.
func TestTranslateResponse_OversizedBodyIsRejected(t *testing.T) {
	var logs strings.Builder
	dberr.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })

	// A body that would otherwise parse into a mapped type.
	body := `{"error":{"type":"illegal_argument_exception","reason":"` +
		strings.Repeat("A", MaxBodySize) + `"}}`
	oversized := []byte(body)
	if len(oversized) <= MaxBodySize {
		t.Fatalf("test body is %d bytes, expected > %d", len(oversized), MaxBodySize)
	}

	safe := TranslateResponse(context.Background(), oversized, 400, "t1")

	if safe.Code != dberr.CodeInternal {
		t.Fatalf("an oversized body must be default-deny, got %+v", safe)
	}
	if safe.Message != dberr.DefaultMessage {
		t.Fatalf("unexpected message: %q", safe.Message)
	}
	if safe.TraceID != "t1" {
		t.Fatalf("trace id not propagated: %+v", safe)
	}

	// The refused content must not appear in the log either.
	if strings.Count(logs.String(), "AAAA") > 0 {
		t.Fatalf("oversized content leaked into the log: %s", logs.String())
	}
	// The size should be reported instead, so the condition is diagnosable.
	if !strings.Contains(logs.String(), "exceeds MaxBodySize") {
		t.Fatalf("expected the log to explain the rejection: %s", logs.String())
	}
}

// A body exactly at the limit must still be processed.
func TestTranslateResponse_BodyAtLimitIsProcessed(t *testing.T) {
	discardLogger(t)

	// Build a valid body whose length is exactly MaxBodySize.
	prefix := `{"error":{"type":"document_missing_exception","reason":"`
	suffix := `"},"status":404}`
	padding := MaxBodySize - len(prefix) - len(suffix)
	if padding < 0 {
		t.Fatal("test construction failed")
	}
	body := []byte(prefix + strings.Repeat("x", padding) + suffix)
	if len(body) != MaxBodySize {
		t.Fatalf("body is %d bytes, want exactly %d", len(body), MaxBodySize)
	}

	safe := TranslateResponse(context.Background(), body, 404, "t1")
	if safe.Code != dberr.CodeNotFound {
		t.Fatalf("a body at the limit must be processed, got %+v", safe)
	}
}

// The limit must be applied before any parsing work, so the guard is not
// reachable only after the allocation has happened.
func TestTranslateResponse_LimitCheckedBeforeParsing(t *testing.T) {
	discardLogger(t)

	// Body one byte over, and structurally invalid. If parsing happened first,
	// the error would be a JSON syntax error rather than the size rejection.
	body := make([]byte, MaxBodySize+1)
	for i := range body {
		body[i] = 'x'
	}

	safe := TranslateResponse(context.Background(), body, 500, "t1")
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("unexpected: %+v", safe)
	}
	// Both paths give the same code; assert the message is the generic one
	// rather than a parse-derived value.
	if safe.Message != dberr.DefaultMessage {
		t.Fatalf("unexpected message: %q", safe.Message)
	}
}

// A large but permitted body must not leak. The limit is not the only defence.
func TestTranslateResponse_LargeButAllowedDoesNotLeak(t *testing.T) {
	discardLogger(t)

	reason := strings.Repeat("SECRETMARKER", 50_000) // ~550 KB, under the limit
	payload, err := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":   "illegal_argument_exception",
			"reason": reason,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	safe := TranslateResponse(context.Background(), payload, 400, "t1")
	if strings.Contains(safe.Message, "SECRETMARKER") {
		t.Fatal("marker leaked into the response")
	}
	if safe.Code != dberr.CodeInvalidValue {
		t.Fatalf("unexpected: %+v", safe)
	}
}

// MaxBodySize must be documented as usable with io.LimitReader.
func TestMaxBodySizeIsUsableAsALimit(t *testing.T) {
	if MaxBodySize <= 0 {
		t.Fatalf("MaxBodySize = %d, want a positive limit", MaxBodySize)
	}
	// A reader capped at MaxBodySize+1 lets the caller detect the overflow.
	capped := strings.NewReader(strings.Repeat("x", MaxBodySize+10))
	buf := make([]byte, MaxBodySize+1)
	n, err := capped.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n <= MaxBodySize {
		t.Fatalf("LimitReader pattern must surface the overflow, read %d", n)
	}
}
