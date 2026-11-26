package telemetry

import (
	"io"
	"log/slog"
	"regexp"
	"strings"
)

const redacted = "[REDACTED]"

// nama atribut yang isinya pasti rahasia, dicocokkan sebagai potongan nama
var secretKeys = []string{"password", "passwd", "secret", "token", "cookie", "session", "authorization", "dsn"}

var (
	// postgres://user:rahasia@host → postgres://user:[REDACTED]@host
	urlPassword = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://[^:/@\s]+:)[^@\s]+@`)
	// password=rahasia di DSN format key=value
	kvPassword = regexp.MustCompile(`(?i)\b(password|pwd)\s*=\s*('[^']*'|\S+)`)
	// PASSWORD 'rahasia' di SQL seperti CREATE/ALTER ROLE
	sqlPassword = regexp.MustCompile(`(?i)\b(password)\s+'[^']*'`)
)

// logger JSON ke w, semua atribut rahasia disamarkan
func NewLogger(w io.Writer) *slog.Logger {
	return slog.New(Handler{slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: redactAttr})})
}

// samarkan password yang nyelip di teks, misalnya DSN di pesan error
func Redact(s string) string {
	s = urlPassword.ReplaceAllString(s, "${1}"+redacted+"@")
	s = kvPassword.ReplaceAllString(s, "${1}="+redacted)
	return sqlPassword.ReplaceAllString(s, "${1} '"+redacted+"'")
}

func secretKey(key string) bool {
	key = strings.ToLower(key)
	for _, s := range secretKeys {
		if strings.Contains(key, s) {
			return true
		}
	}
	return false
}

func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if secretKey(a.Key) {
		return slog.String(a.Key, redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(Redact(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			a.Value = slog.StringValue(Redact(err.Error()))
		}
	}
	return a
}
