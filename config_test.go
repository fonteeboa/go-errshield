package dberr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestSanitizeTraceID_WithinLimit(t *testing.T) {
	original := MaxTraceIDLength()
	SetMaxTraceIDLength(256)
	defer SetMaxTraceIDLength(original)

	traceID := strings.Repeat("a", 100)
	got := sanitizeTraceID(traceID)
	if got != traceID {
		t.Fatalf("expected unchanged traceID, got %d chars", len(got))
	}
}

func TestSanitizeTraceID_ExceedsLimit(t *testing.T) {
	original := MaxTraceIDLength()
	SetMaxTraceIDLength(256)
	defer SetMaxTraceIDLength(original)

	traceID := strings.Repeat("a", 300)
	got := sanitizeTraceID(traceID)
	if len(got) != 256 {
		t.Fatalf("expected 256 chars, got %d", len(got))
	}
}

func TestSanitizeTraceID_Empty(t *testing.T) {
	got := sanitizeTraceID("")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestSetMaxTraceIDLength_ZeroDisablesTruncation(t *testing.T) {
	original := MaxTraceIDLength()
	SetMaxTraceIDLength(0)
	defer SetMaxTraceIDLength(original)

	traceID := strings.Repeat("a", 1000)
	got := sanitizeTraceID(traceID)
	if len(got) != 1000 {
		t.Fatalf("expected no truncation, got %d chars", len(got))
	}
}

func TestSetMaxTraceIDLength_NegativeDisablesTruncation(t *testing.T) {
	original := MaxTraceIDLength()
	SetMaxTraceIDLength(-1)
	defer SetMaxTraceIDLength(original)

	traceID := strings.Repeat("a", 1000)
	got := sanitizeTraceID(traceID)
	if len(got) != 1000 {
		t.Fatalf("expected no truncation, got %d chars", len(got))
	}
}

func TestDefaultMaxTraceIDLength_IsApplied(t *testing.T) {
	if DefaultMaxTraceIDLength != 256 {
		t.Fatalf("DefaultMaxTraceIDLength = %d, want 256", DefaultMaxTraceIDLength)
	}
}

func TestResolve_TruncatesLongTraceID(t *testing.T) {
	discardLogger(t)
	r := NewRegistry("test", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "not found"},
	})

	longTraceID := strings.Repeat("x", 500)
	safe := r.Resolve(context.Background(), "1", errors.New("test"), longTraceID)

	if len(safe.TraceID) > MaxTraceIDLength() {
		t.Fatalf("TraceID not truncated: got %d, max %d", len(safe.TraceID), MaxTraceIDLength())
	}
}

func TestRegistry_SetMaxTraceIDLength_OverridesProcessWide(t *testing.T) {
	discardLogger(t)
	r := NewRegistry("test", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "not found"},
	})

	r.SetMaxTraceIDLength(10)
	safe := r.Resolve(context.Background(), "1", errors.New("test"), "1234567890ABCDEF")

	if len(safe.TraceID) != 10 {
		t.Fatalf("expected TraceID truncated to 10, got %d", len(safe.TraceID))
	}
}

func TestRegistry_SetMaxTraceIDLength_NegativeRestoresProcessWide(t *testing.T) {
	discardLogger(t)
	original := MaxTraceIDLength()
	SetMaxTraceIDLength(20)
	defer SetMaxTraceIDLength(original)

	r := NewRegistry("test", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "not found"},
	})

	r.SetMaxTraceIDLength(-1)
	safe := r.Resolve(context.Background(), "1", errors.New("test"), strings.Repeat("x", 50))

	if len(safe.TraceID) != 20 {
		t.Fatalf("expected process-wide limit of 20, got %d", len(safe.TraceID))
	}
}

func TestRedact_MarkersWithinLimit(t *testing.T) {
	original := MaxRedactMarkers()
	SetMaxRedactMarkers(1000)
	defer SetMaxRedactMarkers(original)

	discardLogger(t)
	r := NewRegistry("test", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "dup"},
	})

	for i := 0; i < 500; i++ {
		r.Redact(fmt.Sprintf("marker%d=", i))
	}

	if len(r.redact) != 500 {
		t.Fatalf("expected 500 markers, got %d", len(r.redact))
	}
}

