package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// discardLogger silences dberr's internal logging for the duration of a test.
func discardLogger(t *testing.T) {
	t.Helper()
	dberr.SetLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })
}

func TestTranslate_PgxDuplicate(t *testing.T) {
	discardLogger(t)
	err := &pgconn.PgError{Code: "23505", Message: `duplicate key value violates unique constraint "ai_analysis_jobs_pkey"`}
	safe := Translate(context.Background(), err, "t1")
	if safe.Code != dberr.CodeDuplicateItem || safe.Message != "cannot duplicate item" {
		t.Fatalf("unexpected: %+v", safe)
	}
	if safe.TraceID != "t1" {
		t.Fatalf("trace id missing: %+v", safe)
	}
}

// The constraint name from the raw error must never reach the client.
func TestTranslate_DoesNotLeakConstraintName(t *testing.T) {
	discardLogger(t)
	err := &pgconn.PgError{Code: "23505", Message: `duplicate key value violates unique constraint "ai_analysis_jobs_pkey"`}
	safe := Translate(context.Background(), err, "t1")
	if strings.Contains(safe.Message, "ai_analysis_jobs_pkey") {
		t.Fatalf("constraint name leaked: %+v", safe)
	}
}

func TestTranslate_LibPqDuplicate(t *testing.T) {
	discardLogger(t)
	err := &pq.Error{Code: "23505", Message: "duplicate key value violates unique constraint"}
	safe := Translate(context.Background(), err, "t2")
	if safe.Message != "cannot duplicate item" {
		t.Fatalf("unexpected: %+v", safe)
	}
}

func TestTranslate_WrappedError(t *testing.T) {
	discardLogger(t)
	inner := &pgconn.PgError{Code: "23503", Message: "fk violation"}
	safe := Translate(context.Background(), fmt.Errorf("query failed: %w", inner), "t3")
	if safe.Code != dberr.CodeReferenceNotFound {
		t.Fatalf("errors.As should unwrap fmt.Errorf: %+v", safe)
	}
}

// The connection-exception class was a gap closed in the allowlist expansion.
func TestTranslate_ConnectionFailure(t *testing.T) {
	discardLogger(t)
	for _, sqlstate := range []string{"08001", "08003", "08004", "08006", "08007", "08P01"} {
		safe := Translate(context.Background(), &pgconn.PgError{Code: sqlstate}, "t4")
		if safe.Code != dberr.CodeUnavailable {
			t.Fatalf("SQLSTATE %s: unexpected %+v", sqlstate, safe)
		}
	}
}

// pgx reports connection failures as *pgconn.ConnectError, not PgError, so
// without this branch the whole class 08 allowlist is unreachable via pgx.
func TestTranslate_PgxConnectError(t *testing.T) {
	discardLogger(t)
	err := &pgconn.ConnectError{Config: &pgconn.Config{}}
	safe := Translate(context.Background(), err, "t1")
	if safe.Code != dberr.CodeUnavailable {
		t.Fatalf("unexpected: %+v", safe)
	}
	if strings.Contains(safe.Message, "5432") || strings.Contains(safe.Message, "10.0.0.5") {
		t.Fatalf("connection detail leaked: %+v", safe)
	}
}

func TestTranslate_PgxConnectErrorWrapped(t *testing.T) {
	discardLogger(t)
	err := fmt.Errorf("pool acquire: %w", &pgconn.ConnectError{Config: &pgconn.Config{}})
	if safe := Translate(context.Background(), err, "t2"); safe.Code != dberr.CodeUnavailable {
		t.Fatalf("errors.As should unwrap: %+v", safe)
	}
}

// pgx's ConnectError.Error() dereferences Config unconditionally, so calling
// it on a zero value panics. Translate must not propagate that.
func TestTranslate_ConnectErrorNilConfigDoesNotPanic(t *testing.T) {
	discardLogger(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Translate panicked on a zero-value ConnectError: %v", r)
		}
	}()
	safe := Translate(context.Background(), &pgconn.ConnectError{}, "t0")
	if safe.Code != dberr.CodeUnavailable {
		t.Fatalf("unexpected: %+v", safe)
	}
}

// A socket read deadline is a net.Error with Timeout() true, not a PgError.
func TestTranslate_NetErrorTimeout(t *testing.T) {
	discardLogger(t)
	err := fmt.Errorf("read: %w", &timeoutError{msg: "i/o timeout on 10.0.0.5:5432", isTimeout: true})
	safe := Translate(context.Background(), err, "t3")
	if safe.Code != dberr.CodeTimeout {
		t.Fatalf("unexpected: %+v", safe)
	}
	if strings.Contains(safe.Message, "10.0.0.5") {
		t.Fatalf("address leaked: %+v", safe)
	}
}

// A net.Error that is not a timeout must NOT be treated as one.
func TestTranslate_NetErrorNotTimeout(t *testing.T) {
	discardLogger(t)
	err := &timeoutError{msg: "connection refused", isTimeout: false}
	if safe := Translate(context.Background(), err, "t4"); safe.Code != dberr.CodeInternal {
		t.Fatalf("non-timeout net.Error must be default-deny, got %+v", safe)
	}
}

// A server-reported error takes precedence over a co-occurring deadline: the
// SQLSTATE is the more specific answer.
func TestTranslate_SQLStateBeatsTimeout(t *testing.T) {
	discardLogger(t)
	inner := fmt.Errorf("query: %w", &pgconn.PgError{Code: "23505", Message: "dup"})
	wrapped := fmt.Errorf("ctx: %w", multiError{inner, &timeoutError{msg: "timeout", isTimeout: true}})
	if safe := Translate(context.Background(), wrapped, "t5"); safe.Code != dberr.CodeDuplicateItem {
		t.Fatalf("SQLSTATE should win: %+v", safe)
	}
}

