package httpapi

// Who a request is, and what it may touch (TODO 25).
//
// Every route in the table says how it is reached: as a signed-in user (the
// default), as the instance admin, with a run's own token (the in-workspace
// shim), or by anyone (sign-in itself). A user route also says which
// resource its path names — a run, a project, a ticket — and the guard
// refuses, as a 404, anything that resource's owner is not. Handlers never
// check ownership themselves: it is one rule, in one place, and a test
// asserts every route declares its access.
//
// A browser proves itself with a session cookie (EventSource and WebSocket
// cannot carry a header), a script with a personal bearer token. Cookie
// requests that change something must come from the UI's own origin; a
// bearer header cannot be sent cross-site, so those are trusted as is.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// Access is how a route is reached.
type Access int

const (
	// AccessUser needs a signed-in user: a session cookie or a personal
	// token. The zero value, so an undeclared route is a protected one.
	AccessUser Access = iota
	// AccessAdmin needs an instance admin.
	AccessAdmin
	// AccessRunToken is the in-workspace shim's: the run's own bearer
	// token, scoped to that run.
	AccessRunToken
	// AccessPublic is reachable by anyone: the setup and sign-in flow.
	AccessPublic
	// AccessBrokerToken is omp's, in a run's workspace: the run's auth
	// broker token (Engine.BrokerToken), scoped to that run.
	AccessBrokerToken
)

// Scope is which resource a route's path names, and so whose it must be.
type Scope int

const (
	ScopeNone Scope = iota
	ScopeRun
	ScopeProject
	ScopeTicket
	ScopePing
	ScopeIntegration
	ScopeChannel
	ScopeAttachment
	ScopeSnapshot
	ScopeEnvCheck
	ScopeCredential
	ScopeSkill
	ScopeSkillset
)

// sessionCookie is the browser session's cookie.
const sessionCookie = "apothecary_session"

// guard wraps a route's handler with its declared access: the principal
// (or 401), the admin check (or 403), the same-origin check on cookie
// writes (or 403), and the ownership check on the path's resource (or 404).
func (s *Server) guard(rt Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if refuseBadText(w, r) {
			return
		}
		switch rt.Auth {
		case AccessPublic:
			if !s.sameOrigin(w, r) {
				return
			}
			rt.Handler(w, r)
			return
		case AccessRunToken:
			if !s.authPing(w, r, r.PathValue("id")) {
				return
			}
			rt.Handler(w, r)
			return
		case AccessBrokerToken:
			if !s.authBrokerToken(w, r, r.PathValue("id")) {
				return
			}
			rt.Handler(w, r)
			return
		}
		p, ok := s.principal(w, r)
		if !ok {
			return
		}
		if rt.Auth == AccessAdmin && !p.User.Admin {
			writeErr(w, http.StatusForbidden, "this needs an instance admin")
			return
		}
		if p.ViaCookie() && !s.sameOrigin(w, r) {
			return
		}
		ctx := auth.WithPrincipal(r.Context(), p)
		if rt.Scope != ScopeNone {
			id := r.PathValue("id")
			if rt.Scope == ScopePing {
				id = r.PathValue("pid")
			}
			owner, err := s.ownerOf(ctx, rt.Scope, id)
			if err != nil || owner != p.User.ID {
				// Not theirs reads exactly like not there: the id itself is
				// not something to confirm.
				writeErr(w, http.StatusNotFound, scopeName(rt.Scope)+" "+id+" not found")
				return
			}
		}
		rt.Handler(w, r.WithContext(ctx))
	}
}

func scopeName(sc Scope) string {
	switch sc {
	case ScopeRun:
		return "run"
	case ScopeProject:
		return "project"
	case ScopeTicket:
		return "ticket"
	case ScopePing:
		return "ping"
	case ScopeIntegration:
		return "integration"
	case ScopeCredential:
		return "credential"
	case ScopeSkill:
		return "skill"
	case ScopeSkillset:
		return "skillset"
	case ScopeChannel:
		return "channel"
	case ScopeAttachment:
		return "attachment"
	case ScopeSnapshot:
		return "snapshot"
	case ScopeEnvCheck:
		return "envcheck"
	}
	return "resource"
}

