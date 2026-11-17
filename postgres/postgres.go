// Package postgres translates PostgreSQL driver errors into safe,
// client-facing messages.
//
// It supports both pgx v5 (github.com/jackc/pgx/v5/pgconn) and lib/pq
// (github.com/lib/pq); both report errors keyed by SQLSTATE, so they share
// one allowlist. Importing this package pulls in those two drivers — use
// dberr/mysql, dberr/mongo or dberr/opensearch instead if you do not use
// Postgres.
//
//	safe := postgres.Translate(ctx, err, traceID)
//	if safe != nil {
//	    c.JSON(http.StatusConflict, safe)
//	}
package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/fonteeboa/go-errshield"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

const source = "postgres"

// registry holds the allowlist of SQLSTATEs considered safe to describe
// externally. Reference:
// https://www.postgresql.org/docs/current/errcodes-appendix.html
//
// Only human-readable, client-actionable conditions are listed. SQLSTATEs
// whose own message would name a table, column, function or schema map to
// CodeColumnNotFound or CodeSchemaError: a category that is safe to say
// without saying which object is missing.
var registry = dberr.NewRegistry(source, map[string]dberr.SafeError{
	// Class 23 — integrity constraint violation
	"23505": {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},
	"23503": {Code: dberr.CodeReferenceNotFound, Message: "related item does not exist"},
	"23502": {Code: dberr.CodeMissingRequiredField, Message: "a required field is missing"},
	"23514": {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},

	// Class 22 — data exception
	"22001": {Code: dberr.CodeValueTooLong, Message: "value is too long"},
	"22003": {Code: dberr.CodeValueOutOfRange, Message: "value is out of range"},
	"22007": {Code: dberr.CodeInvalidInput, Message: "invalid date or time format"},
	"22008": {Code: dberr.CodeValueOutOfRange, Message: "date or time value is out of range"},
	"22012": {Code: dberr.CodeInvalidValue, Message: "value must not be zero"},
	"22023": {Code: dberr.CodeInvalidValue, Message: "invalid value for this parameter"},

	// Class 08 — connection exception
	"08001": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"08003": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"08004": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"08006": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"08007": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"08P01": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},

	// Class 40 — transaction rollback
	"40001": {Code: dberr.CodeConcurrentUpdate, Message: "please try again"},
	"40P01": {Code: dberr.CodeDeadlock, Message: "please try again"},

	// Class 53 — insufficient resources
	"53300": {Code: dberr.CodeTooManyConnections, Message: "service temporarily unavailable"},

	// Class 55 — object not in prerequisite state
	"55P03": {Code: dberr.CodeLockTimeout, Message: "operation timed out, please try again"},

	// Class 57 — operator intervention
	"57014": {Code: dberr.CodeTimeout, Message: "operation timed out"},

	// Class 28 — invalid authorization specification
	"28000": {Code: dberr.CodeAuthFailed, Message: "authentication failed"},
	"28P01": {Code: dberr.CodeAuthFailed, Message: "authentication failed"},

	// Class 42 — syntax error or access rule violation. Only the access denial
	// is client-facing; the schema errors below are not.
	"42501": {Code: dberr.CodePermissionDenied, Message: "insufficient permissions"},

	// The query referenced an object that does not exist. Whether that is the
	// client's fault or yours depends on whether you interpolate their input
	// into SQL, which only your application knows — so it gets its own code
	// rather than being folded into NotFound.
	"42703": {Code: dberr.CodeColumnNotFound, Message: "column does not exist"},
	"42P01": {Code: dberr.CodeSchemaError, Message: "schema error"},
	"42P07": {Code: dberr.CodeSchemaError, Message: "schema error"},
	"42704": {Code: dberr.CodeSchemaError, Message: "schema error"},
	"42883": {Code: dberr.CodeSchemaError, Message: "schema error"},
	"3D000": {Code: dberr.CodeSchemaError, Message: "schema error"},
	"42P02": {Code: dberr.CodeSchemaError, Message: "schema error"},

	// 42601 (syntax_error) is NOT mapped: Postgres echoes the offending
	// statement in the message, so it would leak the query.

	// pgx reports connection failures as *pgconn.ConnectError, which carries
	// no SQLSTATE, and I/O timeouts as a plain net.Error. Both are common and
	// client-relevant, so they get synthetic keys rather than falling through
	// to the generic fallback. See extract.
	"pgconn.ConnectError": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"net.Error.Timeout":   {Code: dberr.CodeTimeout, Message: "operation timed out"},
})

