// Package mongo translates MongoDB driver errors into safe, client-facing
// messages.
//
// Importing this package pulls in go.mongodb.org/mongo-driver. Use
// dberr/postgres, dberr/mysql or dberr/opensearch instead if you do not use
// MongoDB.
//
//	safe := mongo.Translate(ctx, err, traceID)
//	if safe != nil {
//	    c.JSON(http.StatusConflict, safe)
//	}
package mongo

import (
	"context"
	"errors"
	"strconv"

	"github.com/fonteeboa/go-errshield"
	mongodrv "go.mongodb.org/mongo-driver/mongo"
)

const source = "mongo"

// registry holds the allowlist of server error codes considered safe to
// describe externally. Reference:
// https://github.com/mongodb/mongo/blob/master/src/mongo/base/error_codes.yml
//
// This code space overlaps numerically with MySQL's (both are plain integers)
// but means something different. Registries are keyed by source, so the two
// are never confused.
//
// Codes that would leak collection, index or field names (27, 28, 29, 68) map
// to a generic constraint message rather than the code's own description.
// 121 (document failed validation) is a catch-all that echoes the offending
// document, so it ranks last among mapped codes — see precedence.
var registry = dberr.NewRegistry(source, map[string]dberr.SafeError{
	// Duplicate keys
	"11000": {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},
	"11001": {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},
	"24":    {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},
	"48":    {Code: dberr.CodeDuplicateItem, Message: "cannot duplicate item"},

	// Bad input
	"2":  {Code: dberr.CodeInvalidInput, Message: "invalid value"},
	"9":  {Code: dberr.CodeInvalidInput, Message: "invalid input format"},
	"14": {Code: dberr.CodeInvalidInput, Message: "invalid value for this field"},
	"22": {Code: dberr.CodeInvalidInput, Message: "invalid options"},

	// Index and schema conflicts, reported without naming the index
	"27":  {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},
	"28":  {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},
	"29":  {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},
	"68":  {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},
	"121": {Code: dberr.CodeInvalidValue, Message: "value violates a constraint"},

	// Concurrency: write conflicts and primary step-downs
	"112":   {Code: dberr.CodeConcurrentUpdate, Message: "please try again"},
	"189":   {Code: dberr.CodeConcurrentUpdate, Message: "please try again"},
	"234":   {Code: dberr.CodeConcurrentUpdate, Message: "please try again"},
	"10107": {Code: dberr.CodeConcurrentUpdate, Message: "please try again"},
	"11602": {Code: dberr.CodeConcurrentUpdate, Message: "please try again"},

	// Timeouts
	"50":  {Code: dberr.CodeTimeout, Message: "operation timed out"},
	"89":  {Code: dberr.CodeTimeout, Message: "operation timed out"},
	"262": {Code: dberr.CodeTimeout, Message: "operation timed out"},

	// Availability
	"6":   {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"7":   {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"91":  {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},
	"133": {Code: dberr.CodeUnavailable, Message: "service temporarily unavailable"},

	// Access control
	"13": {Code: dberr.CodeAuthFailed, Message: "not authorized"},
	"18": {Code: dberr.CodeAuthFailed, Message: "authentication failed"},

	// Missing objects. In Mongo a client usually names the collection itself,
	// so NotFound is defensible here where a table reference in SQL is not:
	// 26 is reported when a query touches a collection that does not exist.
	"26": {Code: dberr.CodeNotFound, Message: "resource not found"},
})

// Registry returns this source's registry, so you can register log redaction
// markers during setup:
//
//	mongo.Registry().Redact("email=")
func Registry() *dberr.Registry { return registry }

// Translate inspects err for a MongoDB driver error, logs the original error
// tagged with traceID, and returns a SafeError ready to serialize in the HTTP
// response.
//
// The log record keeps your own wrapping context ("repo.InsertUser: ..."),
// which is usually what identifies the failing call site. The response contains
// nothing from the original error.
//
// It returns nil if err is nil. An error that is not a MongoDB driver error at
// all yields the generic dberr.CodeInternal fallback, so Translate is safe to
// call unconditionally.
func Translate(ctx context.Context, err error, traceID string) *dberr.SafeError {
	if err == nil {
		return nil
	}
	if code, ok := extract(err); ok {
		return registry.Resolve(ctx, code, err, traceID)
	}
	return dberr.Unrecognized(ctx, err, traceID)
}

// Message is a convenience wrapper for the common case: you only want the
// ready-to-return string.
//
//	c.JSON(http.StatusConflict, gin.H{"error": mongo.Message(ctx, err, traceID)})
func Message(ctx context.Context, err error, traceID string) string {
	safe := Translate(ctx, err, traceID)
	if safe == nil {
		return ""
	}
	return safe.Message
}

// Matches reports whether err is, or wraps, a MongoDB driver error. Use it to
// try several sources in turn when a service talks to more than one database.
func Matches(err error) bool {
	_, ok := extract(err)
	return ok
}

// candidate is one code found in a driver error. The message is not kept:
// Resolve logs the original error, which includes every wrapped message.
type candidate struct {
	code string
}

// precedence ranks a mapped code by how specific and actionable it is to the
// client. Lower is better.
//
// This exists because a single MongoDB error can carry several write errors
// (BulkWriteException in particular), and the right answer should not depend
// on the order Mongo happened to report them. Code 121 (document failed
// validation) is a catch-all that maps to INVALID_VALUE; a duplicate key in
// the same batch is far more useful to the client, so it must win regardless
// of position.
// Only the codes this package's registry can actually produce are listed.
// TestPrecedenceHasNoUnusedEntries fails if the two tables drift apart, and
// TestPrecedenceCoversEveryMappedCode fails if a new entry is added without a
// rank.
var precedence = map[string]int{
	dberr.CodeDuplicateItem:    0,
	dberr.CodeNotFound:         1,
	dberr.CodeConcurrentUpdate: 1,
	dberr.CodeAuthFailed:       2,
	dberr.CodeTimeout:          2,
	dberr.CodeUnavailable:      2,
	dberr.CodeInvalidInput:     3,
	dberr.CodeInvalidValue:     4,
}

const worstPrecedence = 5

// best picks the most actionable candidate: the mapped code with the lowest
// precedence rank, falling back to an unmapped code only when nothing is
// mapped. Among candidates of equal rank the first one wins, so a stable input
// gives a stable result.
func best(candidates []candidate) (candidate, bool) {
	if len(candidates) == 0 {
		return candidate{}, false
	}

	winner := candidates[0]
	winnerRank := rank(winner.code)

	for _, c := range candidates[1:] {
		if r := rank(c.code); r < winnerRank {
			winner, winnerRank = c, r
		}
	}

	return winner, true
}

// rank returns the precedence of code. Unmapped codes rank last, so a mapped
// error always beats an unrecognized one from the same batch.
func rank(code string) int {
	mapped, known := registry.Lookup(code)
	if !known {
		return worstPrecedence
	}
	if r, ok := precedence[mapped.Code]; ok {
		return r
	}
	return worstPrecedence
}

// extract unwraps err looking for a MongoDB driver error and returns the
// registry key for the most actionable code it carries.
//
// Mongo reports failures through several distinct types depending on the
// operation (single command vs. single write vs. bulk write), so all three are
// checked. Each carries its own numeric code, which is this source's key space.
//
// Only the key is returned: Resolve logs the original error, so the caller's
// wrapping context survives into the log record.
//
// Both the value and the pointer form of each type are matched. The driver
// returns values (see replaceErrors in mongo/errors.go), but Error() has a
// value receiver, so *T also satisfies error — meaning code that takes the
// address before propagating, or a mock built that way, would otherwise fall
// through to the generic fallback and report a duplicate key as a 500.
func extract(err error) (code string, ok bool) {
	if c, found := asCommandError(err); found {
		return strconv.FormatInt(int64(c.Code), 10), true
	}

	var candidates []candidate

	if we, found := asWriteException(err); found {
		for _, w := range we.WriteErrors {
			candidates = append(candidates, candidate{strconv.Itoa(w.Code)})
		}
		if we.WriteConcernError != nil {
			candidates = append(candidates, candidate{strconv.Itoa(we.WriteConcernError.Code)})
		}
	}

	if bwe, found := asBulkWriteException(err); found {
		for _, w := range bwe.WriteErrors {
			candidates = append(candidates, candidate{strconv.Itoa(w.Code)})
		}
		if bwe.WriteConcernError != nil {
			candidates = append(candidates, candidate{strconv.Itoa(bwe.WriteConcernError.Code)})
		}
	}

	winner, ok := best(candidates)
	if !ok {
		return "", false
	}
	return winner.code, true
}

func asCommandError(err error) (mongodrv.CommandError, bool) {
	var byValue mongodrv.CommandError
	if errors.As(err, &byValue) {
		return byValue, true
	}
	var byPointer *mongodrv.CommandError
	if errors.As(err, &byPointer) && byPointer != nil {
		return *byPointer, true
	}
	return mongodrv.CommandError{}, false
}

func asWriteException(err error) (mongodrv.WriteException, bool) {
	var byValue mongodrv.WriteException
	if errors.As(err, &byValue) {
		return byValue, true
	}
	var byPointer *mongodrv.WriteException
	if errors.As(err, &byPointer) && byPointer != nil {
		return *byPointer, true
	}
	return mongodrv.WriteException{}, false
}

func asBulkWriteException(err error) (mongodrv.BulkWriteException, bool) {
	var byValue mongodrv.BulkWriteException
	if errors.As(err, &byValue) {
		return byValue, true
	}
	var byPointer *mongodrv.BulkWriteException
	if errors.As(err, &byPointer) && byPointer != nil {
		return *byPointer, true
	}
	return mongodrv.BulkWriteException{}, false
}
