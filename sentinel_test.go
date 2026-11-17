package dberr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestSentinels_MatchByCodeNotIdentity is the reason Is exists: a translated
// error is a different pointer from the sentinel, and errors.Is must still work.
func TestSentinels_MatchByCodeNotIdentity(t *testing.T) {
	fresh := &SafeError{Code: CodeDuplicateItem, Message: "cannot duplicate item", TraceID: "t1"}

	if !errors.Is(fresh, ErrDuplicateItem) {
		t.Fatal("errors.Is must match by Code, not by pointer identity")
	}
	if fresh == ErrDuplicateItem {
		t.Fatal("the test is meaningless if the pointers are the same")
	}
}

// Every sentinel must be reachable, and must answer Error and HTTPStatus.
func TestSentinels_AreUsableErrors(t *testing.T) {
	cases := []struct {
		sentinel *SafeError
		code     string
		status   int
	}{
		{ErrDuplicateItem, CodeDuplicateItem, 409},
		{ErrNotFound, CodeNotFound, 404},
		{ErrInternal, CodeInternal, 500},
		{ErrTimeout, CodeTimeout, 504},
		{ErrUnavailable, CodeUnavailable, 503},
		{ErrMethodNotAllowed, CodeMethodNotAllowed, 405},
		{ErrPermissionDenied, CodePermissionDenied, 403},
		{ErrAuthFailed, CodeAuthFailed, 401},
	}
	for _, tc := range cases {
		if tc.sentinel.Code != tc.code {
			t.Errorf("%v: Code = %q, want %q", tc.sentinel, tc.sentinel.Code, tc.code)
		}
		if tc.sentinel.Error() == "" {
			t.Errorf("%v: Error() is empty", tc.sentinel)
		}
		if got := tc.sentinel.HTTPStatus(); got != tc.status {
			t.Errorf("%s: HTTPStatus() = %d, want %d", tc.code, got, tc.status)
		}
		if tc.sentinel.TraceID != "" {
			t.Errorf("%s: a sentinel must not carry a trace id", tc.code)
		}
	}
}

// No Code may lack a sentinel, and no sentinel may be unreachable by Code.
func TestSentinels_CoverVocabulary(t *testing.T) {
	for _, code := range AllCodes() {
		sentinel, ok := sentinelByCode[code]
		if !ok {
			t.Errorf("code %q has no sentinel", code)
			continue
		}
		if sentinel.Code != code {
			t.Errorf("sentinelByCode[%q] points at a %q sentinel", code, sentinel.Code)
		}
		if !errors.Is(sentinel, sentinel) {
			t.Errorf("code %q: sentinel does not match itself", code)
		}
	}

	covered := map[string]bool{}
	for code := range sentinelByCode {
		covered[code] = true
	}
	for _, code := range AllCodes() {
		if !covered[code] {
			t.Errorf("code %q missing from sentinelByCode", code)
		}
	}
}

// TraceID and Message must not affect matching: the Code is the contract.
func TestSentinels_IgnoreTraceIDAndMessage(t *testing.T) {
	noTrace := &SafeError{Code: CodeNotFound, Message: "something else entirely"}
	if !errors.Is(noTrace, ErrNotFound) {
		t.Fatal("matching must ignore Message and TraceID")
	}
}

// errors.Is must see through wrapping on both sides.
func TestSentinels_SeeThroughWrapping(t *testing.T) {
	safe := &SafeError{Code: CodeNotFound, Message: "resource not found"}
	wrapped := fmt.Errorf("handler.GetUser: %w", fmt.Errorf("repo: %w", safe))

	if !errors.Is(wrapped, ErrNotFound) {
		t.Fatal("errors.Is must unwrap the chain")
	}
	if !HasCode(wrapped, CodeNotFound) {
		t.Fatal("HasCode must unwrap the chain")
	}
}

// A different code must not match.
func TestSentinels_DifferentCodeDoesNotMatch(t *testing.T) {
	dup := &SafeError{Code: CodeDuplicateItem}
	if errors.Is(dup, ErrNotFound) {
		t.Fatal("different codes must not match")
	}
	if errors.Is(dup, ErrTimeout) || errors.Is(dup, ErrAuthFailed) {
		t.Fatal("different codes must not match")
	}
}

