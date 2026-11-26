package migration

import (
	"strings"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	sql := `-- bikin table
CREATE TABLE a (s text DEFAULT ';');
/* komentar; */ INSERT INTO a VALUES ('x;y'), ("q;");
CREATE FUNCTION f() RETURNS int AS $body$ BEGIN RETURN 1; END; $body$ LANGUAGE plpgsql;
(SELECT 1);
-- cuma komentar;
`
	got := splitStatements(sql)
	if len(got) != 4 {
		t.Fatalf("dapat %d statement: %q", len(got), got)
	}
	if !strings.HasPrefix(got[2], "CREATE FUNCTION") || !strings.HasSuffix(got[2], "plpgsql") {
		t.Errorf("dollar-quote kepotong: %q", got[2])
	}
	if got[3] != "(SELECT 1)" {
		t.Errorf("statement diawali kurung hilang: %q", got[3])
	}
}

func TestCleanInput(t *testing.T) {
	tests := []struct {
		label string
		in    Input
		want  error
	}{
		{"reversible valid", Input{Name: "add table", SQLUp: "CREATE TABLE a (id int);", SQLDown: "DROP TABLE a;"}, nil},
		{"function plpgsql", Input{Name: "fn", SQLUp: "DO $$ BEGIN PERFORM 1; END $$;", SQLDown: "SELECT 1;"}, nil},
		{"empty name", Input{Name: "", SQLUp: "SELECT 1", SQLDown: "SELECT 1"}, ErrName},
		{"name too long", Input{Name: strings.Repeat("x", 101), SQLUp: "SELECT 1", SQLDown: "SELECT 1"}, ErrName},
		{"empty sql_up", Input{Name: "kosong", SQLUp: "  -- cuma komentar\n ; ", SQLDown: "SELECT 1"}, ErrEmptySQL},
		{"commit in up", Input{Name: "commit", SQLUp: "CREATE TABLE a (id int); COMMIT;", SQLDown: "DROP TABLE a;"}, ErrTxControl},
		{"begin in up", Input{Name: "begin", SQLUp: "/* x */ begin; CREATE TABLE a (id int);", SQLDown: "DROP TABLE a;"}, ErrTxControl},
		{"rollback in up", Input{Name: "rb", SQLUp: "rollback", SQLDown: "SELECT 1"}, ErrTxControl},
		{"reversible missing down", Input{Name: "add table", SQLUp: "CREATE TABLE a (id int);"}, ErrDownRequired},
		{"tx control in down", Input{Name: "add table", SQLUp: "CREATE TABLE a (id int);", SQLDown: "DROP TABLE a; COMMIT;"}, ErrTxControl},
		{"irreversible without down", Input{Name: "drop old data", SQLUp: "DELETE FROM a;", IrreversibleReason: "data deleted permanently"}, nil},
		{"batch valid single stmt", Input{Name: "backfill", SQLUp: "UPDATE a SET x = 1 WHERE id > $1 LIMIT $2", SQLDown: "UPDATE a SET x = 0 WHERE id > $1 AND id <= $2", BatchColumn: "id", BatchSize: 1000}, nil},
		{"batch negative size", Input{Name: "bad", SQLUp: "SELECT 1", SQLDown: "SELECT 1", BatchSize: -1}, ErrBatchConfig},
		{"batch missing column", Input{Name: "bad", SQLUp: "SELECT 1", SQLDown: "SELECT 1", BatchSize: 100}, ErrBatchConfig},
		{"batch multi stmt up", Input{Name: "bad", SQLUp: "SELECT 1; SELECT 2;", SQLDown: "SELECT 1", BatchColumn: "id", BatchSize: 100}, ErrBatchMultiSQL},
		{"batch multi stmt down", Input{Name: "bad", SQLUp: "SELECT 1", SQLDown: "SELECT 1; SELECT 2;", BatchColumn: "id", BatchSize: 100}, ErrBatchMultiSQL},
		{"batch irreversible single stmt", Input{Name: "purge", SQLUp: "DELETE FROM a WHERE id > $1", IrreversibleReason: "purge", BatchColumn: "id", BatchSize: 500}, nil},
	}
	for _, tt := range tests {
		if _, got := cleanInput(tt.in); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.label, got, tt.want)
		}
	}
}
