package pgstore

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
)

// queryTracer turns a statement into a client span under the caller's span
// (an HTTP request, a stage attempt). A statement with no span around it —
// a background poll, a run loop between attempts — is not traced: as a
// trace of its own it would be one-query noise.
//
// Nothing a statement carries reaches the span: never its arguments (user
// content, secrets, their ciphertext), its text only with every string
// literal blanked (literalFree), and a failure only as its SQLSTATE — a
// server message can quote the offending value, and pgx's encode errors
// print the argument.
type queryTracer struct{ tracer trace.Tracer }

var _ pgx.QueryTracer = queryTracer{}

func (q queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	ctx, _ = q.tracer.Start(ctx, "pg "+firstKeyword(data.SQL),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemNamePostgreSQL, semconv.DBQueryText(literalFree(data.SQL))))
	return ctx
}

func (q queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if data.Err != nil {
		var pgErr *pgconn.PgError
		switch {
		case errors.As(data.Err, &pgErr):
			span.SetAttributes(semconv.DBResponseStatusCode(pgErr.Code), semconv.ErrorTypeKey.String(pgErr.Code))
			span.SetStatus(codes.Error, "SQLSTATE "+pgErr.Code)
		case errors.Is(data.Err, context.Canceled), errors.Is(data.Err, context.DeadlineExceeded):
			span.SetAttributes(semconv.ErrorTypeKey.String("canceled"))
			span.SetStatus(codes.Error, "canceled")
		default:
			span.SetAttributes(semconv.ErrorTypeKey.String("_OTHER"))
			span.SetStatus(codes.Error, "query failed")
		}
	}
	span.End()
}

// RedactError is err as a log line may show it when it came from Postgres
// or pgx (a logs.ErrorRedactor): a server error keeps its SQLSTATE and the
// constraint, table and column it names, not its message, which can quote
// the offending input; an encode error stops at the argument's index, since
// pgx prints the argument. Anything else is not this redactor's (ok false).
func RedactError(err error) (string, bool) {
	s := err.Error()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		safe := "ERROR (SQLSTATE " + pgErr.Code
		for _, f := range [][2]string{{"table", pgErr.TableName}, {"column", pgErr.ColumnName}, {"constraint", pgErr.ConstraintName}} {
			if f[1] != "" {
				safe += ", " + f[0] + " " + f[1]
			}
		}
		safe += ")"
		if i := strings.Index(s, pgErr.Error()); i >= 0 {
			return s[:i] + safe, true
		}
		return safe, true
	}
	// A server error flattened into text (wrapped with %v, not %w).
	if flat := serverErrorText.ReplaceAllString(s, "$1 (SQLSTATE $2)"); flat != s {
		return flat, true
	}
	if i := strings.Index(s, "failed to encode args["); i >= 0 {
		if j := strings.IndexByte(s[i:], ']'); j >= 0 {
			return s[:i+j+1] + ": <redacted>", true
		}
		return s[:i] + "failed to encode an argument", true
	}
	if i := strings.Index(s, "unable to encode "); i >= 0 {
		return s[:i] + "unable to encode an argument", true
	}
	return "", false
}

// serverErrorText is a server error's text, "ERROR: <message> (SQLSTATE
// XXXXX)": the message is what may quote a value.
var serverErrorText = regexp.MustCompile(`(?s)\b(ERROR|FATAL|PANIC): .*? \(SQLSTATE ([0-9A-Z]{5})\)`)

// literalFree is sql with each string literal ('…', E'…', $tag$…$tag$)
// replaced by '?'. Statements are parameterised, but nothing stops a value
// being spliced into one; this keeps it out of the span if it ever is.
// Comments and quoted identifiers are copied as they are, so a quote in
// them opens no literal.
func literalFree(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	n := len(sql)
	for i := 0; i < n; {
		switch c := sql[i]; {
		case strings.HasPrefix(sql[i:], "--"):
			end := strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				end = n - i
			}
			b.WriteString(sql[i : i+end])
			i += end
		case strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				b.WriteString(sql[i:])
				return b.String()
			}
			b.WriteString(sql[i : i+2+end+2])
			i += 2 + end + 2
		case c == '"':
			end := strings.IndexByte(sql[i+1:], '"')
			if end < 0 {
				b.WriteString(sql[i:])
				return b.String()
			}
			b.WriteString(sql[i : i+1+end+1])
			i += 1 + end + 1
		case c == '\'':
			backslashes := i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i < 2 || !identByte(sql[i-2]))
			j := i + 1
			for j < n {
				if backslashes && sql[j] == '\\' {
					j += 2
					continue
				}
				if sql[j] == '\'' {
					if j+1 < n && sql[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			b.WriteString("'?'")
			i = j + 1
		case c == '$' && (i == 0 || !identByte(sql[i-1])) && i+1 < n && !isDigit(sql[i+1]):
			k := i + 1
			for k < n && sql[k] != '$' && identByte(sql[k]) {
				k++
			}
			if k >= n || sql[k] != '$' {
				b.WriteByte(c)
				i++
				continue
			}
			tag := sql[i : k+1]
			end := strings.Index(sql[k+1:], tag)
			b.WriteString("'?'")
			if end < 0 {
				return b.String()
			}
			i = k + 1 + end + len(tag)
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func identByte(c byte) bool {
	return c == '_' || c == '$' || isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// firstKeyword is the statement's verb (SELECT, INSERT, WITH…), upper-cased,
// past leading blanks and "--" comment lines: a low-cardinality span name.
func firstKeyword(sql string) string {
	s := sql
	for {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		if !strings.HasPrefix(s, "--") {
			break
		}
		nl := strings.IndexByte(s, '\n')
		if nl < 0 {
			return "QUERY"
		}
		s = s[nl+1:]
	}
	end := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		end = len(s)
	}
	if end == 0 {
		return "QUERY"
	}
	return strings.ToUpper(s[:end])
}
