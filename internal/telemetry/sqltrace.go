package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const maxParamLen = 100

type (
	actorKey    struct{}
	reqActorKey struct{}
	attrsKey    struct{}
	queryKey    struct{}
)

type queryStart struct {
	sql     string
	event   string
	started time.Time
	span    trace.Span
}

// sumber actor untuk request API, diisi main.go dari session (worker pakai WithActor)
var actorFunc func(context.Context) string

func SetActorFunc(f func(context.Context) string) { actorFunc = f }

// tandai siapa yang menjalankan query di ctx ini, dipakai worker dengan requested_by
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ID korelasi (database, job, execution, attempt, batch) yang ikut di setiap log SQL dalam ctx ini
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	prev, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	return context.WithValue(ctx, attrsKey{}, append(append([]slog.Attr{}, prev...), attrs...))
}

// isi actor untuk request HTTP yang berjalan di dalam Middleware
func SetRequestActor(ctx context.Context, name string) {
	if slot, ok := ctx.Value(reqActorKey{}).(*string); ok && name != "" {
		*slot = name
	}
}

func actor(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok {
		return a
	}
	if slot, ok := ctx.Value(reqActorKey{}).(*string); ok && *slot != "" {
		return *slot
	}
	if actorFunc != nil {
		return actorFunc(ctx)
	}
	return ""
}

// pgx.QueryTracer yang mencatat mulai, hasil, dan commit/rollback setiap query sebagai log + span
type QueryTracer struct {
	DB     string // "platform" atau "target"
	Logger *slog.Logger
}

func (t *QueryTracer) logger() *slog.Logger {
	if t.Logger != nil {
		return t.Logger
	}
	return slog.Default()
}

func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := Redact(data.SQL)
	event := sqlEvent(sql)
	ctx, span := Tracer().Start(ctx, event, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.role", t.DB), attribute.String("db.statement", sql)))
	attrs := t.common(ctx, event, sql)
	attrs = append(attrs, slog.Any("params", params(sql, data.Args)))
	t.logger().LogAttrs(ctx, slog.LevelInfo, event+".start", attrs...)
	return context.WithValue(ctx, queryKey{}, queryStart{sql: sql, event: event, started: time.Now(), span: span})
}

func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	q, ok := ctx.Value(queryKey{}).(queryStart)
	if !ok {
		return
	}
	attrs := t.common(ctx, q.event, q.sql)
	attrs = append(attrs,
		slog.Float64("duration_ms", float64(time.Since(q.started).Microseconds())/1000),
		slog.Int64("rows", data.CommandTag.RowsAffected()),
	)
	level, status := slog.LevelInfo, "ok"
	if data.Err != nil {
		level, status = slog.LevelError, "error"
		attrs = append(attrs, slog.String("error", Redact(data.Err.Error())))
		var pgErr *pgconn.PgError
		if errors.As(data.Err, &pgErr) {
			attrs = append(attrs, slog.String("sqlstate", pgErr.Code))
			q.span.SetAttributes(attribute.String("db.sqlstate", pgErr.Code))
		}
		RecordError(q.span, data.Err)
	}
	attrs = append(attrs, slog.String("status", status))
	q.span.SetAttributes(attribute.Int64("db.rows", data.CommandTag.RowsAffected()))
	q.span.End()
	t.logger().LogAttrs(ctx, level, q.event+".end", attrs...)
}

func (t *QueryTracer) common(ctx context.Context, event, sql string) []slog.Attr {
	return []slog.Attr{slog.String("event", event), slog.String("db", t.DB), slog.String("query", sql)}
}

// commit/rollback/begin dicatat sebagai event tersendiri, sisanya sql.query
func sqlEvent(sql string) string {
	word := strings.ToLower(strings.TrimSpace(sql))
	if i := strings.IndexAny(word, " ;\n\t"); i >= 0 {
		word = word[:i]
	}
	switch word {
	case "commit", "end":
		return "sql.commit"
	case "rollback", "abort":
		return "sql.rollback"
	case "begin", "start":
		return "sql.begin"
	}
	return "sql.query"
}

// nilai parameter untuk log: dipotong, dan semuanya disamarkan kalau query menyentuh kolom rahasia
func params(sql string, args []any) []string {
	if len(args) == 0 {
		return nil
	}
	hide := secretKey(sql)
	out := make([]string, len(args))
	for i, a := range args {
		if rv := reflect.ValueOf(a); rv.IsValid() && rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				out[i] = "NULL"
				continue
			}
			a = rv.Elem().Interface()
		}
		switch v := a.(type) {
		case nil:
			out[i] = "NULL"
		case []byte:
			out[i] = fmt.Sprintf("[%d bytes]", len(v))
		default:
			s := fmt.Sprint(v)
			if hide {
				s = redacted
			} else if r := []rune(Redact(s)); len(r) > maxParamLen {
				s = string(r[:maxParamLen]) + "…"
			} else {
				s = string(r)
			}
			out[i] = s
		}
	}
	return out
}
