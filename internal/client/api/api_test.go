package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"apothecary/internal/account/auth"
	"apothecary/internal/httpapi"
	"apothecary/internal/platform/wstunnel"
	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/portrelay"
)

// engine is a stand-in for the API: what the client reads, as the engine
// writes it (the engine's own types), and what it was asked.
type engine struct {
	mu   sync.Mutex
	seen []string
}

func (e *engine) saw(s string) {
	e.mu.Lock()
	e.seen = append(e.seen, s)
	e.mu.Unlock()
}

func newEngine(t *testing.T) (*engine, *httptest.Server) {
	t.Helper()
	e := &engine{}
	mux := http.NewServeMux()
	bearer := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "sign in"})
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if bearer(w, r) {
			_ = json.NewEncoder(w).Encode(httpapi.Me{User: auth.User{ID: "u1", Handle: "ada", Email: "ada@example.com"}})
		}
	})
	mux.HandleFunc("GET /api/auth/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"setup_required":false,"methods":["password"],"version":"v3.4.0"}`)
	})
	mux.HandleFunc("POST /api/cli/logins", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		e.saw("start " + body["name"])
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"device_code":"dc1","user_code":"ABCD-EFGH","verification_url":"https://ui/cli-login?code=ABCD-EFGH","interval":2,"expires_in":600}`)
	})
	mux.HandleFunc("POST /api/cli/logins/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["device_code"] {
		case "pending":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"status":"pending"}`)
		case "fast":
			w.WriteHeader(http.StatusTooManyRequests)
		case "denied":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"the sign-in was denied"}`)
		case "empty":
			_, _ = io.WriteString(w, `{"status":"approved"}`)
		default:
			_, _ = io.WriteString(w, `{"status":"approved","token":"good","token_id":"t1","handle":"ada"}`)
		}
	})
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !bearer(w, r) {
			return
		}
		if r.PathValue("id") != "r1" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"run":{"run_id":"r1","state":"running","env_id":"apo-fix-r1","ticket_key":"PROJ-1","ticket_title":"Fix it"},"events":[]}`)
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		if bearer(w, r) {
			_, _ = io.WriteString(w, `[{"run_id":"r2","state":"running","env_id":"apo-two","ticket_title":"Second"},{"run_id":"r3","state":"done"}]`)
		}
	})
	mux.HandleFunc("DELETE /api/me/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		if bearer(w, r) {
			e.saw("revoke " + r.PathValue("id"))
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("GET /api/runs/{id}/ports", func(w http.ResponseWriter, r *http.Request) {
		if !bearer(w, r) {
			return
		}
		_ = json.NewEncoder(w).Encode(httpapi.RunPorts{
			Ports:    []portrelay.Exposure{{Port: 3000, URL: "https://3000-x.apps.test/"}},
			Observed: []envport.ObservedPort{{Port: 3000}, {Port: 5432, Service: "db"}, {Port: 0}},
			Links:    true,
		})
	})
	tunnelTo := func(w http.ResponseWriter, r *http.Request, what string) {
		if !bearer(w, r) {
			return
		}
		e.saw(what)
		c, err := wstunnel.Accept(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte(what))
	}
	mux.HandleFunc("GET /api/runs/{id}/ports/{port}/tunnel", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("port") == "9" {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "nothing listens on 9"})
			return
		}
		tunnelTo(w, r, "port "+r.PathValue("id")+" "+r.PathValue("port"))
	})
	mux.HandleFunc("GET /api/ssh/tunnel", func(w http.ResponseWriter, r *http.Request) {
		tunnelTo(w, r, "ssh "+r.URL.Query().Get("workspace"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return e, srv
}

func client(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	c, err := New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseBase(t *testing.T) {
	for in, want := range map[string]string{
		"apothecary.example.com":         "https://apothecary.example.com",
		" http://box:8080/ ":             "http://box:8080",
		"https://h/apothecary/?x=1#frag": "https://h/apothecary",
		"https://apothecary.tail.ts.net": "https://apothecary.tail.ts.net",
	} {
		u, err := ParseBase(in)
		if err != nil || u.String() != want {
			t.Errorf("%q = %v, %v; want %s", in, u, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://h", "https://", "http://[::1"} {
		if _, err := ParseBase(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if _, err := New("", "t"); err == nil {
		t.Error("New with no address")
	}
}

func TestMeAndRevoke(t *testing.T) {
	e, srv := newEngine(t)
	me, err := client(t, srv, "good").Me(context.Background())
	if err != nil || me.Handle != "ada" || me.ID != "u1" {
		t.Fatalf("me = %+v, %v", me, err)
	}
	if _, err := client(t, srv, "stale").Me(context.Background()); !IsUnauthorized(err) || err.Error() != "sign in" {
		t.Errorf("stale token = %v", err)
	}
	if err := client(t, srv, "good").RevokeToken(context.Background(), "t1"); err != nil || e.seen[0] != "revoke t1" {
		t.Errorf("revoke = %v %v", err, e.seen)
	}
}

// The client reads the engine's own encoding of a run's ports; Listening is
// every port once.
func TestPortsReadsTheEnginesAnswer(t *testing.T) {
	_, srv := newEngine(t)
	p, err := client(t, srv, "good").Ports(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Links || len(p.Ports) != 1 || p.Ports[0].URL != "https://3000-x.apps.test/" || p.Observed[1].Service != "db" {
		t.Errorf("ports = %+v", p)
	}
	var got []int
	for _, x := range p.Listening() {
		got = append(got, x.Port)
	}
	if len(got) != 2 || got[0] != 3000 || got[1] != 5432 {
		t.Errorf("listening = %v", got)
	}
}

func read(t *testing.T, c net.Conn) string {
	t.Helper()
	defer c.Close()
	b, _ := io.ReadAll(c)
	return string(b)
}

// Both tunnels reach the engine with the token, and a refusal comes back
// in the engine's words.
func TestTunnels(t *testing.T) {
	_, srv := newEngine(t)
	c := client(t, srv, "good")
	ctx := context.Background()
	conn, err := c.DialPort(ctx, "r1", 5432)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, conn); got != "port r1 5432" {
		t.Errorf("port tunnel = %q", got)
	}
	conn, err = c.DialSSH(ctx, "apo-w")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, conn); got != "ssh apo-w" {
		t.Errorf("ssh tunnel = %q", got)
	}
	_, err = c.DialPort(ctx, "r1", 9)
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != http.StatusBadGateway || ae.Error() != "nothing listens on 9" {
		t.Errorf("refused = %v", err)
	}
	if _, err := client(t, srv, "stale").DialSSH(ctx, "apo-w"); !IsUnauthorized(err) {
		t.Errorf("stale = %v", err)
	}
	if _, err := client(t, srv, "").DialSSH(ctx, "apo-w"); !IsUnauthorized(err) {
		t.Errorf("no token = %v", err)
	}
}

func TestErrors(t *testing.T) {
	if (&Error{Status: 502}).Error() != "the engine answered 502 Bad Gateway" {
		t.Error("an empty message says the status")
	}
	if errorText([]byte(" plain \n")) != "plain" {
		t.Error("a body that is not JSON is the message")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	defer srv.Close()
	if _, err := client(t, srv, "good").Me(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected answer") {
		t.Errorf("garbage = %v", err)
	}
	srv.Close()
	if _, err := client(t, srv, "good").Me(context.Background()); err == nil {
		t.Error("nothing listening")
	}
	if c, _ := New("https://h", ""); c.Base() != "https://h" {
		t.Error("base")
	}
}

func TestStatusSaysTheEnginesVersion(t *testing.T) {
	_, srv := newEngine(t)
	st, err := client(t, srv, "").Status(context.Background())
	if err != nil || st.Version != "v3.4.0" {
		t.Errorf("status = %+v, %v", st, err)
	}
}

// A sign-in is opened, then polled until approved; every other answer is
// the engine's.
func TestDeviceLogin(t *testing.T) {
	e, srv := newEngine(t)
	c := client(t, srv, "")
	ctx := context.Background()
	l, err := c.StartLogin(ctx, "apothecary CLI on laptop")
	if err != nil || l.DeviceCode != "dc1" || l.UserCode != "ABCD-EFGH" || l.Interval != 2 || l.ExpiresIn != 600 ||
		l.VerificationURL != "https://ui/cli-login?code=ABCD-EFGH" || e.seen[0] != "start apothecary CLI on laptop" {
		t.Fatalf("start = %+v, %v (%v)", l, err, e.seen)
	}
	for _, code := range []string{"pending", "fast"} {
		if _, err := c.PollLogin(ctx, code); !errors.Is(err, ErrLoginPending) {
			t.Errorf("%s = %v", code, err)
		}
	}
	if _, err := c.PollLogin(ctx, "denied"); err == nil || err.Error() != "the sign-in was denied" {
		t.Errorf("denied = %v", err)
	}
	if _, err := c.PollLogin(ctx, "empty"); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("approved without a token = %v", err)
	}
	tok, err := c.PollLogin(ctx, "dc1")
	if err != nil || tok.Secret != "good" || tok.ID != "t1" || tok.Handle != "ada" {
		t.Errorf("approved = %+v, %v", tok, err)
	}
	// An engine that opens a sign-in without its codes.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) }))
	defer bad.Close()
	if _, err := client(t, bad, "").StartLogin(ctx, "x"); err == nil {
		t.Error("a sign-in without codes must fail")
	}
}

// A run is found by its id, else by its workspace's name, among one's own.
func TestFindRun(t *testing.T) {
	_, srv := newEngine(t)
	c := client(t, srv, "good")
	ctx := context.Background()
	r, err := c.FindRun(ctx, "r1")
	if err != nil || r.Workspace != "apo-fix-r1" || r.Label() != "PROJ-1 · Fix it" || r.State != "running" {
		t.Fatalf("by id = %+v, %v", r, err)
	}
	r, err = c.FindRun(ctx, "apo-two")
	if err != nil || r.ID != "r2" || r.Label() != "Second" {
		t.Fatalf("by workspace = %+v, %v", r, err)
	}
	if _, err := c.FindRun(ctx, "nope"); err == nil || !strings.Contains(err.Error(), `no run or workspace named "nope"`) {
		t.Errorf("unknown = %v", err)
	}
	if _, err := client(t, srv, "stale").FindRun(ctx, "r1"); !IsUnauthorized(err) {
		t.Errorf("stale = %v", err)
	}
	for _, tc := range []struct {
		r    Run
		want string
	}{{Run{TicketKey: "K"}, "K"}, {Run{ID: "r9"}, "run r9"}} {
		if got := tc.r.Label(); got != tc.want {
			t.Errorf("label = %q, want %q", got, tc.want)
		}
	}
}

// The runs list failing is the error, not "not found".
func TestFindRunWhenTheListFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/runs" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := client(t, srv, "good").FindRun(context.Background(), "x"); err == nil || strings.Contains(err.Error(), "no run") {
		t.Errorf("err = %v", err)
	}
}
