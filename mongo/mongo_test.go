package mongo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
	mongodrv "go.mongodb.org/mongo-driver/mongo"
)

func discardLogger(t *testing.T) {
	t.Helper()
	dberr.SetLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })
}

func TestTranslate_CommandErrorWriteConflict(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), mongodrv.CommandError{Code: 112, Message: "WriteConflict"}, "t1")
	if safe.Code != dberr.CodeConcurrentUpdate {
		t.Fatalf("unexpected: %+v", safe)
	}
	if safe.TraceID != "t1" {
		t.Fatalf("trace id missing: %+v", safe)
	}
}

func TestTranslate_WriteExceptionDuplicate(t *testing.T) {
	discardLogger(t)
	err := mongodrv.WriteException{
		WriteErrors: mongodrv.WriteErrors{
			{Code: 11000, Message: "E11000 duplicate key error collection: db.jobs index: _id_"},
		},
	}
	safe := Translate(context.Background(), err, "t2")
	if safe.Message != "cannot duplicate item" {
		t.Fatalf("unexpected: %+v", safe)
	}
	if strings.Contains(safe.Message, "db.jobs") {
		t.Fatalf("collection name leaked: %+v", safe)
	}
}

func TestTranslate_BulkWriteException(t *testing.T) {
	discardLogger(t)
	err := mongodrv.BulkWriteException{
		WriteErrors: []mongodrv.BulkWriteError{
			{WriteError: mongodrv.WriteError{Code: 11000, Message: "duplicate key"}},
		},
	}
	safe := Translate(context.Background(), err, "t3")
	if safe.Message != "cannot duplicate item" {
		t.Fatalf("unexpected: %+v", safe)
	}
}

func TestTranslate_WriteConcernError(t *testing.T) {
	discardLogger(t)
	err := mongodrv.WriteException{
		WriteConcernError: &mongodrv.WriteConcernError{Code: 64, Message: "waiting for replication timed out"},
	}
	safe := Translate(context.Background(), err, "t4")
	// 64 is not in the allowlist, so it must be default-deny.
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

// A bulk write can carry several errors. The most actionable one must win
// regardless of the order Mongo reported them in, otherwise the same failure
// gets a different client-facing code depending on slice order.
func TestTranslate_BulkWritePrecedenceIsOrderIndependent(t *testing.T) {
	discardLogger(t)
	validation := mongodrv.BulkWriteError{WriteError: mongodrv.WriteError{
		Code: 121, Message: "Document failed validation: email: Required",
	}}
	duplicate := mongodrv.BulkWriteError{WriteError: mongodrv.WriteError{
		Code: 11000, Message: "E11000 duplicate key error collection: app.users index: email_1",
	}}

	for _, tc := range []struct {
		name  string
		order []mongodrv.BulkWriteError
	}{
		{"validation first", []mongodrv.BulkWriteError{validation, duplicate}},
		{"duplicate first", []mongodrv.BulkWriteError{duplicate, validation}},
	} {
		safe := Translate(context.Background(), mongodrv.BulkWriteException{
			WriteErrors: tc.order,
		}, "t1")
		if safe.Code != dberr.CodeDuplicateItem {
			t.Fatalf("%s: got %q want DUPLICATE_ITEM (%+v)", tc.name, safe.Code, safe)
		}
		if strings.Contains(safe.Message, "email") || strings.Contains(safe.Message, "app.users") {
			t.Fatalf("%s: leaked detail: %+v", tc.name, safe)
		}
	}
}

// A mapped error must beat an unrecognized one from the same batch.
func TestTranslate_MappedCodeBeatsUnmapped(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), mongodrv.BulkWriteException{
		WriteErrors: []mongodrv.BulkWriteError{
			{WriteError: mongodrv.WriteError{Code: 999999, Message: "obscure"}},
			{WriteError: mongodrv.WriteError{Code: 11000, Message: "dup"}},
		},
	}, "t2")
	if safe.Code != dberr.CodeDuplicateItem {
		t.Fatalf("unexpected: %+v", safe)
	}
}

// When nothing in the batch is mapped, the result is still the generic
// fallback, and it stays order-independent too.
func TestTranslate_BulkWriteAllUnmapped(t *testing.T) {
	discardLogger(t)
	first := Translate(context.Background(), mongodrv.BulkWriteException{
		WriteErrors: []mongodrv.BulkWriteError{
			{WriteError: mongodrv.WriteError{Code: 999998, Message: "a"}},
			{WriteError: mongodrv.WriteError{Code: 999999, Message: "b"}},
		},
	}, "t3")
	second := Translate(context.Background(), mongodrv.BulkWriteException{
		WriteErrors: []mongodrv.BulkWriteError{
			{WriteError: mongodrv.WriteError{Code: 999999, Message: "b"}},
			{WriteError: mongodrv.WriteError{Code: 999998, Message: "a"}},
		},
	}, "t4")
	if first.Code != dberr.CodeInternal || second.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v and %+v", first, second)
	}
}

