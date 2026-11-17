package dberr

import "errors"

// Sentinel errors for every client-facing Code. Compare with errors.Is:
//
//	if errors.Is(err, dberr.ErrDuplicateItem) {
//	    // 409
//	}
//
// They are ordinary *SafeError values carrying the canonical message, so they
// also answer Error() and HTTPStatus() if you need to log or return one.
//
// Treat them as read-only. They are package-level pointers and SafeError has
// exported fields; mutating one would corrupt the sentinel for the whole
// process.
var (
	ErrInternal             = &SafeError{Code: CodeInternal, Message: DefaultMessage}
	ErrDuplicateItem        = &SafeError{Code: CodeDuplicateItem, Message: "cannot duplicate item"}
	ErrReferenceNotFound    = &SafeError{Code: CodeReferenceNotFound, Message: "related item does not exist"}
	ErrReferenceInUse       = &SafeError{Code: CodeReferenceInUse, Message: "item is referenced by other records"}
	ErrMissingRequiredField = &SafeError{Code: CodeMissingRequiredField, Message: "a required field is missing"}
	ErrInvalidValue         = &SafeError{Code: CodeInvalidValue, Message: "value violates a constraint"}
	ErrValueTooLong         = &SafeError{Code: CodeValueTooLong, Message: "value is too long"}
	ErrValueOutOfRange      = &SafeError{Code: CodeValueOutOfRange, Message: "value is out of range"}
	ErrInvalidInput         = &SafeError{Code: CodeInvalidInput, Message: "invalid input format"}
	ErrConcurrentUpdate     = &SafeError{Code: CodeConcurrentUpdate, Message: "please try again"}
	ErrDeadlock             = &SafeError{Code: CodeDeadlock, Message: "please try again"}
	ErrLockTimeout          = &SafeError{Code: CodeLockTimeout, Message: "operation timed out, please try again"}
	ErrTimeout              = &SafeError{Code: CodeTimeout, Message: "operation timed out"}
	ErrTooManyConnections   = &SafeError{Code: CodeTooManyConnections, Message: "service temporarily unavailable"}
	ErrUnavailable          = &SafeError{Code: CodeUnavailable, Message: "service temporarily unavailable"}
	ErrAuthFailed           = &SafeError{Code: CodeAuthFailed, Message: "authentication failed"}
	ErrPermissionDenied     = &SafeError{Code: CodePermissionDenied, Message: "insufficient permissions"}
	ErrMethodNotAllowed     = &SafeError{Code: CodeMethodNotAllowed, Message: "method not allowed for this resource"}
	ErrNotFound             = &SafeError{Code: CodeNotFound, Message: "resource not found"}
	ErrColumnNotFound       = &SafeError{Code: CodeColumnNotFound, Message: "column does not exist"}
	ErrSchemaError          = &SafeError{Code: CodeSchemaError, Message: "schema error"}
)

// sentinelByCode indexes the sentinels for HasCode.
var sentinelByCode = map[string]*SafeError{
	CodeInternal:             ErrInternal,
	CodeDuplicateItem:        ErrDuplicateItem,
	CodeReferenceNotFound:    ErrReferenceNotFound,
	CodeReferenceInUse:       ErrReferenceInUse,
	CodeMissingRequiredField: ErrMissingRequiredField,
	CodeInvalidValue:         ErrInvalidValue,
	CodeValueTooLong:         ErrValueTooLong,
	CodeValueOutOfRange:      ErrValueOutOfRange,
	CodeInvalidInput:         ErrInvalidInput,
	CodeConcurrentUpdate:     ErrConcurrentUpdate,
	CodeDeadlock:             ErrDeadlock,
	CodeLockTimeout:          ErrLockTimeout,
	CodeTimeout:              ErrTimeout,
	CodeTooManyConnections:   ErrTooManyConnections,
	CodeUnavailable:          ErrUnavailable,
	CodeAuthFailed:           ErrAuthFailed,
	CodePermissionDenied:     ErrPermissionDenied,
	CodeMethodNotAllowed:     ErrMethodNotAllowed,
	CodeNotFound:             ErrNotFound,
	CodeColumnNotFound:       ErrColumnNotFound,
	CodeSchemaError:          ErrSchemaError,
}

// Is reports whether target is a *SafeError carrying the same Code.
//
// This is what makes errors.Is work by Code rather than by identity, so a
// freshly translated error matches the sentinel even though it is a different
// pointer:
//
//	errors.Is(postgres.Translate(ctx, err, "t1"), dberr.ErrDuplicateItem) // true
//
// TraceID and Message are deliberately ignored: the Code is the contract.
func (e *SafeError) Is(target error) bool {
	if e == nil {
		return false
	}
	t, ok := target.(*SafeError)
	if !ok {
		return false
	}
	return t.Code == e.Code
}

// HasCode reports whether err is, or wraps, a *SafeError with the given Code.
//
// It is the string-keyed counterpart to errors.Is, for code that already works
// in terms of the Code constants:
//
//	if dberr.HasCode(err, dberr.CodeNotFound) {
//	    c.Status(http.StatusNotFound)
//	}
func HasCode(err error, code string) bool {
	var safe *SafeError
	if errors.As(err, &safe) {
		return safe.Code == code
	}
	return false
}

// A SafeError deliberately does NOT unwrap to the error it was translated from.
//
// Exposing the original through Unwrap would let any caller walk the chain back
// to the driver's message and log or render the constraint, table, column and
// host names that dberr exists to keep out of responses. The original error is
// in the log, correlated by TraceID; it is not reachable from the response
// value.
//
// The consequence is that errors.Is(safe, someDriverError) is false, which is
// the intended behaviour: a SafeError carries a classification, not a cause.