func TestRedact_MarkersExceedsLimit(t *testing.T) {
	original := MaxRedactMarkers()
	SetMaxRedactMarkers(100)
	defer SetMaxRedactMarkers(original)

	discardLogger(t)
	r := NewRegistry("test", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "dup"},
	})

	for i := 0; i < 150; i++ {
		r.Redact(fmt.Sprintf("marker%d=", i))
	}

	if len(r.redact) != 100 {
		t.Fatalf("expected 100 markers (capped), got %d", len(r.redact))
	}
}

func TestSetMaxRedactMarkers_ZeroDisablesCap(t *testing.T) {
	original := MaxRedactMarkers()
	SetMaxRedactMarkers(0)
	defer SetMaxRedactMarkers(original)

	r := NewRegistry("test", nil)
	for i := 0; i < 200; i++ {
		r.Redact(fmt.Sprintf("m%d=", i))
	}

	if len(r.redact) != 200 {
		t.Fatalf("expected no cap, got %d markers", len(r.redact))
	}
}

func TestRegistry_SetMaxMarkers_OverridesProcessWide(t *testing.T) {
	original := MaxRedactMarkers()
	SetMaxRedactMarkers(1000)
	defer SetMaxRedactMarkers(original)

	r := NewRegistry("test", nil)
	r.SetMaxMarkers(3)

	for i := 0; i < 10; i++ {
		r.Redact(fmt.Sprintf("m%d=", i))
	}

	if len(r.redact) != 3 {
		t.Fatalf("expected 3 markers, got %d", len(r.redact))
	}
}

func TestRegistry_SetMaxMarkers_NegativeRestoresProcessWide(t *testing.T) {
	original := MaxRedactMarkers()
	SetMaxRedactMarkers(5)
	defer SetMaxRedactMarkers(original)

	r := NewRegistry("test", nil)
	r.SetMaxMarkers(-1)

	for i := 0; i < 10; i++ {
		r.Redact(fmt.Sprintf("m%d=", i))
	}

	if len(r.redact) != 5 {
		t.Fatalf("expected process-wide limit of 5, got %d", len(r.redact))
	}
}

func TestDefaultMaxRedactMarkers_IsApplied(t *testing.T) {
	if DefaultMaxRedactMarkers != 1000 {
		t.Fatalf("DefaultMaxRedactMarkers = %d, want 1000", DefaultMaxRedactMarkers)
	}
}

func TestInstance_RedactInHook_DisabledByDefault(t *testing.T) {
	inst := New()
	if inst.RedactInHook() {
		t.Fatal("RedactInHook should be disabled by default")
	}
}

func TestInstance_RedactInHook_CanBeEnabled(t *testing.T) {
	discardLogger(t)
	inst := New()

	var gotEvent UnmappedEvent
	inst.SetUnmappedHook(func(e UnmappedEvent) {
		gotEvent = e
	})

	inst.SetRedactInHook(true)

	if !inst.RedactInHook() {
		t.Fatal("RedactInHook should be enabled after SetRedactInHook(true)")
	}

	r := NewRegistryWith(inst, "test", nil)
	r.Resolve(context.Background(), "1", errors.New("secret=data"), "t1")

	if gotEvent.RedactedErr == nil {
		t.Fatal("expected RedactedErr to be populated when RedactInHook is enabled")
	}
}

func TestInstance_RedactInHook_DisabledMeansNilRedactedErr(t *testing.T) {
	discardLogger(t)
	inst := New()

	var gotEvent UnmappedEvent
	inst.SetUnmappedHook(func(e UnmappedEvent) {
		gotEvent = e
	})

	r := NewRegistryWith(inst, "test", nil)
	r.Resolve(context.Background(), "1", errors.New("secret=data"), "t1")

	if gotEvent.RedactedErr != nil {
		t.Fatal("expected RedactedErr to be nil when RedactInHook is disabled")
	}
}

