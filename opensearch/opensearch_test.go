package opensearch

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/fonteeboa/go-errshield"
)

func discardLogger(t *testing.T) {
	t.Helper()
	dberr.SetLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))
	t.Cleanup(func() { dberr.SetLogger(nil) })
}

func TestTranslateResponse_Structured(t *testing.T) {
	discardLogger(t)
	body := []byte(`{
		"error":{
			"root_cause":[{"type":"version_conflict_engine_exception","reason":"[1]: version conflict"}],
			"type":"version_conflict_engine_exception",
			"reason":"[1]: version conflict, required seqNo"
		},
		"status":409
	}`)
	safe := TranslateResponse(context.Background(), body, 409, "t1")
	if safe.Code != dberr.CodeConcurrentUpdate {
		t.Fatalf("unexpected: %+v", safe)
	}
	if safe.TraceID != "t1" {
		t.Fatalf("trace id missing: %+v", safe)
	}
}

func TestTranslateResponse_PlainStringShape(t *testing.T) {
	discardLogger(t)
	body := []byte(`{"error":"Incorrect HTTP method for uri [/_doc]","status":405}`)
	safe := TranslateResponse(context.Background(), body, 405, "t2")
	if safe.Code != dberr.CodeInternal || safe.Message != dberr.DefaultMessage {
		t.Fatalf("expected default-deny for unstructured shape, got %+v", safe)
	}
}

func TestTranslateResponse_UnmappedType(t *testing.T) {
	discardLogger(t)
	body := []byte(`{"error":{"type":"some_new_exception_type","reason":"detail"},"status":500}`)
	safe := TranslateResponse(context.Background(), body, 500, "t3")
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslateResponse_InvalidJSON(t *testing.T) {
	discardLogger(t)
	for _, body := range [][]byte{
		[]byte(``),
		[]byte(`not json at all`),
		[]byte(`{"error":`),
	} {
		safe := TranslateResponse(context.Background(), body, 500, "t4")
		if safe.Code != dberr.CodeInternal {
			t.Fatalf("body %q: expected default-deny, got %+v", body, safe)
		}
	}
}

func TestTranslateResponse_MissingErrorField(t *testing.T) {
	discardLogger(t)
	body := []byte(`{"status":500}`)
	safe := TranslateResponse(context.Background(), body, 500, "t5")
	if safe.Code != dberr.CodeInternal {
		t.Fatalf("expected default-deny, got %+v", safe)
	}
}

func TestTranslateResponse_NewlyMappedTypes(t *testing.T) {
	discardLogger(t)
	cases := []struct {
		exceptionType string
		want          string
	}{
		{"document_already_exists_exception", dberr.CodeDuplicateItem},
		{"index_closed_exception", dberr.CodeNotFound},
		{"parsing_exception", dberr.CodeInvalidInput},
		{"no_mapping_definition_exception", dberr.CodeInvalidInput},
		{"strict_dynamic_mapping_exception", dberr.CodeInvalidInput},
		{"data_exception", dberr.CodeInvalidInput},
		{"validation_exception", dberr.CodeInvalidValue},
		{"invalid_index_name_exception", dberr.CodeInvalidValue},
		{"unavailable_shards_exception", dberr.CodeUnavailable},
		{"no_shard_available_action_exception", dberr.CodeUnavailable},
		{"index_primary_shard_not_allocated_exception", dberr.CodeUnavailable},
		{"snapshot_in_progress_exception", dberr.CodeConcurrentUpdate},
		{"token_expired_exception", dberr.CodeAuthFailed},
		{"incorrect_http_method_exception", dberr.CodeMethodNotAllowed},
	}
	for _, tc := range cases {
		body := []byte(`{"error":{"type":"` + tc.exceptionType + `","reason":"detail"},"status":400}`)
		safe := TranslateResponse(context.Background(), body, 400, "t6")
		if safe.Code != tc.want {
			t.Fatalf("type %s: got %q want %q", tc.exceptionType, safe.Code, tc.want)
		}
	}
}

// The reason field routinely contains index and field names.
func TestTranslateResponse_DoesNotLeakReason(t *testing.T) {
	discardLogger(t)
	body := []byte(`{"error":{"type":"illegal_argument_exception","reason":"failed to parse field [secret_field] of type [long] in document with _id [42]"},"status":400}`)
	safe := TranslateResponse(context.Background(), body, 400, "t7")
	for _, leak := range []string{"secret_field", "42"} {
		if strings.Contains(safe.Message, leak) {
			t.Fatalf("reason leaked (%q): %+v", leak, safe)
		}
	}
}

func TestMessageResponse(t *testing.T) {
	discardLogger(t)
	body := []byte(`{"error":{"type":"document_missing_exception","reason":"[42] missing"},"status":404}`)
	if got := MessageResponse(context.Background(), body, 404, "t8"); got != "resource not found" {
		t.Fatalf("MessageResponse() = %q", got)
	}
	if got := MessageResponse(context.Background(), nil, 500, "t9"); got != dberr.DefaultMessage {
		t.Fatalf("MessageResponse(nil) = %q", got)
	}
}