// principal authenticates the request: a personal bearer token, else the
// session cookie. 401 with a message when neither checks out; 403 for a
// disabled account.
func (s *Server) principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	if s.Auth == nil {
		writeErr(w, http.StatusServiceUnavailable, "authentication is not wired on this server")
		return auth.Principal{}, false
	}
	var p auth.Principal
	var err error
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		p, err = s.Auth.AuthenticateToken(r.Context(), strings.TrimSpace(bearer))
	} else if c, cerr := r.Cookie(sessionCookie); cerr == nil {
		p, err = s.Auth.AuthenticateSession(r.Context(), c.Value)
	} else {
		err = auth.ErrUnauthorized
	}
	switch {
	case errors.Is(err, auth.ErrDisabled):
		writeErr(w, http.StatusForbidden, "this account is disabled")
		return auth.Principal{}, false
	case errors.Is(err, auth.ErrUnauthorized):
		writeErr(w, http.StatusUnauthorized, "sign in to continue")
		return auth.Principal{}, false
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return auth.Principal{}, false
	}
	return p, true
}

// sameOrigin refuses a state-changing request a browser sent from another
// site. Sec-Fetch-Site is what current browsers say; an Origin header is the
// fallback; a request with neither is not a browser's cross-site one.
func (s *Server) sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		if site == "same-origin" || site == "none" {
			return true
		}
		writeErr(w, http.StatusForbidden, "cross-site request refused")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeErr(w, http.StatusForbidden, "cross-site request refused")
			return false
		}
	}
	return true
}

// sameOriginUpgrade is the WebSocket flavour: a browser always sends Origin
// on an upgrade, and it must be this host.
func (s *Server) sameOriginUpgrade(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
		writeErr(w, http.StatusForbidden, "cross-site request refused")
		return false
	}
	return true
}

// ownerOf resolves the owner of the resource a path names. Everything
// hangs off a run, a project or a row with its own owner column.
func (s *Server) ownerOf(ctx context.Context, sc Scope, id string) (string, error) {
	switch sc {
	case ScopeRun:
		return s.runOwner(ctx, id)
	case ScopeProject:
		return s.projectOwner(ctx, id)
	case ScopeTicket:
		if s.Conf == nil {
			return "", errors.New("no config plane")
		}
		t, err := s.Conf.GetTicket(ctx, id)
		if err != nil {
			return "", err
		}
		return t.UserID, nil
	case ScopeIntegration:
		if s.Conf == nil {
			return "", errors.New("no config plane")
		}
		in, err := s.Conf.GetIntegration(ctx, id)
		if err != nil {
			return "", err
		}
		return in.UserID, nil
	case ScopeCredential:
		if s.Conf == nil {
			return "", errors.New("no config plane")
		}
		pc, err := s.Conf.GetCredential(ctx, id)
		if err != nil {
			return "", err
		}
		return pc.UserID, nil
	case ScopePing:
		if s.Pings != nil {
			v, err := s.Pings.GetPing(ctx, id)
			if err == nil {
				return v.UserID, nil
			}
		}
		if p := s.Runs.GetPing(id); p != nil {
			return s.runOwner(ctx, p.RunID)
		}
		return "", errors.New("ping not found")
	case ScopeChannel:
		if e, ok := s.Registry.Get(id); ok {
			return e.UserID, nil
		}
		return "", errors.New("channel not found")
	case ScopeAttachment:
		if s.Attachments == nil {
			return "", errors.New("no file store")
		}
		a, err := s.Attachments.Get(ctx, id)
		if err != nil {
			return "", err
		}
		return a.UserID, nil
	case ScopeSnapshot:
		if s.Snapshots == nil {
			return "", errors.New("no file store")
		}
		sn, err := s.Snapshots.Get(ctx, id)
		if err != nil {
			return "", err
		}
		return s.runOwner(ctx, sn.RunID)
	case ScopeEnvCheck:
		cr, ok := s.EnvCheck.Get(id)
		if !ok {
			return "", errors.New("envcheck not found")
		}
		return s.projectOwner(ctx, cr.ProjectID)
	case ScopeSkill:
		if s.Skills == nil {
			return "", errors.New("no skill library")
		}
		sk, err := s.Skills.GetSkill(ctx, id)
		if err != nil {
			return "", err
		}
		return sk.UserID, nil
	case ScopeSkillset:
		if s.Skills == nil {
			return "", errors.New("no skill library")
		}
		set, err := s.Skills.GetSkillset(ctx, id)
		if err != nil {
			return "", err
		}
		return set.UserID, nil
	}
	return "", fmt.Errorf("unknown scope %d", sc)
}

