package httpapi

// The terminal client's sign-in: the OAuth 2.0 device authorization grant's
// shape (RFC 8628), like `gh auth login`.
//
// The CLI asks for a login (public): it gets a secret device code it keeps,
// and a short user code it prints with a link to the UI's /cli-login page.
// The person opens the link signed in, checks the code matches their
// terminal, and approves: the engine mints a personal token for THEM with an
// inactivity window (APOTHECARY_CLI_TOKEN_IDLE) and holds its secret on the
// login. The CLI, polling with its device code, collects the secret once.
//
// Pending logins live in memory only and for minutes: the engine is a single
// replica, and a restart costs the person one `apothecary login`.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"apothecary/internal/account/auth"
	"apothecary/internal/run/event"
)

const (
	// cliLoginTTL is how long a login waits for its approval.
	cliLoginTTL = 10 * time.Minute
	// cliLoginInterval is the polling interval the CLI is told (seconds);
	// cliLoginMinPoll the gap under which a poll is refused (lenient: half).
	cliLoginInterval = 2
	cliLoginMinPoll  = time.Second
	// cliLoginMax caps the pending logins: the start is public.
	cliLoginMax = 1000
	// cliLoginNameMax caps the name the CLI gives its login (in runes).
	cliLoginNameMax = 100
	// DefaultCLITokenIdle is the CLI token's inactivity window when the
	// server is given none.
	DefaultCLITokenIdle = 7 * 24 * time.Hour
	// userCodeAlphabet has nothing two people would read differently: no
	// 0/O, 1/I/L.
	userCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	userCodeLen      = 8
)

type cliLoginState int

const (
	cliPending cliLoginState = iota
	cliApproved
	cliDenied
)

// cliLogin is one pending sign-in.
type cliLogin struct {
	device    string // sha256 of the device code: the secret itself is the CLI's
	userCode  string // normalised: 8 characters, no dash
	name      string
	expiresAt time.Time
	lastPoll  time.Time
	state     cliLoginState
	// Set on approval, collected by the next poll.
	token, tokenID, handle string
}

// cliLogins is the pending logins, by device-code hash and by user code.
// The zero value is ready.
type cliLogins struct {
	mu       sync.Mutex
	byDevice map[string]*cliLogin
	byCode   map[string]*cliLogin
	// now is the clock (tests move it); nil = time.Now.
	now func() time.Time
}

func (c *cliLogins) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// sweepLocked drops the expired logins.
func (c *cliLogins) sweepLocked(now time.Time) {
	for k, l := range c.byDevice {
		if !now.Before(l.expiresAt) {
			delete(c.byDevice, k)
			delete(c.byCode, l.userCode)
		}
	}
}

func (c *cliLogins) dropLocked(l *cliLogin) {
	delete(c.byDevice, l.device)
	delete(c.byCode, l.userCode)
}

