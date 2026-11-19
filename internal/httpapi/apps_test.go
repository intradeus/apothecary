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

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
	"apothecary/internal/workspace/portrelay"
)

// tcpConn dials one fixed address: the "workspace app".
type tcpConn struct{ addr string }

func (c tcpConn) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", c.addr)
}
func (tcpConn) Close() error { return nil }

// appHost exposes a run's port 3000 on the apps.test domain in front of x's
// server, with a browser that keeps each host's cookies.
type appHost struct {
	client *http.Client
	page   string
}

func newAppHost(t *testing.T, x *env, runID string) *appHost {
	t.Helper()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(appCookie); err == nil {
			t.Error("apothecary's app cookie leaked to the workspace app")
		}
		io.WriteString(w, "app:"+r.URL.Path)
	}))
	t.Cleanup(app.Close)

	relay := portrelay.New("")
	relay.RouteByHost("apps.test", "http")
	appURL, err := relay.Expose(runID, 3000, tcpConn{strings.TrimPrefix(app.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	x.srv.Apps = NewApps(relay, "http://ui.test")
	ts := httptest.NewServer(x.srv.Handler())
	t.Cleanup(ts.Close)

	// Every host resolves to the test server; the Host header picks the route.
	client := &http.Client{
		Jar: newJar(t),
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", strings.TrimPrefix(ts.URL, "http://"))
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &appHost{client: client, page: strings.TrimSuffix(appURL, "/") + "/page?x=1"}
}

func (h *appHost) get(t *testing.T, u string) *http.Response {
	t.Helper()
	res, err := h.client.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// session signs the browser in on the UI host as user.
func (h *appHost) session(t *testing.T, x *env, user string) {
	t.Helper()
	session, _, err := x.srv.Auth.NewSession(context.Background(), user, "test")
	if err != nil {
		t.Fatal(err)
	}
	ui, _ := url.Parse("http://ui.test/")
	h.client.Jar.SetCookies(ui, []*http.Cookie{{Name: sessionCookie, Value: session}})
}

// signIn walks the whole hand-off as user, who must be let in.
func (h *appHost) signIn(t *testing.T, x *env, user string) {
	t.Helper()
	h.session(t, x, user)
	loc := h.get(t, h.page).Header.Get("Location")
	back := h.get(t, loc).Header.Get("Location")
	if res := h.get(t, back); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/page?x=1" {
		t.Fatalf("sign-in as %s: %d %s", user, res.StatusCode, res.Header.Get("Location"))
	}
}

func TestAppHostsNeedTheRunOwnersSession(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicket(t, "T")
	h := newAppHost(t, x, runID)

	// 1. No app cookie: off to the UI host.
	res := h.get(t, h.page)
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "http://ui.test/api/apps/auth?to=") {
		t.Fatalf("step 1: %d %s", res.StatusCode, loc)
	}
	// 2a. Not signed in on the UI host: refused.
	if res := h.get(t, loc); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous authorize: %d", res.StatusCode)
	}
	// 2b. Signed in as the owner: a one-time code back to the app host.
	h.session(t, x, testUser)
	res = h.get(t, loc)
	back := res.Header.Get("Location")
	if res.StatusCode != http.StatusFound || !strings.Contains(back, appAuthPath+"?code=") {
		t.Fatalf("step 2: %d %s", res.StatusCode, back)
	}
	// 3. The app host trades it for its cookie and sends the browser on.
	res = h.get(t, back)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/page?x=1" {
		t.Fatalf("step 3: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if again := h.get(t, back); again.StatusCode != http.StatusForbidden {
		t.Fatalf("a code worked twice: %d", again.StatusCode)
	}
	// Now the app answers.
	r2, err := h.client.Get(h.page)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK || string(body) != "app:/page" {
		t.Fatalf("proxied: %d %q", r2.StatusCode, body)
	}
	// The UI host is untouched by the apps wrapper.
	if res := h.get(t, "http://ui.test/api/auth/status"); res.StatusCode != http.StatusOK {
		t.Fatalf("UI host: %d", res.StatusCode)
	}
	// An unexposed port on the apps domain is a 404, not a redirect.
	if res := h.get(t, "http://3001-"+portrelay.RunLabel(runID)+".apps.test/"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unexposed: %d", res.StatusCode)
	}
}

// An app cookie is the account it was issued to, rechecked on every request:
// a maintainer who loses run:shell loses the app on the next request, though
// the cookie has hours left.
func TestAppGrantFollowsTheMembership(t *testing.T) {
	ctx := context.Background()
	for name, revoke := range map[string]func(t *testing.T, x *env){
		"demoted": func(t *testing.T, x *env) { x.share(t, testProject, "maint", "operator") },
		"removed": func(t *testing.T, x *env) {
			if err := x.srv.Conf.DeleteMember(ctx, testProject, "maint"); err != nil {
				t.Fatal(err)
			}
		},
		"disabled": func(t *testing.T, x *env) {
			if _, err := x.srv.Auth.SetDisabled(ctx, "maint", true); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newEnv(t, engine.Config{})
			runID := x.createTicket(t, "T")
			x.account(t, "maint", auth.RoleMember)
			x.share(t, testProject, "maint", "maintainer")
			h := newAppHost(t, x, runID)
			h.signIn(t, x, "maint")
			if res := h.get(t, h.page); res.StatusCode != http.StatusOK {
				t.Fatalf("maintainer's app: %d", res.StatusCode)
			}
			revoke(t, x)
			res := h.get(t, h.page)
			if loc := res.Header.Get("Location"); res.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "http://ui.test/api/apps/auth?to=") {
				t.Fatalf("after %s: %d %s, want the sign-in redirect", name, res.StatusCode, loc)
			}
		})
	}
}
