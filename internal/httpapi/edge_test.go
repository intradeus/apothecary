package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// edgeRig serves h through edge with a logger recording to a buffer.
func edgeRig(h http.HandlerFunc) (http.Handler, *bytes.Buffer) {
	var buf bytes.Buffer
	s := &Server{Log: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	return s.edge(h), &buf
}

// lines decodes the JSON log lines written so far.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestEdgeKeepsAWellFormedRequestID(t *testing.T) {
	h, buf := edgeRig(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-Id", "abc.DEF-123_4")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got != "abc.DEF-123_4" {
		t.Errorf("echoed id = %q", got)
	}
	l := lines(t, buf)
	if len(l) != 1 || l[0]["request_id"] != "abc.DEF-123_4" || l[0]["status"] != float64(204) || l[0]["msg"] != "http request" {
		t.Errorf("access line = %v", l)
	}
}

func TestEdgeReplacesAMalformedRequestID(t *testing.T) {
	for _, bad := range []string{"", "has space", "x\ny", strings.Repeat("a", 65)} {
		h, _ := edgeRig(func(w http.ResponseWriter, r *http.Request) {})
		req := httptest.NewRequest("GET", "/x", nil)
		if bad != "" {
			req.Header["X-Request-Id"] = []string{bad}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		got := rec.Header().Get("X-Request-Id")
		if got == bad || !requestIDShape.MatchString(got) {
			t.Errorf("inbound %q: echoed %q", bad, got)
		}
	}
}

func TestEdgeTurnsAPanicInto500JSON(t *testing.T) {
	h, buf := edgeRig(func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "internal error" {
		t.Errorf("body = %q (%v)", rec.Body.String(), err)
	}
	l := lines(t, buf)
	if len(l) != 2 || l[0]["level"] != "ERROR" || l[0]["panic"] != "boom" || !strings.Contains(l[0]["stack"].(string), "edge") {
		t.Fatalf("panic line = %v", l)
	}
	if l[1]["status"] != float64(500) || l[1]["level"] != "INFO" {
		t.Errorf("access line = %v", l[1])
	}
}

// A panic after the response started cannot change it: the status already
// sent is what the access line reports.
func TestEdgePanicAfterWriteKeepsTheResponse(t *testing.T) {
	h, buf := edgeRig(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Errorf("response = %d %q", rec.Code, rec.Body.String())
	}
	if l := lines(t, buf); l[len(l)-1]["status"] != float64(202) {
		t.Errorf("access line = %v", l[len(l)-1])
	}
}

func TestEdgeAccessLogLevel(t *testing.T) {
	cases := []struct {
		name  string
		h     http.HandlerFunc
		level string
	}{
		{"fast ok is debug", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }, "DEBUG"},
		{"client error is info", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }, "INFO"},
		{"slow is info", func(w http.ResponseWriter, r *http.Request) { time.Sleep(slowRequest + 10*time.Millisecond) }, "INFO"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, buf := edgeRig(c.h)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
			l := lines(t, buf)
			if len(l) != 1 || l[0]["level"] != c.level || l[0]["path"] != "/x" || l[0]["method"] != "GET" {
				t.Errorf("access line = %v", l)
			}
			if _, ok := l[0]["duration_ms"]; !ok {
				t.Error("no duration_ms")
			}
		})
	}
}

// The wrapped writer keeps Flusher, which the event streams require.
func TestEdgeKeepsTheFlusher(t *testing.T) {
	var flushed bool
	h, _ := edgeRig(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if ok {
			f.Flush()
		}
		flushed = ok
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if !flushed {
		t.Error("edge hid http.Flusher")
	}
}