// Same precedence rule for WriteException, and the write concern error must
// participate as a candidate too.
func TestTranslate_WriteExceptionPrecedence(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), mongodrv.WriteException{
		WriteErrors: mongodrv.WriteErrors{
			{Code: 121, Message: "validation"},
			{Code: 26, Message: "ns not found db.secret"},
		},
	}, "t5")
	if safe.Code != dberr.CodeNotFound {
		t.Fatalf("unexpected: %+v", safe)
	}
	if strings.Contains(safe.Message, "db.secret") {
		t.Fatalf("namespace leaked: %+v", safe)
	}
}

func TestTranslate_BulkWriteConcernError(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), mongodrv.BulkWriteException{
		WriteConcernError: &mongodrv.WriteConcernError{
			Code:    64,
			Message: "waiting for replication timed out",
		},
	}, "t6")
	// 64 is unmapped, so default-deny.
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

// A write concern error must lose to a write error in the same exception.
func TestTranslate_WriteConcernLosesToWriteError(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), mongodrv.BulkWriteException{
		WriteErrors: []mongodrv.BulkWriteError{
			{WriteError: mongodrv.WriteError{Code: 11000, Message: "dup"}},
		},
		WriteConcernError: &mongodrv.WriteConcernError{Code: 64, Message: "wce"},
	}, "t7")
	if safe.Code != dberr.CodeDuplicateItem {
		t.Fatalf("unexpected: %+v", safe)
	}
}

func TestTranslate_EmptyWriteExceptionIsDefaultDeny(t *testing.T) {
	discardLogger(t)
	// A WriteException with neither write errors nor a concern error must not
	// panic and must fall through to the generic fallback.
	safe := Translate(context.Background(), mongodrv.WriteException{}, "t5")
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslate_NamespaceNotFound(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), mongodrv.CommandError{
		Code:    26,
		Message: "ns not found db.missing",
	}, "t6")
	if safe.Code != dberr.CodeNotFound {
		t.Fatalf("unexpected: %+v", safe)
	}
}

func TestTranslate_NewlyMappedCodes(t *testing.T) {
	discardLogger(t)
	cases := []struct {
		code int32
		want string
	}{
		{2, dberr.CodeInvalidInput},
		{9, dberr.CodeInvalidInput},
		{14, dberr.CodeInvalidInput},
		{22, dberr.CodeInvalidInput},
		{24, dberr.CodeDuplicateItem},
		{48, dberr.CodeDuplicateItem},
		{27, dberr.CodeInvalidValue},
		{28, dberr.CodeInvalidValue},
		{29, dberr.CodeInvalidValue},
		{68, dberr.CodeInvalidValue},
		{6, dberr.CodeUnavailable},
		{7, dberr.CodeUnavailable},
		{189, dberr.CodeConcurrentUpdate},
		{10107, dberr.CodeConcurrentUpdate},
		{11602, dberr.CodeConcurrentUpdate},
		{234, dberr.CodeConcurrentUpdate},
		{262, dberr.CodeTimeout},
	}
	for _, tc := range cases {
		safe := Translate(context.Background(), mongodrv.CommandError{Code: tc.code}, "t7")
		if safe.Code != tc.want {
			t.Fatalf("code %d: got %q want %q", tc.code, safe.Code, tc.want)
		}
	}
}

// Index-conflict codes must not name the index, even though Mongo's own
// messages do.
func TestTranslate_IndexConflictDoesNotLeakIndexName(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), mongodrv.CommandError{
		Code:    86,
		Message: "Index already exists with a different name: jobs_status_idx",
	}, "t8")
	// 86 is intentionally unmapped.
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
	if strings.Contains(safe.Message, "jobs_status_idx") {
		t.Fatalf("index name leaked: %+v", safe)
	}
}

func TestTranslate_UnknownCode(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), mongodrv.CommandError{Code: 99999, Message: "detail"}, "t9")
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslate_PlainError(t *testing.T) {
	discardLogger(t)
	if safe := Translate(context.Background(), errors.New("random"), "t10"); safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslate_Nil(t *testing.T) {
	if Translate(context.Background(), nil, "t11") != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestMessage(t *testing.T) {
	discardLogger(t)
	if got := Message(context.Background(), mongodrv.CommandError{Code: 13}, "t12"); got != "not authorized" {
		t.Fatalf("Message() = %q", got)
	}
	if got := Message(context.Background(), nil, "t13"); got != "" {
		t.Fatalf("Message(nil) = %q, want empty", got)
	}
}

func TestMatches(t *testing.T) {
	if !Matches(mongodrv.CommandError{Code: 11000}) {
		t.Error("expected CommandError to match")
	}
	if !Matches(mongodrv.WriteException{WriteErrors: mongodrv.WriteErrors{{Code: 11000}}}) {
		t.Error("expected WriteException to match")
	}
	if !Matches(mongodrv.BulkWriteException{WriteErrors: []mongodrv.BulkWriteError{
		{WriteError: mongodrv.WriteError{Code: 11000}},
	}}) {
		t.Error("expected BulkWriteException to match")
	}
	if !Matches(fmt.Errorf("wrapped: %w", mongodrv.CommandError{Code: 11000})) {
		t.Error("expected wrapped error to match")
	}
	if Matches(errors.New("plain")) {
		t.Error("plain error must not match")
	}
	if Matches(nil) {
		t.Error("nil must not match")
	}
}
