package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/auth/authtest"
	"apothecary/internal/engine"
)

func newJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jar
}

func readBody(res *http.Response) string {
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return string(data)
}

// Every route says how it is reached, and a route whose path names a
// resource says whose it must be. The guard is one rule in one place, so
// this is the test that keeps a new endpoint from being open by accident:
// an undeclared route is a user route (the zero value), and a user route
// with an id in its path must carry a scope.
func TestEveryRouteDeclaresItsAccess(t *testing.T) {
	public := map[string]bool{
		"GET /api/auth/status": true,
		"POST /api/auth/setup": true,
		"POST /api/auth/login": true,
	}
	idParam := regexp.MustCompile(`\{(id|pid)\}`)
	// Routes whose id is checked inside the handler rather than by scope:
	// the token belongs to the caller by construction (DeleteToken takes
	// the user id), the user routes are the admin's.
	inHandler := map[string]bool{
		"DELETE /api/me/tokens/{id}":   true,
		"DELETE /api/me/ssh-keys/{id}": true,
	}
	for _, rt := range (&Server{}).routes() {
		where := rt.Method + " " + rt.Path
		switch rt.Auth {
		case AccessPublic:
			if !public[where] {
				t.Errorf("%s is public but not on the allowlist — is it meant to be?", where)
			}
		case AccessAdmin:
			// Admin routes name users, not owned resources.
		case AccessRunToken, AccessBrokerToken:
			if !strings.HasPrefix(rt.Path, "/api/runs/{id}/") {
				t.Errorf("%s takes a run token but is not under a run", where)
			}
		case AccessUser:
			if idParam.MatchString(rt.Path) && rt.Scope == ScopeNone && !inHandler[where] {
				t.Errorf("%s names a resource in its path but declares no ownership scope", where)
			}
		}
	}
	for where := range public {
		found := false
		for _, rt := range (&Server{}).routes() {
			if rt.Method+" "+rt.Path == where {
				found = true
			}
		}
		if !found {
			t.Errorf("allowlisted public route %s does not exist", where)
		}
	}
}

// Without a credential the API says so; with someone else's it says the
// resource does not exist. The second person is a real account on the same
// server: what they see of the first person's work is nothing.
func TestOwnershipIsTheWholeView(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicket(t, "mine")

	// No credential at all: 401.
	req, _ := http.NewRequest("GET", x.ts.URL+"/api/runs/"+runID, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", res.StatusCode)
	}

	// Another account, with its own token.
	now := time.Now().UTC()
	if err := x.users.CreateUser(context.Background(), auth.User{
		ID: "usr-other", Handle: "other", Email: "other@example.test", Name: "Other",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	other, _, err := x.srv.Auth.NewToken(context.Background(), "usr-other", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	hdr := map[string]string{"Authorization": "Bearer " + other}

	// The run, the project and the ticket are not there for them.
	for _, path := range []string{
		"/api/runs/" + runID,
		"/api/projects/" + testProject,
		"/api/runs/" + runID + "/events",
	} {
		res, body := x.do(t, "GET", path, nil, hdr)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s as other = %d %s, want 404", path, res.StatusCode, body)
		}
	}
	// And the lists are empty, not filtered-looking.
	if res, body := x.do(t, "GET", "/api/runs", nil, hdr); res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("other's runs = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "GET", "/api/projects", nil, hdr); res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("other's projects = %d %s", res.StatusCode, body)
	}
	// Nor can they file a ticket into it.
	res, body := x.do(t, "POST", "/api/tickets", map[string]any{"title": "x", "project_id": testProject}, hdr)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("ticket into someone else's project = %d %s, want 404", res.StatusCode, body)
	}
	// The owner still sees everything.
	if res, _ := x.do(t, "GET", "/api/runs/"+runID, nil, nil); res.StatusCode != http.StatusOK {
		t.Errorf("owner status = %d", res.StatusCode)
	}
}

// A browser's cookie session: setup claims a fresh instance, sign-in works
// by handle or email, a cross-site write is refused, and a wrong password
// is throttled after enough tries.
func TestSessionsAndSetup(t *testing.T) {
	svc := authtest.New(auth.NewMem())
	x := newEnv(t, engine.Config{})
	x.srv.Auth = svc
	ts := httptest.NewServer(x.srv.Handler())
	t.Cleanup(ts.Close)
	client := &http.Client{}
	client.Jar = newJar(t)

	get := func(path string) (*http.Response, string) {
		res, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return res, readBody(res)
	}
	post := func(path string, body string, site string) (*http.Response, string) {
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res, readBody(res)
	}

	if res, body := get("/api/auth/status"); res.StatusCode != http.StatusOK || !strings.Contains(body, `"setup_required":true`) {
		t.Fatalf("status before setup = %d %s", res.StatusCode, body)
	}
	if res, body := get("/api/me"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me before setup = %d %s", res.StatusCode, body)
	}
	// Too short a password is refused, with the reason.
	if res, body := post("/api/auth/setup", `{"handle":"lu","email":"lu@example.test","password":"short"}`, ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("weak password = %d %s", res.StatusCode, body)
	}
	res, body := post("/api/auth/setup", `{"handle":"Lu","email":"Lu@Example.test","name":"Lu","password":"correct horse"}`, "same-origin")
	if res.StatusCode != http.StatusCreated || !strings.Contains(body, `"admin":true`) || !strings.Contains(body, `"handle":"lu"`) {
		t.Fatalf("setup = %d %s", res.StatusCode, body)
	}
	// The cookie is the session now: me answers.
	if res, body := get("/api/me"); res.StatusCode != http.StatusOK || !strings.Contains(body, `"password"`) {
		t.Fatalf("me after setup = %d %s", res.StatusCode, body)
	}
	// A second setup is refused.
	if res, _ := post("/api/auth/setup", `{"handle":"eve","email":"eve@example.test","password":"correct horse"}`, "same-origin"); res.StatusCode != http.StatusConflict {
		t.Errorf("second setup = %d", res.StatusCode)
	}
	// A cookie write from another site is refused.
	if res, _ := post("/api/auth/logout", ``, "cross-site"); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site write = %d, want 403", res.StatusCode)
	}
	if res, _ := post("/api/auth/logout", ``, "same-origin"); res.StatusCode != http.StatusNoContent {
		t.Errorf("logout = %d", res.StatusCode)
	}
	if res, _ := get("/api/me"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("me after logout = %d", res.StatusCode)
	}
	// Sign in by email, case-insensitively.
	if res, body := post("/api/auth/login", `{"login":"LU@example.test","proof":"correct horse"}`, "same-origin"); res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %s", res.StatusCode, body)
	}
	if res, _ := get("/api/me"); res.StatusCode != http.StatusOK {
		t.Errorf("me after login = %d", res.StatusCode)
	}
	// Wrong passwords: 401 each, then throttled.
	status := 0
	for i := 0; i < loginFailMax+1; i++ {
		res, _ := post("/api/auth/login", `{"login":"lu","proof":"nope"}`, "same-origin")
		status = res.StatusCode
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("after %d failures status = %d, want 429", loginFailMax+1, status)
	}
}
