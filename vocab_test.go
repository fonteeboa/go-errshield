package dberr

import "testing"

// TestAllCodesCoversVocabulary keeps the mirrored list in sync with the const
// block. The const block cannot be enumerated at run time, so the mirror is the
// only way to check it; this test is what stops the mirror from going stale.
func TestAllCodesCoversVocabulary(t *testing.T) {
	declared := map[string]bool{
		CodeInternal:             true,
		CodeDuplicateItem:        true,
		CodeReferenceNotFound:    true,
		CodeReferenceInUse:       true,
		CodeMissingRequiredField: true,
		CodeInvalidValue:         true,
		CodeValueTooLong:         true,
		CodeValueOutOfRange:      true,
		CodeInvalidInput:         true,
		CodeConcurrentUpdate:     true,
		CodeDeadlock:             true,
		CodeLockTimeout:          true,
		CodeTimeout:              true,
		CodeTooManyConnections:   true,
		CodeUnavailable:          true,
		CodeAuthFailed:           true,
		CodePermissionDenied:     true,
		CodeMethodNotAllowed:     true,
		CodeNotFound:             true,
		CodeColumnNotFound:       true,
		CodeSchemaError:          true,
	}

	seen := map[string]bool{}
	for _, code := range AllCodes() {
		if !declared[code] {
			t.Errorf("AllCodes contains %q, which is not a declared code", code)
		}
		if seen[code] {
			t.Errorf("AllCodes contains %q twice", code)
		}
		seen[code] = true
	}

	for code := range declared {
		if !seen[code] {
			t.Errorf("declared code %q is missing from AllCodes", code)
		}
	}

	if len(declared) != len(allCodes) {
		t.Errorf("declared %d codes but AllCodes has %d", len(declared), len(allCodes))
	}
}

// AllCodes returns a copy, so a caller mutating the result cannot corrupt the
// package's state.
func TestAllCodes_ReturnsCopy(t *testing.T) {
	first := AllCodes()
	if len(first) == 0 {
		t.Fatal("AllCodes is empty")
	}
	first[0] = "MUTATED"

	for _, code := range AllCodes() {
		if code == "MUTATED" {
			t.Fatal("AllCodes exposed its backing array")
		}
	}
}

// The code values are part of the library's contract: they appear in HTTP
// responses. Renaming one is a breaking change for clients.
func TestCodeValuesAreStable(t *testing.T) {
	want := map[string]string{
		CodeInternal:             "INTERNAL_ERROR",
		CodeDuplicateItem:        "DUPLICATE_ITEM",
		CodeReferenceNotFound:    "REFERENCE_NOT_FOUND",
		CodeReferenceInUse:       "REFERENCE_IN_USE",
		CodeMissingRequiredField: "MISSING_REQUIRED_FIELD",
		CodeInvalidValue:         "INVALID_VALUE",
		CodeValueTooLong:         "VALUE_TOO_LONG",
		CodeValueOutOfRange:      "VALUE_OUT_OF_RANGE",
		CodeInvalidInput:         "INVALID_INPUT",
		CodeConcurrentUpdate:     "CONCURRENT_UPDATE",
		CodeDeadlock:             "DEADLOCK",
		CodeLockTimeout:          "LOCK_TIMEOUT",
		CodeTimeout:              "TIMEOUT",
		CodeTooManyConnections:   "TOO_MANY_CONNECTIONS",
		CodeUnavailable:          "UNAVAILABLE",
		CodeAuthFailed:           "AUTH_FAILED",
		CodePermissionDenied:     "PERMISSION_DENIED",
		CodeMethodNotAllowed:     "METHOD_NOT_ALLOWED",
		CodeNotFound:             "NOT_FOUND",
		CodeColumnNotFound:       "COLUMN_NOT_FOUND",
		CodeSchemaError:          "SCHEMA_ERROR",
	}
	for got, expected := range want {
		if got != expected {
			t.Errorf("code value changed: got %q, want %q", got, expected)
		}
	}
}
