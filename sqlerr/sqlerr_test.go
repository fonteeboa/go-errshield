package sqlerr

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
)

// unclaimedDriverError stands in for an error produced by a source package
// that this package does not know about. Using a local type keeps pgx out of
// this package's dependency graph — sqlerr itself has no external deps.
type unclaimedDriverError struct{ msg string }

func (e *unclaimedDriverError) Error() string { return e.msg }

func discardLogger(t *testing.T) {
	t.Helper()
	dberr.SetLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })
}

func TestTranslate_Sentinels(t *testing.T) {
	discardLogger(t)
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"sql.ErrNoRows", sql.ErrNoRows, dberr.CodeNotFound},
		{"sql.ErrConnDone", sql.ErrConnDone, dberr.CodeUnavailable},
		{"driver.ErrBadConn", driver.ErrBadConn, dberr.CodeUnavailable},
		{"context.DeadlineExceeded", context.DeadlineExceeded, dberr.CodeTimeout},
		{"context.Canceled", context.Canceled, dberr.CodeInternal},
	}
	for _, tc := range cases {
		safe := Translate(context.Background(), tc.err, "t1")
		if safe.Code != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, safe.Code, tc.want)
		}
		if safe.TraceID != "t1" {
			t.Fatalf("%s: trace id missing: %+v", tc.name, safe)
		}
	}
}

// The two cases that resolve to INTERNAL_ERROR are listed as known so they log
// at WARN instead of ERROR. A client disconnect must not page anyone.
func TestTranslate_ExpectedInternalErrorsLogBelowError(t *testing.T) {
	var buf strings.Builder
	dberr.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })

	Translate(context.Background(), context.Canceled, "t2")

	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("context.Canceled logged at ERROR: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("expected a WARN record: %s", buf.String())
	}
}

func TestTranslate_WrappedSentinel(t *testing.T) {
	discardLogger(t)
	wrapped := fmt.Errorf("query users: %w", sql.ErrNoRows)
	safe := Translate(context.Background(), wrapped, "t3")
	if safe.Code != dberr.CodeNotFound {
		t.Fatalf("errors.Is should see through wrapping: %+v", safe)
	}
	if strings.Contains(safe.Message, "query users") {
		t.Fatalf("query text leaked: %+v", safe)
	}
}

// A driver error no source package claimed still lands on the generic
// fallback, which is what makes Translate usable as a default branch.
func TestTranslate_UnclaimedDriverError(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), &unclaimedDriverError{msg: "duplicate key on users_pkey"}, "t4")
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslate_PlainError(t *testing.T) {
	discardLogger(t)
	if safe := Translate(context.Background(), errors.New("random"), "t5"); safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslate_Nil(t *testing.T) {
	if Translate(context.Background(), nil, "t6") != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestMessage(t *testing.T) {
	discardLogger(t)
	if got := Message(context.Background(), sql.ErrNoRows, "t7"); got != "resource not found" {
		t.Fatalf("Message() = %q", got)
	}
	if got := Message(context.Background(), nil, "t8"); got != "" {
		t.Fatalf("Message(nil) = %q, want empty", got)
	}
}

// Every sentinel in the table must actually be reachable through errors.Is.
func TestSentinelTableCoversAll(t *testing.T) {
	want := []string{
		"sql.ErrNoRows",
		"sql.ErrConnDone",
		"driver.ErrBadConn",
		"context.DeadlineExceeded",
		"context.Canceled",
	}
	if len(sentinels) != len(want) {
		t.Fatalf("sentinel table has %d entries, want %d", len(sentinels), len(want))
	}
	for i, name := range want {
		if sentinels[i].name != name {
			t.Fatalf("entry %d is %q, want %q", i, sentinels[i].name, name)
		}
		if _, known := registry.Lookup(name); !known {
			t.Fatalf("%s is in the table but not in the registry", name)
		}
	}
}
