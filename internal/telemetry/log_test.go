package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"postgres://app:s3cret@db:5432/x":            "postgres://app:[REDACTED]@db:5432/x",
		"host=db user=app password=s3cret":           "host=db user=app password=[REDACTED]",
		"host=db password='a b' dbname=x":            "host=db password=[REDACTED] dbname=x",
		"SELECT * FROM users WHERE id = $1":          "SELECT * FROM users WHERE id = $1",
		"gagal: postgresql://u:p@h/db timeout":       "gagal: postgresql://u:[REDACTED]@h/db timeout",
		"ALTER ROLE app WITH PASSWORD 'supersecret'": "ALTER ROLE app WITH PASSWORD '[REDACTED]'",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoggerRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf)
	log.Info("koneksi",
		"password", "s3cret",
		"session_id", "abc123",
		"dsn", "postgres://app:s3cret@db/x",
		"error", errors.New("dial postgres://app:s3cret@db/x gagal"),
		"database_id", 7,
	)
	out := buf.String()
	if strings.Contains(out, "s3cret") || strings.Contains(out, "abc123") {
		t.Fatalf("secret bocor di log: %s", out)
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log bukan JSON: %v", err)
	}
	if rec["msg"] != "koneksi" || rec["database_id"] != float64(7) {
		t.Fatalf("atribut biasa hilang: %v", rec)
	}
}
