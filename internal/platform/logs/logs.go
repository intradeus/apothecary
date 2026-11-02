// Package logs builds the daemon's slog logger: one stderr handler in the
// configured format and level, fanned out to OpenTelemetry when telemetry is
// on, and a wrapper that stamps every record with the context it was logged
// under (run, user, request; trace and span IDs). Correlation then needs no
// discipline at call sites: code that has a ctx and calls the *Context slog
// methods gets its line tied to the run and trace it belongs to. The same
// wrapper passes every error attribute through the redactors New was given,
// so an error that can quote a stored value (a database driver's) is cut
// before it reaches stderr or the export.
package logs

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// The shared attribute keys, so a run's lines are found by one name.
const (
	RunID     = "run_id"
	ProjectID = "project_id"
	UserID    = "user_id"
	StageID   = "stage_id"
	RequestID = "request_id"
)

type ctxKey struct{}

// With returns ctx carrying key/value pairs every record logged under it
// gets, after any pairs ctx already carried.
func With(ctx context.Context, args ...any) context.Context {
	prev, _ := ctx.Value(ctxKey{}).([]any)
	next := make([]any, 0, len(prev)+len(args))
	next = append(append(next, prev...), args...)
	return context.WithValue(ctx, ctxKey{}, next)
}

// Exporter is what New needs from the telemetry providers.
type Exporter interface {
	Enabled() bool
	LoggerProvider() log.LoggerProvider
}

// ErrorRedactor describes err without what it may quote: ok is false when
// err is none of the redactor's kind, and the error is logged as it is.
type ErrorRedactor func(err error) (safe string, ok bool)

// New builds the logger: format "text" or "json" at level
// debug|info|warn|error on w, and an OTel log export when prov is enabled.
// Every error attribute goes through redact, first match wins.
func New(format, level string, w io.Writer, prov Exporter, redact ...ErrorRedactor) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("logs: level %q: %w", level, err)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch format {
	case "text":
		h = slog.NewTextHandler(w, opts)
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		return nil, fmt.Errorf("logs: format must be text or json, got %q", format)
	}
	if prov != nil && prov.Enabled() {
		h = slog.NewMultiHandler(h, leveled{
			Handler: otelslog.NewHandler("apothecaryd", otelslog.WithLoggerProvider(prov.LoggerProvider())),
			min:     lvl,
		})
	}
	return slog.New(contextHandler{h, redact}), nil
}

// leveled holds the OTel export to the configured level, as the stderr
// handler is.
type leveled struct {
	slog.Handler
	min slog.Level
}

func (l leveled) Enabled(ctx context.Context, lv slog.Level) bool {
	return lv >= l.min && l.Handler.Enabled(ctx, lv)
}

func (l leveled) WithAttrs(as []slog.Attr) slog.Handler {
	return leveled{l.Handler.WithAttrs(as), l.min}
}

func (l leveled) WithGroup(name string) slog.Handler {
	return leveled{l.Handler.WithGroup(name), l.min}
}

// contextHandler adds the pairs With stored and the active span's IDs, and
// redacts error attributes. A pair whose key the call site already set is
// dropped, so a line logged under a run's ctx that also names the run
// carries one run_id, not two.
type contextHandler struct {
	slog.Handler
	redact []ErrorRedactor
}

func (c contextHandler) Handle(ctx context.Context, r slog.Record) error {
	pairs, _ := ctx.Value(ctxKey{}).([]any)
	sc := trace.SpanContextFromContext(ctx)
	if len(pairs) == 0 && !sc.IsValid() && len(c.redact) == 0 {
		return c.Handler.Handle(ctx, r)
	}
	set := make(map[string]bool, r.NumAttrs())
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		set[a.Key] = true
		out.AddAttrs(c.redacted(a))
		return true
	})
	var from slog.Record
	from.Add(pairs...)
	from.Attrs(func(a slog.Attr) bool {
		if !set[a.Key] {
			set[a.Key] = true
			out.AddAttrs(c.redacted(a))
		}
		return true
	})
	if sc.IsValid() {
		out.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return c.Handler.Handle(ctx, out)
}

// redacted is a with every error in it (in groups too) as its redactor
// describes it.
func (c contextHandler) redacted(a slog.Attr) slog.Attr {
	if len(c.redact) == 0 {
		return a
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		g := v.Group()
		out := make([]slog.Attr, len(g))
		for i, ga := range g {
			out[i] = c.redacted(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		if err, ok := v.Any().(error); ok && err != nil {
			for _, r := range c.redact {
				if safe, ok := r(err); ok {
					return slog.String(a.Key, safe)
				}
			}
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func (c contextHandler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = c.redacted(a)
	}
	return contextHandler{c.Handler.WithAttrs(out), c.redact}
}

func (c contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{c.Handler.WithGroup(name), c.redact}
}
