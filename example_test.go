package dberr_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/fonteeboa/go-errshield"
	"github.com/fonteeboa/go-errshield/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func init() {
	// Keep example output clean.
	dberr.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

// The one-liner: a translated error is ready to write, status included.
func ExampleSafeError_HTTPStatus() {
	err := &pgconn.PgError{Code: "23505"}

	safe := postgres.Translate(context.Background(), err, "req-1")
	fmt.Println(safe.HTTPStatus(), safe.Code, safe.Message)
	// Output: 409 DUPLICATE_ITEM cannot duplicate item
}

// An unknown table is a defect in the deployed code, not a missing resource,
// so it reports SCHEMA_ERROR and 500 rather than a misleading 404.
func ExampleSafeError_HTTPStatus_schemaError() {
	err := &pgconn.PgError{Code: "42P01"}

	safe := postgres.Translate(context.Background(), err, "req-1")
	fmt.Println(safe.HTTPStatus(), safe.Code, safe.Message)
	// Output: 500 SCHEMA_ERROR schema error
}

// An unknown column is its own code: whether that is the client's fault or
// yours depends on whether you interpolate their input into SQL.
func ExampleSafeError_HTTPStatus_columnNotFound() {
	err := &pgconn.PgError{Code: "42703"}

	safe := postgres.Translate(context.Background(), err, "req-1")
	fmt.Println(safe.HTTPStatus(), safe.Code, safe.Message)
	// Output: 400 COLUMN_NOT_FOUND column does not exist
}

// An unmapped code still answers 500, never a wrong-but-plausible status.
func ExampleStatusFor_unknown() {
	status, known := dberr.StatusFor("NOT_A_REAL_CODE")
	fmt.Println(status, known)
	// Output: 500 false
}

// The hook is how the allowlist improves itself: it reports the codes a real
// database produces that the library has not been taught to describe.
func ExampleSetUnmappedHook() {
	dberr.SetUnmappedHook(func(e dberr.UnmappedEvent) {
		// In production: emit a metric, or append to a bounded set.
		fmt.Printf("unmapped: source=%s code=%q\n", e.Source, e.Code)
	})
	defer dberr.SetUnmappedHook(nil)

	// 42601 (syntax_error) is not in the postgres allowlist: its message echoes
	// the offending statement.
	err := &pgconn.PgError{
		Code:    "42601",
		Message: `syntax error at or near "SELEKT"`,
	}

	safe := postgres.Translate(context.Background(), err, "req-1")
	fmt.Println(safe.HTTPStatus(), safe.Code, safe.Message)
	// Output:
	// unmapped: source=postgres code="42601"
	// 500 INTERNAL_ERROR internal server error
}