// start opens a login; ok is false when too many are pending.
func (c *cliLogins) start(name string) (device, userCode string, ok bool, err error) {
	device, err = randomHex(32)
	if err != nil {
		return "", "", false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byDevice == nil {
		c.byDevice, c.byCode = map[string]*cliLogin{}, map[string]*cliLogin{}
	}
	now := c.clock()
	c.sweepLocked(now)
	if len(c.byDevice) >= cliLoginMax {
		return "", "", false, nil
	}
	for {
		if userCode, err = newUserCode(); err != nil {
			return "", "", false, err
		}
		if _, taken := c.byCode[userCode]; !taken {
			break
		}
	}
	l := &cliLogin{device: hashDevice(device), userCode: userCode, name: name, expiresAt: now.Add(cliLoginTTL)}
	c.byDevice[l.device] = l
	c.byCode[userCode] = l
	return device, userCode, true, nil
}

// byUserCodeLocked finds a live login by the code a person typed or followed.
func (c *cliLogins) byUserCodeLocked(code string) *cliLogin {
	c.sweepLocked(c.clock())
	return c.byCode[normalizeUserCode(code)]
}

func hashDevice(device string) string {
	sum := sha256.Sum256([]byte(device))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// newUserCode is 8 characters of userCodeAlphabet, uniformly drawn.
func newUserCode() (string, error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(userCodeAlphabet)))
	for range userCodeLen {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b.WriteByte(userCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// normalizeUserCode accepts a code in any case, with or without its dash.
func normalizeUserCode(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

// formatUserCode is the printed form: ABCD-EFGH.
func formatUserCode(code string) string {
	if len(code) != userCodeLen {
		return code
	}
	return code[:userCodeLen/2] + "-" + code[userCodeLen/2:]
}

// cliLoginName is the login's name as the token will carry it.
func cliLoginName(raw string) string {
	name := strings.Join(strings.Fields(event.CleanText(raw)), " ")
	if utf8.RuneCountInString(name) > cliLoginNameMax {
		name = strings.TrimSpace(string([]rune(name)[:cliLoginNameMax]))
	}
	if name == "" {
		return "apothecary CLI"
	}
	return name
}

// uiOrigin is where a browser reaches the UI: the configured public URL,
// else this request's own scheme and host.
func (s *Server) uiOrigin(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimSuffix(s.PublicURL, "/")
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) cliTokenIdle() time.Duration {
	if s.CLITokenIdle > 0 {
		return s.CLITokenIdle
	}
	return DefaultCLITokenIdle
}

// --- handlers -------------------------------------------------------------------

// postCLILogin starts a sign-in for the terminal client.
func (s *Server) postCLILogin(w http.ResponseWriter, r *http.Request) {
	var body CLILoginRequest
	if !readJSON(w, r, &body) {
		return
	}
	device, code, ok, err := s.cliLogins.start(cliLoginName(body.Name))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "too many sign-ins are waiting for approval; try again in a few minutes")
		return
	}
	shown := formatUserCode(code)
	writeJSON(w, http.StatusCreated, CLILoginStarted{
		DeviceCode: device, UserCode: shown,
		VerificationURL: s.uiOrigin(r) + "/cli-login?code=" + shown,
		Interval:        cliLoginInterval, ExpiresIn: int(cliLoginTTL / time.Second),
	})
}

// postCLILoginToken is the CLI's poll: pending until a person approves, then
// the token, once.
func (s *Server) postCLILoginToken(w http.ResponseWriter, r *http.Request) {
	var body CLILoginPollRequest
	if !readJSON(w, r, &body) {
		return
	}
	c := &s.cliLogins
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	c.sweepLocked(now)
	l, ok := c.byDevice[hashDevice(strings.TrimSpace(body.DeviceCode))]
	if !ok {
		writeErr(w, http.StatusGone, "this sign-in expired or is unknown; run apothecary login again")
		return
	}
	if now.Sub(l.lastPoll) < cliLoginMinPoll {
		writeErr(w, http.StatusTooManyRequests, "polling too fast; wait the interval between polls")
		return
	}
	l.lastPoll = now
	switch l.state {
	case cliApproved:
		c.dropLocked(l)
		writeJSON(w, http.StatusOK, CLILoginPoll{Status: "approved", Token: l.token, TokenID: l.tokenID, Handle: l.handle})
	case cliDenied:
		c.dropLocked(l)
		writeErr(w, http.StatusForbidden, "the sign-in was denied")
	default:
		writeJSON(w, http.StatusAccepted, CLILoginPoll{Status: "pending"})
	}
}

// findCLILoginLocked resolves the path's code for a signed-in person, or
// answers. Guessing codes is throttled per person, like passwords.
func (s *Server) findCLILoginLocked(w http.ResponseWriter, r *http.Request) (*cliLogin, bool) {
	k := "cli-login|" + auth.UserID(r.Context())
	now := time.Now()
	if s.logins.blocked(k, now) {
		writeErr(w, http.StatusTooManyRequests, "too many unknown codes; try again later")
		return nil, false
	}
	l := s.cliLogins.byUserCodeLocked(r.PathValue("code"))
	if l == nil {
		s.logins.failed(k, now)
		writeErr(w, http.StatusNotFound, "this sign-in code expired or is unknown; run apothecary login again")
		return nil, false
	}
	return l, true
}

// getCLILogin is what the approval page shows.
func (s *Server) getCLILogin(w http.ResponseWriter, r *http.Request) {
	s.cliLogins.mu.Lock()
	defer s.cliLogins.mu.Unlock()
	l, ok := s.findCLILoginLocked(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, CLILoginInfo{UserCode: formatUserCode(l.userCode), Name: l.name, ExpiresAt: l.expiresAt.UTC()})
}

// approveCLILogin mints the signed-in person a token for the terminal and
// leaves it for the CLI's next poll.
func (s *Server) approveCLILogin(w http.ResponseWriter, r *http.Request) {
	s.cliLogins.mu.Lock()
	defer s.cliLogins.mu.Unlock()
	l, ok := s.findCLILoginLocked(w, r)
	if !ok || !cliLoginOpen(w, l) {
		return
	}
	p, _ := auth.PrincipalFrom(r.Context())
	// Minted under the lock: a second approval racing this one must find it
	// approved, not mint a second token.
	secret, tok, err := s.Auth.NewIdleToken(r.Context(), p.User.ID, l.name, s.cliTokenIdle())
	if err != nil {
		authErr(w, err)
		return
	}
	l.state, l.token, l.tokenID, l.handle = cliApproved, secret, tok.ID, p.User.Handle
	w.WriteHeader(http.StatusNoContent)
}

// denyCLILogin refuses the terminal: its next poll is told so.
func (s *Server) denyCLILogin(w http.ResponseWriter, r *http.Request) {
	s.cliLogins.mu.Lock()
	defer s.cliLogins.mu.Unlock()
	l, ok := s.findCLILoginLocked(w, r)
	if !ok || !cliLoginOpen(w, l) {
		return
	}
	l.state = cliDenied
	w.WriteHeader(http.StatusNoContent)
}

// cliLoginOpen answers 409 for a login already approved or denied.
func cliLoginOpen(w http.ResponseWriter, l *cliLogin) bool {
	if l.state != cliPending {
		writeErr(w, http.StatusConflict, "this sign-in was already answered")
		return false
	}
	return true
}

// cliLoginRoutes is the terminal client's browser-approved sign-in.
func (s *Server) cliLoginRoutes() []Route {
	return []Route{
		{Method: "POST", Path: "/api/cli/logins", ID: "startCliLogin", Tag: tagAuth,
			Summary: "Start a terminal sign-in (device authorization)",
			Doc: "The CLI keeps device_code secret and prints user_code with verification_url, where a signed-in person approves it. " +
				"Then it polls POST /api/cli/logins/token every interval seconds until expires_in passes. 503 when too many sign-ins are waiting.",
			Handler: s.postCLILogin, Body: CLILoginRequest{}, Returns: CLILoginStarted{}, Status: http.StatusCreated, Auth: AccessPublic},
		{Method: "POST", Path: "/api/cli/logins/token", ID: "pollCliLogin", Tag: tagAuth,
			Summary: "Collect a terminal sign-in's token once it is approved",
			Doc: "202 {status: pending} until approved; 200 {status: approved, token, token_id, handle} once, then the sign-in is gone; " +
				"403 when it was denied; 410 when it expired or is unknown; 429 when polled faster than the interval. " +
				"The token is refused after the deployment's inactivity window (APOTHECARY_CLI_TOKEN_IDLE) without use; each use slides it.",
			Handler: s.postCLILoginToken, Body: CLILoginPollRequest{}, Returns: CLILoginPoll{}, Auth: AccessPublic},
		{Method: "GET", Path: "/api/cli/logins/{code}", ID: "getCliLogin", Tag: tagAuth,
			Summary: "A terminal sign-in waiting for approval, by its user code",
			Doc:     "The code in any case, with or without its dash. 404 when it expired or is unknown.",
			Handler: s.getCLILogin, Returns: CLILoginInfo{}},
		{Method: "POST", Path: "/api/cli/logins/{code}/approve", ID: "approveCliLogin", Tag: tagAuth,
			Summary: "Approve a terminal sign-in: it signs in as you",
			Doc:     "Mints a personal token named after the sign-in, refused after the deployment's inactivity window without use. 404 when expired or unknown; 409 when already answered.",
			Handler: s.approveCLILogin, Status: http.StatusNoContent},
		{Method: "POST", Path: "/api/cli/logins/{code}/deny", ID: "denyCliLogin", Tag: tagAuth,
			Summary: "Deny a terminal sign-in",
			Doc:     "404 when expired or unknown; 409 when already answered.",
			Handler: s.denyCLILogin, Status: http.StatusNoContent},
	}
}
