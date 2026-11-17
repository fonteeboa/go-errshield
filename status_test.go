package dberr

import (
	"net/http"
	"testing"
)

// TestStatusFor_EveryCodeHasAStatus is the guard against a new Code being added
// to the vocabulary without a status. Without it, an unmapped Code silently
// degrades to 500 — which is exactly the bug the table-as-code is meant to
// prevent.
func TestStatusFor_EveryCodeHasAStatus(t *testing.T) {
	all := AllCodes()
	if len(all) == 0 {
		t.Fatal("AllCodes is empty")
	}
	for _, code := range all {
		status, known := StatusFor(code)
		if !known {
			t.Errorf("code %q has no HTTP status", code)
			continue
		}
		if status < 400 || status > 599 {
			t.Errorf("code %q maps to %d, which is not an error status", code, status)
		}
	}
}

// The reverse direction: no status without a code.
func TestStatusFor_NoOrphanStatuses(t *testing.T) {
	known := map[string]bool{}
	for _, code := range AllCodes() {
		known[code] = true
	}
	for code := range httpStatus {
		if !known[code] {
			t.Errorf("httpStatus has an entry for %q, which is not in AllCodes", code)
		}
	}
}

// An unknown code degrades to 500 and reports that it was unknown.
func TestStatusFor_UnknownCode(t *testing.T) {
	status, known := StatusFor("SOMETHING_ELSE")
	if known {
		t.Fatal("expected an unknown code to report known=false")
	}
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
}

func TestSafeError_HTTPStatus(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{CodeDuplicateItem, http.StatusConflict},
		{CodeNotFound, http.StatusNotFound},
		{CodeMethodNotAllowed, http.StatusMethodNotAllowed},
		{CodePermissionDenied, http.StatusForbidden},
		{CodeAuthFailed, http.StatusUnauthorized},
		{CodeReferenceNotFound, http.StatusUnprocessableEntity},
		{CodeUnavailable, http.StatusServiceUnavailable},
		{CodeTimeout, http.StatusGatewayTimeout},
		{CodeInternal, http.StatusInternalServerError},
		{"BOGUS", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		e := &SafeError{Code: tc.code}
		if got := e.HTTPStatus(); got != tc.want {
			t.Errorf("%s: HTTPStatus() = %d, want %d", tc.code, got, tc.want)
		}
	}
}

// A nil *SafeError is what a nil error translates to; the method must not
// panic on it.
func TestSafeError_HTTPStatusNilReceiver(t *testing.T) {
	var e *SafeError
	if got := e.HTTPStatus(); got != http.StatusInternalServerError {
		t.Fatalf("HTTPStatus() on nil = %d, want 500", got)
	}
}

// The whole point: a code resolves to the same status regardless of which
// source produced it.
func TestHTTPStatus_SourceIndependent(t *testing.T) {
	// DUPLICATE_ITEM from Postgres, MySQL, Mongo and OpenSearch are four
	// different raw codes that collapse to one client-facing code.
	status, _ := StatusFor(CodeDuplicateItem)
	if status != http.StatusConflict {
		t.Fatal("DUPLICATE_ITEM should be 409")
	}
	if (&SafeError{Code: CodeDuplicateItem}).HTTPStatus() != 409 {
		t.Fatal("method and function must agree")
	}
}

// Every status must be a 4xx or 5xx. A 200-range status here would tell a
// client the write succeeded.
func TestStatusFor_NoSuccessStatuses(t *testing.T) {
	for _, code := range AllCodes() {
		status, _ := StatusFor(code)
		if status < 400 {
			t.Errorf("code %q maps to success status %d", code, status)
		}
	}
}
