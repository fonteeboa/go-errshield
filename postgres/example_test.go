package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/fonteeboa/go-errshield"
	"github.com/fonteeboa/go-errshield/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func ExampleTranslate() {
	dberr.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// A duplicate key from Postgres.
	err := &pgconn.PgError{
		Code:    "23505",
		Message: `duplicate key value violates unique constraint "users_email_key"`,
	}

	safe := postgres.Translate(context.Background(), err, "req-abc123")
	b, _ := json.Marshal(safe)
	fmt.Println(string(b))
	// Output: {"code":"DUPLICATE_ITEM","error":"cannot duplicate item","trace_id":"req-abc123"}
}

// Errors that are not recognized fall back to a generic message, and the raw
// detail goes to the log only.
func ExampleTranslate_unrecognized() {
	dberr.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	safe := postgres.Translate(context.Background(), fmt.Errorf("boom"), "req-abc123")
	fmt.Println(safe.Code, "|", safe.Message)
	// Output: INTERNAL_ERROR | internal server error
}

// nil in, nil out, so Translate is safe to call unconditionally.
func ExampleTranslate_nil() {
	fmt.Println(postgres.Translate(context.Background(), nil, "req-abc123") == nil)
	// Output: true
}

// pgx reports connection failures without a SQLSTATE, so they get their own
// branch rather than falling through to the generic message.
func ExampleTranslate_connectError() {
	dberr.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// Pool exhausted, server down, DNS failure — all of these arrive here.
	// A zero value is safe too: pgx's Error() would panic on a nil Config.
	safe := postgres.Translate(context.Background(), &pgconn.ConnectError{}, "req-abc123")
	fmt.Println(safe.Code)
	// Output: UNAVAILABLE
}
