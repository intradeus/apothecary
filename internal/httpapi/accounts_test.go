package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/auth/authtest"
	"apothecary/internal/engine"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run"
)

const goodPassword = "correct horse"

// addPasswordUser makes a password account over the admin API.
func (x *env) addPasswordUser(t *testing.T, handle string, admin bool) auth.User {
	t.Helper()
	var made []string
	x.srv.NewAccount = func(_ context.Context, userID string) error { made = append(made, userID); return nil }
	defer func() { x.srv.NewAccount = nil }()
	defer func() {
		if !t.Failed() && len(made) != 1 {
			t.Errorf("an account made by an admin gets the deployment's defaults: %v", made)
		}
	}()
	res, body := x.do(t, "POST", "/api/users", UserCreateRequest{
		Handle: handle, Email: handle + "@example.test", Password: goodPassword, Admin: admin,
	}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create user = %d %s", res.StatusCode, body)
	}
	return decode[auth.User](t, body)
}

// login signs in and returns the session cookie header.
func (x *env) login(t *testing.T, who, password string) (*http.Response, map[string]string) {
	t.Helper()
	res, body := x.do(t, "POST", "/api/auth/login", LoginRequest{Login: who, Proof: password}, map[string]string{"Authorization": "none"})
	if res.StatusCode != http.StatusOK {
		t.Logf("login %s = %d %s", who, res.StatusCode, body)
		return res, nil
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			return res, map[string]string{"Cookie": c.Name + "=" + c.Value}
		}
	}
	t.Fatalf("login set no session cookie: %v", res.Header)
	return nil, nil
}

// The session cookie is what a browser's whole identity rests on: script
// cannot read it, it goes to this site only, and HTTPS keeps it there.
func TestSessionCookieAttributes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.addPasswordUser(t, "lu", false)

	res, cookie := x.login(t, "lu", goodPassword)
	if cookie == nil {
		t.Fatal("no session")
	}
	var c *http.Cookie
	for _, got := range res.Cookies() {
		if got.Name == sessionCookie {
			c = got
		}
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure {
		t.Errorf("cookie over plain HTTP = %+v", c)
	}
	if want := int(x.srv.Auth.SessionTTL / time.Second); c.MaxAge != want {
		t.Errorf("cookie max age = %d, want %d", c.MaxAge, want)
	}

	// Behind a TLS-terminating proxy the cookie is Secure.
	res, body := x.do(t, "POST", "/api/auth/login", LoginRequest{Login: "lu", Proof: goodPassword},
		map[string]string{"X-Forwarded-Proto": "HTTPS", "Authorization": "none"})
	if res.StatusCode != http.StatusOK || !res.Cookies()[0].Secure {
		t.Errorf("login behind TLS = %d %s, cookies %v", res.StatusCode, body, res.Cookies())
	}

	// Logging out clears it, and it is dead on the server too.
	res, body = x.do(t, "POST", "/api/auth/logout", nil, merged(cookie, map[string]string{"Authorization": "none", "X-Forwarded-Proto": "https"}))
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d %s", res.StatusCode, body)
	}
	cleared := res.Cookies()[0]
	if cleared.Name != sessionCookie || cleared.MaxAge >= 0 || cleared.Value != "" || !cleared.Secure {
		t.Errorf("logout cookie = %+v", cleared)
	}
	if res, _ := x.bare(t, "GET", "/api/me", "", cookie); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("session after logout = %d", res.StatusCode)
	}
}

// A person logging out over a token has no cookie to clear and is fine.
func TestLogoutWithoutACookie(t *testing.T) {
	x := newEnv(t, engine.Config{})
	if res, body := x.do(t, "POST", "/api/auth/logout", nil, nil); res.StatusCode != http.StatusNoContent {
		t.Errorf("logout by token = %d %s", res.StatusCode, body)
	}
}

