package migration

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrName      = errors.New("nama migration wajib diisi (maksimal 100 karakter)")
	ErrEmptySQL  = errors.New("SQL migration tidak boleh kosong")
	ErrTxControl = errors.New("SQL tidak boleh berisi BEGIN/COMMIT/ROLLBACK, transaction sudah diatur sistem")
)

// statement yang bakal ngerusak transaction buatan sistem
var txKeywords = map[string]bool{
	"BEGIN": true, "START": true, "COMMIT": true, "END": true, "ROLLBACK": true, "ABORT": true,
}

// cek nama dan isi SQL sebelum disimpan
func Validate(name, sql string) error {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return ErrName
	}
	stmts := splitStatements(sql)
	if len(stmts) == 0 {
		return ErrEmptySQL
	}
	for _, s := range stmts {
		if txKeywords[firstWord(s)] {
			return ErrTxControl
		}
	}
	return nil
}

// pecah SQL per statement, titik koma di dalam string/komentar/dollar-quote dilewatin
func splitStatements(sql string) []string {
	var out []string
	start := 0
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'' || sql[i] == '"':
			i = skipUntil(sql, i+1, sql[i:i+1])
		case strings.HasPrefix(sql[i:], "--"):
			i = skipUntil(sql, i+2, "\n")
		case strings.HasPrefix(sql[i:], "/*"):
			i = skipUntil(sql, i+2, "*/")
		case sql[i] == '$':
			if tag, ok := dollarTag(sql[i:]); ok {
				i = skipUntil(sql, i+len(tag), tag)
			} else {
				i++
			}
		case sql[i] == ';':
			if s := strings.TrimSpace(sql[start:i]); stripComments(s) != "" {
				out = append(out, s)
			}
			i++
			start = i
		default:
			i++
		}
	}
	if s := strings.TrimSpace(sql[start:]); stripComments(s) != "" {
		out = append(out, s)
	}
	return out
}

// lompat ke setelah penutup berikutnya, kalau ga ketemu ya sampai akhir
func skipUntil(s string, from int, end string) int {
	j := strings.Index(s[from:], end)
	if j < 0 {
		return len(s)
	}
	return from + j + len(end)
}

// baca tag dollar-quote kayak $$ atau $body$
func dollarTag(s string) (string, bool) {
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '$' {
			return s[:i+1], true
		}
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		digit := c >= '0' && c <= '9'
		if !letter && !(digit && i > 1) {
			return "", false
		}
	}
	return "", false
}

// buang komentar dan spasi di awal statement
func stripComments(s string) string {
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--"):
			s = s[skipUntil(s, 2, "\n"):]
		case strings.HasPrefix(s, "/*"):
			s = s[skipUntil(s, 2, "*/"):]
		default:
			return s
		}
	}
}

// kata pertama statement dalam huruf besar
func firstWord(s string) string {
	s = stripComments(s)
	end := strings.IndexFunc(s, func(r rune) bool {
		return !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	})
	if end < 0 {
		end = len(s)
	}
	return strings.ToUpper(s[:end])
}
