package db

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectError(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{ErrNotFound, http.StatusNotFound},
		{ErrUnreachable, http.StatusBadGateway},
		{errors.New("password=rahasia"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		ConnectError(rec, tt.err)
		if rec.Code != tt.want {
			t.Errorf("%v: status %d, want %d", tt.err, rec.Code, tt.want)
		}
		if strings.Contains(rec.Body.String(), "rahasia") {
			t.Errorf("%v: detail error bocor ke response", tt.err)
		}
	}
}

func TestDSNRoundTrip(t *testing.T) {
	t.Setenv("DATABASE_CREDENTIALS_KEY", strings.Repeat("ab", 32))
	enc, err := encryptDSN("postgres://u:p@127.0.0.1:1/x")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptDSN(enc)
	if err != nil || plain != "postgres://u:p@127.0.0.1:1/x" {
		t.Fatalf("decrypt: %q %v", plain, err)
	}
}
