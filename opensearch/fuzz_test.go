package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
)

// knownCodes is the set of codes TranslateResponse is allowed to return. It is
// derived from the registry so adding an allowlist entry does not break the
// fuzz invariant.
func knownCodes() map[string]bool {
	codes := map[string]bool{dberr.CodeInternal: true}
	for _, code := range registry.Codes() {
		safe, ok := registry.Lookup(code)
		if ok {
			codes[safe.Code] = true
		}
	}
	return codes
}

// sensitiveMarkers are substrings that must never appear in a SafeError. The
// fuzzer injects them into the reason field of every generated body.
var sensitiveMarkers = []string{
	"secret_index",
	"secret_field",
	"topsecret",
	"internal-host",
	"10.0.0.5",
	"app.users",
	"/var/lib/private",
}

// FuzzTranslateResponse is the security-relevant test in this package: it
// parses attacker-controlled bytes and must never leak them, and must never
// invent a Code outside the allowlist.
func FuzzTranslateResponse(f *testing.F) {
	seeds := []string{
		`{"error":{"type":"version_conflict_engine_exception","reason":"[1]: version conflict"},"status":409}`,
		`{"error":{"type":"document_missing_exception","reason":"[secret_field] missing"},"status":404}`,
		`{"error":"Incorrect HTTP method for uri [/_doc]","status":405}`,
		`{"error":{"type":"unknown_exception","reason":"secret_index topsecret"},"status":500}`,
		`{"error":{"type":"","reason":"empty type"},"status":500}`,
		`{"error":null,"status":500}`,
		`{"error":{}}`,
		`{"error":123}`,
		`{"error":[1,2,3]}`,
		`{"error":{"type":null}}`,
		`{}`,
		`not json`,
		``,
		`{"error":{"type":"illegal_argument_exception","reason":"failed to parse field [secret_field] in document with _id [42]"},"status":400}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s), 500)
	}

	known := knownCodes()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, body []byte, status int) {
		safe := TranslateResponse(ctx, body, status, "fuzz")

		if safe == nil {
			t.Fatal("TranslateResponse must never return nil for non-nil input")
		}
		if !known[safe.Code] {
			t.Fatalf("code %q is outside the allowlist (body %q)", safe.Code, body)
		}
		if safe.TraceID != "fuzz" {
			t.Fatalf("trace id not propagated: %+v", safe)
		}
		if safe.Code == dberr.CodeInternal && safe.Message != dberr.DefaultMessage {
			t.Fatalf("INTERNAL_ERROR must carry the default message, got %q", safe.Message)
		}
		for _, marker := range sensitiveMarkers {
			if strings.Contains(safe.Message, marker) {
				t.Fatalf("leaked %q in message %q (body %q)", marker, safe.Message, body)
			}
		}
		// The response body must marshal cleanly.
		if _, err := json.Marshal(safe); err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
	})
}

// FuzzRegistryResolve covers the core resolution path the same way: an
// arbitrary code must never produce a Code outside the known set.
func FuzzRegistryResolve(f *testing.F) {
	seeds := []string{"23505", "1062", "11000", "", "99999", "\x00", "../../etc/passwd", "version_conflict_engine_exception"}
	for _, s := range seeds {
		f.Add(s)
	}

	known := knownCodes()
	r := dberr.NewRegistry("fuzzsrc", map[string]dberr.SafeError{
		"23505": {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},
	})
	ctx := context.Background()
	raw := errors.New("raw sensitive detail")

	f.Fuzz(func(t *testing.T, code string) {
		safe := r.Resolve(ctx, code, raw, "fuzz")
		if !known[safe.Code] {
			t.Fatalf("code %q outside allowlist (input %q)", safe.Code, code)
		}
		if strings.Contains(safe.Message, "sensitive") {
			t.Fatalf("raw message leaked: %q", safe.Message)
		}
	})
}
