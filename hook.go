package dberr

import "context"

// An UnmappedEvent reports a driver code that was not in a source's allowlist,
// so the default-deny fallback answered instead.
type UnmappedEvent struct {
	// Source is the registry that saw the code: "postgres", "mysql", "mongo",
	// "opensearch" or "sql".
	Source string

	// Code is the source-specific code, in that source's own key space. This
	// is the value you would add to the allowlist.
	//
	// Code is empty when Source is "unknown", which means no extractor
	// recognized the error type at all rather than that a code was unmapped.
	Code string

	// TraceID matches the trace_id in the log record, so an event can be
	// correlated with the request that produced it.
	TraceID string

	// Err is the original error. Its Error() may contain constraint, table,
	// index and host names. Register the hook before serving traffic and do not
	// forward Err anywhere the client can see.
	//
	// If RedactInHook is enabled on the instance, use RedactedErr instead of Err
	// to avoid leaking sensitive data to external systems.
	Err error

	// RedactedErr is the original error with redaction markers applied.
	// It is only populated when the instance has redaction configured and
	// RedactInHook is enabled. If no redaction is configured, RedactedErr is nil.
	//
	// Use this field when forwarding the error to external systems (metrics,
	// monitoring) to avoid leaking sensitive data.
	RedactedErr error
}

// UnmappedHook is called for every code that fell through to the default-deny
// fallback. It runs synchronously on the goroutine that called Translate, so
// keep it cheap: increment a counter, emit a metric, or append to a bounded
// set. Do not do network I/O.
//
// A hook that panics is recovered and logged, so a faulty metrics client cannot
// take down the request that happened to trigger it.
type UnmappedHook func(UnmappedEvent)

// SetUnmappedHook installs a hook on Default for every unmapped code. Call it
// during setup. A nil argument removes the hook.
//
// This is how the allowlist improves itself: an unmapped code is the only
// evidence that a source produces something the library has not been taught to
// describe, and without a signal you find out from a user's bug report.
//
//	dberr.SetUnmappedHook(func(e dberr.UnmappedEvent) {
//	    unmappedCodes.WithLabelValues(e.Source, e.Code).Inc()
//	})
//
// The hook runs in addition to the ERROR-level log record, not instead of it.
//
// Prefer WithUnmappedHook on your own Instance when one process needs more than
// one policy. This function mutates process-wide state.
func SetUnmappedHook(h UnmappedHook) {
	Default.SetUnmappedHook(h)
}

func emitUnmapped(ctx context.Context, source, code, traceID string, err error) {
	Default.emitUnmapped(ctx, source, code, traceID, err)
}

// SetRedactInHook enables or disables redaction of the error before it reaches
// the unmapped hook, for the whole process.
//
// When enabled, the hook's UnmappedEvent.RedactedErr field is populated with
// the redacted version of the error, using the markers registered on the
// source registry. Err still carries the original.
//
// This is off by default: the hook is documented as receiving the original
// error. Turn it on when the hook forwards errors to an external system that
// must not see sensitive values.
//
//	dberr.SetRedactInHook(true)
//
// Prefer WithRedactInHook on your own Instance when one process needs more
// than one policy. This function mutates process-wide state.
func SetRedactInHook(enabled bool) {
	Default.SetRedactInHook(enabled)
}
