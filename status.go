package dberr

import "net/http"

// httpStatus is the canonical mapping from a client-facing Code to an HTTP
// status. It lives in one place so every consumer agrees on it — the failure
// mode this replaces was each project retyping the table, and eventually
// sending 500 for something that should have been 404.
//
// This is a recommendation, not a decision: dberr does not write responses and
// never imports net/http for anything but these constants. Override per route
// when your API contract differs.
var httpStatus = map[string]int{
	CodeDuplicateItem:        http.StatusConflict,            // 409
	CodeReferenceInUse:       http.StatusConflict,            // 409
	CodeConcurrentUpdate:     http.StatusConflict,            // 409
	CodeDeadlock:             http.StatusConflict,            // 409
	CodeMissingRequiredField: http.StatusBadRequest,          // 400
	CodeInvalidValue:         http.StatusBadRequest,          // 400
	CodeValueTooLong:         http.StatusBadRequest,          // 400
	CodeValueOutOfRange:      http.StatusBadRequest,          // 400
	CodeInvalidInput:         http.StatusBadRequest,          // 400
	CodeReferenceNotFound:    http.StatusUnprocessableEntity, // 422
	CodeNotFound:             http.StatusNotFound,            // 404
	CodeColumnNotFound:       http.StatusBadRequest,          // 400
	CodeSchemaError:          http.StatusInternalServerError, // 500
	CodeMethodNotAllowed:     http.StatusMethodNotAllowed,    // 405
	CodePermissionDenied:     http.StatusForbidden,           // 403
	CodeAuthFailed:           http.StatusUnauthorized,        // 401
	CodeUnavailable:          http.StatusServiceUnavailable,  // 503
	CodeTooManyConnections:   http.StatusServiceUnavailable,  // 503
	CodeTimeout:              http.StatusGatewayTimeout,      // 504
	CodeLockTimeout:          http.StatusGatewayTimeout,      // 504
	CodeInternal:             http.StatusInternalServerError, // 500
}

// StatusFor returns the recommended HTTP status for a client-facing Code, and
// whether the Code is known.
//
// An unknown Code yields http.StatusInternalServerError and false, so an
// unhandled case degrades to a 500 rather than to a wrong-but-plausible status.
//
//	safe := postgres.Translate(ctx, err, traceID)
//	c.JSON(safe.Status(), safe)
//
// If you validate codes before adding them to an allowlist, guard against the
// false return:
//
//	if status, known := dberr.StatusFor(safe.Code); known {
//	    // your own policy
//	}
func StatusFor(code string) (status int, known bool) {
	status, known = httpStatus[code]
	if !known {
		return http.StatusInternalServerError, false
	}
	return status, true
}

// HTTPStatus returns the recommended HTTP status for this error. An unknown
// Code yields 500.
//
// It is shorthand for StatusFor(e.Code) with the known flag discarded, and it
// never fails: a nil receiver returns 500.
func (e *SafeError) HTTPStatus() int {
	if e == nil {
		return http.StatusInternalServerError
	}
	status, _ := StatusFor(e.Code)
	return status
}
