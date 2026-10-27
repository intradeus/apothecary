package httpapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"apothecary/internal/engine"
	"apothecary/internal/portrelay"
)

// tcpConn dials one fixed address: the "workspace app".
type tcpConn struct{ addr string }

func (c tcpConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", c.addr)
}
func (tcpConn) Close() error { return nil }

func TestAppHostsNeedTheRunOwnersSession(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicket(t, "T")

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(appCookie); err == nil {
			t.Error("apothecary's app cookie leaked to the workspace app")
		}
		io.WriteString(w, "app:"+r.URL.Path)
	}))
	defer app.Close()

	relay := portrelay.New("")
	relay.RouteByHost("apps.test", "http")
	appURL, err := relay.Expose(runID, 3000, tcpConn{strings.TrimPrefix(app.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	x.srv.Apps = NewApps(relay, "http://ui.test")
	ts := httptest.NewServer(x.srv.Handler())
	defer ts.Close()

	// Every host resolves to the test server; the Host header picks the route.
	client := &http.Client{
		Jar: newJar(t),
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", strings.TrimPrefix(ts.URL, "http://"))
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	get := func(u string) *http.Response {
		t.Helper()
		res, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	page := strings.TrimSuffix(appURL, "/") + "/page?x=1"

	// 1. No app cookie: off to the UI host.
	res := get(page)
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "http://ui.test/api/apps/auth?to=") {
		t.Fatalf("step 1: %d %s", res.StatusCode, loc)
	}
	// 2a. Not signed in on the UI host: refused.
	if res := get(loc); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous authorize: %d", res.StatusCode)
	}
	// 2b. Signed in as the owner: a one-time code back to the app host.
	session, _, err := x.srv.Auth.NewSession(context.Background(), testUser, "test")
	if err != nil {
		t.Fatal(err)
	}
	ui, _ := url.Parse("http://ui.test/")
	client.Jar.SetCookies(ui, []*http.Cookie{{Name: sessionCookie, Value: session}})
	res = get(loc)
	back := res.Header.Get("Location")
	if res.StatusCode != http.StatusFound || !strings.Contains(back, appAuthPath+"?code=") {
		t.Fatalf("step 2: %d %s", res.StatusCode, back)
	}
	// 3. The app host trades it for its cookie and sends the browser on.
	res = get(back)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/page?x=1" {
		t.Fatalf("step 3: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if again := get(back); again.StatusCode != http.StatusForbidden {
		t.Fatalf("a code worked twice: %d", again.StatusCode)
	}
	// Now the app answers.
	r2, err := client.Get(page)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK || string(body) != "app:/page" {
		t.Fatalf("proxied: %d %q", r2.StatusCode, body)
	}
	// The UI host is untouched by the apps wrapper.
	if res := get("http://ui.test/api/auth/status"); res.StatusCode != http.StatusOK {
		t.Fatalf("UI host: %d", res.StatusCode)
	}
	// An unexposed port on the apps domain is a 404, not a redirect.
	if res := get("http://3001-" + portrelay.RunLabel(runID) + ".apps.test/"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unexposed: %d", res.StatusCode)
	}
}
