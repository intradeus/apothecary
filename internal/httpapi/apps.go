package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"apothecary/internal/auth"
	"apothecary/internal/portrelay"
)

// Apps serves exposed workspace ports by hostname — <port>-<run>.<apps
// domain> — from the engine's own HTTP server, for an engine behind a Service
// and an Ingress where a listener per port is unreachable (Kubernetes).
//
// An app host is a different origin from the UI, so the UI's session cookie
// never reaches it. Access is handed over once per app host:
//
//  1. no app cookie → redirect to the UI host's /api/apps/auth?to=<app URL>;
//  2. there, the signed-in owner of the run gets a one-time code and is sent
//     back to <app host>/__apothecary/auth?code=…&to=<path>;
//  3. the app host trades the code for its own host-only cookie and sends the
//     browser on to the page it asked for.
//
// So an exposed app — code-server included, which runs with --auth none — is
// reachable by its run's owner only, never by whoever guesses a hostname.
type Apps struct {
	Relay *portrelay.Manager
	// PublicURL is the UI's own origin (where step 2 happens).
	PublicURL string

	mu     sync.Mutex
	codes  map[string]appGrant // one-time, minutes
	grants map[string]appGrant // app-host cookies, hours
}

type appGrant struct {
	user, run string
	host      string // the app host it was issued for
	until     time.Time
}

const (
	appCookie   = "apothecary_app"
	appAuthPath = "/__apothecary/auth"
	codeTTL     = 2 * time.Minute
	grantTTL    = 12 * time.Hour
)

func NewApps(relay *portrelay.Manager, publicURL string) *Apps {
	return &Apps{Relay: relay, PublicURL: strings.TrimSuffix(publicURL, "/"),
		codes: map[string]appGrant{}, grants: map[string]appGrant{}}
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *Apps) issue(m map[string]appGrant, g appGrant) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range m { // expired entries go as new ones arrive
		if now.After(v.until) {
			delete(m, k)
		}
	}
	tok := randomToken()
	m[tok] = g
	return tok
}

// wrap sends app-host requests to the app and everything else to the API.
func (a *Apps) wrap(s *Server, api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Relay.InAppsDomain(r.Host) {
			api.ServeHTTP(w, r)
			return
		}
		a.serveApp(s, w, r)
	})
}

func (a *Apps) serveApp(s *Server, w http.ResponseWriter, r *http.Request) {
	run, _, pc, ok := a.Relay.Lookup(r.Host)
	if !ok {
		http.Error(w, "apothecary: nothing is exposed at "+r.Host, http.StatusNotFound)
		return
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	if r.URL.Path == appAuthPath {
		a.finish(w, r, run)
		return
	}
	if !a.allowed(r, run) {
		if a.PublicURL == "" {
			http.Error(w, "apothecary: app hosts need APOTHECARY_PUBLIC_URL to sign in", http.StatusServiceUnavailable)
			return
		}
		to := scheme + "://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, a.PublicURL+"/api/apps/auth?to="+url.QueryEscape(to), http.StatusFound)
		return
	}
	// The app's own cookies pass; apothecary's does not.
	stripCookie(r, appCookie)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = pr.In.Host
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		Transport: &http.Transport{
			DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return pc.Dial(ctx) },
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       60 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "apothecary: the workspace did not answer on "+r.Host+": "+err.Error(), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// allowed: an app cookie granted for this host's run.
func (a *Apps) allowed(r *http.Request, run string) bool {
	c, err := r.Cookie(appCookie)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.grants[c.Value]
	return ok && g.run == run && strings.EqualFold(g.host, r.Host) && time.Now().Before(g.until)
}

// authorize is step 2, on the UI host: the signed-in user must own the run.
func (a *Apps) authorize(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		to, err := url.Parse(r.URL.Query().Get("to"))
		if err != nil || to.Host == "" || !a.Relay.InAppsDomain(to.Host) {
			http.Error(w, "apothecary: not an app address", http.StatusBadRequest)
			return
		}
		run, _, _, ok := a.Relay.Lookup(to.Host)
		if !ok {
			http.Error(w, "apothecary: nothing is exposed at "+to.Host, http.StatusNotFound)
			return
		}
		p, ok := s.appPrincipal(r)
		if !ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<!doctype html><title>Sign in</title><p>Sign in to <a href="/">Apothecary</a>, then open <a href="` +
				html.EscapeString(to.String()) + `">` + html.EscapeString(to.Host) + `</a> again.`))
			return
		}
		ctx := auth.WithPrincipal(r.Context(), p)
		if owner, err := s.ownerOf(ctx, ScopeRun, run); err != nil || owner != p.User.ID {
			http.Error(w, "apothecary: not your run", http.StatusNotFound)
			return
		}
		code := a.issue(a.codes, appGrant{user: p.User.ID, run: run, host: to.Host, until: time.Now().Add(codeTTL)})
		back := url.URL{Scheme: to.Scheme, Host: to.Host, Path: appAuthPath,
			RawQuery: url.Values{"code": {code}, "to": {to.RequestURI()}}.Encode()}
		http.Redirect(w, r, back.String(), http.StatusFound)
	}
}

// finish is step 3, on the app host: trade the code for this host's cookie.
func (a *Apps) finish(w http.ResponseWriter, r *http.Request, run string) {
	code := r.URL.Query().Get("code")
	a.mu.Lock()
	g, ok := a.codes[code]
	delete(a.codes, code) // one use
	a.mu.Unlock()
	if !ok || g.run != run || !strings.EqualFold(g.host, r.Host) || time.Now().After(g.until) {
		http.Error(w, "apothecary: this sign-in link expired; open the app again", http.StatusForbidden)
		return
	}
	g.until = time.Now().Add(grantTTL)
	tok := a.issue(a.grants, g)
	http.SetCookie(w, &http.Cookie{Name: appCookie, Value: tok, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: int(grantTTL / time.Second)})
	to := r.URL.Query().Get("to")
	if !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") {
		to = "/"
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// appPrincipal is the session-cookie principal, without writing an error.
func (s *Server) appPrincipal(r *http.Request) (auth.Principal, bool) {
	if s.Auth == nil {
		return auth.Principal{}, false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return auth.Principal{}, false
	}
	p, err := s.Auth.AuthenticateSession(r.Context(), c.Value)
	return p, err == nil
}

// stripCookie removes one cookie from the request's Cookie header.
func stripCookie(r *http.Request, name string) {
	cs := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cs {
		if c.Name != name {
			r.AddCookie(c)
		}
	}
}
