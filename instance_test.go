package dberr

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// A fresh Instance must be independent of Default, so one program can run two
// policies at once.
func TestInstance_IndependentOfDefault(t *testing.T) {
	buf := &strings.Builder{}
	inst := New(WithLogger(slog.New(slog.NewTextHandler(buf, nil))))

	r := NewRegistryWith(inst, "testsrc", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "resource not found"},
	})
	r.Resolve(context.Background(), "1", errors.New("boom"), "t1")

	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("instance logger did not receive the record: %s", buf.String())
	}
}

func TestInstance_DefaultOptions(t *testing.T) {
	// No options: a usable logger, no hook.
	inst := New()
	if inst.Logger() == nil {
		t.Fatal("New() must install a usable logger")
	}
	// Must not panic with no hook installed.
	inst.emitUnmapped(context.Background(), "testsrc", "1", "t1", errors.New("boom"))
}

// A nil logger must not silently disable logging.
func TestInstance_WithLoggerIgnoresNil(t *testing.T) {
	inst := New()
	before := inst.Logger()
	inst = New(WithLogger(nil))
	if inst.Logger() == nil {
		t.Fatal("WithLogger(nil) must not install a nil logger")
	}
	if before == nil {
		t.Fatal("unexpected")
	}
}

func TestInstance_SetLogger(t *testing.T) {
	inst := New()
	buf := &strings.Builder{}
	inst.SetLogger(slog.New(slog.NewTextHandler(buf, nil)))
	inst.log(context.Background(), slog.LevelWarn, "hello")

	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("SetLogger had no effect: %s", buf.String())
	}

	// nil restores the default logger rather than breaking.
	inst.SetLogger(nil)
	if inst.Logger() == nil {
		t.Fatal("SetLogger(nil) installed a nil logger")
	}
}

func TestInstance_UnmappedHook(t *testing.T) {
	inst := New()
	var count atomic.Int32
	inst.SetUnmappedHook(func(UnmappedEvent) { count.Add(1) })

	inst.emitUnmapped(context.Background(), "testsrc", "1", "t1", errors.New("boom"))
	if count.Load() != 1 {
		t.Fatalf("hook ran %d times, want 1", count.Load())
	}

	inst.SetUnmappedHook(nil)
	inst.emitUnmapped(context.Background(), "testsrc", "2", "t1", errors.New("boom"))
	if count.Load() != 1 {
		t.Fatal("hook still runs after being removed")
	}
}

func TestInstance_HookPanicIsRecovered(t *testing.T) {
	buf := &strings.Builder{}
	inst := New(WithLogger(slog.New(slog.NewTextHandler(buf, nil))))
	inst.SetUnmappedHook(func(UnmappedEvent) { panic("boom") })

	inst.emitUnmapped(context.Background(), "testsrc", "1", "t1", errors.New("x"))

	if !strings.Contains(buf.String(), "unmapped hook panicked") {
		t.Fatalf("expected a log record: %s", buf.String())
	}
}

// Two instances must not interfere: this is the whole point, and the reason
// dberr stopped relying solely on package globals.
func TestInstance_TwoPoliciesDoNotInterfere(t *testing.T) {
	tenantA := &strings.Builder{}
	tenantB := &strings.Builder{}

	instA := New(WithLogger(slog.New(slog.NewTextHandler(tenantA, nil))))
	instB := New(WithLogger(slog.New(slog.NewTextHandler(tenantB, nil))))

	var hookA, hookB atomic.Int32
	instA.SetUnmappedHook(func(UnmappedEvent) { hookA.Add(1) })
	instB.SetUnmappedHook(func(UnmappedEvent) { hookB.Add(1) })

	rA := NewRegistryWith(instA, "testsrc", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "resource not found"},
	})
	rB := NewRegistryWith(instB, "testsrc", map[string]SafeError{
		"1": {Code: CodeNotFound, Message: "resource not found"},
	})
	rA.Redact("alpha")
	rB.Redact("beta")

	instA.log(context.Background(), slog.LevelWarn, "from-a")
	instB.log(context.Background(), slog.LevelWarn, "from-b")

	if strings.Contains(tenantA.String(), "from-b") {
		t.Fatal("instance B wrote to instance A's logger")
	}
	if strings.Contains(tenantB.String(), "from-a") {
		t.Fatal("instance A wrote to instance B's logger")
	}

	// Redaction is per registry.
	rA.Resolve(context.Background(), "1", errors.New("alpha-secret"), "t1")
	rB.Resolve(context.Background(), "1", errors.New("beta-secret"), "t1")
	if !strings.Contains(tenantA.String(), "***") {
		t.Fatalf("A redaction missing: %s", tenantA.String())
	}
	if strings.Contains(tenantB.String(), "beta-secret") {
		t.Fatalf("B redaction missing: %s", tenantB.String())
	}
	if hookA.Load() != 0 || hookB.Load() != 0 {
		t.Fatal("hooks fired for mapped codes")
	}
}

// Concurrent use of one Instance is safe.
func TestInstance_ConcurrentUse(t *testing.T) {
	inst := New()
	var hookCalls atomic.Int32
	inst.SetUnmappedHook(func(UnmappedEvent) { hookCalls.Add(1) })

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst.log(context.Background(), slog.LevelWarn, "x")
			inst.emitUnmapped(context.Background(), "s", "c", "t", errors.New("e"))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		inst.SetLogger(slog.Default())
		inst.SetUnmappedHook(nil)
	}()
	wg.Wait()
}

// The package-level helpers must still work and still target Default.
func TestDefault_PackageHelpersTargetDefault(t *testing.T) {
	buf := captureLogger(t)

	var got atomic.Int32
	SetUnmappedHook(func(UnmappedEvent) { got.Add(1) })
	t.Cleanup(func() { SetUnmappedHook(nil) })

	Unrecognized(context.Background(), errors.New("boom"), "t1")

	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("SetLogger did not target Default: %s", buf.String())
	}
	if got.Load() != 1 {
		t.Fatalf("hook ran %d times, want 1", got.Load())
	}
}
