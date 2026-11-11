package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fastPolls(t *testing.T) {
	t.Helper()
	old := pollUnit
	pollUnit = time.Millisecond
	t.Cleanup(func() { pollUnit = old })
}

// A pasted personal token is checked against the engine before it is kept,
// and the file that keeps it is the user's only.
func TestLoginWithAToken(t *testing.T) {
	e := newFakeEngine(t)
	r := newRig(t)
	r.signIn(t, e)
	if !strings.Contains(r.out.String(), "as ada") {
		t.Errorf("out = %q", r.out)
	}
	path := filepath.Join(r.env.ConfigDir, "apothecary", "config.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config %v %v", info, err)
	}
	cfg, _ := loadConfig(r.env)
	if cfg.URL != e.srv.URL || cfg.Token != "good" || cfg.TokenID != "" {
		t.Errorf("cfg = %+v", cfg)
	}
	r2 := newRig(t)
	if code := r2.run("login", "--token", "bad", e.srv.URL); code != 1 || !strings.Contains(r2.err.String(), "token expired — sign in again") {
		t.Errorf("bad token = %d %q", code, r2.err)
	}
	if cfg, _ := loadConfig(r2.env); cfg.Token != "" {
		t.Error("a refused token was kept")
	}
	if code := r2.run("login", "ftp://nope", "--token", "x"); code != 1 {
		t.Errorf("bad url = %d", code)
	}
	if code := r2.run("login", e.srv.URL, "--bogus"); code != 2 {
		t.Errorf("bad flag = %d", code)
	}
}

// The browser login: the page opens on the code, the client waits for the
// approval, and keeps the token the engine minted for this machine.
func TestLoginInTheBrowser(t *testing.T) {
	fastPolls(t)
	e := newFakeEngine(t)
	r := newRig(t)
	var opened string
	r.env.OpenBrowser = func(u string) error { opened = u; return nil }
	if code := r.run("login", e.srv.URL); code != 0 {
		t.Fatalf("login = %d %q", code, r.err)
	}
	want := e.srv.URL + "/cli-login?code=ABCD-EFGH"
	if opened != want || !strings.Contains(r.out.String(), "approve code ABCD-EFGH") || !strings.Contains(r.out.String(), want) ||
		!strings.Contains(r.out.String(), "Signed in to "+e.srv.URL+" as ada.") {
		t.Errorf("opened %q, out %q", opened, r.out)
	}
	cfg, _ := loadConfig(r.env)
	if cfg.Token != "good" || cfg.TokenID != "t-cli" {
		t.Errorf("cfg = %+v", cfg)
	}
	// Logout revokes what login minted.
	if code := r.run("logout"); code != 0 || !strings.Contains(r.out.String(), "sign-in is revoked") || len(e.revoked) != 1 || e.revoked[0] != "t-cli" {
		t.Errorf("logout = %d %q %v", code, r.out, e.revoked)
	}
	if cfg, _ := loadConfig(r.env); cfg.Token != "" {
		t.Error("logout must forget the token")
	}
}

func TestLoginWithoutABrowser(t *testing.T) {
	fastPolls(t)
	e := newFakeEngine(t)
	r := newRig(t)
	r.env.OpenBrowser = func(string) error { t.Error("--no-browser opened one"); return nil }
	if code := r.run("login", e.srv.URL, "--no-browser"); code != 0 {
		t.Fatalf("login = %d %q", code, r.err)
	}
}

func TestLoginDeniedOrNeverApproved(t *testing.T) {
	fastPolls(t)
	e := newFakeEngine(t)
	r := newRig(t)
	e.deny = true
	if code := r.run("login", e.srv.URL); code != 1 || !strings.Contains(r.err.String(), "the sign-in was denied") {
		t.Errorf("denied = %d %q", code, r.err)
	}
	e.deny, e.approve = false, 0
	if code := r.run("login", e.srv.URL); code != 1 || !strings.Contains(r.err.String(), "not approved in time") {
		t.Errorf("never approved = %d %q", code, r.err)
	}
	// An engine that cannot open a sign-in.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	if code := r.run("login", srv.URL); code != 1 {
		t.Errorf("no sign-in = %d", code)
	}
}

func TestLogoutOfAPastedToken(t *testing.T) {
	e := newFakeEngine(t)
	r := newRig(t)
	r.signIn(t, e)
	if code := r.run("logout"); code != 0 || !strings.Contains(r.out.String(), "still works until you revoke it") || len(e.revoked) != 0 {
		t.Errorf("logout = %d %q %v", code, r.out, e.revoked)
	}
}

func TestLogoutRefusals(t *testing.T) {
	e := newFakeEngine(t)
	r := newRig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_ = saveConfig(r.env, Config{URL: srv.URL, Token: "good", TokenID: "t1"})
	if code := r.run("logout"); code != 1 || !strings.Contains(r.err.String(), "revoke this machine's sign-in") {
		t.Errorf("refused revoke = %d %q", code, r.err)
	}
	// A sign-in already gone is signed out anyway.
	_ = saveConfig(r.env, Config{URL: e.srv.URL, Token: "bad", TokenID: "t1"})
	if code := r.run("logout"); code != 0 {
		t.Errorf("stale logout = %d %q", code, r.err)
	}
	_ = saveConfig(r.env, Config{URL: "ftp://x", Token: "good"})
	if code := r.run("logout"); code != 1 {
		t.Errorf("bad saved url = %d", code)
	}
}

func TestSplitURLAndTokenName(t *testing.T) {
	u, rest := splitURL([]string{"--no-browser", "https://h", "--token=t"})
	if u != "https://h" || strings.Join(rest, " ") != "--no-browser --token=t" {
		t.Errorf("split = %q %v", u, rest)
	}
	if u, _ := splitURL([]string{"--token", "t"}); u != "" {
		t.Errorf("no url = %q", u)
	}
	if tokenName(Env{}) != "apothecary CLI" || tokenName(Env{Hostname: "h"}) != "apothecary CLI on h" {
		t.Error("token names")
	}
}
