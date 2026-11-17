package dberr

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
)

// An Instance is a self-contained dberr configuration: where it logs, and what
// it reports when it meets a code it does not know.
//
// The package-level functions (postgres.Translate, dberr.SetLogger, …) all
// operate on Default, an Instance created at init. That is the right thing for
// almost every program: one policy, one logger.
//
// Reach for your own Instance when one process needs more than one policy:
//
//   - a multi-tenant server that must redact differently per tenant
//   - a test binary that runs several policies in parallel
//   - a program that embeds two independent services
//
// Passing an Instance explicitly is safe from the start: it has no global state,
// so two instances cannot interfere. Instances are immutable after construction
// and safe for concurrent use.
type Instance struct {
	logger atomic.Pointer[slog.Logger]
	hook   atomic.Pointer[UnmappedHook]

	// redactInHook controls whether redaction markers are applied to the error
	// before sending it to the UnmappedHook. When false (default), the hook
	// receives the original error. When true, RedactedErr is populated with
	// the redacted version.
	redactInHook atomic.Bool
}

// New returns an Instance with the given options. With no options it logs to
// slog.Default() and reports nothing.
func New(opts ...Option) *Instance {
	inst := &Instance{}
	inst.logger.Store(slog.Default())
	for _, opt := range opts {
		opt(inst)
	}
	return inst
}

// An Option configures an Instance.
type Option func(*Instance)

// WithLogger sends the instance's log records to l. A nil logger is ignored, so
// a nil argument cannot silently disable logging.
func WithLogger(l *slog.Logger) Option {
	return func(i *Instance) {
		if l != nil {
			i.logger.Store(l)
		}
	}
}

// WithUnmappedHook installs a hook called for every unmapped code. Pass nil to
// install no hook.
func WithUnmappedHook(h UnmappedHook) Option {
	return func(i *Instance) {
		if h == nil {
			i.hook.Store(nil)
			return
		}
		i.hook.Store(&h)
	}
}

// WithRedactInHook enables redaction of the error before it reaches the
// UnmappedHook.
//
// When enabled, the hook's UnmappedEvent.RedactedErr field is populated with
// the redacted version of the error, using the markers registered on the
// source registry. Err still carries the original.
//
// This is off by default: the hook is documented as receiving the original
// error. Turn it on when the hook forwards errors to an external system
// (metrics, monitoring) that must not see sensitive values.
//
//	dberr.New(dberr.WithRedactInHook(true))
func WithRedactInHook(enabled bool) Option {
	return func(i *Instance) {
		i.redactInHook.Store(enabled)
	}
}

// Logger returns the instance's logger.
func (i *Instance) Logger() *slog.Logger { return i.logger.Load() }

// SetLogger redirects the instance's logging. Safe to call concurrently with
// Translate.
func (i *Instance) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.Default()
	}
	i.logger.Store(l)
}

// SetUnmappedHook installs or, for nil, removes the instance's hook. Safe to
// call concurrently with Translate.
func (i *Instance) SetUnmappedHook(h UnmappedHook) {
	if h == nil {
		i.hook.Store(nil)
		return
	}
	i.hook.Store(&h)
}

// RedactInHook returns true if redaction markers are applied to errors before
// sending them to the UnmappedHook.
func (i *Instance) RedactInHook() bool {
	return i.redactInHook.Load()
}

// SetRedactInHook enables or disables redaction of errors before sending them
// to the UnmappedHook. When enabled, the hook's RedactedErr field is populated
// with the redacted version of the error.
//
// Call this during setup, before serving traffic. Safe to call concurrently.
func (i *Instance) SetRedactInHook(enabled bool) {
	i.redactInHook.Store(enabled)
}

func (i *Instance) log(ctx context.Context, level slog.Level, msg string, args ...any) {
	i.logger.Load().Log(ctx, level, msg, args...)
}

// emitUnmapped notifies the instance's hook, if any. A panicking hook is
// recovered and logged, so a faulty metrics client cannot take down the request
// that triggered it.
func (i *Instance) emitUnmapped(ctx context.Context, source, code, traceID string, err error) {
	i.emitUnmappedWithMarkers(ctx, source, code, traceID, err, nil)
}

// emitUnmappedWithMarkers notifies the instance's hook with redaction markers
// from the source registry. If RedactInHook is enabled, the hook's RedactedErr
// field is populated with the redacted version of the error.
func (i *Instance) emitUnmappedWithMarkers(ctx context.Context, source, code, traceID string, err error, markers []string) {
	hookPtr := i.hook.Load()
	if hookPtr == nil {
		return
	}

	hook := *hookPtr
	defer func() {
		if r := recover(); r != nil {
			i.log(ctx, slog.LevelError, "dberr: unmapped hook panicked",
				"trace_id", traceID,
				"source", source,
				"code", code,
				"panic", r,
			)
		}
	}()

	event := UnmappedEvent{
		Source:  source,
		Code:    code,
		TraceID: traceID,
		Err:     err,
	}

	// Populate RedactedErr if redaction in hook is enabled
	if i.redactInHook.Load() {
		event.RedactedErr = redactError(err, markers)
	}

	hook(event)
}

// redactError applies redaction markers from a source registry to an error's message.
// Returns a new error with redacted content, or the original if no markers match.
func redactError(err error, markers []string) error {
	if err == nil {
		return nil
	}

	raw := err.Error()
	redacted := raw

	for _, marker := range markers {
		if marker == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, marker, "***")
	}

	if redacted == raw {
		return err // no changes
	}

	return errors.New(redacted)
}

// Default is the Instance backing the package-level functions. Most programs
// never need another one.
var Default = New()
