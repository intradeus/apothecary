package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"apothecary/internal/adapters/pgstore/pgtest"
)

func TestFirstKeyword(t *testing.T) {
	for sql, want := range map[string]string{
		"SELECT 1":                          "SELECT",
		"\n\t  insert into runs values($1)": "INSERT",
		"-- rebuild\n-- more\nWITH x AS (SELECT 1) SELECT * FROM x": "WITH",
		"select(1)":         "SELECT",
		"":                  "QUERY",
		"-- only a comment": "QUERY",
		"(SELECT 1)":        "QUERY",
	} {
		if got := firstKeyword(sql); got != want {
			t.Errorf("firstKeyword(%q) = %q, want %q", sql, got, want)
		}
	}
}

// A statement's span names its verb and carries its text, never a value:
// not the arguments bound to it, not a literal spliced into it, not a
// failure that quotes one (a server message, a pgx encode error) — the
// failure is only its SQLSTATE.
func TestQueryTracerNeverRecordsAValue(t *testing.T) {
	const secret = "hunter2-sk-live"
	for _, c := range []struct {
		name, sql, text string
		err             error
		status, errType string
	}{
		{"server error quoting the value", "UPDATE users SET token = $1 WHERE id = $2", "UPDATE users SET token = $1 WHERE id = $2",
			fmt.Errorf("save user: %w", &pgconn.PgError{Severity: "ERROR", Code: "22P02", Message: `invalid input syntax for type uuid: "` + secret + `"`}),
			"SQLSTATE 22P02", "22P02"},
		{"encode error printing the argument", "INSERT INTO secrets (value) VALUES ($1)", "INSERT INTO secrets (value) VALUES ($1)",
			fmt.Errorf("failed to encode args[0]: unable to encode %#v into binary format for int4 (OID 23)", []byte(secret)),
			"query failed", "_OTHER"},
		{"spliced literal", "UPDATE users SET token = '" + secret + "', note = E'a\\'" + secret + "' WHERE id = $1",
			"UPDATE users SET token = '?', note = E'?' WHERE id = $1", context.Canceled, "canceled", "canceled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			spans := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
			q := queryTracer{tracer: tp.Tracer("test")}
			parent, request := tp.Tracer("test").Start(context.Background(), "request")
			defer request.End()

			ctx := q.TraceQueryStart(parent, nil, pgx.TraceQueryStartData{SQL: c.sql, Args: []any{secret, "u1"}})
			q.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: c.err})

			got := spans.GetSpans()
			if len(got) != 1 {
				t.Fatalf("spans = %d", len(got))
			}
			s := got[0]
			attrs := map[string]string{}
			for _, a := range s.Attributes {
				attrs[string(a.Key)] = a.Value.Emit()
			}
			if attrs["db.system.name"] != "postgresql" || attrs["db.query.text"] != c.text || attrs["error.type"] != c.errType {
				t.Errorf("attributes = %v", attrs)
			}
			if s.Status.Code != codes.Error || s.Status.Description != c.status {
				t.Errorf("status = %+v, want %q", s.Status, c.status)
			}
			everything := s.Name + " " + s.Status.Description
			for _, v := range attrs {
				everything += " " + v
			}
			for _, e := range s.Events {
				everything += " " + e.Name
				for _, a := range e.Attributes {
					everything += " " + a.Value.Emit()
				}
			}
			if strings.Contains(everything, secret) {
				t.Errorf("the span shows the value: %s", everything)
			}
		})
	}
}

func TestLiteralFree(t *testing.T) {
	for sql, want := range map[string]string{
		"SELECT $1, $2::text":                             "SELECT $1, $2::text",
		"SELECT 'it''s', E'a\\'b', 'x'":                   "SELECT '?', E'?', '?'",
		"SELECT $$body$$, $fn$ a $$ b $fn$, $1":           "SELECT '?', '?', $1",
		"SELECT 1 -- don't\nFROM t /* it's */ WHERE a=$1": "SELECT 1 -- don't\nFROM t /* it's */ WHERE a=$1",
		`SELECT "o'k" FROM t WHERE x = 'v'`:               `SELECT "o'k" FROM t WHERE x = '?'`,
		"SELECT 'unterminated":                            "SELECT '?'",
		"SELECT $tag$ unterminated":                       "SELECT '?'",
		"SELECT foo$bar, $":                               "SELECT foo$bar, $",
	} {
		if got := literalFree(sql); got != want {
			t.Errorf("literalFree(%q) = %q, want %q", sql, got, want)
		}
	}
}

// The errors Postgres and pgx actually return can quote a bound value; the
// log redactor keeps what locates the failure (SQLSTATE, the wrapping
// context, the argument's index) and drops the value. Errors that are not
// the driver's are not its business.
func TestRedactErrorDropsQuotedValues(t *testing.T) {
	s := openDSN(t, pgtest.Empty(t))
	ctx := context.Background()
	const secret = "c2VjcmV0LWNpcGhlcnRleHQ"
	_, serverErr := s.pool.Exec(ctx, "SELECT $1::text::uuid", secret)
	_, encodeErr := s.pool.Exec(ctx, "SELECT $1::int4", struct{ S string }{secret})
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"server", fmt.Errorf("save secret: %w", serverErr), "save secret: ERROR (SQLSTATE 22P02)"},
		{"encode", fmt.Errorf("save secret: %w", encodeErr), "save secret: failed to encode args[0]: <redacted>"},
		{"flattened", fmt.Errorf("save secret: %v", serverErr), "save secret: ERROR (SQLSTATE 22P02)"},
	} {
		if c.err == nil || !strings.Contains(c.err.Error(), secret) {
			t.Fatalf("%s: precondition: the driver's error %v does not quote the value", c.name, c.err)
		}
		got, ok := RedactError(c.err)
		if !ok || got != c.want {
			t.Errorf("%s: RedactError = %q, %v; want %q", c.name, got, ok, c.want)
		}
	}
	pgErr := &pgconn.PgError{Severity: "ERROR", Code: "23505", Message: `duplicate key value violates unique constraint "users_email_key"`,
		TableName: "users", ConstraintName: "users_email_key"}
	if got, _ := RedactError(pgErr); got != "ERROR (SQLSTATE 23505, table users, constraint users_email_key)" {
		t.Errorf("constraint error = %q", got)
	}
	if _, ok := RedactError(errors.New("connection refused")); ok {
		t.Error("a non-driver error was redacted")
	}
}

// A statement outside any span (a background poll, a run loop between
// attempts) is not a trace of its own: one query per trace is noise.
func TestQueryTracerSkipsStatementsWithoutAParent(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	q := queryTracer{tracer: tp.Tracer("test")}

	ctx := q.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	q.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if got := spans.GetSpans(); len(got) != 0 {
		t.Errorf("spans = %d (%s), want none", len(got), got[0].Name)
	}
}
