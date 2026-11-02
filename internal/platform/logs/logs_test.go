package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/embedded"
	"go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/trace"
)

type off struct{}

func (off) Enabled() bool                      { return false }
func (off) LoggerProvider() log.LoggerProvider { return noop.NewLoggerProvider() }

// on is an enabled exporter recording what reaches the OTel side.
type on struct{ got *[]string }

func (o on) Enabled() bool                      { return true }
func (o on) LoggerProvider() log.LoggerProvider { return provider{got: o.got} }

type provider struct {
	embedded.LoggerProvider
	got *[]string
}

func (p provider) Logger(string, ...log.LoggerOption) log.Logger { return logger{got: p.got} }

type logger struct {
	embedded.Logger
	got *[]string
}

func (l logger) Emit(_ context.Context, r log.Record)                { *l.got = append(*l.got, r.Body().AsString()) }
func (l logger) Enabled(context.Context, log.EnabledParameters) bool { return true }

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("%v: %s", err, buf)
	}
	return m
}

// Records logged under a With context and an active span carry the pairs and
// the trace and span IDs, without touching records logged elsewhere.
func TestContextPairsAndTraceIDsAreStamped(t *testing.T) {
	var buf bytes.Buffer
	l, err := New("json", "info", &buf, off{})
	if err != nil {
		t.Fatal(err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	})
	ctx := With(With(context.Background(), RunID, "r1"), UserID, "u1")
	ctx = trace.ContextWithSpanContext(ctx, sc)
	l.InfoContext(ctx, "hello", StageID, "build")
	m := decode(t, &buf)
	for k, want := range map[string]string{
		"msg": "hello", RunID: "r1", UserID: "u1", StageID: "build",
		"trace_id": sc.TraceID().String(), "span_id": sc.SpanID().String(),
	} {
		if m[k] != want {
			t.Errorf("%s = %v, want %s", k, m[k], want)
		}
	}

	buf.Reset()
	l.With(ProjectID, "p1").InfoContext(context.Background(), "plain")
	m = decode(t, &buf)
	if m[ProjectID] != "p1" || m[RunID] != nil || m["trace_id"] != nil {
		t.Errorf("plain record = %v", m)
	}

	// A key the call site sets itself wins over the ctx's, once.
	buf.Reset()
	l.InfoContext(ctx, "named", RunID, "r1")
	if line := buf.String(); strings.Count(line, `"run_id"`) != 1 || !strings.Contains(line, `"user_id":"u1"`) {
		t.Errorf("named record = %s", line)
	}
}

// The level gates both outputs; text is the other format; bad ones refuse.
func TestLevelFormatAndExport(t *testing.T) {
	var buf bytes.Buffer
	var exported []string
	l, err := New("text", "warn", &buf, on{got: &exported})
	if err != nil {
		t.Fatal(err)
	}
	l.Info("quiet")
	l.Warn("loud")
	if out := buf.String(); strings.Contains(out, "quiet") || !strings.Contains(out, "msg=loud") {
		t.Errorf("text output = %q", out)
	}
	if len(exported) != 1 || exported[0] != "loud" {
		t.Errorf("exported = %v", exported)
	}
	l.WithGroup("g").With("k", "v").Error("grouped")
	if len(exported) != 2 || !strings.Contains(buf.String(), "g.k=v") {
		t.Errorf("grouped: exported %v, out %q", exported, buf.String())
	}

	if _, err := New("xml", "info", &buf, off{}); err == nil {
		t.Error("xml accepted")
	}
	if _, err := New("json", "loud", &buf, off{}); err == nil {
		t.Error("level loud accepted")
	}
}

// attrsOn is an enabled exporter recording each exported record's attributes.
type attrsOn struct{ got *[]string }

func (o attrsOn) Enabled() bool                      { return true }
func (o attrsOn) LoggerProvider() log.LoggerProvider { return attrsProvider{got: o.got} }

type attrsProvider struct {
	embedded.LoggerProvider
	got *[]string
}

func (p attrsProvider) Logger(string, ...log.LoggerOption) log.Logger { return attrsLogger{got: p.got} }

type attrsLogger struct {
	embedded.Logger
	got *[]string
}

func (l attrsLogger) Emit(_ context.Context, r log.Record) {
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		*l.got = append(*l.got, string(kv.Key)+"="+kv.Value.Emit())
		return true
	})
}
func (l attrsLogger) Enabled(context.Context, log.EnabledParameters) bool { return true }

type quoting struct{ value string }

func (q quoting) Error() string { return `bad input "` + q.value + `"` }

// An error a redactor knows reaches neither output as it is: not as a call
// site's attribute, a logger's bound one, or one inside a group. Other
// errors and values are left alone.
func TestErrorsAreRedactedInBothOutputs(t *testing.T) {
	const secret = "c2VjcmV0LWNpcGhlcnRleHQ"
	redact := func(err error) (string, bool) {
		var q quoting
		if errors.As(err, &q) {
			return "bad input", true
		}
		return "", false
	}
	var buf bytes.Buffer
	var exported []string
	l, err := New("json", "info", &buf, attrsOn{got: &exported}, redact)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := fmt.Errorf("save secret: %w", quoting{secret})
	l.With("bound", wrapped).Info("saving",
		"err", wrapped,
		slog.Group("db", slog.Any("err", quoting{secret})),
		"other", errors.New("plain failure"),
		"n", 3)

	line := buf.String()
	if strings.Contains(line, secret) || strings.Contains(strings.Join(exported, " "), secret) {
		t.Fatalf("the value got out:\n%s\n%v", line, exported)
	}
	if got := strings.Join(exported, " "); !strings.Contains(got, "err=bad input") || !strings.Contains(got, "bound=bad input") {
		t.Errorf("exported = %v", exported)
	}
	m := decode(t, &buf)
	if m["err"] != "bad input" || m["bound"] != "bad input" || m["db"].(map[string]any)["err"] != "bad input" {
		t.Errorf("redacted = %v", m)
	}
	if m["other"] != "plain failure" || m["n"] != float64(3) {
		t.Errorf("untouched = %v", m)
	}
}
