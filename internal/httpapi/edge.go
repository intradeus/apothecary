package httpapi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/oklog/ulid/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"

	"apothecary/internal/platform/logs"
)

// requestIDShape is what an inbound X-Request-Id must look like to be kept:
// anything else (absent, too long, a header-injection attempt) is replaced.
var requestIDShape = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// slowRequest is the duration past which an otherwise unremarkable request
// is logged at Info, not Debug.
const slowRequest = time.Second

// edge is the outermost layer every request crosses: it names the request
// (X-Request-Id, echoed and stamped on every log line under it), turns a
// handler panic into a 500 instead of a dropped connection, and writes one
// access line. The status is observed through httpsnoop, so the
// ResponseWriter keeps every interface the handler relies on (Flusher for
// streams, Hijacker for WebSockets).
func (s *Server) edge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get("X-Request-Id")
		if !requestIDShape.MatchString(id) {
			id = ulid.Make().String()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := logs.With(r.Context(), logs.RequestID, id)
		r = r.WithContext(ctx)

		status := 0
		mark := func(code int) {
			if status == 0 {
				status = code
			}
		}
		w = httpsnoop.Wrap(w, httpsnoop.Hooks{
			WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
				return func(code int) { mark(code); next(code) }
			},
			Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
				return func(b []byte) (int, error) { mark(http.StatusOK); return next(b) }
			},
			ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
				return func(src io.Reader) (int64, error) { mark(http.StatusOK); return next(src) }
			},
			Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
				return func() { mark(http.StatusOK); next() }
			},
			Hijack: func(next httpsnoop.HijackFunc) httpsnoop.HijackFunc {
				return func() (net.Conn, *bufio.ReadWriter, error) { mark(http.StatusSwitchingProtocols); return next() }
			},
		})

		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v) // the stdlib's own "abort quietly"
				}
				s.logger().ErrorContext(ctx, "panic serving request",
					"method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				if status == 0 {
					writeErr(w, http.StatusInternalServerError, "internal error")
				}
			}
			if status == 0 {
				status = http.StatusOK
			}
			d := time.Since(start)
			level := slog.LevelDebug
			if status >= 400 || d >= slowRequest {
				level = slog.LevelInfo
			}
			// The request id is explicit (not from ctx) so the line carries
			// it whatever handler the logger was built with.
			s.logger().Log(context.Background(), level, "http request",
				"method", r.Method, "path", r.URL.Path, "status", status,
				"duration_ms", d.Milliseconds(), logs.RequestID, id)
		}()
		next.ServeHTTP(w, r)
	})
}

// logger is the server's logger, the process default when none was wired.
func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// traced puts a table route under an otelhttp server span named by its
// method and path template, labelled with the route on the span and the
// request metrics. Event streams stay out: a span (and a duration sample)
// lasting as long as a browser tab is open measures nothing.
func (s *Server) traced(rt Route, h http.HandlerFunc) http.Handler {
	if rt.Produces == "text/event-stream" {
		return h
	}
	labelled := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		trace.SpanFromContext(ctx).SetAttributes(semconv.HTTPRoute(rt.Path), attribute.String("apothecary.route_id", rt.ID))
		if l, ok := otelhttp.LabelerFromContext(ctx); ok {
			l.Add(attribute.String("http.route", rt.Path))
		}
		h(w, r)
	})
	return otelhttp.NewHandler(labelled, rt.Method+" "+rt.Path)
}
