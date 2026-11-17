# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The library has not been released yet, so there is no prior public API to
preserve. This section records the decisions taken before the first tag.

### Contract

The `Code` strings returned in responses (`DUPLICATE_ITEM`, `NOT_FOUND`, …) are
part of the public contract. They appear in HTTP response bodies, so changing
one is a breaking change for any client that switches on them. `CodeSchemaError`
and `CodeColumnNotFound` were added for this reason rather than folded into
existing codes.

A `*SafeError` deliberately does not implement `Unwrap`. Exposing the original
error through the chain would let a caller walk back to the driver's message and
render it.

## [0.1.0] — Unreleased

### Added

- Per-source subpackages so importing one driver does not pull in the others:
  `dberr/postgres` (pgx v5 and lib/pq), `dberr/mysql` (go-sql-driver/mysql),
  `dberr/mongo` (mongo-driver), `dberr/opensearch` (no driver dependency),
  `dberr/sqlerr` (`database/sql` sentinels).
- `Translate`, `Message` and `Matches` on every source package.
- Default-deny resolution: only codes in a source's allowlist get a specific
  message; everything else falls back to `INTERNAL_ERROR`.
- `HTTPStatus()` on `*SafeError` and `StatusFor(code)`, so the code-to-status
  mapping lives in one place instead of being retyped per project.
- Sentinel errors for every code, matched by `errors.Is`, plus `HasCode` for
  string-keyed branching.
- `SetUnmappedHook` / `WithUnmappedHook`, reporting codes the allowlist does not
  cover — the signal for growing it.
- `Registry.Redact` for stripping values from log records.
- `dberr.New` returning an `Instance` with its own logger and hook, for programs
  that need more than one policy in a process.
- `opensearch.MaxBodySize`, bounding parsing and logging of response bodies.
- Fuzz targets for the OpenSearch body parser and the core resolver.
- Per-source leak-injection tests asserting no allowlist entry can leak.

### Fixed

- MongoDB errors addressed by pointer (`*CommandError`, `*WriteException`,
  `*BulkWriteException`) were not recognised and fell through to
  `INTERNAL_ERROR`, reporting a duplicate key as HTTP 500 instead of 409. The
  driver returns values, but `Error()` has a value receiver, so both forms
  satisfy `error`.
- `database/sql` sentinels could never be matched by a driver extractor, so
  `sql.ErrNoRows` returned 500 rather than 404. Handled by `dberr/sqlerr`.
- pgx connection failures (`*pgconn.ConnectError`) and socket deadlines
  (`net.Error` with `Timeout()`) carry no SQLSTATE, so the class 08 allowlist
  entries were unreachable via pgx. Both are now handled.
- An unknown table (`42P01`), unknown database (`3D000`, `1049`) and unknown
  table in MySQL (`1146`) were reported as `NOT_FOUND`, which answered HTTP 404
  for a broken deployment and told the client a record was missing when the
  service was in fact defective. They now report `SCHEMA_ERROR` and 500.
- MongoDB bulk writes resolved to a code chosen by slice position, so the same
  set of write errors produced different client-facing codes depending on order.
  The most actionable mapped code now wins, and write-concern errors participate
  as candidates.
- `Translate` logged only the driver's own message, discarding the caller's
  wrapping context (`repo.InsertUser: …`), which is usually what identifies the
  failing call site. The full error chain is now logged.
- `*pgconn.ConnectError` with a nil `Config` panicked inside `extract`, because
  pgx's `Error()` dereferences `Config` unconditionally.
- `Registry.Redact` mutated a shared slice without synchronization and was
  documented as setup-only. It is now guarded by a `sync.RWMutex` and safe to
  call concurrently with `Translate`.

### Security

- Response messages are hand-written and never derived from driver output, so
  they can be specific about the failure category without naming the object:
  precise about *what kind* of failure, silent about *which* table, column,
  index, host or value.
- Syntax errors (`42601`, `1064`) are intentionally unmapped, because their
  messages embed the failing statement.
- `UnmappedEvent.Err` carries the original error **unredacted**. Hooks must not
  forward it anywhere a client can observe.

### Requirements

- Go 1.27 or later.