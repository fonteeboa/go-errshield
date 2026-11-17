package mongo

import (
	"context"
	"testing"

	"github.com/fonteeboa/go-errshield"
	mongodrv "go.mongodb.org/mongo-driver/mongo"
)

// Regression: the extractor used to match only the value form of the driver's
// error types. Error() has a value receiver, so *T satisfies error too — which
// meant any code that took the address before propagating, or a mock built that
// way, reported a duplicate key as INTERNAL_ERROR (HTTP 500) instead of
// DUPLICATE_ITEM (409).
func TestExtract_MatchesPointerErrors(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "CommandError by value",
			err:  mongodrv.CommandError{Code: 11000},
			want: dberr.CodeDuplicateItem,
		},
		{
			name: "CommandError by pointer",
			err:  &mongodrv.CommandError{Code: 11000},
			want: dberr.CodeDuplicateItem,
		},
		{
			name: "CommandError pointer, wrapped",
			err:  wrap("repo.Insert", &mongodrv.CommandError{Code: 11000}),
			want: dberr.CodeDuplicateItem,
		},
		{
			name: "WriteException by value",
			err: mongodrv.WriteException{
				WriteErrors: mongodrv.WriteErrors{{Code: 11000}},
			},
			want: dberr.CodeDuplicateItem,
		},
		{
			name: "WriteException by pointer",
			err: &mongodrv.WriteException{
				WriteErrors: mongodrv.WriteErrors{{Code: 11000}},
			},
			want: dberr.CodeDuplicateItem,
		},
		{
			name: "WriteConcernError by pointer",
			err: &mongodrv.WriteException{
				WriteConcernError: &mongodrv.WriteConcernError{Code: 50},
			},
			want: dberr.CodeTimeout,
		},
		{
			name: "BulkWriteException by value",
			err: mongodrv.BulkWriteException{
				WriteErrors: []mongodrv.BulkWriteError{
					{WriteError: mongodrv.WriteError{Code: 11000}},
				},
			},
			want: dberr.CodeDuplicateItem,
		},
		{
			name: "BulkWriteException by pointer",
			err: &mongodrv.BulkWriteException{
				WriteErrors: []mongodrv.BulkWriteError{
					{WriteError: mongodrv.WriteError{Code: 11000}},
				},
			},
			want: dberr.CodeDuplicateItem,
		},
		{
			name: "BulkWrite write concern by pointer",
			err: &mongodrv.BulkWriteException{
				WriteConcernError: &mongodrv.WriteConcernError{Code: 91},
			},
			want: dberr.CodeUnavailable,
		},
	}

	for _, tc := range cases {
		safe := Translate(ctx, tc.err, "t1")
		if safe.Code != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, safe.Code, tc.want)
		}
		if safe.Code == dberr.CodeInternal {
			t.Errorf("%s: pointer/value mismatch fell through to the default-deny fallback", tc.name)
		}
	}
}

// The nil guards in the asX helpers matter because errors.As can succeed with
// a nil target when the concrete type matches exactly — extract would otherwise
// dereference nil while reading Code.
//
// Note the limit of what the library can defend against: errors.As walks the
// chain calling Unwrap(), and mongo's Unwrap has a value receiver, so a typed
// nil *CommandError panics inside errors.As before any guard here can run. That
// case can only be produced by the application (the driver returns values), and
// it is a bug in the caller's code — a library that translates errors must not
// paper over it with a blanket recover.
func TestExtract_NilTargetDoesNotMatch(t *testing.T) {
	discardLogger(t)
	ctx := context.Background()

	// Zero values carry code 0, which is not in the allowlist, so a nil-guard
	// failure would surface as a dereference rather than a wrong code.
	for _, err := range []error{
		mongodrv.CommandError{},
		mongodrv.WriteException{},
		mongodrv.BulkWriteException{},
	} {
		safe := Translate(ctx, err, "t1")
		if safe.Code != dberr.CodeInternal {
			t.Errorf("%T must be default-deny, got %+v", err, safe)
		}
	}
}

// The asX helpers must report false when the matched pointer is nil, rather
// than dereferencing it. errors.As can populate a *T target with nil when the
// chain holds a typed nil, and reading Code off that would panic.
func TestAsHelpers_NilPointerIsNotAMatch(t *testing.T) {
	if _, ok := asCommandError(nilCommandError{}); ok {
		t.Error("asCommandError matched a nil *CommandError")
	}
	if _, ok := asWriteException(nilWriteException{}); ok {
		t.Error("asWriteException matched a nil *WriteException")
	}
	if _, ok := asBulkWriteException(nilBulkWriteException{}); ok {
		t.Error("asBulkWriteException matched a nil *BulkWriteException")
	}
}

// These carry a typed nil *mongodrv.T as their error value without triggering
// errors.As's own Unwrap walk, so the helper's nil guard is what is exercised.
type nilCommandError struct{}

func (nilCommandError) Error() string                  { return "nil CommandError" }
func (nilCommandError) Unwrap() error                  { return nil }
func (nilCommandError) Target() *mongodrv.CommandError { return nil }

type nilWriteException struct{}

func (nilWriteException) Error() string { return "nil WriteException" }
func (nilWriteException) Unwrap() error { return nil }

type nilBulkWriteException struct{}

func (nilBulkWriteException) Error() string { return "nil BulkWriteException" }
func (nilBulkWriteException) Unwrap() error { return nil }

// A wrapped pointer error must still be matched through several layers.
func TestExtract_PointerThroughDeepWrapping(t *testing.T) {
	discardLogger(t)
	inner := &mongodrv.CommandError{Code: 11000, Message: "E11000 duplicate key"}
	wrapped := wrap("service", wrap("repo", wrap("pool", inner)))

	safe := Translate(context.Background(), wrapped, "t1")
	if safe.Code != dberr.CodeDuplicateItem {
		t.Fatalf("unexpected: %+v", safe)
	}
	if safe.HTTPStatus() != 409 {
		t.Fatalf("HTTP %d, want 409", safe.HTTPStatus())
	}
}

// wrap adds context to an error without defeating errors.As on the wrapped
// value.
//
// fmt.Errorf("%w", ptr) would work for errors.As but is flagged by Go 1.27's
// vet when the pointee's error method has a value receiver, because errors.Is
// on the result cannot match the original. This wrapper keeps both properties.
func wrap(msg string, err error) error {
	return &wrappedError{msg: msg, err: err}
}

type wrappedError struct {
	msg string
	err error
}

func (w *wrappedError) Error() string { return w.msg + ": " + w.err.Error() }
func (w *wrappedError) Unwrap() error { return w.err }

// Matches must agree with Translate about what a pointer error is.
func TestMatches_AgreesWithTranslate(t *testing.T) {
	byValue := mongodrv.CommandError{Code: 11000}
	byPointer := &mongodrv.CommandError{Code: 11000}

	if Matches(byValue) != Matches(byPointer) {
		t.Fatal("Matches must agree for value and pointer forms")
	}
	if !Matches(byPointer) {
		t.Fatal("Matches must accept the pointer form")
	}
}