func TestWithRedactInHook_Option(t *testing.T) {
	inst := New(WithRedactInHook(true))
	if !inst.RedactInHook() {
		t.Fatal("WithRedactInHook(true) did not enable redaction")
	}

	inst2 := New(WithRedactInHook(false))
	if inst2.RedactInHook() {
		t.Fatal("WithRedactInHook(false) did not disable redaction")
	}
}

func TestRedactedErr_UsesRegistryMarkers(t *testing.T) {
	discardLogger(t)
	inst := New(WithRedactInHook(true))

	var gotEvent UnmappedEvent
	inst.SetUnmappedHook(func(e UnmappedEvent) {
		gotEvent = e
	})

	r := NewRegistryWith(inst, "test", nil)
	r.Redact("hunter2")

	r.Resolve(context.Background(), "unmapped", errors.New("password=hunter2"), "t1")

	if gotEvent.RedactedErr == nil {
		t.Fatal("expected RedactedErr to be populated")
	}
	if strings.Contains(gotEvent.RedactedErr.Error(), "hunter2") {
		t.Fatalf("expected marker to be redacted, got %q", gotEvent.RedactedErr.Error())
	}
	if !strings.Contains(gotEvent.RedactedErr.Error(), "***") {
		t.Fatalf("expected redaction marker, got %q", gotEvent.RedactedErr.Error())
	}
	// The original error is still intact.
	if !strings.Contains(gotEvent.Err.Error(), "hunter2") {
		t.Fatalf("expected Err to keep the original, got %q", gotEvent.Err.Error())
	}
}

func TestRedactError_NoMarkers(t *testing.T) {
	err := errors.New("no sensitive data here")
	got := redactError(err, nil)
	if got.Error() != err.Error() {
		t.Fatalf("expected unchanged error, got %q", got.Error())
	}
}

func TestRedactError_NilError(t *testing.T) {
	if got := redactError(nil, []string{"x"}); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestConcurrentTraceIDSanitization(t *testing.T) {
	original := MaxTraceIDLength()
	SetMaxTraceIDLength(256)
	defer SetMaxTraceIDLength(original)

	discardLogger(t)
	r := NewRegistry("test", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "dup"},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			longID := strings.Repeat(fmt.Sprintf("%d", i), 500)
			safe := r.Resolve(context.Background(), "1", errors.New("test"), longID)
			if len(safe.TraceID) > MaxTraceIDLength() {
				t.Errorf("TraceID not truncated: got %d", len(safe.TraceID))
			}
		}(i)
	}
	wg.Wait()
}

func TestConcurrentRedactWithinLimit(t *testing.T) {
	original := MaxRedactMarkers()
	SetMaxRedactMarkers(200)
	defer SetMaxRedactMarkers(original)

	r := NewRegistry("test", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "dup"},
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Redact(fmt.Sprintf("m%d=", i))
		}(i)
	}
	wg.Wait()

	if len(r.redact) != 50 {
		t.Fatalf("expected 50 markers, got %d", len(r.redact))
	}
}

func TestRedactError_AppliesMarkers(t *testing.T) {
	err := errors.New("error with password=hunter2 and token=abc123")
	got := redactError(err, []string{"hunter2", "abc123"})
	gotStr := got.Error()
	if strings.Contains(gotStr, "hunter2") || strings.Contains(gotStr, "abc123") {
		t.Fatalf("expected redacted error, got %q", gotStr)
	}
	if !strings.Contains(gotStr, "***") {
		t.Fatalf("expected redaction markers, got %q", gotStr)
	}
}

func TestInstance_SetRedactInHook_Idempotent(t *testing.T) {
	discardLogger(t)
	inst := New()

	inst.SetRedactInHook(true)
	inst.SetRedactInHook(false)

	if inst.RedactInHook() {
		t.Fatal("expected RedactInHook to be false after setting false")
	}
}

