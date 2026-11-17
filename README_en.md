# go-errshield

![Go](https://img.shields.io/badge/go-1.27%2B-blue)
![License](https://img.shields.io/badge/license-MIT-green)
![CI](https://github.com/fonteeboa/go-errshield/actions/workflows/ci.yml/badge.svg)

Translates database driver errors (Postgres, MySQL, MongoDB, OpenSearch) into
client-safe messages, logging the original error with a trace ID for tracing.

Requires **Go 1.27+**.

Module: `github.com/fonteeboa/go-errshield`

Principle: **default-deny, with precise messages**. Each source has its own
allowlist of codes. Only codes explicitly present in the allowlist get a
specific message. Anything else falls back to the generic `internal server
error`.

The message is hand-written and **never** derived from the driver message. That
is what keeps it safe: it can be as specific as you want about the failure
category, because it comes from nowhere the database controls.

The distinction that governs the allowlist:

| Safe to say | Unsafe to say |
|---|---|
| *what kind* of failure — "column does not exist", "duplicate key" | *which* object — `users_email_key`, `secret_orders` |
| that the service is unavailable | `db.internal.prod.svc:5432` |
| that a required field is missing | the value the client sent |

In other words: **precise about the category of failure, silent about the
object**. A client can safely read "duplicate key value" — it is about their
request. They cannot read `users_email_key`, which is about your schema.

What you get in the API is always a stable `Code` and a generic message, plus
the `trace_id` for correlation. The real detail goes entirely to the log —
constraint, table, field, host, and your own wrapping context — but never to
the response.

## Installation by source

Each source is a **separate subpackage**, so you only download the driver you
use. The root package (`go-errshield`) imports no driver.

```bash
# only Postgres (brings pgx and lib/pq)
go get github.com/fonteeboa/go-errshield/postgres

# only MySQL
go get github.com/fonteeboa/go-errshield/mysql

# only MongoDB
go get github.com/fonteeboa/go-errshield/mongo

# only OpenSearch (no driver dependency)
go get github.com/fonteeboa/go-errshield/opensearch

# database/sql sentinels (recommended alongside any source)
go get github.com/fonteeboa/go-errshield/sqlerr

# multiple sources at once
go get github.com/fonteeboa/go-errshield/postgres github.com/fonteeboa/go-errshield/mongo
```

You normally already have the driver in your `go.mod`, so in practice you just
import the subpackage you use and run `go mod tidy`.

| Subpackage | Dependencies it brings | Allowlist key |
|---|---|---|
| `go-errshield/postgres` | `pgx/v5`, `lib/pq` | SQLSTATE (`"23505"`) |
| `go-errshield/mysql` | `go-sql-driver/mysql` | number (`"1062"`) |
| `go-errshield/mongo` | `mongo-driver` | number (`"11000"`) |
| `go-errshield/opensearch` | none | exception type (`"version_conflict_engine_exception"`) |
| `go-errshield/sqlerr` | none | sentinel name (`"sql.ErrNoRows"`) |
| `go-errshield` (root) | none | — |

Each bank uses a different code format, and none is shared: MySQL `1062` and
Mongo `1062` (if it existed) would be different things. That is why each source
has its own `Registry`, with the source namespace in the log.

## Usage

### Postgres (pgx or lib/pq)

```go
import "github.com/fonteeboa/go-errshield/postgres"

func (h *Handler) CreateJob(c *gin.Context) {
	traceID := requestid.FromContext(c)

	_, err := h.db.Exec(ctx, insertQuery, args...)
	if err != nil {
		safe := postgres.Translate(ctx, err, traceID)
		c.JSON(safe.HTTPStatus(), safe) // {"code":"DUPLICATE_ITEM","error":"cannot duplicate item","trace_id":"..."}
		return
	}
}
```

`postgres.Translate` uses `errors.As`, so it works with both pgx and lib/pq,
and traverses `fmt.Errorf("...: %w", err)`.

### MySQL

```go
import "github.com/fonteeboa/go-errshield/mysql"

safe := mysql.Translate(ctx, err, traceID)
c.JSON(safe.HTTPStatus(), safe)
```

### MongoDB

```go
import "github.com/fonteeboa/go-errshield/mongo"

safe := mongo.Translate(ctx, err, traceID)
c.JSON(safe.HTTPStatus(), safe)
```

### OpenSearch

OpenSearch **does not** go through `Translate`. The `opensearch-go` client does
not return a typed Go error for API-level failures — it returns a `*Response`
HTTP whose body you read and parse yourself. That is why this package receives
the raw body bytes instead of a client type (works the same in v2/v3/v4, since
the server wire format does not change between versions):

```go
import (
	"io"
	"github.com/fonteeboa/go-errshield/opensearch"
)

res, err := client.Index(indexName, body)
if err != nil {
	// transport/network failure — not what TranslateResponse handles
	c.JSON(http.StatusInternalServerError, gin.H{"error": goerrshield.DefaultMessage})
	return
}
defer res.Body.Close()

if res.IsError() {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, opensearch.MaxBodySize+1))
	safe := opensearch.TranslateResponse(ctx, raw, res.StatusCode, traceID)
	c.JSON(safe.HTTPStatus(), safe)
	return
}
```

OpenSearch is also inconsistent in its own format: most endpoints send
`{"error":{"type":"...","reason":"..."},"status":N}`, but some (e.g. 405 Method
Not Allowed) send `{"error":"string simple",...}`. `TranslateResponse` handles
both; the second format always falls back to `internal server error` because it
has no structured `type` to map.

### `database/sql` sentinels (`sqlerr`)

Sentinels of `database/sql`, `database/sql/driver` and `context` **are not**
driver errors, so no per-driver extractor can match them. Without `sqlerr`
they disappear into the generic — and `sql.ErrNoRows`, the most common error a
Go route encounters, would become `internal server error` instead of a 404.

```go
import "github.com/fonteeboa/go-errshield/sqlerr"

switch {
case postgres.Matches(err):
	safe = postgres.Translate(ctx, err, traceID)
default:
	safe = sqlerr.Translate(ctx, err, traceID)
}
```

| Sentinel | Code |
|---|---|
| `sql.ErrNoRows` | `NOT_FOUND` |
| `sql.ErrConnDone` | `UNAVAILABLE` |
| `driver.ErrBadConn` | `UNAVAILABLE` |
| `context.DeadlineExceeded` | `TIMEOUT` |
| `context.Canceled` | `INTERNAL_ERROR` |

`sqlerr.Translate` is a good `default`: it handles the sentinels and falls back
to `goerrshield.CodeInternal` for anything else. The last two log at `WARN`, not
`ERROR` — a client disconnecting should not page anyone.

### Using multiple sources

`Matches` lets you try each source your service uses. Only the subpackages you
imported — and their dependencies — enter the `switch`:

```go
import (
	"github.com/fonteeboa/go-errshield"
	"github.com/fonteeboa/go-errshield/mongo"
	"github.com/fonteeboa/go-errshield/postgres"
	"github.com/fonteeboa/go-errshield/sqlerr"
)

func translateAny(ctx context.Context, err error, traceID string) *goerrshield.SafeError {
	switch {
	case postgres.Matches(err):
		return postgres.Translate(ctx, err, traceID)
	case mongo.Matches(err):
		return mongo.Translate(ctx, err, traceID)
	default:
		return sqlerr.Translate(ctx, err, traceID)
	}
}
```

PG `23505` and Mongo `11000` are different codes but both become
`DUPLICATE_ITEM`, so the route does not need to know which database failed.

### Complete flow

What the API returns and what you see in the log, side by side:

```
=== API responses (what the client sees) ===
  DUPLICATE_ITEM         "cannot duplicate item"        trace_id=req-001
  NOT_FOUND              "resource not found"           trace_id=req-002
  UNAVAILABLE            "service temporarily unavailable" trace_id=req-003

=== log records (what you see) ===
WARN dberr: db error translated trace_id=req-001 source=postgres code=23505
  error="repo.InsertUser(email=***): duplicate key value violates unique constraint \"users_email_key\" (SQLSTATE 23505)" known=true
WARN dberr: db error translated trace_id=req-002 source=sql code=sql.ErrNoRows
  error="repo.FindUser: sql: no rows in result set" known=true
WARN dberr: db error translated trace_id=req-003 source=postgres code=pgconn.ConnectError
  error="db.Connect: failed to connect to `host=db.internal.prod.svc user=app database=app`" known=true
```

Note how the internal hostname and database name stay in the log but not in the
response, and how the `trace_id` ties both sides together.

### pgx failures without SQLSTATE

pgx does not report connection failures as `PgError`. They are their own types,
which `postgres` handles separately:

| Error | Code |
|---|---|
| `*pgconn.ConnectError` | `UNAVAILABLE` |
| `net.Error` with `Timeout()` | `TIMEOUT` |

Without this, the entire class 08 allowlist would be unreachable via pgx —
exactly where it matters.

### String only

```go
c.JSON(http.StatusConflict, gin.H{"error": postgres.Message(ctx, err, traceID)})
```

## Logging

By default `go-errshield` logs to `slog.Default()`:

- `WARN` — known code, translated successfully
- `ERROR` — unknown code (signal that the allowlist is missing an entry),
  unrecognized driver type, or unparseable OpenSearch body

The log records the **entire original error**, including your own wrapping
context:

```
error="repo.InsertUser(email=admin@example.com): duplicate key value violates unique constraint \"users_email_key\" (SQLSTATE 23505)"
```

That context is what tells you which call failed — `repo.InsertUser` — and is
the reason the library logs the whole chain instead of just the driver message.

### Redacting what you yourself wrote

The text **you** add in `fmt.Errorf` often carries the sensitive value, and it
goes to the log along with it:

```go
// your repository, at every call site
return "", fmt.Errorf("repo.InsertUser(email=%s): %w", email, err)
```

That is your choice, and it may be exactly what you do not want to log.
`Redact` lets you keep the context and discard the value:

```go
// in application setup, before serving traffic
postgres.Registry().Redact("admin@example.com")
```

Or, for values you cannot enumerate, use structural markers:

```go
mongo.Registry().Redact("token=")
```

Each marker is replaced by `***`. Considerations:

- It is **substring-based**, not structural. The library does not know that an
  email address is sensitive while a constraint name is not — choose markers
  that only appear in your own wrapping text.
- The marker must match the **value**, not the label. `"email="` removes the
  label and leaves the address intact; `"admin@example.com"` removes the
  address.
- Values logged by the **driver** itself (constraint, table and column names)
  are not redacted: those are what you need to debug, and they never reach a
  response.
- It does not affect the response in any way. `Redact` only touches the log.
- Not thread-safe. Call at setup, not per request.

## HTTP status

The canonical `Code` → status mapping lives in code, so every consumer agrees
on it. The problem this solves: every project retyped the table and ended up
sending 500 for something that should have been 404.

```go
safe := postgres.Translate(ctx, err, traceID)
c.JSON(safe.HTTPStatus(), safe)
```

Or, if you need to validate beforehand:

```go
if status, known := goerrshield.StatusFor(safe.Code); known {
	// your own policy
}
```

An unknown `Code` returns **500** and `known=false` — it degrades to an error
rather than a wrong-but-plausible status. The library never writes the response
and only imports `net/http` for these constants: override per route when your
API contract differs.

| Code                    | HTTP status | When |
|-------------------------|-------------|------|
| DUPLICATE_ITEM          | 409         | unique key violated |
| REFERENCE_NOT_FOUND     | 422         | FK points to nonexistent record |
| REFERENCE_IN_USE        | 409         | record is referenced by others |
| MISSING_REQUIRED_FIELD  | 400         | NOT NULL column received null |
| INVALID_VALUE           | 400         | CHECK violated |
| VALUE_TOO_LONG          | 400         | string truncated |
| VALUE_OUT_OF_RANGE      | 400         | numeric/date out of range |
| INVALID_INPUT           | 400         | invalid format |
| COLUMN_NOT_FOUND        | 400         | column does not exist (see note) |
| NOT_FOUND               | 404         | record not found (`sql.ErrNoRows`) |
| CONCURRENT_UPDATE       | 409         | serialization failed, stepped down |
| DEADLOCK                | 409         | deadlock detected |
| LOCK_TIMEOUT            | 504         | could not acquire lock |
| TIMEOUT                 | 504         | query timeout |
| TOO_MANY_CONNECTIONS    | 503         | pool exhausted |
| UNAVAILABLE             | 503         | database inaccessible |
| AUTH_FAILED             | 401         | authentication failure |
| PERMISSION_DENIED       | 403         | insufficient privileges |
| METHOD_NOT_ALLOWED      | 405         | invalid HTTP method |
| SCHEMA_ERROR            | 500         | table/function/column ambiguous nonexistent |
| INTERNAL_ERROR          | 500         | anything outside the allowlist |

### `SCHEMA_ERROR` is not `NOT_FOUND`

That distinction was the easiest thing to get wrong. A previous version mapped
`42P01` (undefined_table) to `NOT_FOUND`, resulting in a 404 "resource not
found" response for the case where **the query deployed in your service
references a table that does not exist**.

The client reads it as "the record you asked for does not exist" and shows the
wrong screen, while the real problem — your deploy is broken — stays invisible.
For the operator, "table not found" is what says the service is down; for the
client, "record not found" tells them to look for data that is there.

That is why schema has its own code and status 500:

| SQLSTATE | Situation | Code | HTTP |
|---|---|---|---|
| `42P01` | table does not exist | `SCHEMA_ERROR` | 500 |
| `42883` | function does not exist | `SCHEMA_ERROR` | 500 |
| `42P02` | ambiguous column | `SCHEMA_ERROR` | 500 |
| `3D000` | database does not exist | `SCHEMA_ERROR` | 500 |
| `42601` | syntax error | `INTERNAL_ERROR` | 500 |

`42601` stays generic on purpose: the Postgres message echoes the failing
statement, and no map survives that safely.

### `COLUMN_NOT_FOUND`: the fault may be yours

`42703` (undefined_column) is the case where **only your application knows the
answer**. It may be the client sending a field name your API does not accept —
their fault, 400. Or it may be a hardcoded query with a typo — your fault, 500.

The library cannot guess, so the code is separate (`COLUMN_NOT_FOUND`, 400 by
default) and you decide at the route:

```go
if errors.Is(safe, goerrshield.ErrColumnNotFound) {
	// if your API never interpolates client input into SQL,
	// this is always your bug: respond 500
	c.JSON(http.StatusInternalServerError, safe)
	return
}
```

`goerrshield.AllCodes()` returns this list for tools and tests.

## Branching with `errors.Is`

Every Code has a sentinel error, so the route can decide based on `Code`
without string switching:

```go
safe := postgres.Translate(ctx, err, traceID)

switch {
case errors.Is(safe, goerrshield.ErrDuplicateItem):
	c.JSON(409, gin.H{"error": "that email is already registered"})
case errors.Is(safe, goerrshield.ErrMissingRequiredField):
	c.JSON(400, gin.H{"error": "name is required"})
default:
	c.JSON(safe.HTTPStatus(), safe)
}
```

The match is by `Code`, not by pointer: a freshly translated error matches the
sentinel even though it is a different pointer, and `Message` and `TraceID` are
ignored — the `Code` is the contract.

For code that already works with the `Code` constants, there is the
string-keyed form:

```go
if goerrshield.HasCode(err, goerrshield.CodeNotFound) {
	c.Status(http.StatusNotFound)
}
```

A `SafeError` also survives re-wrapping, so a middleware can add context
without losing the classification:

```go
reported := fmt.Errorf("service.CreateJob: %w", safe)
errors.Is(reported, goerrshield.ErrTooManyConnections) // true
```

### What a `SafeError` deliberately does not do

It **does not** implement `Unwrap`, so the error chain ends there:

```go
errors.Unwrap(safe)  // nil
```

Exposing the original through the chain would let any caller walk back to the
driver's message and log or render constraint, table, column and host names —
exactly what the library exists to keep out of responses. The original is in
the log, correlated by `TraceID`; it is not reachable from the response value.

The consequence is that `errors.Is(safe, someDriverError)` is `false`, which is
intentional: a `SafeError` carries a classification, not a cause.

The sentinels are `*SafeError` with the canonical message, so they respond to
`Error()` and `HTTPStatus()`. Treat them as read-only: they are package-level
pointers with exported fields, and mutating one would corrupt the sentinel for
the whole process.

## Discovering codes missing from the allowlist

An unmapped code is the only evidence that a real database produces something
the library has not been taught to describe. Without a signal, you find out
from a user bug report.

```go
goerrshield.SetUnmappedHook(func(e goerrshield.UnmappedEvent) {
	unmappedTotal.WithLabelValues(e.Source, e.Code).Inc()
})
```

The hook fires for codes outside the allowlist, with `Source` and `Code` in the
respective database's own space — `e.Code` is literally the value you would add
to the allowlist. If no extractor matched, `Source` comes as `"unknown"` and
`Code` comes empty, which distinguishes "unmapped code" from "unrecognized
driver type".

In practice, running real traffic:

```
=== how the API answered ===
  duplicate email        -> HTTP 409  DUPLICATE_ITEM       "cannot duplicate item"
  duplicate column       -> HTTP 500  INTERNAL_ERROR       "internal server error"
  no rows                -> HTTP 404  NOT_FOUND            "resource not found"

=== what the unmapped hook reported ===
  postgres:42P07                     2 occurrence(s)
  postgres:42883                     1 occurrence(s)
```

`42P07` appears three times a day and nobody complained; `42883` appears once
and may be a query bug. The frequency table decides what is worth mapping —
without it, both are invisible.

Caveats:

- Runs **synchronously** on the goroutine that called `Translate`. Keep it
  cheap: counter, metric, or append to a bounded set. No network I/O.
- A hook that **panics** is recovered and logged, so a faulty metrics client
  cannot take down the request that happened to trigger it.
- `e.Err` is the original error, **unredacted**. Do not forward it anywhere
  the client can see.
- Runs **in addition to** the `ERROR`-level log, not instead of it.

## Extending the allowlists

Add entries to the `registry` in the corresponding source file only when you
have consciously decided that code is safe to describe externally. Codes the
authors recommend **not** mapping, because they leak schema:

- Postgres: `42601` (syntax_error) stays in the generic fallback, because the
  message echoes the statement
- MySQL: `1064` (syntax_error) for the same reason
- Mongo: `86` (IndexKeySpecsConflict) stays in the generic fallback;
  `27`/`28`/`29`/`68` are mapped as generic `INVALID_VALUE`, without the index
  name
- OpenSearch: there is no closed official list of `type` values — the values
  used here come from the most common exception types observed on the REST API.

References:
- Postgres: https://www.postgresql.org/docs/current/errcodes-appendix.html
- MySQL: https://dev.mysql.com/doc/mysql-errors/8.4/en/server-error-reference.html
- Mongo: https://github.com/mongodb/mongo/blob/master/src/mongo/base/error_codes.yml

## Precedence in errors with multiple codes

A single MongoDB error can carry several write errors (common in
`BulkWriteException`). The response cannot depend on the order the server
reported them, so `mongo` ranks the candidates and returns the most actionable
one for the client:

```
[validation(121), duplicate(11000)]  -> DUPLICATE_ITEM
[duplicate(11000), validation(121)]  -> DUPLICATE_ITEM
```

Mapped codes always beat unmapped codes, and among mapped codes the most
specific wins (`121` is catch-all and ranks last). The same applies to write
concern errors, which participate as candidates.

## Tests

```bash
go test ./...
go test -race ./...
go test -cover ./...
```

Tests run offline against real driver structs
(`pgconn.PgError`, `pq.Error`, `mysql.MySQLError`, `mongo.CommandError`,
`mongo.WriteException`, `mongo.BulkWriteException`) and cover the four
sources, `errors.As` through `fmt.Errorf("%w")`, both OpenSearch error
formats, and the default-deny fallback for unknown code and unknown error type.

Additionally:

- **`leak_test.go` in each source** — walks the entire allowlist, injects a
  message containing constraint, table, column, collection, index, host and
  port, and asserts nothing of it appears in the `SafeError`. This is the test
  that holds the library's central promise against a carelessly written future
  entry.
- **Fuzz** in `opensearch` — `FuzzTranslateResponse` parses untrusted bytes and
  maintains three invariants: never returns `nil`, never invents a `Code`
  outside the allowlist, never leaks a sensitive marker. `FuzzRegistryResolve`
  does the same for code resolution.
- **Drift guards** — in `mongo`, tests cross the precedence table with the
  registry in both directions, so the two cannot diverge silently. In core,
  tests cross `AllCodes()` with the HTTP status table and with the code
  constants block, in both directions.

## Multiple policies in the same process

`SetLogger`, `SetUnmappedHook` and `Redact` are process-wide state. That is the
right behavior for most programs, but prevents two independent services in the
same binary, a multi-tenant server with per-client policy, or parallel tests
with different policies.

For those cases, create explicit `Instance`s — without global state:

```go
billing := goerrshield.New(
	goerrshield.WithLogger(billingLogger),
	goerrshield.WithUnmappedHook(func(e goerrshield.UnmappedEvent) {
		billingUnmapped.WithLabelValues(e.Source, e.Code).Inc()
	}),
)
registry := goerrshield.NewRegistryWith(billing, "postgres", postgresCodes)
```

`Instance` is immutable after construction and safe for concurrent use, so two
instances do not interfere. Package-level functions still exist and point to
`goerrshield.Default`.

## OpenSearch body size limit

This is the only point in the library that processes bytes influenced by third
parties — the OpenSearch error body carries the document that failed to parse,
and a document can be arbitrarily large.

`opensearch.MaxBodySize` (1 MiB) limits how much is parsed and logged. Above
that, the body is neither interpreted nor logged — the log reports the **size**,
not the content, and the response is the generic one. And limit the read from
the start, so you never buffer more than that:

```go
raw, _ := io.ReadAll(io.LimitReader(res.Body, opensearch.MaxBodySize+1))
safe := opensearch.TranslateResponse(ctx, raw, res.StatusCode, traceID)
```

The `+1` is so the overflow is detectable.

## Limits and validation

The library applies defensive limits with **default values**. All are
configurable — the caller decides, the library only guarantees a ceiling.

### TraceID

The `traceID` normally comes from a request header, i.e. it is influenced by
the caller. Without a ceiling, a single request can put megabytes into the log
record and response body.

```go
// Default: 256 characters
goerrshield.SetMaxTraceIDLength(128)  // global
postgres.Registry().SetMaxTraceIDLength(64)  // per registry
```

Truncation, not rejection: the traceID comes back shorter, and log and response
remain correlatable. A value of `0` or less disables truncation — only do that
if the traceID source is trusted.

### Redaction markers

`Redact` is for setup, but the list grows if called per request. The library
keeps a ceiling of markers per registry.

```go
// Default: 1000 markers
goerrshield.SetMaxRedactMarkers(500)      // global
postgres.Registry().SetMaxMarkers(50) // per registry
```

### HTTP status in OpenSearch

A status outside the accepted range becomes `500` in the log, to avoid breaking
monitoring system parsers.

```go
// Default: 100..599
opensearch.SetMinStatusCode(200)
opensearch.SetMaxStatusCode(499)
```

### Redaction in the hook

By default the hook receives the **original** error — that is how it serves to
discover codes missing from the allowlist. If the hook forwards errors to an
external system (metrics, monitoring), enable redaction:

```go
goerrshield.SetUnmappedHook(func(e goerrshield.UnmappedEvent) {
    // e.Err         -> original, never redacted
    // e.RedactedErr  -> with registry markers applied
    metrics.Counter("db_error", "error", e.RedactedErr.Error()).Inc()
})

// Enable redaction in the hook, process-wide:
inst := goerrshield.New(goerrshield.WithRedactInHook(true))
```

`RedactedErr` is `nil` while the option is off, so a hook using
`e.RedactedErr.Error()` needs to check for `nil` — or the hook itself is
installed on an instance with the option on.

## Recommended setup

```go
func main() {
	// 1. JSON log, to a destination the client cannot reach.
	goerrshield.SetLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// 2. Metric for codes the allowlist does not yet cover.
	goerrshield.SetUnmappedHook(func(e goerrshield.UnmappedEvent) {
		unmappedTotal.WithLabelValues(e.Source, e.Code).Inc()
	})

	// 3. Redact what your own wrapping text carries.
	postgres.Registry().Redact("admin@example.com")

	// 4. In routes: c.JSON(safe.HTTPStatus(), safe), and errors.Is to branch.
}
```

The four steps are optional and independent. Without any of them the library
works — the default is logging to `slog.Default()`, no hook, no redaction, and
the status comes from your own switch.

```bash
go test -run=XXX -fuzz=FuzzTranslateResponse -fuzztime=30s ./opensearch/...
```

CI in `.github/workflows/ci.yml` runs `gofmt`, `go vet`, `go test -race` and
coverage on Go 1.22/1.23/1.24.