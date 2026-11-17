package mysql

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
	"github.com/go-sql-driver/mysql"
)

// sensitive carries every identifier a MySQL error message can contain, so one
// injected message covers them all.
const sensitive = `Duplicate entry 'topsecret@example.com' for key 'secret_users.email'. ` +
	`Table 'secret_db.secret_users' doesn't exist. ` +
	`Unknown column 'secret_column' in 'field list'. ` +
	`Column 'secret_column' cannot be null. ` +
	`Data too long for column 'secret_column' at row 1. ` +
	`host "internal-host" port 3306`

// TestNoAllowlistEntryLeaks walks the whole allowlist and asserts no entry can
// leak the raw error. This guards the library's central claim against a future
// entry that interpolates the raw message.
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

		number, err := strconv.ParseUint(code, 10, 16)
		if err != nil {
			t.Fatalf("key %q is not a numeric MySQL error number: %v", code, err)
		}

		got := Translate(ctx, &mysql.MySQLError{Number: uint16(number), Message: sensitive}, "t1")

		if got.Message != want.Message {
			t.Errorf("number %s: message changed\n got: %q\nwant: %q", code, got.Message, want.Message)
		}
		if got.Code != want.Code && got.Code != dberr.CodeInternal {
			t.Errorf("number %s: resolved to %q, want %q", code, got.Code, want.Code)
		}
		for _, marker := range []string{
			"topsecret", "secret_users", "secret_db", "secret_column",
			"internal-host", "3306",
		} {
			if strings.Contains(got.Message, marker) {
				t.Errorf("number %s: leaked %q in %q", code, marker, got.Message)
			}
		}
	}
}