// A non-SafeError target must not match.
func TestSentinels_NonSafeErrorTarget(t *testing.T) {
	safe := &SafeError{Code: CodeDuplicateItem}
	if errors.Is(safe, errors.New("cannot duplicate item")) {
		t.Fatal("an unrelated error must not match a Code")
	}
	if safe.Is(errors.New("DUPLICATE_ITEM")) {
		t.Fatal("Is must require a *SafeError target")
	}
	if safe.Is(nil) {
		t.Fatal("Is(nil) must be false")
	}
}

func TestSafeError_IsNilReceiver(t *testing.T) {
	var e *SafeError
	if e.Is(ErrNotFound) {
		t.Fatal("a nil *SafeError must not match anything")
	}
}

// The two fallback errors share CodeInternal and must therefore match, which is
// what lets callers funnel every unknown failure through one branch.
func TestSentinels_FallbacksMatch(t *testing.T) {
	fallback := &SafeError{Code: CodeInternal, Message: DefaultMessage}
	if !errors.Is(fallback, ErrInternal) {
		t.Fatal("two INTERNAL_ERROR values must match")
	}
}

// A zero-value SafeError has an empty Code; it must not match a real code.
func TestSentinels_ZeroValueMatchesNothing(t *testing.T) {
	zero := &SafeError{}
	if errors.Is(zero, ErrInternal) || errors.Is(zero, ErrNotFound) {
		t.Fatal("a zero-value SafeError must not match a real sentinel")
	}
	if zero.HTTPStatus() != 500 {
		t.Fatalf("a zero-value SafeError should degrade to 500, got %d", zero.HTTPStatus())
	}
}

func TestHasCode(t *testing.T) {
	err := &SafeError{Code: CodePermissionDenied, Message: "insufficient permissions"}
	if !HasCode(err, CodePermissionDenied) {
		t.Fatal("HasCode should match")
	}
	if HasCode(err, CodeNotFound) {
		t.Fatal("HasCode matched the wrong code")
	}
	if HasCode(errors.New("plain"), CodeNotFound) {
		t.Fatal("HasCode must not match a plain error")
	}
	if HasCode(nil, CodeNotFound) {
		t.Fatal("HasCode(nil) must be false")
	}
}

// The security property: a SafeError must not expose the original error, so
// walking the chain cannot recover the driver's message.
func TestSafeError_DoesNotLeakOriginalThroughChain(t *testing.T) {
	original := errors.New(`duplicate key value violates unique constraint "users_email_key"`)
	safe := &SafeError{Code: CodeDuplicateItem, Message: "cannot duplicate item", TraceID: "t1"}

	if errors.Unwrap(safe) != nil {
		t.Fatal("SafeError must not unwrap to the original error")
	}
	if errors.Is(safe, original) {
		t.Fatal("SafeError must not match the original error")
	}

	// SafeError must not expose an Unwrap method at all. If it ever grows one,
	// it must not point at the driver's error.
	var node any = safe
	if _, ok := node.(interface{ Unwrap() error }); ok {
		t.Fatal("SafeError must not implement Unwrap")
	}
	if _, ok := node.(interface{ Unwrap() []error }); ok {
		t.Fatal("SafeError must not implement multi-Unwrap")
	}

	// Nothing on the response value carries the constraint name.
	for _, s := range []*SafeError{safe, ErrDuplicateItem} {
		if strings.Contains(s.Error(), "users_email_key") {
			t.Fatalf("constraint name reachable via %q", s.Error())
		}
	}
}

// A SafeError is still usable as an error value in a return statement.
func TestSafeError_UsableAsError(t *testing.T) {
	var err error = &SafeError{Code: CodeNotFound, Message: "resource not found", TraceID: "t1"}

	wrapped := fmt.Errorf("service.GetUser: %w", err)
	if wrapped.Error() == "" {
		t.Fatal("wrapped error must have a message")
	}
	if !HasCode(wrapped, CodeNotFound) {
		t.Fatal("wrapping a SafeError must preserve the Code")
	}
}
