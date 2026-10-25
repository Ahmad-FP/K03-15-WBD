package viewer

import "testing"

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
