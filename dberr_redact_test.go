package dberr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// A wrapped error must reach the log intact. The wrapping text is what tells
// you which call site failed, and dberr used to drop it by logging only the
// driver's own message.
func TestResolve_LogsWrappingContext(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
	})

	inner := errors.New(`duplicate key value violates unique constraint "users_email_key"`)
	wrapped := fmt.Errorf("repo.InsertUser: %w", fmt.Errorf("pool exec: %w", inner))

	r.Resolve(context.Background(), "1", wrapped, "t1")

	out := buf.String()
	for _, want := range []string{"repo.InsertUser", "pool exec", "users_email_key", "t1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log record missing %q: %s", want, out)
		}
	}
}

// Redact replaces each marker with ***. It is substring-based, so the marker
// must match the sensitive value itself — redacting "email=" would remove the
// label and leave the address intact.
func TestRegistry_Redact(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
	})
	r.Redact("admin@example.com", "hunter2")

	inner := errors.New("violates unique constraint")
	wrapped := fmt.Errorf("repo.InsertUser(email=%s, password=%s): %w",
		"admin@example.com", "hunter2", inner)

	r.Resolve(context.Background(), "1", wrapped, "t1")

	out := buf.String()
	for _, leaked := range []string{"admin@example.com", "hunter2"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("redaction failed, %q still present: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("expected redaction markers: %s", out)
	}
	// The non-sensitive context must survive, since that is the part that
	// identifies the failing call site.
	for _, keep := range []string{"repo.InsertUser", "violates unique constraint"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("redaction removed useful context %q: %s", keep, out)
		}
	}
}

// A marker redacts every occurrence.
func TestRegistry_RedactReplacesAllOccurrences(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", nil)
	r.Redact("admin@example.com")

	r.Resolve(context.Background(), "1",
		errors.New("insert admin@example.com then update admin@example.com"), "t1")

	if strings.Contains(buf.String(), "admin@example.com") {
		t.Fatalf("only some occurrences were redacted: %s", buf.String())
	}
	if n := strings.Count(buf.String(), "***"); n != 2 {
		t.Fatalf("expected 2 redactions, got %d: %s", n, buf.String())
	}
}

// Redact is additive: several calls accumulate.
func TestRegistry_RedactAdditive(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", nil)
	r.Redact("a@b")
	r.Redact("xyz")

	r.Resolve(context.Background(), "1", errors.New("email=a@b token=xyz"), "t1")

	out := buf.String()
	if strings.Contains(out, "a@b") || strings.Contains(out, "xyz") {
		t.Fatalf("additive redaction failed: %s", out)
	}
}

// An empty marker would match at every position, so it is ignored.
func TestRegistry_RedactIgnoresEmptyMarker(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", nil)
	r.Redact("", "x=")

	r.Resolve(context.Background(), "1", errors.New("hello world"), "t1")

	out := buf.String()
	if strings.Contains(out, "***") {
		t.Fatalf("empty marker redacted something: %s", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Fatalf("redaction mangled unrelated text: %s", out)
	}
}

// A marker that does not appear must leave the message untouched.
func TestRegistry_RedactUnmatchedMarker(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", nil)
	r.Redact("never-appears")

	r.Resolve(context.Background(), "1", errors.New("hello world"), "t1")

	if !strings.Contains(buf.String(), "hello world") {
		t.Fatalf("unexpected: %s", buf.String())
	}
}

// Redact used to be documented as setup-only and unsafe to call concurrently.
// It is now guarded by a RWMutex, so calling it while Translate runs is safe.
// Run with -race: this test is what proves it.
func TestRegistry_RedactIsConcurrencySafe(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "resource not found"},
	})

	var wg sync.WaitGroup
	const writers, readers = 8, 64

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Redact(fmt.Sprintf("marker%d=", i))
		}(i)
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Resolve(context.Background(), "1", errors.New("boom marker1=x"), "t1")
			r.Lookup("1")
			r.Codes()
		}()
	}
	wg.Wait()

	// Redaction must still have taken effect.
	if !strings.Contains(buf.String(), "***") {
		t.Fatalf("expected redaction to apply: %s", buf.String())
	}
}

// Concurrent Resolve with a stable marker set: the common production shape.
func TestRegistry_ConcurrentResolveWithRedaction(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "resource not found"},
	})
	r.Redact("secret@example.com")

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Resolve(context.Background(), "1",
				errors.New("repo: secret@example.com"), "t1")
		}()
	}
	wg.Wait()

	if strings.Contains(buf.String(), "secret@example.com") {
		t.Fatal("redaction did not apply under concurrency")
	}
}

// A nil error must not panic.
func TestResolve_NilError(t *testing.T) {
	buf := captureLogger(t)
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "resource not found"},
	})
	safe := r.Resolve(context.Background(), "1", nil, "t1")
	if safe.Code != CodeNotFound {
		t.Fatalf("unexpected: %+v", safe)
	}
	if strings.Contains(buf.String(), "error=null") {
		t.Fatalf("nil error should log an empty string: %s", buf.String())
	}
}

// Redaction applies to the response only in the sense that the response is
// always the registered message — a test that says so explicitly.
func TestResolve_RedactDoesNotChangeResponse(t *testing.T) {
	captureLogger(t)
	r := NewRegistry("testsrc", map[string]SafeError{
		"1": {Code: CodeDuplicateItem, Message: "cannot duplicate item"},
	})
	r.Redact("email=")

	safe := r.Resolve(context.Background(), "1", errors.New("email=a@b"), "t1")
	if safe.Message != "cannot duplicate item" {
		t.Fatalf("unexpected: %+v", safe)
	}
}