func (s *Server) projectOwner(ctx context.Context, id string) (string, error) {
	if s.Conf == nil {
		return "", errors.New("no config plane")
	}
	p, err := s.Conf.GetProject(ctx, id)
	if err != nil {
		return "", err
	}
	return p.UserID, nil
}

// runOwner is a run's owner from the store's row when it keeps one, else
// from its reduced log.
func (s *Server) runOwner(ctx context.Context, id string) (string, error) {
	if vl, ok := s.Store.(run.ViewLookup); ok {
		v, found, err := vl.RunView(ctx, id)
		if err != nil {
			return "", err
		}
		if found {
			return v.UserID, nil
		}
	}
	evs, err := s.Store.Events(ctx, id, 0)
	if err != nil {
		return "", err
	}
	if len(evs) == 0 {
		return "", errors.New("run not found")
	}
	return run.Reduce(evs).UserID, nil
}

// ownsProject is the in-handler check for a project named in a request
// BODY (a ticket's project, a flow copy's target): the guard covers paths
// only. Answers like the guard: not theirs is not found.
func (s *Server) ownsProject(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if projectID == "" {
		writeErr(w, http.StatusBadRequest, "project_id is required")
		return false
	}
	owner, err := s.projectOwner(r.Context(), projectID)
	if err != nil || owner != auth.UserID(r.Context()) {
		writeErr(w, http.StatusNotFound, "project "+projectID+" not found")
		return false
	}
	return true
}

// ownsIntegration is ownsProject for an integration named in a BODY (the
// tracker a ticket is imported through): those are its owner's credentials.
func (s *Server) ownsIntegration(w http.ResponseWriter, r *http.Request, id string) bool {
	owner, err := s.ownerOf(r.Context(), ScopeIntegration, id)
	if err != nil || owner != auth.UserID(r.Context()) {
		writeErr(w, http.StatusNotFound, "integration "+id+" not found")
		return false
	}
	return true
}

// --- cookies --------------------------------------------------------------------

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
		MaxAge: int(s.Auth.SessionTTL / time.Second),
	})
}

func clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: -1,
	})
}

// isHTTPS: the connection itself, or a reverse proxy in front saying so.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// --- login throttling -------------------------------------------------------------

// loginLimiter slows a password guesser: after a few failures for one
// account from one address, further attempts wait. In memory — a restart
// resets it, which is fine for what it defends against.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

const (
	loginFailWindow = 15 * time.Minute
	loginFailMax    = 8
)

func (l *loginLimiter) key(r *http.Request, login string) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host + "|" + strings.ToLower(strings.TrimSpace(login))
}

// blocked reports whether the account+address has failed too often lately.
func (l *loginLimiter) blocked(k string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	keep := l.fails[k][:0]
	for _, t := range l.fails[k] {
		if now.Sub(t) < loginFailWindow {
			keep = append(keep, t)
		}
	}
	l.fails[k] = keep
	return len(keep) >= loginFailMax
}

func (l *loginLimiter) failed(k string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	l.fails[k] = append(l.fails[k], now)
}

func (l *loginLimiter) reset(k string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, k)
}

// --- handlers: auth ---------------------------------------------------------------

func authErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, auth.ErrConflict), errors.Is(err, auth.ErrSetupDone):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrUnauthorized):
		writeErr(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrDisabled):
		writeErr(w, http.StatusForbidden, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// getAuthStatus is what the UI asks first: whether the instance still
// needs its first account, and how a person may sign in.
func (s *Server) getAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusServiceUnavailable, "authentication is not wired on this server")
		return
	}
	needsSetup, err := s.Auth.SetupRequired(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, AuthStatus{SetupRequired: needsSetup, Methods: s.Auth.Methods()})
}

// postSetup claims a fresh instance: the first account, signed in at once.
func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusServiceUnavailable, "authentication is not wired on this server")
		return
	}
	var body SetupRequest
	if !readJSON(w, r, &body) {
		return
	}
	u, err := s.Auth.Setup(r.Context(), auth.NewUser{
		Handle: body.Handle, Email: body.Email, Name: body.Name, Password: body.Password,
	})
	if err != nil {
		authErr(w, err)
		return
	}
	s.signIn(w, r, u, http.StatusCreated)
}

// postLogin verifies a proof for an account and starts a browser session.
func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusServiceUnavailable, "authentication is not wired on this server")
		return
	}
	var body LoginRequest
	if !readJSON(w, r, &body) {
		return
	}
	if body.Method == "" {
		body.Method = auth.KindPassword
	}
	k := s.logins.key(r, body.Login)
	now := time.Now()
	if s.logins.blocked(k, now) {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts; try again later")
		return
	}
	u, raw, err := s.Auth.SignIn(r.Context(), body.Method, body.Login, body.Proof, event.CleanText(r.UserAgent()))
	if err != nil {
		if errors.Is(err, auth.ErrUnauthorized) {
			s.logins.failed(k, now)
		}
		authErr(w, err)
		return
	}
	s.logins.reset(k)
	s.signedIn(w, r, u, raw, http.StatusOK)
}

