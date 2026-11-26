package viewer

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/Ahmad-FP/K03-15-WBD/internal/httpx"
	"github.com/jackc/pgx/v5"
)

var errNoTable = errors.New("table tidak ditemukan")

const userSchemaFilter = `n.nspname NOT IN ('pg_catalog', 'information_schema')
	AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'`

type schemaInfo struct {
	Name   string      `json:"name"`
	Tables []tableInfo `json:"tables"`
}

type tableInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default"`
}

type constraint struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Definition string `json:"definition"`
}

type index struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Unique     bool   `json:"unique"`
	Primary    bool   `json:"primary"`
}

type tableDetail struct {
	Schema      string       `json:"schema"`
	Table       string       `json:"table"`
	Kind        string       `json:"kind"`
	Columns     []column     `json:"columns"`
	Constraints []constraint `json:"constraints"`
	Indexes     []index      `json:"indexes"`
	Data        [][]any      `json:"data"`
	HasNext     bool         `json:"has_next"`
	Page        int          `json:"page"`
}

func listSchemas(ctx context.Context, tx pgx.Tx) ([]schemaInfo, error) {
	rows, err := tx.Query(ctx, `SELECT n.nspname, COALESCE(c.relname, ''), COALESCE(c.relkind::text, '')
		FROM pg_namespace n
		LEFT JOIN pg_class c ON c.relnamespace = n.oid AND c.relkind IN ('r','p','v','m') AND NOT c.relispartition
		WHERE `+userSchemaFilter+`
		ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schemaInfo{}
	for rows.Next() {
		var schema, name, kind string
		if err := rows.Scan(&schema, &name, &kind); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != schema {
			out = append(out, schemaInfo{Name: schema, Tables: []tableInfo{}})
		}
		if name != "" {
			last := &out[len(out)-1]
			last.Tables = append(last.Tables, tableInfo{Name: name, Kind: relKind(kind)})
		}
	}
	return out, rows.Err()
}

func loadTable(ctx context.Context, tx pgx.Tx, schema, name string, page int) (tableDetail, error) {
	oid, kind, err := findTable(ctx, tx, schema, name)
	if err != nil {
		return tableDetail{}, err
	}
	cols, err := listColumns(ctx, tx, oid)
	if err != nil {
		return tableDetail{}, err
	}
	cons, err := listConstraints(ctx, tx, oid)
	if err != nil {
		return tableDetail{}, err
	}
	idx, err := listIndexes(ctx, tx, oid)
	if err != nil {
		return tableDetail{}, err
	}
	data, hasNext, err := readRows(ctx, tx, schema, name, oid, page)
	if err != nil {
		return tableDetail{}, err
	}
	return tableDetail{schema, name, kind, cols, cons, idx, data, hasNext, page}, nil
}

func findTable(ctx context.Context, tx pgx.Tx, schema, table string) (oid uint32, kind string, err error) {
	err = tx.QueryRow(ctx, `SELECT c.oid, c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','v','m') AND `+userSchemaFilter,
		schema, table).Scan(&oid, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", errNoTable
	}
	return oid, relKind(kind), err
}

func listColumns(ctx context.Context, tx pgx.Tx, oid uint32) ([]column, error) {
	return httpx.Collect[column](ctx, tx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
			COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
		FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, oid)
}

func listConstraints(ctx context.Context, tx pgx.Tx, oid uint32) ([]constraint, error) {
	list, err := httpx.Collect[constraint](ctx, tx, `SELECT conname, contype::text, pg_get_constraintdef(oid)
		FROM pg_constraint WHERE conrelid = $1 ORDER BY contype, conname`, oid)
	for i := range list {
		list[i].Type = constraintType(list[i].Type)
	}
	return list, err
}

func listIndexes(ctx context.Context, tx pgx.Tx, oid uint32) ([]index, error) {
	return httpx.Collect[index](ctx, tx, `SELECT i.relname, pg_get_indexdef(ix.indexrelid), ix.indisunique, ix.indisprimary
		FROM pg_index ix JOIN pg_class i ON i.oid = ix.indexrelid
		WHERE ix.indrelid = $1 ORDER BY i.relname`, oid)
}

var relKinds = map[string]string{"r": "table", "p": "table", "v": "view", "m": "materialized view"}
var constraintTypes = map[string]string{
	"p": "PRIMARY KEY", "f": "FOREIGN KEY", "u": "UNIQUE",
	"c": "CHECK", "x": "EXCLUDE", "t": "TRIGGER", "n": "NOT NULL",
}

func relKind(k string) string {
	if v, ok := relKinds[k]; ok {
		return v
	}
	return k
}

func constraintType(t string) string {
	if v, ok := constraintTypes[t]; ok {
		return v
	}
	return t
}

const (
	pageSize = 50
	maxCell  = 200
)

func primaryKey(ctx context.Context, tx pgx.Tx, oid uint32) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT a.attname FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1 AND i.indisprimary
		ORDER BY array_position(i.indkey::int2[], a.attnum)`, oid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func readRows(ctx context.Context, tx pgx.Tx, schema, table string, oid uint32, page int) ([][]any, bool, error) {
	pk, err := primaryKey(ctx, tx, oid)
	if err != nil {
		return nil, false, err
	}
	q := "SELECT * FROM " + pgx.Identifier{schema, table}.Sanitize()
	if len(pk) > 0 {
		cols := make([]string, len(pk))
		for i, c := range pk {
			cols[i] = pgx.Identifier{c}.Sanitize()
		}
		q += " ORDER BY " + strings.Join(cols, ", ")
	}
	q += " LIMIT $1 OFFSET $2"
	rows, err := tx.Query(ctx, q, pgx.QueryResultFormats{pgx.TextFormatCode}, pageSize+1, (page-1)*pageSize)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := [][]any{}
	hasNext := false
	for rows.Next() {
		if len(out) == pageSize {
			hasNext = true
			break
		}
		raw := rows.RawValues()
		row := make([]any, len(raw))
		for i, v := range raw {
			row[i] = toCell(v)
		}
		out = append(out, row)
	}
	return out, hasNext, rows.Err()
}

func toCell(raw []byte) any {
	if raw == nil {
		return nil
	}
	r := []rune(string(raw))
	if len(r) > maxCell {
		return string(r[:maxCell]) + "…"
	}
	return string(r)
}

func parsePage(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return min(n, 1_000_000)
}
