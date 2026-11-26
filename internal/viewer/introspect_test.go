package viewer

import (
	"strings"
	"testing"
)

func TestRelKind(t *testing.T) {
	for in, want := range map[string]string{"r": "table", "p": "table", "v": "view", "m": "materialized view", "?": "?"} {
		if got := relKind(in); got != want {
			t.Errorf("relKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConstraintType(t *testing.T) {
	for in, want := range map[string]string{"p": "PRIMARY KEY", "f": "FOREIGN KEY", "u": "UNIQUE", "c": "CHECK", "x": "EXCLUDE"} {
		if got := constraintType(in); got != want {
			t.Errorf("constraintType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToCell(t *testing.T) {
	if c := toCell(nil); c != nil {
		t.Errorf("nil harus jadi nil, got %v", c)
	}
	if c := toCell([]byte{}); c != "" {
		t.Errorf("string kosong bukan nil, got %v", c)
	}
	long := strings.Repeat("é", maxCell+10)
	if c, ok := toCell([]byte(long)).(string); !ok || len([]rune(c)) != maxCell+1 || !strings.HasSuffix(c, "…") {
		t.Errorf("isi panjang tidak dipotong dengan benar: %q", c)
	}
}

func TestParsePage(t *testing.T) {
	for in, want := range map[string]int{"": 1, "abc": 1, "0": 1, "-3": 1, "2": 2, "99999999999": 1_000_000} {
		if got := parsePage(in); got != want {
			t.Errorf("parsePage(%q) = %d, want %d", in, got, want)
		}
	}
}