func TestLoginRefusals(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.addPasswordUser(t, "lu", false)
	admin := x.addPasswordUser(t, "boss", true)

	for name, c := range map[string]struct {
		body LoginRequest
		want int
	}{
		"wrong password":  {LoginRequest{Login: "lu", Proof: "wrong one"}, http.StatusUnauthorized},
		"unknown account": {LoginRequest{Login: "nobody", Proof: goodPassword}, http.StatusUnauthorized},
		"unknown method":  {LoginRequest{Method: "passkey", Login: "lu", Proof: "x"}, http.StatusBadRequest},
	} {
		res, body := x.do(t, "POST", "/api/auth/login", c.body, map[string]string{"Authorization": "none"})
		if res.StatusCode != c.want {
			t.Errorf("%s = %d %s, want %d", name, res.StatusCode, body, c.want)
		}
	}
	// A wrong login and a wrong proof fail the same way.
	_, a := x.do(t, "POST", "/api/auth/login", LoginRequest{Login: "lu", Proof: "wrong one"}, nil)
	_, b := x.do(t, "POST", "/api/auth/login", LoginRequest{Login: "nobody", Proof: "wrong one"}, nil)
	if string(a) != string(b) {
		t.Errorf("an unknown account reads differently: %s vs %s", a, b)
	}
	if res, _ := x.bare(t, "POST", "/api/auth/login", "{", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}

	// A disabled account proves itself and is still told no.
	if res, body := x.do(t, "PUT", "/api/users/"+admin.ID, UserUpdateRequest{Disabled: ptr(true)}, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("disable = %d %s", res.StatusCode, body)
	}
	if res, _ := x.login(t, "boss", goodPassword); res.StatusCode != http.StatusForbidden {
		t.Errorf("disabled login = %d, want 403", res.StatusCode)
	}
}

func ptr[T any](v T) *T { return &v }

// Failures for one account from one address are throttled; the window lets
// them go, success wipes the slate, and another account is untouched.
func TestLoginThrottle(t *testing.T) {
	var l loginLimiter
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	k := l.key(r, "  LU ")
	if k != "10.0.0.1|lu" {
		t.Fatalf("key = %q", k)
	}
	r2 := httptest.NewRequest("POST", "/", nil)
	r2.RemoteAddr = "10.0.0.1:6666"
	if l.key(r2, "lu") != k {
		t.Error("another port of the same address is a different key")
	}
	r2.RemoteAddr = "10.0.0.2:5555"
	if l.key(r2, "lu") == k {
		t.Error("another address shares the key")
	}

	now := time.Now()
	if l.blocked(k, now) {
		t.Fatal("blocked before any failure")
	}
	for i := 0; i < loginFailMax-1; i++ {
		l.failed(k, now)
	}
	if l.blocked(k, now) {
		t.Errorf("blocked after %d failures, the limit is %d", loginFailMax-1, loginFailMax)
	}
	l.failed(k, now)
	if !l.blocked(k, now) {
		t.Errorf("not blocked after %d failures", loginFailMax)
	}
	if l.blocked("10.0.0.1|other", now) {
		t.Error("the block reached another account")
	}
	if l.blocked(k, now.Add(loginFailWindow)) {
		t.Error("still blocked once the window passed")
	}
	// The expired failures are gone, not just ignored.
	if got := len(l.fails[k]); got != 0 {
		t.Errorf("%d failures kept past their window", got)
	}

	l.failed(k, now)
	l.reset(k)
	if len(l.fails[k]) != 0 {
		t.Error("reset kept the failures")
	}

	// A limiter that was never used answers too (the zero value serves).
	var fresh loginLimiter
	fresh.failed("k", now)
	if len(fresh.fails["k"]) != 1 {
		t.Error("failed on a zero limiter recorded nothing")
	}
}

// A correct password after a few failures clears them.
func TestLoginSuccessClearsFailures(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.addPasswordUser(t, "lu", false)
	for range 2 {
		if res, _ := x.login(t, "lu", "wrong one"); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password = %d", res.StatusCode)
		}
	}
	if len(x.srv.logins.fails) != 1 {
		t.Fatalf("failures recorded = %v", x.srv.logins.fails)
	}
	if res, cookie := x.login(t, "lu", goodPassword); cookie == nil {
		t.Fatalf("login = %d", res.StatusCode)
	}
	if len(x.srv.logins.fails) != 0 {
		t.Errorf("failures after success = %v", x.srv.logins.fails)
	}
}

