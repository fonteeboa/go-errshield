package mysql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
	"github.com/go-sql-driver/mysql"
)

func discardLogger(t *testing.T) {
	t.Helper()
	dberr.SetLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })
}

func TestTranslate_Duplicate(t *testing.T) {
	discardLogger(t)
	err := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry '5' for key 'PRIMARY'"}
	safe := Translate(context.Background(), err, "t1")
	if safe.Code != dberr.CodeDuplicateItem || safe.Message != "cannot duplicate item" {
		t.Fatalf("unexpected: %+v", safe)
	}
	if safe.TraceID != "t1" {
		t.Fatalf("trace id missing: %+v", safe)
	}
	if strings.Contains(safe.Message, "PRIMARY") {
		t.Fatalf("index name leaked: %+v", safe)
	}
}

func TestTranslate_WrappedError(t *testing.T) {
	discardLogger(t)
	inner := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	safe := Translate(context.Background(), fmt.Errorf("exec: %w", inner), "t2")
	if safe.Code != dberr.CodeDeadlock {
		t.Fatalf("errors.As should unwrap fmt.Errorf: %+v", safe)
	}
}

func TestTranslate_Unmapped(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(), &mysql.MySQLError{Number: 9999, Message: "some obscure detail"}, "t3")
	if safe.Code != dberr.CodeInternal || safe.Message != dberr.DefaultMessage {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslate_NewlyMappedCodes(t *testing.T) {
	discardLogger(t)
	cases := []struct {
		number uint16
		want   string
	}{
		{1364, dberr.CodeMissingRequiredField},
		{1171, dberr.CodeMissingRequiredField},
		{1366, dberr.CodeInvalidInput},
		{1367, dberr.CodeInvalidInput},
		{1292, dberr.CodeInvalidInput},
		{3818, dberr.CodeInvalidValue},
		{3819, dberr.CodeInvalidValue},
		{1049, dberr.CodeSchemaError},
		{1142, dberr.CodePermissionDenied},
		{1143, dberr.CodePermissionDenied},
		{1053, dberr.CodeUnavailable},
		{1152, dberr.CodeUnavailable},
		{1317, dberr.CodeTimeout},
	}
	for _, tc := range cases {
		safe := Translate(context.Background(), &mysql.MySQLError{Number: tc.number}, "t4")
		if safe.Code != tc.want {
			t.Fatalf("number %d: got %q want %q", tc.number, safe.Code, tc.want)
		}
	}
}

// 1064 (syntax error) leaks the query, and the InnoDB and disk codes indicate
// a server-side fault. Those stay default-deny. 1054 and 1146 name a column or
// table, but they are mapped to a category that says which kind of problem it
// is without saying which object.
func TestTranslate_SchemaLeakingCodesAreDefaultDeny(t *testing.T) {
	discardLogger(t)
	for _, number := range []uint16{1064, 1136, 1051, 1048} {
		safe := Translate(context.Background(), &mysql.MySQLError{Number: number}, "t5")
		if safe.Code == dberr.CodeSchemaError || safe.Code == dberr.CodeColumnNotFound {
			t.Errorf("number %d should be default-deny, got %+v", number, safe)
		}
	}
}

// An unknown table is a defect in the deployed code, not a missing resource.
func TestTranslate_UnknownTableIsSchemaError(t *testing.T) {
	discardLogger(t)
	safe := Translate(context.Background(),
		&mysql.MySQLError{Number: 1146, Message: "Table 'app.secret_orders' doesn't exist"}, "t1")
	if safe.Code != dberr.CodeSchemaError {
		t.Fatalf("unexpected: %+v", safe)
	}
	if safe.HTTPStatus() != 500 {
		t.Fatalf("HTTPStatus() = %d, want 500", safe.HTTPStatus())
	}
	if strings.Contains(safe.Message, "secret_orders") {
		t.Fatalf("table name leaked: %+v", safe)
	}
}

func TestTranslate_PlainError(t *testing.T) {
	discardLogger(t)
	if safe := Translate(context.Background(), errors.New("random"), "t6"); safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslate_Nil(t *testing.T) {
	if Translate(context.Background(), nil, "t7") != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestMessage(t *testing.T) {
	discardLogger(t)
	if got := Message(context.Background(), &mysql.MySQLError{Number: 1451}, "t8"); got != "item is referenced by other records" {
		t.Fatalf("Message() = %q", got)
	}
	if got := Message(context.Background(), nil, "t9"); got != "" {
		t.Fatalf("Message(nil) = %q, want empty", got)
	}
}

func TestMatches(t *testing.T) {
	if !Matches(&mysql.MySQLError{Number: 1062}) {
		t.Error("expected mysql error to match")
	}
	if !Matches(fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1062})) {
		t.Error("expected wrapped error to match")
	}
	if Matches(errors.New("plain")) {
		t.Error("plain error must not match")
	}
	if Matches(nil) {
		t.Error("nil must not match")
	}
}
