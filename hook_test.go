package dberr

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// collectUnmapped installs a hook that records events and restores the previous
// hook afterwards.
func collectUnmapped(t *testing.T) *[]UnmappedEvent {
	t.Helper()
	var events []UnmappedEvent
	SetUnmappedHook(func(e UnmappedEvent) { events = append(events, e) })
	t.Cleanup(func() { SetUnmappedHook(nil) })
	return &events
}

func TestUnmappedHook_FiresForUnmappedCode(t *testing.T) {
	discardLogger(t)
	events := collectUnmapped(t)

	r := NewRegistry("postgres", map[string]SafeError{
		"23505": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
	})
	raw := errors.New(`relation "secret_table" does not exist`)
	safe := r.Resolve(context.Background(), "42P07", raw, "t1")

	if safe.Code != CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}

	got := *events
	if len(got) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(got))
	}
	e := got[0]
	if e.Source != "postgres" {
		t.Errorf("Source = %q", e.Source)
	}
	if e.Code != "42P07" {
		t.Errorf("Code = %q, want 42P07 — this is the value to add to the allowlist", e.Code)
	}
	if e.TraceID != "t1" {
		t.Errorf("TraceID = %q", e.TraceID)
	}
	if e.Err == nil || !strings.Contains(e.Err.Error(), "secret_table") {
		t.Errorf("Err should carry the original error, got %v", e.Err)
	}
}

// A mapped code must not fire the hook: the hook exists to find gaps, not to
// count every error.
func TestUnmappedHook_DoesNotFireForMappedCode(t *testing.T) {
	discardLogger(t)
	events := collectUnmapped(t)

	r := NewRegistry("postgres", map[string]SafeError{
		"23505": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
	})
	r.Resolve(context.Background(), "23505", errors.New("dup"), "t1")

	if n := len(*events); n != 0 {
		t.Fatalf("hook fired %d times for a mapped code", n)
	}
}

// An unrecognized error type fires too, with Source "unknown" and an empty
// Code, so a hook can tell the two cases apart.
func TestUnmappedHook_FiresForUnrecognizedType(t *testing.T) {
	discardLogger(t)
	events := collectUnmapped(t)

	Unrecognized(context.Background(), errors.New("boom"), "t2")

	got := *events
	if len(got) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(got))
	}
	if got[0].Source != "unknown" {
		t.Errorf("Source = %q, want \"unknown\"", got[0].Source)
	}
	if got[0].Code != "" {
		t.Errorf("Code = %q, want empty for an unrecognized type", got[0].Code)
	}
}

func TestSetUnmappedHook_NilRemovesHook(t *testing.T) {
	discardLogger(t)
	called := false
	SetUnmappedHook(func(UnmappedEvent) { called = true })
	t.Cleanup(func() { SetUnmappedHook(nil) })

	SetUnmappedHook(nil)

	r := NewRegistry("testsrc", nil)
	r.Resolve(context.Background(), "1", errors.New("boom"), "t1")

	if called {
		t.Fatal("hook still ran after SetUnmappedHook(nil)")
	}
}

// A panicking hook must not take down the request that triggered it.
func TestUnmappedHook_PanicIsRecovered(t *testing.T) {
	buf := captureLogger(t)

	SetUnmappedHook(func(UnmappedEvent) { panic("metrics client exploded") })
	t.Cleanup(func() { SetUnmappedHook(nil) })

	r := NewRegistry("testsrc", nil)
	var safe *SafeError

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("panic escaped dberr: %v", rec)
			}
		}()
		safe = r.Resolve(context.Background(), "1", errors.New("boom"), "t1")
	}()

	if safe.Code != CodeInternal {
		t.Fatalf("unexpected: %+v", safe)
	}
	if !strings.Contains(buf.String(), "unmapped hook panicked") {
		t.Fatalf("expected a log record about the panicking hook: %s", buf.String())
	}
}

// Hooks must be safe to call concurrently, since Resolve is.
func TestUnmappedHook_Concurrent(t *testing.T) {
	discardLogger(t)
	var mu sync.Mutex
	count := 0
	SetUnmappedHook(func(UnmappedEvent) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	t.Cleanup(func() { SetUnmappedHook(nil) })

	r := NewRegistry("testsrc", nil)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Resolve(context.Background(), "1", errors.New("boom"), "t1")
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if count != 64 {
		t.Fatalf("hook ran %d times, want 64", count)
	}
}

// The redaction markers must apply to what the hook sees too, otherwise the
// hook becomes a second, easier leak path.
func TestUnmappedHook_ErrIsUnredactedByDesign(t *testing.T) {
	discardLogger(t)
	events := collectUnmapped(t)

	r := NewRegistry("testsrc", nil)
	raw := errors.New("secret@example.com")
	r.Resolve(context.Background(), "1", raw, "t1")

	got := *events
	if len(got) != 1 {
		t.Fatalf("hook fired %d times", len(got))
	}
	// Err is the original error, unsanitized. This is documented, and it is why
	// the hook must not forward Err anywhere the client can see.
	if !strings.Contains(got[0].Err.Error(), "secret@example.com") {
		t.Fatalf("expected Err to be the original error: %v", got[0].Err)
	}
}
