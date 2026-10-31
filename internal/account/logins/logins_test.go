package logins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/confstore"
	"apothecary/internal/platform/secrets"
)

func newStore(t *testing.T) *confstore.Service {
	t.Helper()
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	return confstore.New(confstore.NewMem(), key)
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// tokenServer answers token requests: it records each request's parameters
// and answers with what respond returns.
type tokenServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []map[string]string
	hdr   []http.Header
}

func newTokenServer(t *testing.T, respond func(params map[string]string) (int, any)) *tokenServer {
	ts := &tokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		params := map[string]string{}
		if strings.Contains(r.Header.Get("Content-Type"), "json") {
			_ = json.Unmarshal(raw, &params)
		} else {
			v, _ := url.ParseQuery(string(raw))
			for k := range v {
				params[k] = v.Get(k)
			}
		}
		ts.mu.Lock()
		ts.calls = append(ts.calls, params)
		ts.hdr = append(ts.hdr, r.Header.Clone())
		ts.mu.Unlock()
		status, body := respond(params)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) count() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.calls)
}

func pointEndpoints(t *testing.T, u string) {
	old := endpoints
	endpoints.OpenAIToken, endpoints.AnthropicToken, endpoints.GoogleToken = u, u, u
	t.Cleanup(func() { endpoints = old })
}

// Each flow sends its own CLI's refresh request; a rotated refresh token
// replaces the old one, and a non-rotating answer keeps it.
func TestRefreshPerFlow(t *testing.T) {
	now := time.Now().UTC()
	access := jwt(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-9"}})
	ts := newTokenServer(t, func(p map[string]string) (int, any) {
		if p["client_id"] == "" || p["grant_type"] != "refresh_token" {
			return 400, map[string]string{"error": "invalid_request"}
		}
		if strings.Contains(p["client_id"], "googleusercontent") {
			return 200, map[string]any{"access_token": "g-acc", "expires_in": 3599}
		}
		return 200, map[string]any{"access_token": access, "refresh_token": "rotated", "id_token": "idt", "expires_in": 3600,
			"account": map[string]string{"email_address": "Me@X.com"}}
	})
	pointEndpoints(t, ts.URL)
	for _, tc := range []struct {
		provider, wantRefresh, wantClient string
		json                              bool
	}{
		{"openai-chatgpt", "rotated", openAIClientID, false},
		{"anthropic", "rotated", b64(anthropicB64), true},
		{"google-gemini-cli", "old", "681255809395", false},
		{"google-antigravity", "old", "1071006060591", false},
	} {
		c := providers.Credential{ID: "c", Provider: tc.provider, Kind: providers.KindOAuth, Access: "a", Refresh: "old"}
		got, err := Refresh(context.Background(), http.DefaultClient, c, now)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		last := ts.calls[len(ts.calls)-1]
		if !strings.Contains(last["client_id"], tc.wantClient) || last["refresh_token"] != "old" {
			t.Errorf("%s sent %v", tc.provider, last)
		}
		if strings.Contains(ts.hdr[len(ts.hdr)-1].Get("Content-Type"), "json") != tc.json {
			t.Errorf("%s: body encoding", tc.provider)
		}
		if got.Refresh != tc.wantRefresh || got.Expires.Sub(now) < 59*time.Minute {
			t.Errorf("%s: refresh %q expires %v", tc.provider, got.Refresh, got.Expires)
		}
		if tc.provider == "openai-chatgpt" && (got.Get(providers.ExtraAccountID) != "acct-9" || got.Get(providers.ExtraIDToken) != "idt") {
			t.Errorf("chatgpt identity = %+v", got.Extra)
		}
		if tc.provider == "anthropic" {
			if got.Label() != "me@x.com" || !strings.Contains(ts.hdr[len(ts.hdr)-1].Get("anthropic-beta"), "oauth") {
				t.Errorf("anthropic = %+v, headers %v", got.Extra, ts.hdr[len(ts.hdr)-1])
			}
		}
	}
	// Copilot never calls anything: the GitHub token is the bearer.
	before := ts.count()
	c := providers.Credential{Provider: "github-copilot", Kind: providers.KindOAuth, Access: "gho", Refresh: "gho"}
	if got, err := Refresh(context.Background(), http.DefaultClient, c, now); err != nil || got.Access != "gho" || ts.count() != before {
		t.Errorf("copilot refresh = %+v, %v", got, err)
	}
}

func TestRefreshRefusals(t *testing.T) {
	status, body := 400, map[string]any{"error": "invalid_grant", "error_description": "Refresh token expired"}
	ts := newTokenServer(t, func(map[string]string) (int, any) { return status, body })
	pointEndpoints(t, ts.URL)
	c := providers.Credential{ID: "c", Provider: "anthropic", Kind: providers.KindOAuth, Access: "a", Refresh: "secret-refresh"}
	_, err := Refresh(context.Background(), http.DefaultClient, c, time.Now())
	if !IsPermanent(err) || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "secret-refresh") {
		t.Errorf("invalid_grant: %v", err)
	}
	status, body = 503, map[string]any{"error": "overloaded"}
	if _, err := Refresh(context.Background(), http.DefaultClient, c, time.Now()); err == nil || IsPermanent(err) {
		t.Errorf("a 503 is not permanent: %v", err)
	}
}

