package authentication

import (
	"errors"
	"strings"
	"testing"
)

func TestHash(t *testing.T) {
	h, err := Hash("rahasia")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, err := Hash("rahasia")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(h, "rahasia") || h == h2 {
		t.Fatal("hash leaks the password or is not salted")
	}
	if !Check(h, "rahasia") {
		t.Error("correct password rejected")
	}
	if Check(h, "salah") {
		t.Error("wrong password accepted")
	}
	if Check("", "") {
		t.Error("empty hash accepted")
	}
	if _, err := Hash(strings.Repeat("a", 73)); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("expected ErrPasswordTooLong for >72 byte password, got %v", err)
	}
}
