package opensearch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
)

// sensitiveReason carries every identifier an OpenSearch error reason can
// contain.
const sensitiveReason = `[secret_field]: failed to parse field [secret_field] of type [long] ` +
	`in document with _id [42] index [secret_index] shard [3] node [internal-host-3] ` +
	`partition [topsecret-partition] alias [secret_alias] topsecret`

// TestNoAllowlistEntryLeaks walks the whole allowlist and asserts that no entry
// can leak the raw reason, index or document identifiers into the response.
func TestNoAllowlistEntryLeaks(t *testing.T) {
	discardLogger(t)
	codes := registry.Codes()
	if len(codes) == 0 {
		t.Fatal("registry is empty; the test would pass vacuously")
	}
	t.Logf("checking %d allowlist entries", len(codes))

	ctx := context.Background()
	for _, code := range codes {
		want, known := registry.Lookup(code)
		if !known {
			t.Fatalf("Codes() returned unknown code %q", code)
		}

		body, err := json.Marshal(map[string]any{
			"error":  map[string]string{"type": code, "reason": sensitiveReason},
			"status": 400,
		})
		if err != nil {
			t.Fatal(err)
		}

		got := TranslateResponse(ctx, body, 400, "t1")

		if got.Message != want.Message {
			t.Errorf("type %s: message changed\n got: %q\nwant: %q", code, got.Message, want.Message)
		}
		if got.Code != want.Code {
			t.Errorf("type %s: resolved to %q, want %q", code, got.Code, want.Code)
		}
		for _, marker := range []string{
			"secret_field", "secret_index", "secret_alias",
			"topsecret", "internal-host-3",
		} {
			if strings.Contains(got.Message, marker) {
				t.Errorf("type %s: leaked %q in %q", code, marker, got.Message)
			}
		}
	}
}

// The plain-string error shape must not leak either.
func TestPlainStringShapeDoesNotLeak(t *testing.T) {
	discardLogger(t)
	body, err := json.Marshal(map[string]any{
		"error":  sensitiveReason,
		"status": 405,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := TranslateResponse(context.Background(), body, 405, "t1")
	if got.Code != dberr.CodeInternal || got.Message != dberr.DefaultMessage {
		t.Fatalf("expected default-deny, got %+v", got)
	}
	for _, marker := range []string{"secret", "topsecret", "internal-host-3"} {
		if strings.Contains(got.Message, marker) {
			t.Fatalf("leaked %q: %+v", marker, got)
		}
	}
}
