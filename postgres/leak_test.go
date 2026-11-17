package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// sensitive is a marker that must never survive into a SafeError. Every
// identifier that a Postgres error message can carry is included, so a single
// injected message covers them all.
const sensitive = `duplicate key value violates unique constraint "secret_users_email_key" ` +
	`DETAIL: Key (email)=(topsecret@example.com) already exists. ` +
	`relation "secret_table" column "secret_column" schema "secret_schema" ` +
	`type "secret_type" host "internal-host" port 5432 detail "topsecret"`

// TestNoAllowlistEntryLeaks is the table-driven form of the non-leak property:
// for every code in the allowlist, an error carrying every sensitive marker
// must still produce the registered message and nothing more.
//
// This is the test that guards the library's central claim. A future entry
// added carelessly — for instance one that interpolates the raw message — is
// caught here.
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

		got := Translate(ctx, &pgconn.PgError{Code: code, Message: sensitive}, "t1")

		if got.Message != want.Message {
			t.Errorf("code %s: message changed\n got: %q\nwant: %q", code, got.Message, want.Message)
		}
		if got.Code != want.Code && got.Code != dberr.CodeInternal {
			t.Errorf("code %s: resolved to %q, want %q", code, got.Code, want.Code)
		}
		for _, marker := range []string{
			"secret_users_email_key", "topsecret", "secret_table",
			"secret_column", "secret_schema", "secret_type",
			"internal-host", "5432",
		} {
			if strings.Contains(got.Message, marker) {
				t.Errorf("code %s: leaked %q in %q", code, marker, got.Message)
			}
		}
	}
}

// Same property through the lib/pq extractor, since it is a separate branch in
// extract and could regress independently.
func TestNoAllowlistEntryLeaksViaLibPq(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()
	for _, code := range registry.Codes() {
		got := Translate(ctx, &pq.Error{Code: pq.ErrorCode(code), Message: sensitive}, "t1")
		for _, marker := range []string{"secret", "topsecret", "internal-host", "5432"} {
			if strings.Contains(got.Message, marker) {
				t.Errorf("code %s: leaked %q in %q", code, marker, got.Message)
			}
		}
	}
}

// The synthetic keys for pgx failure modes must not collide with a real
// SQLSTATE. SQLSTATEs are exactly 5 characters; these are longer by design, so
// a driver can never emit them as a code.
func TestSyntheticKeysDoNotCollideWithSQLStates(t *testing.T) {
	for _, code := range registry.Codes() {
		switch code {
		case "pgconn.ConnectError", "net.Error.Timeout":
			if len(code) <= 5 {
				t.Errorf("synthetic key %q is short enough to collide with a SQLSTATE", code)
			}
		default:
			if len(code) != 5 {
				t.Errorf("code %q is not a 5-character SQLSTATE and not a known synthetic key", code)
			}
		}
	}
}
