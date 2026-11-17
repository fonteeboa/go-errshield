package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/fonteeboa/go-errshield"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// A translated Postgres error must satisfy errors.Is against the shared
// sentinel, regardless of which driver produced it.
func TestTranslate_SatisfiesErrorsIs(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	fromPgx := Translate(ctx, &pgconn.PgError{Code: "23505"}, "t1")
	fromLibPq := Translate(ctx, &pq.Error{Code: "23505"}, "t2")

	for _, safe := range []*dberr.SafeError{fromPgx, fromLibPq} {
		if !errors.Is(safe, dberr.ErrDuplicateItem) {
			t.Errorf("errors.Is(safe, dberr.ErrDuplicateItem) = false for %+v", safe)
		}
		if errors.Is(safe, dberr.ErrNotFound) {
			t.Errorf("a duplicate must not match ErrNotFound: %+v", safe)
		}
	}
}

// The whole point of source independence: two different raw codes, one Code.
func TestErrorsIs_SourceIndependent(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	pg := Translate(ctx, &pgconn.PgError{Code: "23505"}, "t1")
	pq := Translate(ctx, &pq.Error{Code: "23505"}, "t2")

	if pg.Code != pq.Code {
		t.Fatalf("codes differ: %q vs %q", pg.Code, pq.Code)
	}
	if !errors.Is(pg, pq) {
		t.Fatal("two SafeErrors with the same Code must match each other")
	}
}

// Wrapping on the way out must not break errors.Is.
func TestErrorsIs_SurvivesWrapping(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	safe := Translate(ctx, &pgconn.PgError{Code: "23503"}, "t1")
	wrapped := fmt.Errorf("service.CreateJob: %w", safe)

	if !errors.Is(wrapped, dberr.ErrReferenceNotFound) {
		t.Fatal("errors.Is must see through wrapping")
	}
	if !dberr.HasCode(wrapped, dberr.CodeReferenceNotFound) {
		t.Fatal("HasCode must see through wrapping")
	}
}

// The security property, at the source level: nothing reachable from the
// returned value exposes the driver's message.
func TestTranslate_SafeErrorExposesNoRawDetail(t *testing.T) {
	discardLogger(t)
	raw := `duplicate key value violates unique constraint "users_email_key" DETAIL: Key (email)=(a@b.com)`
	safe := Translate(context.Background(), &pgconn.PgError{Code: "23505", Message: raw}, "t1")

	// No Unwrap method exists, so the chain ends immediately.
	if errors.Unwrap(safe) != nil {
		t.Fatal("SafeError must not unwrap to the driver error")
	}
	if errors.Is(safe, errors.New(raw)) {
		t.Fatal("SafeError must not match the driver error")
	}
	if safe.Message != "cannot duplicate item" {
		t.Fatalf("unexpected message: %q", safe.Message)
	}
}