func TestUnmappedEvent_RedactedErr_Field(t *testing.T) {
	discardLogger(t)
	inst := New()

	var gotEvent UnmappedEvent
	inst.SetUnmappedHook(func(e UnmappedEvent) {
		gotEvent = e
	})

	inst.SetRedactInHook(true)

	r := NewRegistryWith(inst, "test", nil)
	r.Resolve(context.Background(), "unknown_code", errors.New("test error"), "t1")

	if gotEvent.RedactedErr == nil {
		t.Fatal("expected RedactedErr to be populated")
	}
	if gotEvent.RedactedErr.Error() != "test error" {
		t.Fatalf("expected redacted error, got %q", gotEvent.RedactedErr.Error())
	}
}

func TestConcurrentMixedTraceIDLengths(t *testing.T) {
	original := MaxTraceIDLength()
	SetMaxTraceIDLength(256)
	defer SetMaxTraceIDLength(original)

	discardLogger(t)
	r := NewRegistry("test", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "dup"},
	})

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var traceID string
			if i%2 == 0 {
				traceID = strings.Repeat("x", 500)
			} else {
				traceID = "short"
			}
			safe := r.Resolve(context.Background(), "1", errors.New("test"), traceID)
			if len(safe.TraceID) > MaxTraceIDLength() {
				t.Errorf("TraceID exceeds limit: %d", len(safe.TraceID))
			}
		}(i)
	}
	wg.Wait()
}

func TestRedact_LimitBoundary(t *testing.T) {
	original := MaxRedactMarkers()
	SetMaxRedactMarkers(5)
	defer SetMaxRedactMarkers(original)

	r := NewRegistry("test", nil)

	for i := 0; i < 5; i++ {
		r.Redact(fmt.Sprintf("m%d=", i))
	}

	if len(r.redact) != 5 {
		t.Fatalf("expected 5 markers, got %d", len(r.redact))
	}

	r.Redact("extra1=", "extra2=")

	if len(r.redact) != 5 {
		t.Fatalf("expected to remain at 5 markers, got %d", len(r.redact))
	}
}

func TestSetMaxTraceIDLength_ConcurrentReads(t *testing.T) {
	original := MaxTraceIDLength()
	defer SetMaxTraceIDLength(original)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			SetMaxTraceIDLength(64 + i)
			_ = MaxTraceIDLength()
			_ = sanitizeTraceID("short")
		}(i)
	}
	wg.Wait()
}

func TestSetRedactInHook_PackageLevel(t *testing.T) {
	discardLogger(t)

	prev := Default.RedactInHook()
	t.Cleanup(func() { SetRedactInHook(prev) })

	SetRedactInHook(true)
	if !Default.RedactInHook() {
		t.Fatal("SetRedactInHook(true) did not enable redaction on Default")
	}

	var gotEvent UnmappedEvent
	SetUnmappedHook(func(e UnmappedEvent) {
		gotEvent = e
	})
	t.Cleanup(func() { SetUnmappedHook(nil) })

	r := NewRegistry("test", nil)
	r.Redact("hunter2")
	r.Resolve(context.Background(), "unmapped", errors.New("password=hunter2"), "t1")

	if gotEvent.RedactedErr == nil {
		t.Fatal("expected RedactedErr to be populated")
	}
	if strings.Contains(gotEvent.RedactedErr.Error(), "hunter2") {
		t.Fatalf("expected redaction, got %q", gotEvent.RedactedErr.Error())
	}
}

func TestTraceIDInFallbackIsTruncated(t *testing.T) {
	original := MaxTraceIDLength()
	SetMaxTraceIDLength(16)
	defer SetMaxTraceIDLength(original)

	discardLogger(t)
	got := Unrecognized(context.Background(), errors.New("boom"), strings.Repeat("z", 100))
	if len(got.TraceID) != 16 {
		t.Fatalf("expected fallback TraceID truncated to 16, got %d", len(got.TraceID))
	}
}
