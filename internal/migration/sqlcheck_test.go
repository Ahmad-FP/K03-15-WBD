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

func TestValidate(t *testing.T) {
	tests := []struct {
		name, sql string
		want      error
	}{
		{"tambah table", "CREATE TABLE a (id int);", nil},
		{"function plpgsql", "DO $$ BEGIN PERFORM 1; END $$;", nil},
		{"", "CREATE TABLE a (id int);", ErrName},
		{strings.Repeat("x", 101), "SELECT 1", ErrName},
		{"kosong", "  -- cuma komentar\n ; ", ErrEmptySQL},
		{"commit", "CREATE TABLE a (id int); COMMIT;", ErrTxControl},
		{"begin", "/* x */ begin; CREATE TABLE a (id int);", ErrTxControl},
		{"rollback", "rollback", ErrTxControl},
	}
	for _, tt := range tests {
		if got := Validate(tt.name, tt.sql); got != tt.want {
			t.Errorf("Validate(%q, %q) = %v, want %v", tt.name, tt.sql, got, tt.want)
		}
	}
}
