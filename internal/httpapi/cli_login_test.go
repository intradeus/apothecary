package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/auth/authtest"
	"apothecary/internal/engine"
)

// testClock is a clock the test moves while the server reads it.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// anon is a request without the test user's token: what the CLI sends
// before it has one.
var anon = map[string]string{"Authorization": "none"}

func cliEnv(t *testing.T) (*env, *testClock) {
	t.Helper()
	x := newEnv(t, engine.Config{})
	clock := &testClock{at: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	x.srv.cliLogins.now = clock.now
	return x, clock
}

func startCLILogin(t *testing.T, x *env, name string, hdr map[string]string) CLILoginStarted {
	t.Helper()
	res, body := x.do(t, "POST", "/api/cli/logins", map[string]string{"name": name}, hdr)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("start = %d %s", res.StatusCode, body)
	}
	var out CLILoginStarted
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func pollCLILogin(t *testing.T, x *env, clock *testClock, device string) (int, CLILoginPoll, string) {
	t.Helper()
	clock.add(cliLoginInterval * time.Second)
	res, body := x.do(t, "POST", "/api/cli/logins/token", map[string]string{"device_code": device}, anon)
	var out CLILoginPoll
	_ = json.Unmarshal(body, &out)
	return res.StatusCode, out, string(body)
}

// The whole sign-in: started anonymously, pending until the person approves,
// then the token once — and that token is the person's, with the deployment's
// inactivity window.
func TestCLILoginApproved(t *testing.T) {
	x, clock := cliEnv(t)
	x.srv.CLITokenIdle = 72 * time.Hour
	started := startCLILogin(t, x, "  apothecary CLI on laptop  ", anon)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(started.DeviceCode) {
		t.Errorf("device code = %q", started.DeviceCode)
	}
	if !regexp.MustCompile(`^[A-HJKMNP-Z2-9]{4}-[A-HJKMNP-Z2-9]{4}$`).MatchString(started.UserCode) {
		t.Errorf("user code = %q", started.UserCode)
	}
	if started.VerificationURL != x.ts.URL+"/cli-login?code="+started.UserCode {
		t.Errorf("verification url = %q", started.VerificationURL)
	}
	if started.Interval != 2 || started.ExpiresIn != 600 {
		t.Errorf("interval/expiry = %d %d", started.Interval, started.ExpiresIn)
	}

	if code, poll, body := pollCLILogin(t, x, clock, started.DeviceCode); code != http.StatusAccepted || poll.Status != "pending" || poll.Token != "" {
		t.Errorf("pending poll = %d %s", code, body)
	}
	// The approval page reads it by code, in any case, with or without the dash.
	lower := strings.ToLower(strings.ReplaceAll(started.UserCode, "-", ""))
	res, body := x.do(t, "GET", "/api/cli/logins/"+lower, nil, nil)
	var info CLILoginInfo
	_ = json.Unmarshal(body, &info)
	if res.StatusCode != http.StatusOK || info.UserCode != started.UserCode || info.Name != "apothecary CLI on laptop" ||
		!info.ExpiresAt.Equal(clock.now().Add(-cliLoginInterval*time.Second).Add(cliLoginTTL)) {
		t.Errorf("info = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/cli/logins/"+started.UserCode+"/approve", nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("approve = %d %s", res.StatusCode, body)
	}
	// Answered: a second approval or a denial is a conflict.
	for _, verb := range []string{"approve", "deny"} {
		if res, body := x.do(t, "POST", "/api/cli/logins/"+started.UserCode+"/"+verb, nil, nil); res.StatusCode != http.StatusConflict {
			t.Errorf("%s again = %d %s", verb, res.StatusCode, body)
		}
	}
	code, poll, collected := pollCLILogin(t, x, clock, started.DeviceCode)
	if code != http.StatusOK || poll.Status != "approved" || !strings.HasPrefix(poll.Token, auth.TokenPrefix) ||
		poll.TokenID == "" || poll.Handle != "tester" {
		t.Fatalf("approved poll = %d %s", code, collected)
	}
	// Exactly once.
	if code, _, body := pollCLILogin(t, x, clock, started.DeviceCode); code != http.StatusGone ||
		!strings.Contains(body, "run apothecary login again") {
		t.Errorf("second collect = %d %s", code, body)
	}
	if res, _ := x.do(t, "GET", "/api/cli/logins/"+started.UserCode, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("collected login still shown = %d", res.StatusCode)
	}

	// The token is the tester's, named after the login, with the window.
	cli := map[string]string{"Authorization": "Bearer " + poll.Token}
	if res, body := x.do(t, "GET", "/api/me", nil, cli); res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"handle":"tester"`) {
		t.Errorf("me with the CLI token = %d %s", res.StatusCode, body)
	}
	tokens, _ := x.srv.Auth.ListTokens(context.Background(), testUser)
	var tok auth.Token
	for _, k := range tokens {
		if k.ID == poll.TokenID {
			tok = k
		}
	}
	if tok.Name != "apothecary CLI on laptop" || tok.IdleSeconds != 72*3600 || tok.ExpiresAt != nil {
		t.Errorf("minted token = %+v", tok)
	}
	if res, body := x.do(t, "GET", "/api/me/tokens", nil, nil); !strings.Contains(string(body), `"idle_seconds":259200`) {
		t.Errorf("tokens list = %d %s", res.StatusCode, body)
	}

	// Used just inside its window, it slides; a whole window unused, refused.
	ctx := context.Background()
	hash := tok.Hash
	if err := x.users.TouchToken(ctx, tok.ID, time.Now().Add(-72*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if res, _ := x.do(t, "GET", "/api/me", nil, cli); res.StatusCode != http.StatusOK {
		t.Errorf("inside the window = %d", res.StatusCode)
	}
	if slid, _ := x.users.GetTokenByHash(ctx, hash); slid.LastUsedAt == nil || time.Since(*slid.LastUsedAt) > time.Minute {
		t.Errorf("use did not slide the window: %v", slid.LastUsedAt)
	}
	if err := x.users.TouchToken(ctx, tok.ID, time.Now().Add(-72*time.Hour-time.Second)); err != nil {
		t.Fatal(err)
	}
	if res, body := x.do(t, "GET", "/api/me", nil, cli); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("idle past the window = %d %s", res.StatusCode, body)
	}
}

// A denial reaches the terminal once; the page's answers to codes it does
// not know are 404; a cookie approval from another site is refused.
func TestCLILoginDeniedAndUnknown(t *testing.T) {
	x, clock := cliEnv(t)
	started := startCLILogin(t, x, "", anon)
	if res, body := x.do(t, "GET", "/api/cli/logins/"+started.UserCode, nil, nil); !strings.Contains(string(body), `"name":"apothecary CLI"`) {
		t.Errorf("default name = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/cli/logins/"+started.UserCode+"/deny", nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("deny = %d %s", res.StatusCode, body)
	}
	if code, _, body := pollCLILogin(t, x, clock, started.DeviceCode); code != http.StatusForbidden || !strings.Contains(body, "the sign-in was denied") {
		t.Errorf("denied poll = %d %s", code, body)
	}
	if code, _, _ := pollCLILogin(t, x, clock, started.DeviceCode); code != http.StatusGone {
		t.Errorf("after the denial = %d", code)
	}
	for _, path := range []string{"/api/cli/logins/ZZZZ-ZZZZ", "/api/cli/logins/nonsense"} {
		if res, _ := x.do(t, "GET", path, nil, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d", path, res.StatusCode)
		}
	}
	for _, verb := range []string{"approve", "deny"} {
		if res, _ := x.do(t, "POST", "/api/cli/logins/ZZZZ-ZZZZ/"+verb, nil, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s unknown = %d", verb, res.StatusCode)
		}
	}
	if code, _, _ := pollCLILogin(t, x, clock, "not-a-device-code"); code != http.StatusGone {
		t.Errorf("unknown device = %d", code)
	}
	if res, _ := x.do(t, "POST", "/api/cli/logins/token", nil, anon); res.StatusCode != http.StatusBadRequest {
		t.Errorf("no body = %d", res.StatusCode)
	}
	if res, _ := x.do(t, "POST", "/api/cli/logins", nil, anon); res.StatusCode != http.StatusBadRequest {
		t.Errorf("start without a body = %d", res.StatusCode)
	}
	// The page's routes need a signed-in person.
	if res, _ := x.do(t, "GET", "/api/cli/logins/"+started.UserCode, nil, anon); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous page = %d", res.StatusCode)
	}

	// A browser session approving from another site: refused before it counts.
	other := startCLILogin(t, x, "x", anon)
	raw, _, err := x.srv.Auth.NewSession(context.Background(), testUser, "ua")
	if err != nil {
		t.Fatal(err)
	}
	cookie := map[string]string{"Authorization": "", "Cookie": sessionCookie + "=" + raw, "Sec-Fetch-Site": "cross-site"}
	req, _ := http.NewRequest("POST", x.ts.URL+"/api/cli/logins/"+other.UserCode+"/approve", nil)
	for k, v := range cookie {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site approve = %d", res.StatusCode)
	}
}

// Logins expire after ten minutes, whatever their state; polls faster than a
// second are refused; too many pending logins refuse a new one.
func TestCLILoginLimits(t *testing.T) {
	x, clock := cliEnv(t)
	started := startCLILogin(t, x, "cli", anon)
	poll := func() int {
		res, _ := x.do(t, "POST", "/api/cli/logins/token", map[string]string{"device_code": started.DeviceCode}, anon)
		return res.StatusCode
	}
	if code := poll(); code != http.StatusAccepted {
		t.Fatalf("first poll = %d", code)
	}
	if code := poll(); code != http.StatusTooManyRequests {
		t.Errorf("immediate second poll = %d", code)
	}
	clock.add(cliLoginMinPoll)
	if code := poll(); code != http.StatusAccepted {
		t.Errorf("a second later = %d", code)
	}
	clock.add(cliLoginTTL)
	if code := poll(); code != http.StatusGone {
		t.Errorf("expired = %d", code)
	}
	if res, _ := x.do(t, "GET", "/api/cli/logins/"+started.UserCode, nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("expired page = %d", res.StatusCode)
	}

	for range cliLoginMax {
		startCLILogin(t, x, "cli", anon)
	}
	res, body := x.do(t, "POST", "/api/cli/logins", map[string]string{"name": "one too many"}, anon)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("past the cap = %d %s", res.StatusCode, body)
	}
	// They expire, and room comes back.
	clock.add(cliLoginTTL)
	startCLILogin(t, x, "cli", anon)
}

// createTokenFails is an account store that cannot write a token.
type createTokenFails struct{ auth.DB }

func (createTokenFails) CreateToken(context.Context, auth.Token) error {
	return errors.New("db down")
}

// A token that cannot be minted fails the approval and leaves the sign-in
// waiting: approving again, once the store is back, works.
func TestCLILoginApprovalThatCannotMint(t *testing.T) {
	x, clock := cliEnv(t)
	started := startCLILogin(t, x, "cli", anon)
	good := x.srv.Auth
	x.srv.Auth = authtest.New(createTokenFails{x.users})
	if res, body := x.do(t, "POST", "/api/cli/logins/"+started.UserCode+"/approve", nil, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("approve with the store down = %d %s", res.StatusCode, body)
	}
	if code, _, _ := pollCLILogin(t, x, clock, started.DeviceCode); code != http.StatusAccepted {
		t.Errorf("after the failed approval = %d, want still pending", code)
	}
	x.srv.Auth = good
	if res, _ := x.do(t, "POST", "/api/cli/logins/"+started.UserCode+"/approve", nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("approve again = %d", res.StatusCode)
	}
}

// Guessing codes is throttled per person.
func TestCLILoginCodeGuessingIsThrottled(t *testing.T) {
	x, _ := cliEnv(t)
	status := 0
	for range loginFailMax + 1 {
		res, _ := x.do(t, "GET", "/api/cli/logins/AAAA-AAAA", nil, nil)
		status = res.StatusCode
	}
	if status != http.StatusTooManyRequests {
		t.Errorf("after %d unknown codes = %d", loginFailMax+1, status)
	}
}

// Behind a TLS-terminating proxy the link is https; a configured public URL
// wins over the request's host.
func TestCLILoginVerificationURL(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("POST", "http://engine.internal:8080/api/cli/logins", nil)
	if got := s.uiOrigin(r); got != "http://engine.internal:8080" {
		t.Errorf("plain = %q", got)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := s.uiOrigin(r); got != "https://engine.internal:8080" {
		t.Errorf("forwarded = %q", got)
	}
	s.PublicURL = "https://apothecary.example/"
	if got := s.uiOrigin(r); got != "https://apothecary.example" {
		t.Errorf("public = %q", got)
	}
}

func TestCLILoginHelpers(t *testing.T) {
	long := strings.Repeat("é", cliLoginNameMax+20)
	if got := cliLoginName(long); len([]rune(got)) != cliLoginNameMax {
		t.Errorf("capped name has %d runes", len([]rune(got)))
	}
	if got := cliLoginName(" a \n\t b "); got != "a b" {
		t.Errorf("spaces = %q", got)
	}
	if got := cliLoginName("   "); got != "apothecary CLI" {
		t.Errorf("blank = %q", got)
	}
	if got := formatUserCode("ABC"); got != "ABC" {
		t.Errorf("short code = %q", got)
	}
	if got := normalizeUserCode(" abcd-efgh "); got != "ABCDEFGH" {
		t.Errorf("normalised = %q", got)
	}
	for range 200 {
		c, err := newUserCode()
		if err != nil || len(c) != userCodeLen || strings.ContainsAny(c, "01OIL") {
			t.Fatalf("code %q, %v", c, err)
		}
	}
	if (&Server{}).cliTokenIdle() != DefaultCLITokenIdle {
		t.Error("default window")
	}
	// The zero store's clock is the real one.
	var c cliLogins
	if time.Since(c.clock()) > time.Minute {
		t.Error("zero clock")
	}
}

// Anyone may ask the engine its version: the CLI compares it with its own
// before it signs in.
func TestAuthStatusTellsTheVersion(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.srv.Version = "v3.4.0"
	res, body := x.do(t, "GET", "/api/auth/status", nil, anon)
	var st AuthStatus
	if err := json.Unmarshal(body, &st); err != nil || res.StatusCode != http.StatusOK || st.Version != "v3.4.0" {
		t.Errorf("status = %d %s", res.StatusCode, body)
	}
}
