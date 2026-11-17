package dberr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// captureLogger installs a logger writing into the returned builder on Default,
// and restores the previous one when the test ends.
func captureLogger(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	prev := Default.Logger()
	t.Cleanup(func() { Default.SetLogger(prev) })
	Default.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	return &buf
}

// discardLogger silences the library's own logging for the duration of a test.
func discardLogger(t *testing.T) {
	t.Helper()
	prev := Default.Logger()
	t.Cleanup(func() { Default.SetLogger(prev) })
	Default.SetLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
}

func TestSafeError_Error(t *testing.T) {
	e := &SafeError{Code: CodeDuplicateItem, Message: "cannot duplicate item"}
	if e.Error() != "cannot duplicate item" {
		t.Fatalf("Error() = %q", e.Error())
	}
}

func TestSafeError_JSONOmitsEmptyTraceID(t *testing.T) {
	b, err := json.Marshal(&SafeError{Code: CodeNotFound, Message: "resource not found"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "trace_id") {
		t.Fatalf("trace_id should be omitted when empty: %s", b)
	}

	b, err = json.Marshal(&SafeError{Code: CodeNotFound, Message: "x", TraceID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"trace_id":"t1"`) {
		t.Fatalf("trace_id missing: %s", b)
	}
}

func TestNewRegistry_ResolvesKnownCode(t *testing.T) {
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
	})
	got := r.Resolve(context.Background(), "1", errors.New("raw detail"), "t1")
	if got.Code != CodeDuplicateItem || got.Message != "cannot duplicate item" {
		t.Fatalf("unexpected: %+v", got)
	}
	if got.TraceID != "t1" {
		t.Fatalf("trace id not set: %+v", got)
	}
}

func TestNewRegistry_UnknownCodeIsDefaultDeny(t *testing.T) {
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
	})
	got := r.Resolve(context.Background(), "999", errors.New("secret constraint name"), "t2")
	if got.Code != CodeInternal || got.Message != DefaultMessage {
		t.Fatalf("expected default-deny fallback, got %+v", got)
	}
	// The unsafe raw message must not survive into the result.
	if strings.Contains(got.Message, "secret") {
		t.Fatalf("raw message leaked: %+v", got)
	}
}

func TestNewRegistry_Source(t *testing.T) {
	if got := NewRegistry("testsrc", nil).Source(); got != "testsrc" {
		t.Fatalf("Source() = %q", got)
	}
}

func TestNewRegistry_CopiesInputMap(t *testing.T) {
	codes := map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
	}
	r := NewRegistry("testsrc", codes)

	// Mutating the caller's map must not affect the registry.
	codes["1"] = SafeError{Code: CodeInternal, Message: "tampered"}

	got := r.Resolve(context.Background(), "1", nil, "t1")
	if got.Code != CodeDuplicateItem {
		t.Fatalf("registry aliases the caller's map: %+v", got)
	}
}

func TestNewRegistry_ResolveIsRepeatable(t *testing.T) {
	discardLogger(t)
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "resource not found"},
	})
	for i := 0; i < 3; i++ {
		got := r.Resolve(context.Background(), "1", nil, "t1")
		if got.Code != CodeNotFound || got.TraceID != "t1" {
			t.Fatalf("iteration %d: %+v", i, got)
		}
	}
}

func TestUnrecognized(t *testing.T) {
	discardLogger(t)
	got := Unrecognized(context.Background(), errors.New("boom"), "t3", "status", 500)
	if got.Code != CodeInternal || got.Message != DefaultMessage {
		t.Fatalf("unexpected: %+v", got)
	}
	if got.TraceID != "t3" {
		t.Fatalf("trace id not set: %+v", got)
	}
}

func TestUnrecognized_NilError(t *testing.T) {
	discardLogger(t)
	got := Unrecognized(context.Background(), nil, "t4")
	if got.Code != CodeInternal {
		t.Fatalf("unexpected: %+v", got)
	}
}

// The registry is a read-only value after init, so concurrent Resolve calls
// must be race-free. Run with -race.
func TestRegistry_Lookup(t *testing.T) {
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "cannot duplicate item", TraceID: "stale"},
	})

	got, known := r.Lookup("1")
	if !known {
		t.Fatal("expected code to be known")
	}
	// NewRegistry strips TraceID, so a lookup can never leak one entry's trace
	// into another's result.
	if got.TraceID != "" {
		t.Fatalf("Lookup returned a trace id: %+v", got)
	}

	if _, known := r.Lookup("999"); known {
		t.Fatal("unknown code reported as known")
	}
}

func TestRegistry_Codes(t *testing.T) {
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "a"},
		"2": {Code: CodeNotFound, Message: "b"},
		"3": {Code: CodeTimeout, Message: "c"},
	})
	codes := r.Codes()
	if len(codes) != 3 {
		t.Fatalf("Codes() returned %d entries, want 3: %v", len(codes), codes)
	}

	seen := map[string]bool{}
	for _, code := range codes {
		seen[code] = true
		if _, known := r.Lookup(code); !known {
			t.Errorf("Codes() returned %q which is not in the registry", code)
		}
	}
	for _, want := range []string{"1", "2", "3"} {
		if !seen[want] {
			t.Errorf("Codes() is missing %q: %v", want, codes)
		}
	}
}

func TestRegistry_CodesEmpty(t *testing.T) {
	if got := NewRegistry("testsrc", nil).Codes(); len(got) != 0 {
		t.Fatalf("Codes() on an empty registry = %v, want empty", got)
	}
}

func TestRegistry_ConcurrentResolve(t *testing.T) {
	discardLogger(t)
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
		"2": {Code: CodeNotFound, Message: "resource not found"},
	})

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code := "1"
			want := CodeDuplicateItem
			if i%2 == 0 {
				code, want = "2", CodeNotFound
			}
			if got := r.Resolve(context.Background(), code, nil, "t"); got.Code != want {
				t.Errorf("got %q want %q", got.Code, want)
			}
		}(i)
	}
	wg.Wait()
}

func TestSetLogger_ReceivesRawError(t *testing.T) {
	var buf strings.Builder
	SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { SetLogger(nil) })

	Unrecognized(context.Background(), fmt.Errorf("wrapped: %w", errors.New("constraint users_pkey")), "t5")

	out := buf.String()
	for _, want := range []string{"constraint users_pkey", "t5", "level=ERROR"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log record missing %q: %s", want, out)
		}
	}
}

// SetLogger(nil) must not leave a nil logger behind, which would panic on the
// next translate.
func TestSetLogger_NilIsSafe(t *testing.T) {
	t.Cleanup(func() { SetLogger(nil) })

	SetLogger(nil)
	if logger.Load() == nil {
		t.Fatal("SetLogger(nil) installed a nil logger")
	}

	// Must not panic.
	fallback := Unrecognized(context.Background(), errors.New("boom"), "t6")
	if fallback.Code != CodeInternal {
		t.Fatalf("unexpected: %+v", fallback)
	}
}
