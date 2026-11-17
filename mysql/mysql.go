// Package mysql translates go-sql-driver/mysql errors into safe, client-facing
// messages.
//
// Importing this package pulls in github.com/go-sql-driver/mysql. Use
// dberr/postgres, dberr/mongo or dberr/opensearch instead if you do not use
// MySQL.
//
//	safe := mysql.Translate(ctx, err, traceID)
//	if safe != nil {
//	    c.JSON(http.StatusConflict, safe)
//	}
package mysql

import (
	"context"
	"errors"
	"strconv"

	"github.com/fonteeboa/go-errshield"
	"github.com/go-sql-driver/mysql"
)

const source = "mysql"

// registry holds the allowlist of server error numbers considered safe to
// describe externally. Reference:
// https://dev.mysql.com/doc/mysql-errors/8.4/en/server-error-reference.html
//
// Note this driver reports a plain number (1062), not Postgres's alphanumeric
// SQLSTATE. The two code spaces are kept separate per source and are never
// merged, so a number here means nothing outside this registry.
//
// Numbers whose own message would name a column or table map to a category
// that says what kind of problem it is without naming the object. Numbers
// indicating a server-side fault (InnoDB errors, disk full) stay unmapped and
// fall back to dberr.CodeInternal.
var registry = dberr.NewRegistry(source, map[string]dberr.SafeError{
	// Integrity and constraint violations
	"1062": {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},
	"1451": {Code: dberr.CodeReferenceInUse, Message: "item is referenced by other records"},
	"1452": {Code: dberr.CodeReferenceNotFound, Message: "related item does not exist"},
	"1048": {Code: dberr.CodeMissingRequiredField, Message: "a required field is missing"},
	"1364": {Code: dberr.CodeMissingRequiredField, Message: "a required field is missing"},
	"1171": {Code: dberr.CodeMissingRequiredField, Message: "a required field is missing"},
	"1264": {Code: dberr.CodeValueOutOfRange, Message: "value is out of range"},
	"1406": {Code: dberr.CodeValueTooLong, Message: "value is too long"},

	// Data truncation and malformed values
	"1366": {Code: dberr.CodeInvalidInput, Message: "invalid value"},
	"1367": {Code: dberr.CodeInvalidInput, Message: "invalid date or time format"},
	"1292": {Code: dberr.CodeInvalidInput, Message: "invalid numeric value"},
	"3818": {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},
	"3819": {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},
	"4025": {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},

	// Locking and concurrency
	"1213": {Code: dberr.CodeDeadlock, Message: "please try again"},
	"1205": {Code: dberr.CodeLockTimeout, Message: "operation timed out, please try again"},

	// Resources and connectivity
	"1040": {Code: dberr.CodeTooManyConnections, Message: "service temporarily unavailable"},
	"1226": {Code: dberr.CodeTooManyConnections, Message: "service temporarily unavailable"},
	"1053": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"1152": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"1153": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"1158": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"1159": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"1160": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"1161": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"1927": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},

	// Interruption
	"1317": {Code: dberr.CodeTimeout, Message: "operation timed out"},

	// Access control
	"1045": {Code: dberr.CodeAuthFailed, Message: "authentication failed"},
	"1044": {Code: dberr.CodeAuthFailed, Message: "authentication failed"},
	"1142": {Code: dberr.CodePermissionDenied, Message: "insufficient permissions"},
	"1143": {Code: dberr.CodePermissionDenied, Message: "insufficient permissions"},

	// Missing objects. Whether an unknown column or table is the client's
	// fault or yours depends on whether you interpolate their input into SQL,
	// so those get their own codes rather than being folded into NotFound.
	//
	// 1064 (syntax error) is NOT mapped: its message contains the failing
	// statement, so it would leak the query.
	"1146": {Code: dberr.CodeSchemaError, Message: "schema error"},
	"1054": {Code: dberr.CodeColumnNotFound, Message: "column does not exist"},
	"1049": {Code: dberr.CodeSchemaError, Message: "schema error"},
	"1046": {Code: dberr.CodeSchemaError, Message: "schema error"},
})

// Registry returns this source's registry, so you can register log redaction
// markers during setup:
//
//	mysql.Registry().Redact("email=")
func Registry() *dberr.Registry { return registry }

// Translate inspects err for a go-sql-driver/mysql error, logs the original
// error tagged with traceID, and returns a SafeError ready to serialize in the
// HTTP response.
//
// The log record keeps your own wrapping context ("repo.InsertUser: ..."),
// which is usually what identifies the failing call site. The response contains
// nothing from the original error.
//
// It returns nil if err is nil. An error that is not a MySQL driver error at
// all yields the generic dberr.CodeInternal fallback, so Translate is safe to
// call unconditionally.
func Translate(ctx context.Context, err error, traceID string) *dberr.SafeError {
	if err == nil {
		return nil
	}
	if code, ok := extract(err); ok {
		return registry.Resolve(ctx, code, err, traceID)
	}
	return dberr.Unrecognized(ctx, err, traceID)
}

// Message is a convenience wrapper for the common case: you only want the
// ready-to-return string.
//
//	c.JSON(http.StatusConflict, gin.H{"error": mysql.Message(ctx, err, traceID)})
func Message(ctx context.Context, err error, traceID string) string {
	safe := Translate(ctx, err, traceID)
	if safe == nil {
		return ""
	}
	return safe.Message
}

// Matches reports whether err is, or wraps, a go-sql-driver/mysql error. Use
// it to try several sources in turn when a service talks to more than one
// database.
func Matches(err error) bool {
	_, ok := extract(err)
	return ok
}

// extract unwraps err looking for a go-sql-driver/mysql error, returning the
// registry key for it. Only the key is returned: Resolve logs the original
// error, so the caller's wrapping context survives into the log record.
func extract(err error) (code string, ok bool) {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		return strconv.FormatUint(uint64(myErr.Number), 10), true
	}
	return "", false
}
