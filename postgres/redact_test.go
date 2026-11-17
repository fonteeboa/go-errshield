package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
	"github.com/jackc/pgx/v5/pgconn"
)

// The realistic call site: a repository wraps the driver error with context that
// identifies which operation failed. That context must reach the log.
func TestTranslate_LogsWrappingContext(t *testing.T) {
	var buf strings.Builder
	dberr.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })

	inner := &pgconn.PgError{
		Code:    "23505",
		Message: `duplicate key value violates unique constraint "users_email_key"`,
	}
	wrapped := fmt.Errorf("repo.InsertUser: %w", fmt.Errorf("pool exec: %w", inner))

	Translate(context.Background(), wrapped, "t1")

	out := buf.String()
	for _, want := range []string{"repo.InsertUser", "pool exec", "users_email_key", "t1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log record missing %q: %s", want, out)
		}
	}
}

// A repository that formats the argument into the wrapping text leaks it into
// the log. Redact lets you keep the context and drop the value.
func TestTranslate_RedactRepoArgument(t *testing.T) {
	var buf strings.Builder
	dberr.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })

	// The markers are unique to this test, so registering them on the
	// package-level registry cannot affect any other test. Redact is
	// additive by design and is not concurrency-safe; in production it is a
	// setup-time call.
	const secretEmail = "redact-probe-unique@example.com"
	const secretPassword = "redact-probe-unique-pw"
	Registry().Redact(secretEmail, secretPassword)

	inner := &pgconn.PgError{
		Code:    "23502",
		Message: `null value in column "name" violates not-null constraint`,
	}
	wrapped := fmt.Errorf("repo.InsertUser(email=%s, password=%s): %w",
		secretEmail, secretPassword, inner)

	safe := Translate(context.Background(), wrapped, "t2")

	out := buf.String()
	for _, leaked := range []string{secretEmail, secretPassword} {
		if strings.Contains(out, leaked) {
			t.Fatalf("redaction failed, %q still logged: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "repo.InsertUser") {
		t.Fatalf("redaction removed the call site: %s", out)
	}

	// The response is unaffected by redaction.
	if safe.Message != "a required field is missing" {
		t.Fatalf("unexpected response: %+v", safe)
	}
}
