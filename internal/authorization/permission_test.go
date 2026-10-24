package authorization

import "testing"

func TestEffective(t *testing.T) {
	tests := []struct {
		name string
		in   []Level
		want Level
	}{
		{"no groups", nil, None},
		{"view only", []Level{ViewOnly}, ViewOnly},
		{"edit wins", []Level{ViewOnly, ViewEdit}, ViewEdit},
		{"unknown ignored", []Level{"bad", ViewOnly}, ViewOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Effective(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAllows(t *testing.T) {
	if Allows(None, ViewOnly) || Allows(ViewOnly, ViewEdit) || !Allows(ViewEdit, ViewOnly) || !Allows(ViewEdit, ViewEdit) {
		t.Fatal("permission hierarchy is incorrect")
	}
}

func TestValidateLevel(t *testing.T) {
	if ValidateLevel(ViewOnly) != nil || ValidateLevel(ViewEdit) != nil || ValidateLevel("admin") == nil {
		t.Fatal("unexpected access-level validation result")
	}
}
