package dberr_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/fonteeboa/go-errshield"
	"github.com/fonteeboa/go-errshield/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func init() {
	dberr.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

// A repository returns the raw driver error; the handler translates once and
// then branches on the Code with plain errors.Is.
func ExampleSafeError_Is() {
	// The repository layer, as anyone would write it.
	insertUser := func(email string) error {
		err := &pgconn.PgError{
			Code:    "23505",
			Message: `duplicate key value violates unique constraint "users_email_key"`,
		}
		return fmt.Errorf("repo.InsertUser(email=%s): %w", email, err)
	}

	safe := postgres.Translate(context.Background(), insertUser("a@b.com"), "req-1")

	switch {
	case errors.Is(safe, dberr.ErrDuplicateItem):
		fmt.Println("409 tell the user the email is taken")
	case errors.Is(safe, dberr.ErrMissingRequiredField):
		fmt.Println("400 highlight the missing field")
	default:
		fmt.Println(safe.HTTPStatus(), "internal")
	}
	// Output: 409 tell the user the email is taken
}

// HasCode is the string-keyed form, for code that already works in terms of the
// Code constants.
func ExampleHasCode() {
	safe := postgres.Translate(context.Background(), &pgconn.PgError{Code: "42P07"}, "req-1")

	if dberr.HasCode(safe, dberr.CodeSchemaError) {
		fmt.Println(safe.HTTPStatus())
	}
	// Output: 500
}

// errors.Is survives wrapping on both sides, so a middleware can add its own
// context on the way out without losing the classification.
func ExampleHasCode_wrapped() {
	safe := postgres.Translate(context.Background(),
		&pgconn.PgError{Code: "53300"}, "req-1")

	// The handler adds its own context before handing the error to the router.
	reported := fmt.Errorf("service.CreateJob: %w", safe)

	if errors.Is(reported, dberr.ErrTooManyConnections) {
		fmt.Println(safe.HTTPStatus())
	}
	// Output: 503
}