func TestKeeper(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	var refreshed atomic.Int32
	gate := make(chan struct{})
	ts := newTokenServer(t, func(p map[string]string) (int, any) {
		<-gate
		refreshed.Add(1)
		if p["refresh_token"] == "dead" {
			return 400, map[string]any{"error": "invalid_grant"}
		}
		return 200, map[string]any{"access_token": "new-acc", "refresh_token": "new-ref", "expires_in": 28800}
	})
	pointEndpoints(t, ts.URL)
	mk := func(refresh string, expires time.Duration) string {
		pc, err := store.CreateCredential(ctx, "u1", providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth,
			Access: "old-acc", Refresh: refresh, Expires: time.Now().Add(expires)}, "")
		if err != nil {
			t.Fatal(err)
		}
		return pc.ID
	}
	fresh := mk("r-fresh", 6*time.Hour)
	expiring := mk("r-expiring", 10*time.Minute)
	dead := mk("dead", -time.Minute)

	k := New(store, nil)
	var pushed []string
	var pmu sync.Mutex
	k.OnRefresh = func(_ context.Context, id string) { pmu.Lock(); pushed = append(pushed, id); pmu.Unlock() }

	if c, err := k.Fresh(ctx, fresh); err != nil || c.Access != "old-acc" || ts.count() != 0 {
		t.Fatalf("a fresh login was refreshed: %+v %v", c, err)
	}

	// Single-flight: five callers, one refresh, one push; every caller gets
	// the new token.
	var wg sync.WaitGroup
	results := make([]string, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := k.Fresh(ctx, expiring)
			if err != nil {
				t.Error(err)
			}
			results[i] = c.Access
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if refreshed.Load() != 1 || len(pushed) != 1 || pushed[0] != expiring {
		t.Errorf("refreshes %d, pushes %v", refreshed.Load(), pushed)
	}
	for _, a := range results {
		if a != "new-acc" {
			t.Errorf("a caller got %q", a)
		}
	}
	stored, meta, _ := store.OpenCredential(ctx, expiring)
	if stored.Refresh != "new-ref" || meta.Meta.LastRefresh.IsZero() {
		t.Errorf("the rotated refresh token was not kept: %+v", stored)
	}

	// A refusal disables the login; later callers are told without a call.
	if _, err := k.Fresh(ctx, dead); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("dead login: %v", err)
	}
	calls := ts.count()
	if _, err := k.Fresh(ctx, dead); err == nil || ts.count() != calls {
		t.Errorf("a disabled login was retried: %v", err)
	}
	if pc, _ := store.GetCredential(ctx, dead); pc.Meta.Status != confstore.CredentialDisabled {
		t.Errorf("status = %s", pc.Meta.Status)
	}

	// The sweep renews what is due and leaves the rest.
	soon := mk("r-soon", 5*time.Minute)
	k.Sweep(ctx)
	if c, _, _ := store.OpenCredential(ctx, soon); c.Access != "new-acc" {
		t.Errorf("the sweep missed a due login")
	}
	if c, _, _ := store.OpenCredential(ctx, fresh); c.Access != "old-acc" {
		t.Errorf("the sweep refreshed a fresh login")
	}
}