func TestProfileEdit(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.addUser(t, "taken", false)

	res, body := x.do(t, "PUT", "/api/me", ProfileRequest{Name: "Renamed"}, nil)
	me := decode[Me](t, body)
	if res.StatusCode != http.StatusOK || me.User.Name != "Renamed" || me.User.Handle != "tester" {
		t.Fatalf("rename = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", "/api/me", ProfileRequest{Handle: "taken"}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("a taken handle = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", "/api/me", ProfileRequest{Email: "no-at-sign"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a bad email = %d %s", res.StatusCode, body)
	}
	if res, _ := x.bare(t, "PUT", "/api/me", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
	// A body cannot make anyone an admin: no such field on the profile.
	other := x.addUser(t, "plain", false)
	res, body = x.do(t, "PUT", "/api/me", map[string]any{"name": "x", "admin": true}, other)
	if res.StatusCode != http.StatusOK || decode[Me](t, body).User.Admin {
		t.Errorf("self-promotion = %d %s", res.StatusCode, body)
	}
}

// Changing one's own password signs every other session out and keeps this
// one going; the old password is dead.
func TestChangeMyPassword(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.addPasswordUser(t, "lu", false)
	_, here := x.login(t, "lu", goodPassword)
	_, there := x.login(t, "lu", goodPassword)

	change := func(current, next string, hdr map[string]string) (*http.Response, []byte) {
		return x.do(t, "PUT", "/api/me/password", PasswordChangeRequest{Current: current, Password: next}, merged(hdr, map[string]string{"Authorization": "none"}))
	}
	if res, body := change("wrong one", "a brand new password", here); res.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong current = %d %s", res.StatusCode, body)
	}
	if res, body := change(goodPassword, "short", here); res.StatusCode != http.StatusBadRequest {
		t.Errorf("short new = %d %s", res.StatusCode, body)
	}
	res, body := change(goodPassword, "a brand new password", here)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("change = %d %s", res.StatusCode, body)
	}
	if len(res.Cookies()) != 1 || res.Cookies()[0].Value == "" {
		t.Fatalf("no fresh session: %v", res.Cookies())
	}
	fresh := map[string]string{"Cookie": sessionCookie + "=" + res.Cookies()[0].Value}
	if res, _ := x.bare(t, "GET", "/api/me", "", fresh); res.StatusCode != http.StatusOK {
		t.Errorf("the fresh session = %d", res.StatusCode)
	}
	for name, c := range map[string]map[string]string{"this session's old cookie": here, "the other session": there} {
		if res, _ := x.bare(t, "GET", "/api/me", "", c); res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, res.StatusCode)
		}
	}
	if res, _ := x.login(t, "lu", goodPassword); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("old password = %d", res.StatusCode)
	}
	if res, cookie := x.login(t, "lu", "a brand new password"); cookie == nil {
		t.Errorf("new password = %d", res.StatusCode)
	}

	// A token holder has no session to keep, and an account without a
	// password has none to change.
	res, body = x.do(t, "PUT", "/api/me/password", PasswordChangeRequest{Current: "x", Password: "a brand new password"}, nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "no password") {
		t.Errorf("password-less account = %d %s", res.StatusCode, body)
	}
	if res, _ := x.bare(t, "PUT", "/api/me/password", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
}

