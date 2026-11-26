package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// jalankan satu query palsu lewat tracer, kembalikan baris log start dan end
func traceOnce(t *testing.T, ctx context.Context, sql string, args []any, tag string, err error) (start, end map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	tr := &QueryTracer{DB: "target", Logger: NewLogger(&buf)}
	ctx = tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql, Args: args})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag(tag), Err: err})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("butuh 2 baris log (start & end), dapat %d: %s", len(lines), buf.String())
	}
	if e := json.Unmarshal([]byte(lines[0]), &start); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal([]byte(lines[1]), &end); e != nil {
		t.Fatal(e)
	}
	return start, end
}

func TestQueryTracerLogsStartAndEnd(t *testing.T) {
	ctx := WithActor(context.Background(), "ani")
	ctx = WithAttrs(ctx, slog.Int64("execution_id", 42), slog.Int("batch", 3))
	start, end := traceOnce(t, ctx, "UPDATE items SET status = $1 WHERE id > $2", []any{"migrated", 10}, "UPDATE 5", nil)

	if start["msg"] != "sql.query.start" || end["msg"] != "sql.query.end" {
		t.Fatalf("event salah: %v / %v", start["msg"], end["msg"])
	}
	for _, rec := range []map[string]any{start, end} {
		if rec["actor"] != "ani" || rec["db"] != "target" || rec["execution_id"] != float64(42) || rec["batch"] != float64(3) {
			t.Fatalf("atribut korelasi hilang: %v", rec)
		}
	}
	if p, _ := start["params"].([]any); len(p) != 2 || p[0] != "migrated" || p[1] != "10" {
		t.Fatalf("params = %v", start["params"])
	}
	if end["rows"] != float64(5) || end["status"] != "ok" || end["duration_ms"] == nil {
		t.Fatalf("hasil query tidak lengkap: %v", end)
	}
}

func TestQueryTracerError(t *testing.T) {
	err := &pgconn.PgError{Code: "42P01", Message: `relation "userz" does not exist`}
	_, end := traceOnce(t, context.Background(), "SELECT * FROM userz", nil, "", err)
	if end["level"] != "ERROR" || end["sqlstate"] != "42P01" || end["status"] != "error" {
		t.Fatalf("error tidak tercatat: %v", end)
	}
}

func TestQueryTracerCommitRollbackEvents(t *testing.T) {
	cases := map[string]string{"commit": "sql.commit", "rollback": "sql.rollback", "begin read only": "sql.begin", "SELECT 1": "sql.query"}
	for sql, want := range cases {
		start, _ := traceOnce(t, context.Background(), sql, nil, "", nil)
		if start["event"] != want {
			t.Errorf("event(%q) = %v, want %s", sql, start["event"], want)
		}
	}
}

func TestQueryTracerHidesSecretParams(t *testing.T) {
	start, _ := traceOnce(t, context.Background(),
		"INSERT INTO users (username, password_hash) VALUES ($1, $2)", []any{"ani", "$2a$10$hash"}, "INSERT 0 1", nil)
	if strings.Contains(start["params"].([]any)[1].(string), "hash") {
		t.Fatalf("parameter rahasia bocor: %v", start["params"])
	}
}

func TestActorFromSessionFunc(t *testing.T) {
	SetActorFunc(func(context.Context) string { return "budi" })
	defer SetActorFunc(nil)
	start, _ := traceOnce(t, context.Background(), "SELECT 1", nil, "SELECT 1", nil)
	if start["actor"] != "budi" {
		t.Fatalf("actor dari session tidak dipakai: %v", start)
	}
}

func TestQueryTracerPointerParamsAndHandlerAttrs(t *testing.T) {
	cursor := "120"
	var nilPtr *string
	start, _ := traceOnce(t, context.Background(), "SELECT $1, $2", []any{&cursor, nilPtr}, "SELECT 1", nil)
	p, _ := start["params"].([]any)
	if len(p) != 2 || p[0] != "120" || p[1] != "NULL" {
		t.Fatalf("pointer params harus di-dereference, dapat %v", start["params"])
	}

	var buf bytes.Buffer
	ctx := WithActor(context.Background(), "ani")
	ctx = WithAttrs(ctx, slog.Int64("database_id", 7), slog.Int64("execution_id", 99))
	NewLogger(&buf).InfoContext(ctx, "migration.batch_committed", "batch", 2)
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["actor"] != "ani" || rec["database_id"] != float64(7) || rec["execution_id"] != float64(99) || rec["batch"] != float64(2) {
		t.Fatalf("atribut konteks hilang di log non-SQL: %v", rec)
	}
}