// Registry returns this source's registry, so you can register log redaction
// markers during setup:
//
//	postgres.Registry().Redact("email=", "password=")
func Registry() *dberr.Registry { return registry }

// Translate inspects err for a pgx or lib/pq driver error, logs the original
// error tagged with traceID, and returns a SafeError ready to serialize in the
// HTTP response.
//
// The log record keeps your own wrapping context ("repo.InsertUser: ..."),
// which is usually what identifies the failing call site. The response contains
// nothing from the original error.
//
// It returns nil if err is nil. An error that is not a Postgres driver error
// at all yields the generic dberr.CodeInternal fallback, so Translate is safe
// to call unconditionally.
func Translate(ctx context.Context, err error, traceID string) *dberr.SafeError {
	if err == nil {
		return nil
	}
	if code, ok := extract(err); ok {
		return registry.Resolve(ctx, code, safeForLog(err), traceID)
	}
	return dberr.Unrecognized(ctx, err, traceID)
}

// safeForLog guards against drivers whose Error() method panics on a partially
// populated value. pgx's ConnectError.Error() dereferences Config without a nil
// check, so logging a zero-value ConnectError would panic inside dberr. pgx
// always populates Config, but a library that translates errors must not be the
// thing that brings down a request.
func safeForLog(err error) error {
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) && connErr.Config == nil {
		return fmt.Errorf("postgres: connection failed: %w", sanitizedConnError{})
	}
	return err
}

// sanitizedConnError stands in for a ConnectError whose Error() cannot be
// called. It keeps the original reachable through Unwrap, so the rest of the
// chain still logs.
type sanitizedConnError struct{}

func (sanitizedConnError) Error() string { return "failed to connect" }

// Message is a convenience wrapper for the common case: you only want the
// ready-to-return string.
//
//	c.JSON(http.StatusConflict, gin.H{"error": postgres.Message(ctx, err, traceID)})
func Message(ctx context.Context, err error, traceID string) string {
	safe := Translate(ctx, err, traceID)
	if safe == nil {
		return ""
	}
	return safe.Message
}

// Matches reports whether err is, or wraps, a Postgres driver error. Use it to
// try several sources in turn when a service talks to more than one database:
//
//	if postgres.Matches(err) {
//	    return postgres.Translate(ctx, err, traceID)
//	}
//	if mongo.Matches(err) {
//	    return mongo.Translate(ctx, err, traceID)
//	}
func Matches(err error) bool {
	_, ok := extract(err)
	return ok
}

// extract unwraps err looking for a Postgres driver error and returns the
// registry key for it. ok is false if err wraps none of the supported types.
//
// Only the key is returned, never the raw message: Resolve logs the original
// error itself, so the caller's wrapping context survives into the log record.
//
// The first two cases carry a real SQLSTATE. The last two handle pgx failure
// modes that never reach the SQLSTATE layer: connection failures surface as
// *pgconn.ConnectError, and socket read/write deadlines surface as a
// net.Error whose Timeout() is true. Without these, the class 08 entries above
// would never fire on a real connection failure via pgx, and a slow query
// would be reported as an opaque server error instead of a timeout.
//
// Order matters. PgError is checked first because a server-reported error can
// be wrapped alongside a deadline; the server's own SQLSTATE is the more
// specific answer.
func extract(err error) (code string, ok bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, true
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code), true
	}

	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return "pgconn.ConnectError", true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "net.Error.Timeout", true
	}

	return "", false
}