// A ChatGPT token that carries no expiry is renewed on age, ahead of codex's
// own eight-day rule.
func TestKeeperChatGPTStaleness(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	ts := newTokenServer(t, func(map[string]string) (int, any) {
		return 200, map[string]any{"access_token": "opaque", "refresh_token": "r2"}
	})
	pointEndpoints(t, ts.URL)
	pc, _ := store.CreateCredential(ctx, "u", providers.Credential{Provider: "openai-chatgpt", Kind: providers.KindOAuth, Access: "opaque", Refresh: "r1"}, "")
	k := New(store, nil)
	if _, err := k.Fresh(ctx, pc.ID); err != nil || ts.count() != 0 {
		t.Fatalf("a just-created login was refreshed (%d): %v", ts.count(), err)
	}
	k.now = func() time.Time { return time.Now().Add(6 * 24 * time.Hour) }
	if _, err := k.Fresh(ctx, pc.ID); err != nil || ts.count() != 1 {
		t.Errorf("a six-day-old login was not refreshed (%d): %v", ts.count(), err)
	}
}

func TestSignInDeviceFlows(t *testing.T) {
	approved := false
	access := jwt(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"}, "email": "me@x"})
	mux := http.NewServeMux()
	mux.HandleFunc("/usercode", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_auth_id": "dev-1", "user_code": "ABCD-1234", "interval": "5"})
	})
	mux.HandleFunc("/devtoken", func(w http.ResponseWriter, r *http.Request) {
		if !approved {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"authorization_code": "auth-code", "code_verifier": "ver"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "auth-code" || r.Form.Get("code_verifier") != "ver" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "ref", "id_token": access, "expires_in": 3600})
	})
	mux.HandleFunc("/gh/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "gh-dev", "user_code": "WXYZ", "verification_uri": "https://github.com/login/device", "interval": 5, "expires_in": 900})
	})
	mux.HandleFunc("/gh/token", func(w http.ResponseWriter, r *http.Request) {
		if !approved {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_token"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old, oldTok := signinEndpoints, endpoints
	defer func() { signinEndpoints, endpoints = old, oldTok }()
	signinEndpoints.OpenAIDeviceCode, signinEndpoints.OpenAIDeviceToken = srv.URL+"/usercode", srv.URL+"/devtoken"
	signinEndpoints.GitHubDeviceCode, signinEndpoints.GitHubAccessToken = srv.URL+"/gh/code", srv.URL+"/gh/token"
	endpoints.OpenAIToken = srv.URL + "/token"

	s := NewSignIns()
	ctx := context.Background()
	for _, provider := range []string{"openai-chatgpt", "github-copilot"} {
		approved = false
		p, err := s.Start(ctx, "u1", provider)
		if err != nil || p.UserCode == "" || p.URL == "" || p.Paste || p.IntervalSeconds < 5 {
			t.Fatalf("%s start = %+v, %v", provider, p, err)
		}
		if _, err := s.Complete(ctx, "u2", p.ID, ""); err == nil || err == ErrPending {
			t.Errorf("%s: another user completed the sign-in: %v", provider, err)
		}
		if _, err := s.Complete(ctx, "u1", p.ID, ""); err != ErrPending {
			t.Errorf("%s before approval: %v", provider, err)
		}
		approved = true
		c, err := s.Complete(ctx, "u1", p.ID, "")
		if err != nil || c.Provider != provider || c.Kind != providers.KindOAuth || c.Access == "" || c.Refresh == "" {
			t.Fatalf("%s complete = %+v, %v", provider, c, err)
		}
		if provider == "openai-chatgpt" && (c.Get(providers.ExtraAccountID) != "acct-1" || c.Label() != "me@x") {
			t.Errorf("chatgpt identity = %+v", c.Extra)
		}
		if _, err := s.Complete(ctx, "u1", p.ID, ""); err == nil {
			t.Errorf("%s: a finished sign-in completed twice", provider)
		}
	}
}

func TestSignInPasteFlows(t *testing.T) {
	var gotState, gotVerifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/anthropic", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]string
		_ = json.NewDecoder(r.Body).Decode(&p)
		gotState, gotVerifier = p["state"], p["code_verifier"]
		if p["code"] != "the-code" || p["grant_type"] != "authorization_code" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "refresh_token": "r", "expires_in": 28800,
			"account": map[string]string{"email_address": "me@x"}})
	})
	mux.HandleFunc("/google", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "g-code" || r.Form.Get("client_secret") == "" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ga", "refresh_token": "gr", "expires_in": 3599})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"email": "Me@Gmail.com"})
	})
	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ga" || !strings.HasPrefix(r.Header.Get("User-Agent"), "GeminiCLI/") {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "proj-1", "currentTier": map[string]string{"id": "free-tier"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old, oldTok := signinEndpoints, endpoints
	defer func() { signinEndpoints, endpoints = old, oldTok }()
	endpoints.AnthropicToken, endpoints.GoogleToken = srv.URL+"/anthropic", srv.URL+"/google"
	signinEndpoints.GoogleUserinfo, signinEndpoints.CodeAssist = srv.URL+"/userinfo", srv.URL

	s := NewSignIns()
	ctx := context.Background()
	p, err := s.Start(ctx, "u1", "anthropic")
	if err != nil || !p.Paste || !strings.Contains(p.URL, "code_challenge=") || !strings.Contains(p.URL, "code=true") {
		t.Fatalf("anthropic start = %+v, %v", p, err)
	}
	state := mustQuery(t, p.URL, "state")
	if _, err := s.Complete(ctx, "u1", p.ID, "the-code#not-"+state); err == nil || !strings.Contains(err.Error(), "another sign-in") {
		t.Errorf("a foreign state was accepted: %v", err)
	}
	p, _ = s.Start(ctx, "u1", "anthropic")
	state = mustQuery(t, p.URL, "state")
	c, err := s.Complete(ctx, "u1", p.ID, "the-code#"+state)
	if err != nil || c.Refresh != "r" || c.Label() != "me@x" || gotState != state || gotVerifier == "" {
		t.Fatalf("anthropic = %+v %v (state %q verifier %q)", c, err, gotState, gotVerifier)
	}

	p, err = s.Start(ctx, "u1", "google-gemini-cli")
	if err != nil || !p.Paste || !strings.Contains(p.URL, "access_type=offline") {
		t.Fatalf("gemini start = %+v, %v", p, err)
	}
	landed := "http://127.0.0.1:8085/oauth2callback?state=" + mustQuery(t, p.URL, "state") + "&code=g-code&scope=x"
	c, err = s.Complete(ctx, "u1", p.ID, landed)
	if err != nil || c.Refresh != "gr" || c.Get(providers.ExtraProjectID) != "proj-1" || c.Label() != "me@gmail.com" {
		t.Fatalf("gemini = %+v, %v", c, err)
	}

	if _, err := s.Start(ctx, "u1", "google-antigravity"); err == nil || !strings.Contains(err.Error(), "import") {
		t.Errorf("antigravity sign-in: %v", err)
	}
	if _, err := s.Start(ctx, "u1", "openrouter"); err == nil {
		t.Error("a key-only provider has no sign-in")
	}
}

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}
