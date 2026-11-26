package migration

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

var (
	ErrName          = &Error{http.StatusBadRequest, "nama migration wajib diisi (maksimal 100 karakter)"}
	ErrEmptySQL      = &Error{http.StatusBadRequest, "SQL migration tidak boleh kosong"}
	ErrTxControl     = &Error{http.StatusBadRequest, "SQL tidak boleh berisi BEGIN/COMMIT/ROLLBACK, transaction sudah diatur sistem"}
	ErrDownRequired  = &Error{http.StatusBadRequest, "sql_down wajib diisi kecuali migration ditandai irreversible dengan alasan"}
	ErrBatchConfig   = &Error{http.StatusBadRequest, "konfigurasi batch tidak valid"}
	ErrBatchMultiSQL = &Error{http.StatusBadRequest, "migration mode batch hanya boleh berisi satu SQL statement"}
)

var txKeywords = map[string]bool{
	"BEGIN": true, "START": true, "COMMIT": true, "END": true, "ROLLBACK": true, "ABORT": true,
}

type Input struct {
	Name               string `json:"name"`
	SQLUp              string `json:"sql_up"`
	SQLDown            string `json:"sql_down"`
	IrreversibleReason string `json:"irreversible_reason"`
	BatchColumn        string `json:"batch_column"`
	BatchSize          int    `json:"batch_size"`
}

func cleanInput(in Input) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.IrreversibleReason = strings.TrimSpace(in.IrreversibleReason)
	if in.BatchSize == 0 {
		in.BatchColumn = ""
	} else {
		in.BatchColumn = strings.TrimSpace(in.BatchColumn)
	}
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 {
		return Input{}, ErrName
	}
	if in.BatchSize < 0 {
		return Input{}, ErrBatchConfig
	}
	batch := in.BatchSize > 0
	if batch && in.BatchColumn == "" {
		return Input{}, ErrBatchConfig
	}
	if err := validateSQL(in.SQLUp, batch); err != nil {
		return Input{}, err
	}
	if in.IrreversibleReason == "" {
		if err := validateSQL(in.SQLDown, batch); errors.Is(err, ErrEmptySQL) {
			return Input{}, ErrDownRequired
		} else if err != nil {
			return Input{}, err
		}
	}
	return in, nil
}

func validateSQL(sql string, singleStmt bool) error {
	stmts := splitStatements(sql)
	if len(stmts) == 0 {
		return ErrEmptySQL
	}
	for _, s := range stmts {
		if txKeywords[firstWord(s)] {
			return ErrTxControl
		}
	}
	if singleStmt && len(stmts) != 1 {
		return ErrBatchMultiSQL
	}
	return nil
}

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

func skipUntil(s string, from int, end string) int {
	j := strings.Index(s[from:], end)
	if j < 0 {
		return len(s)
	}
	return from + j + len(end)
}

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
