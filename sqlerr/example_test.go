package sqlerr_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/fonteeboa/go-errshield"
	"github.com/fonteeboa/go-errshield/sqlerr"
)

func init() {
	// Keep the example's log records off stdout, which is where Output is read.
	dberr.SetLogger(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

// sql.ErrNoRows is the most common error a Go handler meets, and it is a plain
// "resource not found" rather than a server fault.
func ExampleTranslate() {
	safe := sqlerr.Translate(context.Background(), sql.ErrNoRows, "req-abc123")
	b, _ := json.Marshal(safe)
	fmt.Println(string(b))
	// Output: {"code":"NOT_FOUND","error":"resource not found","trace_id":"req-abc123"}
}
