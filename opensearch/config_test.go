package opensearch

import (
	"context"
	"testing"

	"github.com/fonteeboa/go-errshield"
)

func TestSanitizeStatusCode_Valid(t *testing.T) {
	tests := []int{200, 404, 500, 599}
	for _, tc := range tests {
		if got := sanitizeStatusCode(tc); got != tc {
			t.Errorf("sanitizeStatusCode(%d) = %d, want %d", tc, got, tc)
		}
	}
}

func TestSanitizeStatusCode_Invalid(t *testing.T) {
	tests := []int{-1, 0, 99, 600, 999}
	for _, tc := range tests {
		if got := sanitizeStatusCode(tc); got != 500 {
			t.Errorf("sanitizeStatusCode(%d) = %d, want 500", tc, got)
		}
	}
}

func TestTranslateResponse_InvalidStatusCode(t *testing.T) {
	body := []byte("{\"error\":{\"type\":\"version_conflict_engine_exception\",\"reason\":\"test\"}}")

	safe := TranslateResponse(context.Background(), body, -1, "t1")
	if safe.Code != dberr.CodeConcurrentUpdate {
		t.Fatalf("expected CodeConcurrentUpdate, got %s", safe.Code)
	}
}

func TestTranslateResponse_StatusCodeBoundary(t *testing.T) {
	body := []byte("{\"error\":{\"type\":\"index_not_found_exception\",\"reason\":\"test\"}}")

	safe := TranslateResponse(context.Background(), body, 599, "t1")
	if safe.Code != dberr.CodeNotFound {
		t.Fatalf("expected CodeNotFound, got %s", safe.Code)
	}
}

func TestTranslateResponse_StatusCodeTooHigh(t *testing.T) {
	body := []byte("{\"error\":{\"type\":\"index_not_found_exception\",\"reason\":\"test\"}}")

	safe := TranslateResponse(context.Background(), body, 1000, "t1")
	if safe.Code != dberr.CodeNotFound {
		t.Fatalf("expected CodeNotFound, got %s", safe.Code)
	}
}

func TestMinStatusCode_CanBeModified(t *testing.T) {
	orig := MinStatusCode()
	SetMinStatusCode(200)
	defer SetMinStatusCode(orig)

	if got := sanitizeStatusCode(100); got != 500 {
		t.Errorf("sanitizeStatusCode(100) = %d, want 500 when MinStatusCode=200", got)
	}
}

func TestMaxStatusCode_CanBeModified(t *testing.T) {
	orig := MaxStatusCode()
	SetMaxStatusCode(400)
	defer SetMaxStatusCode(orig)

	if got := sanitizeStatusCode(500); got != 500 {
		t.Errorf("sanitizeStatusCode(500) = %d, want 500 when MaxStatusCode=400", got)
	}
}

func TestDefaultStatusCodeRange(t *testing.T) {
	if DefaultMinStatusCode != 100 {
		t.Errorf("DefaultMinStatusCode = %d, want 100", DefaultMinStatusCode)
	}
	if DefaultMaxStatusCode != 599 {
		t.Errorf("DefaultMaxStatusCode = %d, want 599", DefaultMaxStatusCode)
	}
}
