package viewer

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

var errNoTable = errors.New("table tidak ditemukan")

type schemaInfo struct {
	Name   string
	Tables []tableInfo
}

type tableInfo struct {
	Name string
	Kind string
}

type column struct {
	Name     string
	Type     string
	Nullable bool
	Default  string
}

type constraint struct {
	Name       string
	Type       string
	Definition string
}

type index struct {
	Name       string
	Definition string
	Unique     bool
	Primary    bool
}

// ambil semua schema user beserta table/view di dalamnya
func listSchemas(ctx context.Context, tx pgx.Tx) ([]schemaInfo, error) {
	rows, err := tx.Query(ctx, `SELECT n.nspname, COALESCE(c.relname, ''), COALESCE(c.relkind::text, '')
		FROM pg_namespace n
		LEFT JOIN pg_class c ON c.relnamespace = n.oid AND c.relkind IN ('r','p','v','m') AND NOT c.relispartition
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
			AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'
		ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []schemaInfo
	for rows.Next() {
		var schema, name, kind string
		if err := rows.Scan(&schema, &name, &kind); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != schema {
			out = append(out, schemaInfo{Name: schema})
		}
		if name != "" {
			last := &out[len(out)-1]
			last.Tables = append(last.Tables, tableInfo{Name: name, Kind: relKind(kind)})
		}
	}
	return out, rows.Err()
}

// cari oid dan jenis relasi dari nama schema + table
func findTable(ctx context.Context, tx pgx.Tx, schema, table string) (oid uint32, kind string, err error) {
	err = tx.QueryRow(ctx, `SELECT c.oid, c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r','p','v','m')`, schema, table).Scan(&oid, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", errNoTable
	}
	return oid, relKind(kind), err
}

// kolom table sesuai urutan aslinya
func listColumns(ctx context.Context, tx pgx.Tx, oid uint32) ([]column, error) {
	rows, err := tx.Query(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
			COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
		FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[column])
}

// constraint table (PK, FK, unique, check, dll)
func listConstraints(ctx context.Context, tx pgx.Tx, oid uint32) ([]constraint, error) {
	rows, err := tx.Query(ctx, `SELECT conname, contype::text, pg_get_constraintdef(oid)
		FROM pg_constraint WHERE conrelid = $1 ORDER BY contype, conname`, oid)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[constraint])
	for i := range list {
		list[i].Type = constraintType(list[i].Type)
	}
	return list, err
}

// index yang nempel di table
func listIndexes(ctx context.Context, tx pgx.Tx, oid uint32) ([]index, error) {
	rows, err := tx.Query(ctx, `SELECT i.relname, pg_get_indexdef(ix.indexrelid), ix.indisunique, ix.indisprimary
		FROM pg_index ix JOIN pg_class i ON i.oid = ix.indexrelid
		WHERE ix.indrelid = $1 ORDER BY i.relname`, oid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[index])
}

// terjemahin kode relkind postgres ke nama yang kebaca
func relKind(k string) string {
	switch k {
	case "r", "p":
		return "table"
	case "v":
		return "view"
	case "m":
		return "materialized view"
	}
	return k
}

// terjemahin kode contype postgres
func constraintType(t string) string {
	switch t {
	case "p":
		return "PRIMARY KEY"
	case "f":
		return "FOREIGN KEY"
	case "u":
		return "UNIQUE"
	case "c":
		return "CHECK"
	case "x":
		return "EXCLUDE"
	case "t":
		return "TRIGGER"
	case "n":
		return "NOT NULL"
	}
	return t
}

const (
	pageSize = 50
	maxCell  = 200
)

type cell struct {
	Text string
	Null bool
}

type tableData struct {
	Columns []string
	Rows    [][]cell
	HasNext bool
}

// kolom primary key, dipakai buat ngurutin data biar pagination stabil
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

// ambil isi table per halaman, nilainya pakai format text bawaan postgres
func readRows(ctx context.Context, tx pgx.Tx, schema, table string, oid uint32, page int) (tableData, error) {
	pk, err := primaryKey(ctx, tx, oid)
	if err != nil {
		return tableData{}, err
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
		return tableData{}, err
	}
	defer rows.Close()
	var data tableData
	for _, f := range rows.FieldDescriptions() {
		data.Columns = append(data.Columns, f.Name)
	}
	for rows.Next() {
		if len(data.Rows) == pageSize {
			data.HasNext = true
			break
		}
		raw := rows.RawValues()
		row := make([]cell, len(raw))
		for i, v := range raw {
			row[i] = toCell(v)
		}
		data.Rows = append(data.Rows, row)
	}
	return data, rows.Err()
}

// nilai NULL ditandain, yang kepanjangan dipotong biar tabel ga melebar
func toCell(raw []byte) cell {
	if raw == nil {
		return cell{Null: true}
	}
	r := []rune(string(raw))
	if len(r) > maxCell {
		return cell{Text: string(r[:maxCell]) + "…"}
	}
	return cell{Text: string(r)}
}

// nomor halaman dari query string, minimal 1 dan dibatasin biar OFFSET ga overflow
func parsePage(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return min(n, 1_000_000)
}