// A password changed over a token sets no session cookie: there was none.
func TestChangePasswordOverAToken(t *testing.T) {
	x := newEnv(t, engine.Config{})
	u := x.addPasswordUser(t, "lu", false)
	secret, _, err := x.srv.Auth.NewToken(context.Background(), u.ID, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, body := x.do(t, "PUT", "/api/me/password", PasswordChangeRequest{Current: goodPassword, Password: "a brand new password"},
		map[string]string{"Authorization": "Bearer " + secret})
	if res.StatusCode != http.StatusNoContent || len(res.Cookies()) != 0 {
		t.Errorf("change = %d %s, cookies %v", res.StatusCode, body, res.Cookies())
	}
}

func TestAdminManagesAccounts(t *testing.T) {
	x := newEnv(t, engine.Config{})

	// Create: bad input and a taken handle say so.
	if res, body := x.do(t, "POST", "/api/users", UserCreateRequest{Handle: "x", Email: "x@example.test", Password: goodPassword}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("short handle = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/users", UserCreateRequest{Handle: "tester", Email: "x@example.test", Password: goodPassword}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("taken handle = %d %s", res.StatusCode, body)
	}
	if res, _ := x.bare(t, "POST", "/api/users", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
	lu := x.addPasswordUser(t, "lu", false)

	res, body := x.do(t, "GET", "/api/users", nil, nil)
	if list := decode[[]auth.User](t, body); res.StatusCode != http.StatusOK || len(list) != 2 {
		t.Fatalf("users = %d %s", res.StatusCode, body)
	}

	// Update: profile, admin, and an id that is not there.
	res, body = x.do(t, "PUT", "/api/users/"+lu.ID, UserUpdateRequest{Name: "Lucas", Admin: ptr(true)}, nil)
	got := decode[auth.User](t, body)
	if res.StatusCode != http.StatusOK || got.Name != "Lucas" || !got.Admin {
		t.Fatalf("update = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", "/api/users/nope", UserUpdateRequest{Name: "x"}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown user = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", "/api/users/"+lu.ID, UserUpdateRequest{Handle: "tester"}, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("taken handle = %d %s", res.StatusCode, body)
	}
	if res, _ := x.bare(t, "PUT", "/api/users/"+lu.ID, "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
	// A no-op update changes nothing and touches nothing.
	res, body = x.do(t, "PUT", "/api/users/"+lu.ID, UserUpdateRequest{Admin: ptr(true), Disabled: ptr(false)}, nil)
	if res.StatusCode != http.StatusOK || !decode[auth.User](t, body).Admin {
		t.Errorf("no-op update = %d %s", res.StatusCode, body)
	}

	// The last admin stays one: step lu down, then try the tester.
	if res, body := x.do(t, "PUT", "/api/users/"+lu.ID, UserUpdateRequest{Admin: ptr(false)}, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("step down = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "PUT", "/api/users/"+testUser, UserUpdateRequest{Admin: ptr(false)}, nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "last admin") {
		t.Errorf("last admin stepping down = %d %s", res.StatusCode, body)
	}
	res, body = x.do(t, "PUT", "/api/users/"+testUser, UserUpdateRequest{Disabled: ptr(true)}, nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "last admin") {
		t.Errorf("disabling the last admin = %d %s", res.StatusCode, body)
	}

	// Reset a password: signs them out, the old one is gone.
	_, cookie := x.login(t, "lu", goodPassword)
	if res, body := x.do(t, "POST", "/api/users/"+lu.ID+"/password", PasswordResetRequest{Password: "short"}, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("short reset = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/users/nope/password", PasswordResetRequest{Password: goodPassword}, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("reset of nobody = %d %s", res.StatusCode, body)
	}
	if res, _ := x.bare(t, "POST", "/api/users/"+lu.ID+"/password", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
	if res, body := x.do(t, "POST", "/api/users/"+lu.ID+"/password", PasswordResetRequest{Password: "a brand new password"}, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("reset = %d %s", res.StatusCode, body)
	}
	if res, _ := x.bare(t, "GET", "/api/me", "", cookie); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("session after reset = %d", res.StatusCode)
	}
	if res, _ := x.login(t, "lu", goodPassword); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("old password after reset = %d", res.StatusCode)
	}
}

// Disabling an account stops everything it is doing: its sessions, tokens,
// live runs, automations and SSH sessions go; its history stays, and someone
// else's work is not touched. Re-enabling lets it back in, without the
// tokens.
func TestDisablingAnAccountStandsItDown(t *testing.T) {
	x := newEnv(t, engine.Config{})
	door := &stubDoor{}
	x.srv.SSH = door
	admin := x.addUser(t, "root", true)
	hostage := x.addUser(t, "bystander", false)

	block := make(chan struct{})
	defer close(block)
	x.runner.RunFunc = blockUntil(block)
	active := x.createTicket(t, "busy")
	waitFor(t, "the run to start", func() bool { return x.e.RunToken(active) != "" })

	ctx := context.Background()
	a, err := x.srv.Conf.CreateAutomation(ctx, confstore.Automation{
		ProjectID: testProject, Name: "nightly", Kind: confstore.AutomationCron, Schedule: "0 3 * * *",
		TicketID: x.newTicket(t, "nightly", ""), Flow: "builder", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	off, err := x.srv.Conf.CreateAutomation(ctx, confstore.Automation{
		ProjectID: testProject, Name: "off", Kind: confstore.AutomationCron, Schedule: "0 4 * * *",
		TicketID: x.newTicket(t, "off", ""), Flow: "builder", Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie := x.session(t, testUser)

	res, body := x.do(t, "PUT", "/api/users/"+testUser, UserUpdateRequest{Disabled: ptr(true)}, admin)
	if res.StatusCode != http.StatusOK || !decode[auth.User](t, body).Disabled() {
		t.Fatalf("disable = %d %s", res.StatusCode, body)
	}

	// Signed out everywhere, whichever way they came.
	if res, _ := x.do(t, "GET", "/api/me", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("token after disable = %d", res.StatusCode)
	}
	if res, _ := x.bare(t, "GET", "/api/me", "", cookie); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("session after disable = %d", res.StatusCode)
	}
	if len(door.dropped) != 1 || door.dropped[0] != testUser {
		t.Errorf("SSH sessions dropped = %v", door.dropped)
	}
	// Its live run stopped, for good.
	x.e.Wait(active)
	evs, err := x.store.Events(ctx, active, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st := run.Reduce(evs).State; st != run.Abandoned {
		t.Errorf("run state = %s, want abandoned", st)
	}
	// Its automations are off; the one already off is left as it was.
	got, err := x.srv.Conf.GetAutomation(ctx, a.ID)
	if err != nil || got.Enabled {
		t.Errorf("automation after disable = %+v, %v", got, err)
	}
	if got, err := x.srv.Conf.GetAutomation(ctx, off.ID); err != nil || got.Enabled || !got.UpdatedAt.Equal(off.UpdatedAt.Truncate(time.Microsecond)) {
		t.Errorf("disabled automation was rewritten: %+v, %v", got, err)
	}
	// Nobody else felt it.
	if res, _ := x.do(t, "GET", "/api/me", nil, hostage); res.StatusCode != http.StatusOK {
		t.Errorf("bystander = %d", res.StatusCode)
	}

	// Back on: the account answers again, but only with new credentials.
	res, body = x.do(t, "PUT", "/api/users/"+testUser, UserUpdateRequest{Disabled: ptr(false)}, admin)
	if res.StatusCode != http.StatusOK || decode[auth.User](t, body).Disabled() {
		t.Fatalf("enable = %d %s", res.StatusCode, body)
	}
	if res, _ := x.do(t, "GET", "/api/me", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked token after enable = %d", res.StatusCode)
	}
	if len(door.dropped) != 1 {
		t.Errorf("enabling dropped sessions: %v", door.dropped)
	}
}

// A disabled principal is told so rather than asked to sign in.
func TestDisabledTokenAndSessionAre403(t *testing.T) {
	x := newEnv(t, engine.Config{})
	bob := x.addUser(t, "bob", false)
	cookie := x.session(t, "bob")
	// Disabled at the store, the way a race with the sign-out would find it:
	// the credential still resolves, the account says no.
	now := time.Now()
	u, err := x.users.GetUser(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	u.DisabledAt = &now
	if err := x.users.UpdateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	for name, hdr := range map[string]map[string]string{"token": bob, "cookie": cookie} {
		res, body := x.bare(t, "GET", "/api/me", "", hdr)
		if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "disabled") {
			t.Errorf("%s of a disabled account = %d %s", name, res.StatusCode, body)
		}
	}
}

func TestSetupBadInput(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.srv.Auth = authtest.New(auth.NewMem())
	if res, _ := x.bare(t, "POST", "/api/auth/setup", "{", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
	// The status page lists how one may sign in.
	res, body := x.bare(t, "GET", "/api/auth/status", "", nil)
	if st := decode[AuthStatus](t, []byte(body)); res.StatusCode != http.StatusOK || !st.SetupRequired || len(st.Methods) != 1 || st.Methods[0] != "password" {
		t.Errorf("status = %d %s", res.StatusCode, body)
	}
	// Setup is public, so it obeys the same origin rule as a cookie write.
	if res, _ := x.bare(t, "POST", "/api/auth/setup", `{}`, map[string]string{"Sec-Fetch-Site": "cross-site"}); res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site setup = %d", res.StatusCode)
	}
}

// sessionFails signs nobody in: the outage surfaces rather than a half-done
// sign-in.
type sessionFails struct{ auth.DB }

func (sessionFails) CreateSession(context.Context, auth.Session) error { return errors.New("db down") }

func TestLoginWhenTheSessionCannotBeStored(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.addPasswordUser(t, "lu", false)
	x.srv.Auth = authtest.New(sessionFails{x.users})
	res, body := x.do(t, "POST", "/api/auth/login", LoginRequest{Login: "lu", Proof: goodPassword}, nil)
	if res.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "db down") {
		t.Errorf("login = %d %s", res.StatusCode, body)
	}
}

func TestLimitsWithoutAStore(t *testing.T) {
	x := newEnv(t, engine.Config{})
	res, body := x.do(t, "GET", "/api/limits", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "max_concurrent_runs") {
		t.Errorf("limits = %d %s", res.StatusCode, body)
	}
	if res, _ := x.do(t, "PUT", "/api/limits", map[string]int{"max_concurrent_runs": 1, "max_environments": 1}, nil); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("set without a store = %d", res.StatusCode)
	}

	boom := brokenLimits{errors.New("limits down")}
	x.srv.Limits = boom
	if res, body := x.do(t, "GET", "/api/limits", nil, nil); res.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "limits down") {
		t.Errorf("read outage = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "PUT", "/api/limits", map[string]int{"max_concurrent_runs": 1, "max_environments": 1}, nil); res.StatusCode != http.StatusInternalServerError {
		t.Errorf("write outage = %d %s", res.StatusCode, body)
	}
	if res, _ := x.bare(t, "PUT", "/api/limits", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
}

type brokenLimits struct{ err error }

func (b brokenLimits) Limits(context.Context) (engine.Limits, error) { return engine.Limits{}, b.err }
func (b brokenLimits) Set(context.Context, engine.Limits) error      { return b.err }

func TestAuthErrMapping(t *testing.T) {
	for err, want := range map[error]int{
		auth.ErrNotFound:     http.StatusNotFound,
		auth.ErrConflict:     http.StatusConflict,
		auth.ErrSetupDone:    http.StatusConflict,
		auth.ErrInvalid:      http.StatusBadRequest,
		auth.ErrUnauthorized: http.StatusUnauthorized,
		auth.ErrDisabled:     http.StatusForbidden,
		errors.New("other"):  http.StatusInternalServerError,
	} {
		w := httptest.NewRecorder()
		authErr(w, err)
		if w.Code != want {
			t.Errorf("%v = %d, want %d", err, w.Code, want)
		}
	}
}

func TestScopeNamesAreDistinct(t *testing.T) {
	seen := map[string]Scope{}
	for sc := ScopeRun; sc <= ScopeSkillset; sc++ {
		name := scopeName(sc)
		if name == "resource" {
			t.Errorf("scope %d has no name", sc)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("scopes %d and %d share the name %q", sc, other, name)
		}
		seen[name] = sc
	}
	if scopeName(ScopeNone) != "resource" {
		t.Errorf("no scope = %q", scopeName(ScopeNone))
	}
}
