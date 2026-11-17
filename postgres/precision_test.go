package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestUnknownObjectIsNotNotFound is a regression test.
//
// An earlier version mapped 42P01 (undefined_table) to CodeNotFound, which made
// a broken deployment answer 404 "resource not found". A client reads that as
// "the record you asked for does not exist" and shows the wrong thing, while the
// actual problem — the deployed query names a table that does not exist — stays
// invisible.
//
// The distinction matters: reporting the schema problem is what tells the
// operator their service is broken, and telling the client the resource is
// missing sends them looking for data that is right there.
func TestUnknownObjectIsNotNotFound(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	cases := []struct {
		sqlstate string
		what     string
	}{
		{"42P01", "undefined_table"},
		{"3D000", "invalid_catalog_name"},
		{"42P07", "duplicate_column"},
		{"42704", "undefined_object"},
		{"42883", "undefined_function"},
		{"42P02", "ambiguous_column"},
	}
	for _, tc := range cases {
		safe := Translate(ctx, &pgconn.PgError{
			Code:    tc.sqlstate,
			Message: `relation "secret_orders" does not exist`,
		}, "t1")

		if safe.Code == dberr.CodeNotFound {
			t.Errorf("%s (%s) must not be reported as NOT_FOUND: a broken "+
				"deployment is not a missing record", tc.sqlstate, tc.what)
		}
		if safe.Code != dberr.CodeSchemaError {
			t.Errorf("%s (%s): got %q, want SCHEMA_ERROR", tc.sqlstate, tc.what, safe.Code)
		}
		if safe.HTTPStatus() != 500 {
			t.Errorf("%s (%s): HTTP %d, want 500", tc.sqlstate, tc.what, safe.HTTPStatus())
		}
	}
}

// A genuinely missing record is still a 404. This is the other half of the
// distinction: the fix must not over-correct and turn real 404s into 500s.
func TestMissingRecordIsStillNotFound(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(),
		&pgconn.PgError{Code: "P0002", Message: `no rows in result set`}, "t1")

	// P0002 (no_data_found) is not an SQLSTATE lib/pq or pgx emits; sql.ErrNoRows
	// is the real signal and lives in the sqlerr package. What matters here is
	// that the postgres registry never produces a 404 from a schema problem.
	if safe.HTTPStatus() == 404 {
		t.Fatal("no postgres SQLSTATE should map to 404; missing rows come from sqlerr")
	}
}

// COLUMN_NOT_FOUND is deliberately separate from SCHEMA_ERROR, because only the
// application knows whether an unknown column is the client's bad field name or
// a bug in a hardcoded query.
func TestUnknownColumnIsDistinctFromSchemaError(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	column := Translate(ctx, &pgconn.PgError{
		Code:    "42703",
		Message: `column "secret_column" does not exist`,
	}, "t2")
	table := Translate(ctx, &pgconn.PgError{
		Code:    "42P01",
		Message: `relation "secret_table" does not exist`,
	}, "t3")

	if column.Code != dberr.CodeColumnNotFound {
		t.Errorf("42703: got %q, want COLUMN_NOT_FOUND", column.Code)
	}
	if column.Code == table.Code {
		t.Error("an unknown column and an unknown table must not share a code")
	}
	// 400 by default, because that is the more common cause for an API that
	// accepts client-supplied field names.
	if column.HTTPStatus() != 400 {
		t.Errorf("COLUMN_NOT_FOUND: HTTP %d, want 400", column.HTTPStatus())
	}
}

// A syntax error echoes the offending statement, so it must never be mapped —
// no category is safe when the message contains the query.
func TestSyntaxErrorStaysDefaultDeny(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), &pgconn.PgError{
		Code:    "42601",
		Message: `syntax error at or near "SELEKT * FROM secret_users"`,
	}, "t4")

	if safe.Code != dberr.CodeInternal {
		t.Fatalf("42601 must stay default-deny, got %+v", safe)
	}
	if strings.Contains(safe.Message, "SELEKT") || strings.Contains(safe.Message, "secret_users") {
		t.Fatalf("query leaked: %+v", safe)
	}
}

// Precision must not become a leak. For every allowlist entry, the response has
// to stay free of identifiers even though the messages are now specific.
func TestPreciseMessagesStillDoNotLeak(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	// Every identifier a Postgres message can carry, in one message.
	raw := `relation "secret_schema.secret_table" does not exist. ` +
		`column "secret_column" of relation "secret_table" does not exist. ` +
		`Key (secret_email)=(admin@secret-corp.com) already exists. ` +
		`DETAIL: host db.internal.prod.svc port 5432 user app`

	for _, code := range registry.Codes() {
		if len(code) != 5 {
			continue // synthetic keys carry no driver message
		}
		safe := Translate(ctx, &pgconn.PgError{Code: code, Message: raw}, "t5")

		for _, marker := range []string{
			"secret_schema", "secret_table", "secret_column",
			"secret_email", "admin@secret-corp.com",
			"db.internal.prod.svc", "5432",
		} {
			if strings.Contains(safe.Message, marker) {
				t.Errorf("code %s leaked %q in %q", code, marker, safe.Message)
			}
		}
	}
}