// timeoutError satisfies net.Error. Temporary() is deprecated but still part of
// the interface as of Go 1.22, so it must be present for errors.As to match.
type timeoutError struct {
	msg       string
	isTimeout bool
}

func (e *timeoutError) Error() string   { return e.msg }
func (e *timeoutError) Timeout() bool   { return e.isTimeout }
func (e *timeoutError) Temporary() bool { return e.isTimeout }

// multiError joins errors so several failure modes arrive under one wrapper.
type multiError []error

func (m multiError) Error() string   { return "multiple errors" }
func (m multiError) Unwrap() []error { return m }

func TestTranslate_NewlyMappedCodes(t *testing.T) {
	discardLogger(t)
	cases := []struct {
		sqlstate string
		want     string
	}{
		{"22003", dberr.CodeValueOutOfRange},
		{"22008", dberr.CodeValueOutOfRange},
		{"22007", dberr.CodeInvalidInput},
		{"22012", dberr.CodeInvalidValue},
		{"22023", dberr.CodeInvalidValue},
		{"42501", dberr.CodePermissionDenied},
		{"00000", dberr.CodeInternal}, // success code arriving as an error
	}
	for _, tc := range cases {
		safe := Translate(context.Background(), &pgconn.PgError{Code: tc.sqlstate}, "t5")
		if safe.Code != tc.want {
			t.Fatalf("SQLSTATE %s: got %q want %q", tc.sqlstate, safe.Code, tc.want)
		}
	}
}

// An unknown table is a defect in the deployed code, so it reports
// SCHEMA_ERROR rather than a 404 that would tell the client their request
// failed on missing data.
func TestTranslate_UndefinedTableIsSchemaError(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), &pgconn.PgError{
		Code:    "42P01",
		Message: `relation "secret_table" does not exist`,
	}, "t6")
	if safe.Code != dberr.CodeSchemaError {
		t.Fatalf("unexpected: %+v", safe)
	}
	if safe.HTTPStatus() != 500 {
		t.Fatalf("HTTPStatus() = %d, want 500", safe.HTTPStatus())
	}
	if strings.Contains(safe.Message, "secret_table") {
		t.Fatalf("table name leaked: %+v", safe)
	}
}

// An unknown column gets its own code, because whether it is the client's
// fault depends on whether the application interpolates client input into SQL.
func TestTranslate_UndefinedColumnIsItsOwnCode(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), &pgconn.PgError{
		Code:    "42703",
		Message: `column "secret_column" does not exist`,
	}, "t7")
	if safe.Code != dberr.CodeColumnNotFound {
		t.Fatalf("unexpected: %+v", safe)
	}
	if safe.HTTPStatus() != 400 {
		t.Fatalf("HTTPStatus() = %d, want 400", safe.HTTPStatus())
	}
	if strings.Contains(safe.Message, "secret_column") {
		t.Fatalf("column name leaked: %+v", safe)
	}
}

// A syntax error echoes the whole query, so it must stay default-deny.
func TestTranslate_SyntaxErrorIsDefaultDeny(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), &pgconn.PgError{
		Code:    "42601",
		Message: `syntax error at or near "SELEKT"`,
	}, "t8")
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("a syntax error echoes the query, expected default-deny, got %+v", safe)
	}
}

func TestTranslate_UnknownSQLSTATE(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), &pgconn.PgError{Code: "XX000", Message: "internal detail"}, "t8")
	if safe.Code != dberr.CodeInternal || safe.Message != dberr.DefaultMessage {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

// A foreignDriverError stands in for any other driver's error type. It is a
// local type so this test file does not need another driver's dependency.
type foreignDriverError struct{ msg string }

func (e *foreignDriverError) Error() string { return e.msg }

func TestTranslate_ForeignDriverError(t *testing.T) {
	discardLogger(t)
	err := &foreignDriverError{msg: "Duplicate entry '5' for key 'PRIMARY'"}
	safe := Translate(context.Background(), err, "t9")
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny for a non-postgres error, got %+v", safe)
	}
}

func TestTranslate_PlainError(t *testing.T) {
	discardLogger(t)
	if safe := Translate(context.Background(), errors.New("some random error"), "t10"); safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslate_Nil(t *testing.T) {
	if Translate(context.Background(), nil, "t11") != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestMessage(t *testing.T) {
	discardLogger(t)
	err := &pgconn.PgError{Code: "23503"}
	if got := Message(context.Background(), err, "t12"); got != "related item does not exist" {
		t.Fatalf("Message() = %q", got)
	}
	if got := Message(context.Background(), nil, "t13"); got != "" {
		t.Fatalf("Message(nil) = %q, want empty", got)
	}
}

func TestMatches(t *testing.T) {
	if !Matches(&pgconn.PgError{Code: "23505"}) {
		t.Error("expected pgx error to match")
	}
	if !Matches(&pq.Error{Code: "23505"}) {
		t.Error("expected lib/pq error to match")
	}
	if !Matches(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505"})) {
		t.Error("expected wrapped error to match")
	}
	if Matches(&foreignDriverError{msg: "mysql-ish error"}) {
		t.Error("foreign driver error must not match")
	}
	if !Matches(&pgconn.ConnectError{Config: &pgconn.Config{}}) {
		t.Error("expected ConnectError to match")
	}
	if !Matches(&timeoutError{msg: "timeout", isTimeout: true}) {
		t.Error("expected timeout net.Error to match")
	}
	if Matches(&timeoutError{msg: "refused", isTimeout: false}) {
		t.Error("non-timeout net.Error must not match")
	}
	if Matches(errors.New("plain")) {
		t.Error("plain error must not match")
	}
	if Matches(nil) {
		t.Error("nil must not match")
	}
}
