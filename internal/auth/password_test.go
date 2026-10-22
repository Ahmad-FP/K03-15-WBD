package auth

import (
	"strings"
	"testing"
)

func TestHash(t *testing.T) {
	h := Hash("rahasia")
	if strings.Contains(h, "rahasia") || h == Hash("rahasia") {
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
}