// signIn starts a session for an account that needs no proof: it was just made.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request, u auth.User, status int) {
	raw, _, err := s.Auth.NewSession(r.Context(), u.ID, event.CleanText(r.UserAgent()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.signedIn(w, r, u, raw, status)
}

func (s *Server) signedIn(w http.ResponseWriter, r *http.Request, u auth.User, raw string, status int) {
	s.setSession(w, r, raw)
	me, err := s.me(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, status, me)
}

func (s *Server) postLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.Auth.Logout(r.Context(), c.Value); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	clearSession(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// --- handlers: me -------------------------------------------------------------------

func (s *Server) me(ctx context.Context, u auth.User) (Me, error) {
	methods, err := s.Auth.ListMethods(ctx, u.ID)
	if err != nil {
		return Me{}, err
	}
	kinds := make([]string, 0, len(methods))
	for _, m := range methods {
		kinds = append(kinds, m.Kind)
	}
	return Me{User: u, Methods: kinds}, nil
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFrom(r.Context())
	me, err := s.me(r.Context(), p.User)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, me)
}

func (s *Server) putMe(w http.ResponseWriter, r *http.Request) {
	var body ProfileRequest
	if !readJSON(w, r, &body) {
		return
	}
	u, err := s.Auth.UpdateProfile(r.Context(), auth.UserID(r.Context()),
		auth.Profile{Handle: body.Handle, Email: body.Email, Name: body.Name})
	if err != nil {
		authErr(w, err)
		return
	}
	me, err := s.me(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, me)
}

func (s *Server) putMyPassword(w http.ResponseWriter, r *http.Request) {
	var body PasswordChangeRequest
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.Auth.ChangePassword(r.Context(), auth.UserID(r.Context()), body.Current, body.Password); err != nil {
		authErr(w, err)
		return
	}
	// Every session went with the old password, this one included: start
	// a fresh one so the person is not signed out by their own change.
	p, _ := auth.PrincipalFrom(r.Context())
	if p.ViaCookie() {
		if raw, _, err := s.Auth.NewSession(r.Context(), p.User.ID, event.CleanText(r.UserAgent())); err == nil {
			s.setSession(w, r, raw)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMyTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Auth.ListTokens(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ts == nil {
		ts = []auth.Token{}
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) createMyToken(w http.ResponseWriter, r *http.Request) {
	var body TokenRequest
	if !readJSON(w, r, &body) {
		return
	}
	var expires *time.Time
	if body.ExpiresAt != nil {
		t := body.ExpiresAt.UTC()
		expires = &t
	}
	secret, tok, err := s.Auth.NewToken(r.Context(), auth.UserID(r.Context()), body.Name, expires)
	if err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, TokenCreated{Token: tok, Secret: secret})
}

func (s *Server) deleteMyToken(w http.ResponseWriter, r *http.Request) {
	if err := s.Auth.DeleteToken(r.Context(), auth.UserID(r.Context()), r.PathValue("id")); err != nil {
		authErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMySSHKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.Auth.ListSSHKeys(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []auth.SSHKey{}
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) addMySSHKey(w http.ResponseWriter, r *http.Request) {
	var body SSHKeyRequest
	if !readJSON(w, r, &body) {
		return
	}
	k, err := s.Auth.AddSSHKey(r.Context(), auth.UserID(r.Context()), body.Name, body.PublicKey)
	if err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, k)
}

func (s *Server) deleteMySSHKey(w http.ResponseWriter, r *http.Request) {
	if err := s.Auth.DeleteSSHKey(r.Context(), auth.UserID(r.Context()), r.PathValue("id")); err != nil {
		authErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- handlers: users (admin) --------------------------------------------------------

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.Auth.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if us == nil {
		us = []auth.User{}
	}
	writeJSON(w, http.StatusOK, us)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body UserCreateRequest
	if !readJSON(w, r, &body) {
		return
	}
	u, err := s.Auth.CreateUser(r.Context(), auth.NewUser{
		Handle: body.Handle, Email: body.Email, Name: body.Name, Password: body.Password, Admin: body.Admin,
	})
	if err != nil {
		authErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	var body UserUpdateRequest
	if !readJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	u, err := s.Auth.GetUser(ctx, id)
	if err != nil {
		authErr(w, err)
		return
	}
	if body.Handle != "" || body.Email != "" || body.Name != "" {
		if u, err = s.Auth.UpdateProfile(ctx, id, auth.Profile{Handle: body.Handle, Email: body.Email, Name: body.Name}); err != nil {
			authErr(w, err)
			return
		}
	}
	if body.Admin != nil && *body.Admin != u.Admin {
		if u, err = s.Auth.SetAdmin(ctx, id, *body.Admin); err != nil {
			authErr(w, err)
			return
		}
	}
	if body.Disabled != nil && *body.Disabled != u.Disabled() {
		if u, err = s.Auth.SetDisabled(ctx, id, *body.Disabled); err != nil {
			authErr(w, err)
			return
		}
		if *body.Disabled {
			s.standDown(ctx, id)
			if s.SSH != nil {
				s.SSH.DropUser(id)
			}
		}
	}
	writeJSON(w, http.StatusOK, u)
}

// standDown is what a disabled account stops doing: its active runs are
// aborted and its automations switched off. Its workspaces stay for the
// reaper, its history stays.
func (s *Server) standDown(ctx context.Context, userID string) {
	if views, err := s.runViews(ctx); err == nil {
		for _, v := range views {
			if v.UserID == userID && !v.State.Terminal() {
				if err := s.Runs.Abort(v.RunID); err != nil {
					s.Log.Warn("disable user: abort run", "user", userID, "run", v.RunID, "err", err)
				}
			}
		}
	}
	if s.Conf == nil {
		return
	}
	projects, err := s.Conf.ListProjects(ctx, userID)
	if err != nil {
		return
	}
	for _, p := range projects {
		autos, err := s.Conf.ListAutomations(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, a := range autos {
			if !a.Enabled {
				continue
			}
			a.Enabled = false
			if _, err := s.Conf.UpdateAutomation(ctx, a.ID, a); err != nil {
				s.Log.Warn("disable user: automation", "user", userID, "automation", a.ID, "err", err)
			}
		}
	}
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	var body PasswordResetRequest
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.Auth.SetPassword(r.Context(), r.PathValue("id"), body.Password); err != nil {
		authErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- handlers: limits ---------------------------------------------------------------

// LimitsSetting is where the instance's caps live (engine.StoredLimits).
type LimitsSetting interface {
	Limits(ctx context.Context) (engine.Limits, error)
	Set(ctx context.Context, l engine.Limits) error
}

// getLimits reports the instance's capacity caps (the capacity strip).
func (s *Server) getLimits(w http.ResponseWriter, r *http.Request) {
	l, err := s.currentLimits(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, LimitsInfo{MaxConcurrentRuns: l.MaxConcurrentRuns, MaxEnvironments: l.MaxEnvironments})
}

func (s *Server) currentLimits(ctx context.Context) (engine.Limits, error) {
	if s.Limits == nil {
		return engine.StaticSettings{}.Limits(ctx)
	}
	return s.Limits.Limits(ctx)
}

// setLimits changes them (admin). They apply to the next admission.
func (s *Server) setLimits(w http.ResponseWriter, r *http.Request) {
	if s.Limits == nil {
		writeErr(w, http.StatusServiceUnavailable, "limits are not configurable on this server")
		return
	}
	var in LimitsInfo
	if !readJSON(w, r, &in) {
		return
	}
	l := engine.Limits{MaxConcurrentRuns: in.MaxConcurrentRuns, MaxEnvironments: in.MaxEnvironments}
	if err := engine.ValidateLimits(l); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Limits.Set(r.Context(), l); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.getLimits(w, r)
}

// Compile-time anchor: ownership lookups read config-plane rows.
var _ = confstore.ErrNotFound

// authRoutes is the account surface — instance setup, signing in, one's own profile, tokens and SSH keys — and the capacity caps.
func (s *Server) authRoutes() []Route {
	return []Route{
		// Accounts (TODO 25): the one public surface is finding out whether the
		// instance is set up, setting it up, and signing in.
		{Method: "GET", Path: "/api/auth/status", ID: "getAuthStatus", Tag: tagAuth,
			Summary: "Whether the instance needs its first account, and how one may sign in",
			Handler: s.getAuthStatus, Returns: AuthStatus{}, Auth: AccessPublic},
		{Method: "POST", Path: "/api/auth/setup", ID: "setupInstance", Tag: tagAuth,
			Summary: "Claim a fresh instance: create its first account, an admin, and sign in",
			Doc:     "Only while no account exists (409 after).",
			Handler: s.postSetup, Body: SetupRequest{}, Returns: Me{}, Status: http.StatusCreated, Auth: AccessPublic},
		{Method: "POST", Path: "/api/auth/login", ID: "login", Tag: tagAuth,
			Summary: "Sign in and start a browser session",
			Doc:     "method is password unless the instance offers another; login is a handle or an email; proof the password. Repeated failures for one account from one address are throttled (429).",
			Handler: s.postLogin, Body: LoginRequest{}, Returns: Me{}, Auth: AccessPublic},
		{Method: "POST", Path: "/api/auth/logout", ID: "logout", Tag: tagAuth,
			Summary: "End the browser session",
			Handler: s.postLogout, Status: http.StatusNoContent},
		{Method: "GET", Path: "/api/me", ID: "getMe", Tag: tagAuth,
			Summary: "The signed-in user",
			Handler: s.getMe, Returns: Me{}},
		{Method: "PUT", Path: "/api/me", ID: "updateMe", Tag: tagAuth,
			Summary: "Change one's own handle, email or name",
			Handler: s.putMe, Body: ProfileRequest{}, Returns: Me{}},
		{Method: "PUT", Path: "/api/me/password", ID: "changeMyPassword", Tag: tagAuth,
			Summary: "Change one's own password",
			Doc:     "Every other session is signed out; this one continues.",
			Handler: s.putMyPassword, Body: PasswordChangeRequest{}, Status: http.StatusNoContent},
		{Method: "GET", Path: "/api/me/tokens", ID: "listMyTokens", Tag: tagAuth,
			Summary: "One's personal API tokens (never their secrets)",
			Handler: s.listMyTokens, Returns: []auth.Token{}},
		{Method: "POST", Path: "/api/me/tokens", ID: "createMyToken", Tag: tagAuth,
			Summary: "Mint a personal API token",
			Doc:     "The secret is in this response and nowhere else: it is stored hashed. Send it as a bearer token.",
			Handler: s.createMyToken, Body: TokenRequest{}, Returns: TokenCreated{}, Status: http.StatusCreated},
		{Method: "DELETE", Path: "/api/me/tokens/{id}", ID: "deleteMyToken", Tag: tagAuth,
			Summary: "Revoke a personal API token",
			Handler: s.deleteMyToken, Status: http.StatusNoContent},
		{Method: "GET", Path: "/api/me/ssh-keys", ID: "listMySshKeys", Tag: tagAuth,
			Summary: "One's SSH public keys: what the SSH door lets into one's workspaces",
			Handler: s.listMySSHKeys, Returns: []auth.SSHKey{}},
		{Method: "POST", Path: "/api/me/ssh-keys", ID: "addMySshKey", Tag: tagAuth,
			Summary: "Register an SSH public key",
			Doc:     "The contents of a .pub file. A key registered on one account cannot be registered on another. Disabling the account keeps its keys and ends their sessions; re-enabling it restores access.",
			Handler: s.addMySSHKey, Body: SSHKeyRequest{}, Returns: auth.SSHKey{}, Status: http.StatusCreated},
		{Method: "DELETE", Path: "/api/me/ssh-keys/{id}", ID: "deleteMySshKey", Tag: tagAuth,
			Summary: "Remove an SSH public key",
			Handler: s.deleteMySSHKey, Status: http.StatusNoContent},
		{Method: "GET", Path: "/api/limits", ID: "getLimits", Tag: tagSystem,
			Summary: "The instance's capacity caps (-1: no cap)",
			Handler: s.getLimits, Returns: LimitsInfo{}},
		{Method: "PUT", Path: "/api/limits", ID: "setLimits", Tag: tagSystem,
			Summary: "Set the instance's capacity caps (admin; -1: no cap)",
			Handler: s.setLimits, Body: LimitsInfo{}, Returns: LimitsInfo{}, Auth: AccessAdmin},
	}
}

// userRoutes is the admin's view of accounts.
func (s *Server) userRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/users", ID: "listUsers", Tag: tagUsers,
			Summary: "Every account",
			Handler: s.listUsers, Returns: []auth.User{}, Auth: AccessAdmin},
		{Method: "POST", Path: "/api/users", ID: "createUser", Tag: tagUsers,
			Summary: "Create an account with a password",
			Handler: s.createUser, Body: UserCreateRequest{}, Returns: auth.User{}, Status: http.StatusCreated, Auth: AccessAdmin},
		{Method: "PUT", Path: "/api/users/{id}", ID: "updateUser", Tag: tagUsers,
			Summary: "Change an account: its profile, whether it is an admin, whether it is disabled",
			Doc:     "Disabling an account signs it out everywhere, revokes its tokens, aborts its active runs and switches its automations off; its history and workspaces stay.",
			Handler: s.updateUser, Body: UserUpdateRequest{}, Returns: auth.User{}, Auth: AccessAdmin},
		{Method: "POST", Path: "/api/users/{id}/password", ID: "resetUserPassword", Tag: tagUsers,
			Summary: "Set an account's password",
			Doc:     "Signs the account out everywhere.",
			Handler: s.resetUserPassword, Body: PasswordResetRequest{}, Status: http.StatusNoContent, Auth: AccessAdmin},
	}
}
