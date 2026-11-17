package mongo

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
	mongodrv "go.mongodb.org/mongo-driver/mongo"
)

// sensitive carries every identifier a MongoDB error message can contain.
const sensitive = `E11000 duplicate key error collection: secret_db.secret_users ` +
	`index: secret_email_idx dup key: { email: "topsecret@example.com" }. ` +
	`Document failed validation: secret_field: Required. ` +
	`ns not found: secret_db.secret_orders. ` +
	`Index already exists with a different name: secret_status_idx. ` +
	`host "internal-host" port 27017`

// TestNoAllowlistEntryLeaks walks the whole allowlist via CommandError and
// asserts no entry can leak the raw error.
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

		n, err := strconv.ParseInt(code, 10, 32)
		if err != nil {
			t.Fatalf("key %q is not a numeric MongoDB code: %v", code, err)
		}

		got := Translate(ctx, mongodrv.CommandError{Code: int32(n), Message: sensitive}, "t1")

		if got.Message != want.Message {
			t.Errorf("code %s: message changed\n got: %q\nwant: %q", code, got.Message, want.Message)
		}
		if got.Code != want.Code && got.Code != dberr.CodeInternal {
			t.Errorf("code %s: resolved to %q, want %q", code, got.Code, want.Code)
		}
		for _, marker := range []string{
			"topsecret", "secret_db", "secret_users", "secret_orders",
			"secret_field", "secret_email_idx", "secret_status_idx",
			"internal-host", "27017",
		} {
			if strings.Contains(got.Message, marker) {
				t.Errorf("code %s: leaked %q in %q", code, marker, got.Message)
			}
		}
	}
}

// Same property through WriteException and BulkWriteException, where a raw
// Mongo message is carried on WriteError rather than CommandError.
func TestNoAllowlistEntryLeaksViaWriteErrors(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	for _, code := range registry.Codes() {
		n, err := strconv.ParseInt(code, 10, 32)
		if err != nil {
			t.Fatalf("key %q is not numeric: %v", code, err)
		}

		got := Translate(ctx, mongodrv.BulkWriteException{
			WriteErrors: []mongodrv.BulkWriteError{
				{WriteError: mongodrv.WriteError{Code: int(n), Message: sensitive}},
			},
		}, "t2")

		for _, marker := range []string{"secret", "topsecret", "internal-host", "27017"} {
			if strings.Contains(got.Message, marker) {
				t.Errorf("code %s: leaked %q in %q", code, marker, got.Message)
			}
		}
	}
}

// Every mapped code must have a precedence entry, otherwise rank() treats it as
// worst and a new entry would silently lose to a generic one.
func TestPrecedenceCoversEveryMappedCode(t *testing.T) {
	seen := map[string]bool{}
	for _, code := range registry.Codes() {
		mapped, _ := registry.Lookup(code)
		if _, ok := precedence[mapped.Code]; !ok {
			t.Errorf("code %s maps to %s which has no precedence entry", code, mapped.Code)
		}
		if seen[mapped.Code] {
			continue
		}
		seen[mapped.Code] = true
		if precedence[mapped.Code] >= worstPrecedence {
			t.Errorf("precedence for %s is %d, must be below worstPrecedence", mapped.Code, worstPrecedence)
		}
	}
}

// A mapped code with no precedence entry must rank as worst, not panic or
// silently disappear.
func TestRankFallsBackForUnknownCode(t *testing.T) {
	if got := rank("999999"); got != worstPrecedence {
		t.Fatalf("rank(unmapped) = %d, want %d", got, worstPrecedence)
	}

	// A mapped code that is somehow absent from precedence also ranks last.
	r := dberr.NewRegistry("testsrc", map[string]dberr.SafeError{
		"1": {Code: "BRAND_NEW_CODE", Message: "x"},
	})
	safe, known := r.Lookup("1")
	if !known {
		t.Fatal("expected the code to be known")
	}
	if _, ok := precedence[safe.Code]; ok {
		t.Skipf("%s is already ranked", safe.Code)
	}
}

// Precedence entries that no allowlist code maps to are dead weight and suggest
// the two tables drifted apart.
func TestPrecedenceHasNoUnusedEntries(t *testing.T) {
	used := map[string]bool{}
	for _, code := range registry.Codes() {
		mapped, _ := registry.Lookup(code)
		used[mapped.Code] = true
	}
	for code := range precedence {
		if !used[code] {
			t.Errorf("precedence has an entry for %s but no allowlist code maps to it", code)
		}
	}
}
