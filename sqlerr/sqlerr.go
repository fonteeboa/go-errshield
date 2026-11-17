// Package sqlerr translates database/sql sentinel errors into safe,
// client-facing messages.
//
// Sentinel errors from database/sql, database/sql/driver and context are not
// driver errors, so no per-driver extractor can ever match them. Left alone
// they all collapse into the generic internal server error, which loses two
// things worth keeping:
//
//   - sql.ErrNoRows is one of the most common errors a Go handler sees, and it
//     is a plain "resource not found", not a server fault.
//   - context.DeadlineExceeded should surface as a timeout, not as an opaque
//     500.
//
// Import this package alongside the source packages you use. It has no
// external dependencies.
//
//	// in a handler
//	switch {
//	case postgres.Matches(err):
//	    safe = postgres.Translate(ctx, err, traceID)
//	default:
//	    safe = sqlerr.Translate(ctx, err, traceID)
//	}
//
// sqlerr.Translate is a good default branch: it handles the sentinels and
// falls back to dberr.CodeInternal for anything else.
package sqlerr

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/fonteeboa/go-errshield"
)

const source = "sql"

// Registry returns this source's registry, so you can register log redaction
// markers during setup:
//
//	sqlerr.Registry().Redact("user_id=")
func Registry() *dberr.Registry { return registry }

// registry holds the allowlist of sentinels. Keys are the sentinel names
// rather than their values, so the log record identifies which one fired
// without depending on error string formatting.
//
// Two entries deliberately resolve to CodeInternal. They are listed as known
// because they are expected conditions, not gaps in the allowlist, and being
// known means they log at WARN instead of ERROR — so a routine client
// disconnect does not page anyone.
var registry = dberr.NewRegistry(source, map[string]dberr.SafeError{
	"sql.ErrNoRows": {
		Code:    dberr.CodeNotFound,
		Message: "resource not found",
	},
	"sql.ErrConnDone": {
		Code:    dberr.CodeUnavailable,
		Message: "service temporarily unavailable",
	},
	"driver.ErrBadConn": {
		Code:    dberr.CodeUnavailable,
		Message: "service temporarily unavailable",
	},
	"context.DeadlineExceeded": {
		Code:    dberr.CodeTimeout,
		Message: "operation timed out",
	},

	// The client went away. Nothing useful can be sent back, and this is not
	// a server fault, so it resolves to the generic message without an
	// ERROR-level log record.
	"context.Canceled": {
		Code:    dberr.CodeInternal,
		Message: dberr.DefaultMessage,
	},
})

// sentinels is checked in order, so the first match wins. Order is irrelevant
// today since the sentinels are mutually exclusive, but it keeps the behavior
// defined if an error ever wraps more than one.
var sentinels = []struct {
	name string
	err  error
}{
	{"sql.ErrNoRows", sql.ErrNoRows},
	{"sql.ErrConnDone", sql.ErrConnDone},
	{"driver.ErrBadConn", driver.ErrBadConn},
	{"context.DeadlineExceeded", context.DeadlineExceeded},
	{"context.Canceled", context.Canceled},
}

// Translate inspects err for one of the database/sql, driver or context
// sentinels, logs it tagged with traceID, and returns a SafeError.
//
// It returns nil if err is nil. Anything else — including a driver error that
// no source package claimed — yields the generic dberr.CodeInternal fallback,
// which makes Translate usable as the default branch of a multi-source
// switch.
func Translate(ctx context.Context, err error, traceID string) *dberr.SafeError {
	if err == nil {
		return nil
	}

	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			return registry.Resolve(ctx, s.name, err, traceID)
		}
	}

	return dberr.Unrecognized(ctx, err, traceID)
}

// Message is a convenience wrapper for the common case: you only want the
// ready-to-return string.
//
//	c.JSON(http.StatusNotFound, gin.H{"error": sqlerr.Message(ctx, err, traceID)})
func Message(ctx context.Context, err error, traceID string) string {
	safe := Translate(ctx, err, traceID)
	if safe == nil {
		return ""
	}
	return safe.Message
}
