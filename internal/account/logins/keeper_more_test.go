package logins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/project/confstore"
)

// flakyStore fails the named store calls.
type flakyStore struct {
	Store
	list, save, disable error
	// openErrAfter fails OpenCredential after this many calls (0 = never).
	openErrAfter int
	opens        int
	mu           sync.Mutex
}

func (f *flakyStore) ListCredentials(ctx context.Context, u string) ([]confstore.ProviderCredential, error) {
	if f.list != nil {
		return nil, f.list
	}
	return f.Store.ListCredentials(ctx, u)
}
func (f *flakyStore) SaveRefreshed(ctx context.Context, c providers.Credential) (confstore.ProviderCredential, error) {
	if f.save != nil {
		return confstore.ProviderCredential{}, f.save
	}
	return f.Store.SaveRefreshed(ctx, c)
}
func (f *flakyStore) DisableCredential(ctx context.Context, id, why string) error {
	if f.disable != nil {
		return f.disable
	}
	return f.Store.DisableCredential(ctx, id, why)
}
func (f *flakyStore) OpenCredential(ctx context.Context, id string) (providers.Credential, confstore.ProviderCredential, error) {
	f.mu.Lock()
	f.opens++
	n := f.opens
	f.mu.Unlock()
	if f.openErrAfter > 0 && n > f.openErrAfter {
		return providers.Credential{}, confstore.ProviderCredential{}, errors.New("store went away")
	}
	return f.Store.OpenCredential(ctx, id)
}

func TestDueDecidesPerFlow(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	k := New(newStore(t), nil)
	k.now = func() time.Time { return now }
	soon, later := now.Add(k.Margin-time.Second), now.Add(k.Margin+time.Second)
	for name, c := range map[string]struct {
		cred providers.Credential
		meta confstore.ProviderCredential
		want bool
	}{
		"an api key":          {providers.Credential{Provider: "openai", Kind: providers.KindAPIKey, Expires: soon}, confstore.ProviderCredential{}, false},
		"a github token":      {providers.Credential{Provider: "github-copilot", Kind: providers.KindOAuth, Expires: soon}, confstore.ProviderCredential{}, false},
		"inside the margin":   {providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth, Expires: soon}, confstore.ProviderCredential{}, true},
		"outside the margin":  {providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth, Expires: later}, confstore.ProviderCredential{}, false},
		"no expiry, no clock": {providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth}, confstore.ProviderCredential{}, false},
	} {
		if got := k.due(c.cred, c.meta); got != c.want {
			t.Errorf("%s: due = %v, want %v", name, got, c.want)
		}
	}
}

func oauth(t *testing.T, store *confstore.Service, refresh string, expires time.Time) string {
	t.Helper()
	pc, err := store.CreateCredential(context.Background(), "u1", providers.Credential{
		Provider: "anthropic", Kind: providers.KindOAuth, Access: "old-acc", Refresh: refresh, Expires: expires}, "")
	if err != nil {
		t.Fatal(err)
	}
	return pc.ID
}

func TestFreshOnAMissingCredential(t *testing.T) {
	k := New(newStore(t), nil)
	if _, err := k.Fresh(context.Background(), "nope"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

// A provider that is down costs nothing while the token still works; once it
// has expired the caller is told.
func TestTransientRefreshFailure(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	ts := newTokenServer(t, func(map[string]string) (int, any) { return 503, map[string]string{"error": "overloaded"} })
	pointEndpoints(t, ts.URL)
	k := New(store, nil)

	stillGood := oauth(t, store, "r1", time.Now().Add(5*time.Minute))
	c, err := k.Fresh(ctx, stillGood)
	if err != nil || c.Access != "old-acc" {
		t.Errorf("token with time left = %+v, %v", c, err)
	}
	expired := oauth(t, store, "r2", time.Now().Add(-time.Minute))
	if _, err := k.Fresh(ctx, expired); err == nil || !strings.Contains(err.Error(), "has expired") {
		t.Errorf("expired = %v", err)
	}
	// Neither was disabled: the next pass tries again.
	if _, meta, err := store.OpenCredential(ctx, expired); err != nil || meta.Meta.Status == confstore.CredentialDisabled {
		t.Errorf("after a transient failure = %+v, %v", meta, err)
	}
}

// recNotices records what the keeper tells its owner.
type recNotices struct{ calls []string }

func (r *recNotices) LoginSignedOut(_ context.Context, userID, credentialID, _, _, _ string) {
	r.calls = append(r.calls, "signed_out "+userID+" "+credentialID)
}
func (r *recNotices) LoginExpired(_ context.Context, userID, credentialID, _ string, err error) {
	if err != nil {
		r.calls = append(r.calls, "expired "+userID+" "+credentialID)
	}
}
func (r *recNotices) LoginRefreshed(_ context.Context, credentialID string) {
	r.calls = append(r.calls, "refreshed "+credentialID)
}

// Each refresh outcome a person must hear about reaches the credential's owner.
func TestFreshTellsNotices(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		status  int
		body    any
		expires time.Duration
		want    string
	}{
		"refused for good": {400, map[string]string{"error": "invalid_grant"}, time.Minute, "signed_out u1 "},
		"expired":          {503, map[string]string{"error": "overloaded"}, -time.Minute, "expired u1 "},
		"refreshed":        {200, map[string]any{"access_token": "new", "expires_in": 3600}, time.Minute, "refreshed "},
	} {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			ts := newTokenServer(t, func(map[string]string) (int, any) { return c.status, c.body })
			pointEndpoints(t, ts.URL)
			id := oauth(t, store, "r", time.Now().Add(c.expires))
			rec := &recNotices{}
			k := New(store, nil)
			k.Notices = rec
			_, _ = k.Fresh(ctx, id)
			if len(rec.calls) != 1 || rec.calls[0] != c.want+id {
				t.Errorf("calls = %q, want [%q]", rec.calls, c.want+id)
			}
		})
	}
}

// The provider spent the old refresh token, so losing the new one must be
// loud: the caller hears, and OnRefresh does not fire for nothing.
func TestRefreshThatCannotBeStored(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	ts := newTokenServer(t, func(map[string]string) (int, any) {
		return 200, map[string]any{"access_token": "new", "refresh_token": "new-r", "expires_in": 3600}
	})
	pointEndpoints(t, ts.URL)
	id := oauth(t, store, "r", time.Now().Add(time.Minute))
	k := New(&flakyStore{Store: store, save: errors.New("disk full")}, nil)
	pushed := false
	k.OnRefresh = func(context.Context, string) { pushed = true }
	if _, err := k.Fresh(ctx, id); err == nil || !strings.Contains(err.Error(), "could not store") {
		t.Errorf("err = %v", err)
	}
	if pushed {
		t.Error("OnRefresh fired for a login that was not stored")
	}
}

// A dead login is reported dead even when recording that fails.
func TestPermanentRefusalWithAStoreOutage(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	ts := newTokenServer(t, func(map[string]string) (int, any) { return 400, map[string]string{"error": "invalid_grant"} })
	pointEndpoints(t, ts.URL)
	id := oauth(t, store, "dead", time.Now().Add(time.Minute))
	k := New(&flakyStore{Store: store, disable: errors.New("disk full")}, nil)
	var dis *DisabledError
	if _, err := k.Fresh(ctx, id); !errors.As(err, &dis) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(dis.Error(), "invalid_grant") {
		t.Errorf("reason = %v", dis)
	}
}

func TestFreshRereadsUnderTheLock(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	id := oauth(t, store, "r", time.Now().Add(time.Minute))
	k := New(&flakyStore{Store: store, openErrAfter: 1}, nil)
	if _, err := k.Fresh(ctx, id); err == nil || !strings.Contains(err.Error(), "store went away") {
		t.Errorf("err = %v", err)
	}
}

// disablesWhileWaiting disables the credential between the two reads, as a
// concurrent caller's failed refresh would.
type disablesWhileWaiting struct {
	Store
	id    string
	opens int
}

func (d *disablesWhileWaiting) OpenCredential(ctx context.Context, id string) (providers.Credential, confstore.ProviderCredential, error) {
	d.opens++
	if d.opens == 2 {
		if err := d.Store.DisableCredential(ctx, d.id, "spent"); err != nil {
			return providers.Credential{}, confstore.ProviderCredential{}, err
		}
	}
	return d.Store.OpenCredential(ctx, id)
}

func TestFreshSeesADisableThatRacedIt(t *testing.T) {
	store := newStore(t)
	id := oauth(t, store, "r", time.Now().Add(time.Minute))
	k := New(&disablesWhileWaiting{Store: store, id: id}, nil)
	var dis *DisabledError
	if _, err := k.Fresh(context.Background(), id); !errors.As(err, &dis) || dis.Reason != "spent" {
		t.Errorf("err = %v", err)
	}
}

func TestSweep(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	ts := newTokenServer(t, func(p map[string]string) (int, any) {
		if p["refresh_token"] == "dead" {
			return 400, map[string]string{"error": "invalid_grant"}
		}
		return 200, map[string]any{"access_token": "new", "expires_in": 3600}
	})
	pointEndpoints(t, ts.URL)
	due := oauth(t, store, "r", time.Now().Add(time.Minute))
	dead := oauth(t, store, "dead", time.Now().Add(time.Minute))
	disabled := oauth(t, store, "r2", time.Now().Add(time.Minute))
	if err := store.DisableCredential(ctx, disabled, "gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateCredential(ctx, "u1", providers.Credential{Provider: "openrouter", Kind: providers.KindAPIKey, Key: "k"}, ""); err != nil {
		t.Fatal(err)
	}

	k := New(store, nil)
	k.Sweep(ctx)
	if c, _, err := store.OpenCredential(ctx, due); err != nil || c.Access != "new" {
		t.Errorf("due login = %+v, %v", c, err)
	}
	if _, meta, _ := store.OpenCredential(ctx, dead); meta.Meta.Status != confstore.CredentialDisabled {
		t.Errorf("dead login = %+v", meta)
	}
	if n := ts.count(); n != 2 {
		t.Errorf("token requests = %d, want the two due logins only", n)
	}

	// A list failure is logged, not fatal; a stopped sweep stops.
	New(&flakyStore{Store: store, list: errors.New("db down")}, nil).Sweep(ctx)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	before := ts.count()
	stale := oauth(t, store, "r3", time.Now().Add(time.Minute))
	k.Sweep(cancelled)
	if ts.count() != before {
		t.Errorf("a cancelled sweep refreshed %s", stale)
	}
}

// Run sweeps at once, then on its clock, until told to stop.
func TestKeeperRun(t *testing.T) {
	store := newStore(t)
	ts := newTokenServer(t, func(map[string]string) (int, any) {
		return 200, map[string]any{"access_token": "new", "expires_in": 1} // due again at once
	})
	pointEndpoints(t, ts.URL)
	oauth(t, store, "r", time.Now().Add(time.Minute))
	k := New(store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { k.Run(ctx, time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for ts.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ts.count() < 2 {
		t.Errorf("sweeps = %d, want it to repeat", ts.count())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run outlived its context")
	}
	// A zero interval means the default, not a tight loop or a panic.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	k.Run(ctx2, 0)
}

func TestRefreshRefusesWhatIsNotALogin(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for name, c := range map[string]providers.Credential{
		"unknown provider": {Provider: "nope", Kind: providers.KindOAuth, Refresh: "r"},
		"an api key":       {Provider: "anthropic", Kind: providers.KindAPIKey, Refresh: "r"},
		"keys only":        {Provider: "openrouter", Kind: providers.KindOAuth, Refresh: "r"},
	} {
		if _, err := Refresh(ctx, http.DefaultClient, c, now); err == nil || IsPermanent(err) {
			t.Errorf("%s = %v", name, err)
		}
	}
	_, err := Refresh(ctx, http.DefaultClient, providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth}, now)
	if !IsPermanent(err) {
		t.Errorf("no refresh token = %v, want a permanent refusal", err)
	}
	// A GitHub token has nothing to refresh: it comes back as it went in.
	c := providers.Credential{Provider: "github-copilot", Kind: providers.KindOAuth, Access: "gho", Refresh: "gho"}
	if got, err := Refresh(ctx, http.DefaultClient, c, now); err != nil || got.Access != "gho" {
		t.Errorf("copilot = %+v, %v", got, err)
	}
}

func TestRefreshAnswers(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	exp := now.Add(2 * time.Hour).Truncate(time.Second)
	var answer any
	status := 200
	ts := newTokenServer(t, func(map[string]string) (int, any) { return status, answer })
	pointEndpoints(t, ts.URL)
	cred := providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth, Refresh: "r"}

	answer = map[string]string{"refresh_token": "x"}
	if _, err := Refresh(ctx, ts.Client(), cred, now); err == nil || !strings.Contains(err.Error(), "without an access_token") {
		t.Errorf("no access token = %v", err)
	}
	// With no expires_in the token's own exp is the expiry, else none.
	answer = map[string]any{"access_token": jwt(map[string]any{"exp": float64(exp.Unix())})}
	got, err := Refresh(ctx, ts.Client(), cred, now)
	if err != nil || !got.Expires.Equal(exp) {
		t.Errorf("exp from the JWT = %v, %v", got.Expires, err)
	}
	cred.Expires = now
	answer = map[string]any{"access_token": "opaque"}
	if got, err = Refresh(ctx, ts.Client(), cred, now); err != nil || !got.Expires.IsZero() {
		t.Errorf("opaque token = %v, %v", got.Expires, err)
	}

	// Refusals: the provider's own words, permanent only when they say so.
	for name, c := range map[string]struct {
		status    int
		body      any
		permanent bool
		says      string
	}{
		"object error, code":    {400, map[string]any{"error": map[string]any{"code": "refresh_token_expired", "message": "too old"}}, true, "too old"},
		"object error, type":    {401, map[string]any{"error": map[string]any{"type": "invalid_token"}}, true, "invalid_token"},
		"description says so":   {403, map[string]any{"error": "denied", "error_description": "Refresh token reused"}, true, "reused"},
		"a 500 is never final":  {500, map[string]any{"error": "invalid_grant"}, false, "invalid_grant"},
		"a rate limit":          {429, map[string]any{"error": "slow_down"}, false, "slow_down"},
		"not json":              {502, "bad gateway", false, "502"},
		"400 for another cause": {400, map[string]any{"error": "invalid_request"}, false, "invalid_request"},
	} {
		status, answer = c.status, c.body
		_, err := Refresh(ctx, ts.Client(), providers.Credential{Provider: "anthropic", Kind: providers.KindOAuth, Refresh: "SECRET-REFRESH"}, now)
		if err == nil || IsPermanent(err) != c.permanent || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s = %v (permanent %v)", name, err, IsPermanent(err))
		}
		if err != nil && strings.Contains(err.Error(), "SECRET-REFRESH") {
			t.Errorf("%s: the refresh token reached an error: %v", name, err)
		}
	}
}

func TestPostTokenTransportFailures(t *testing.T) {
	ctx := context.Background()
	// A server that closes before answering, and one that answers junk.
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	if _, err := postToken(ctx, http.DefaultClient, u, "form", nil, nil); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("unreachable = %v", err)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }))
	defer junk.Close()
	if _, err := postToken(ctx, http.DefaultClient, junk.URL, "json", nil, nil); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("junk = %v", err)
	}
	if _, err := postToken(ctx, http.DefaultClient, "http://bad host/", "form", nil, nil); err == nil {
		t.Error("a bad URL was accepted")
	}
}

func TestIntervalOf(t *testing.T) {
	for _, c := range []struct {
		v    any
		want int
	}{
		{float64(7), 7}, {float64(0), 5}, {float64(-1), 5}, {"9", 9}, {"0", 5}, {"soon", 5}, {nil, 5}, {true, 5},
	} {
		if got := intervalOf(c.v, 5); got != c.want {
			t.Errorf("intervalOf(%v) = %d, want %d", c.v, got, c.want)
		}
	}
}

func TestPastedCode(t *testing.T) {
	for _, c := range []struct {
		pasted, state, want string
		bad                 bool
	}{
		{"", "s", "", true},
		{"  ", "s", "", true},
		{"bare-code", "s", "bare-code", false},
		{"code#s", "s", "code", false},
		{"code#other", "s", "", true},
		{"http://localhost:54545/callback?code=c1&state=s", "s", "c1", false},
		{"http://localhost:54545/callback?code=c1&state=other", "s", "", true},
		{"http://localhost:54545/callback?code=c1", "s", "c1", false},
	} {
		got, err := pastedCode(c.pasted, c.state)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("pastedCode(%q) = %q, %v", c.pasted, got, err)
		}
	}
}

// signinServer serves JSON by path; each handler is a status and a body.
func signinServer(t *testing.T, routes map[string]func() (int, any)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, h := range routes {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			status, body := h()
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oldS, oldE := signinEndpoints, endpoints
	t.Cleanup(func() { signinEndpoints, endpoints = oldS, oldE })
	signinEndpoints.OpenAIDeviceCode, signinEndpoints.OpenAIDeviceToken = srv.URL+"/usercode", srv.URL+"/devtoken"
	signinEndpoints.GitHubDeviceCode, signinEndpoints.GitHubAccessToken = srv.URL+"/gh/code", srv.URL+"/gh/token"
	signinEndpoints.GoogleUserinfo, signinEndpoints.CodeAssist = srv.URL+"/userinfo", srv.URL
	endpoints.OpenAIToken, endpoints.AnthropicToken, endpoints.GoogleToken = srv.URL+"/token", srv.URL+"/token", srv.URL+"/token"
	return srv
}

func answer(status int, body any) func() (int, any) {
	return func() (int, any) { return status, body }
}

func TestStartRefusals(t *testing.T) {
	ctx := context.Background()
	s := NewSignIns()
	if _, err := s.Start(ctx, "u", "nope"); err == nil {
		t.Error("an unknown provider started")
	}
	if _, err := s.Start(ctx, "u", "openrouter"); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("a key-only provider = %v", err)
	}
	if _, err := s.Start(ctx, "u", "google-antigravity"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("a flow with no sign-in = %v", err)
	}
	signinServer(t, map[string]func() (int, any){
		"/usercode": answer(500, map[string]string{}),
		"/gh/code":  answer(200, map[string]string{"device_code": "d"}),
	})
	if _, err := s.Start(ctx, "u", "openai-chatgpt"); err == nil || !strings.Contains(err.Error(), "OpenAI device sign-in") {
		t.Errorf("provider down = %v", err)
	}
	if _, err := s.Start(ctx, "u", "github-copilot"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("half an answer = %v", err)
	}
}

func TestStartMoreAnswers(t *testing.T) {
	ctx := context.Background()
	s := NewSignIns()
	signinServer(t, map[string]func() (int, any){
		"/usercode": answer(200, map[string]string{"user_code": "X"}),
		"/gh/code":  answer(500, map[string]string{}),
	})
	if _, err := s.Start(ctx, "u", "openai-chatgpt"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("half an answer = %v", err)
	}
	if _, err := s.Start(ctx, "u", "github-copilot"); err == nil || !strings.Contains(err.Error(), "GitHub device sign-in") {
		t.Errorf("provider down = %v", err)
	}
}

// One pending sign-in per person and provider; a stale one goes as the next
// arrives, and someone else's stays.
func TestPendingSignInsAreKeptTidy(t *testing.T) {
	ctx := context.Background()
	s := NewSignIns()
	now := time.Now()
	s.now = func() time.Time { return now }

	first, err := s.Start(ctx, "u", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Start(ctx, "v", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Start(ctx, "u", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.m[first.ID]; ok {
		t.Error("the superseded sign-in stayed")
	}
	if _, ok := s.m[other.ID]; !ok {
		t.Error("another person's sign-in went")
	}
	// Nobody else can finish it, whatever they hold.
	if _, err := s.Complete(ctx, "v", second.ID, "code"); err == nil || !strings.Contains(err.Error(), "no such sign-in") {
		t.Errorf("someone else's sign-in = %v", err)
	}
	if _, ok := s.m[second.ID]; !ok {
		t.Error("a refused completion ended the sign-in")
	}

	now = now.Add(signinTTL + time.Minute)
	if _, err := s.Complete(ctx, "u", second.ID, "code"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired = %v", err)
	}
	if _, ok := s.m[second.ID]; ok {
		t.Error("an expired sign-in stayed")
	}
	// Someone else starting sweeps the stale rows of everybody.
	if _, err := s.Start(ctx, "w", "anthropic"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.m[other.ID]; ok {
		t.Error("a stale sign-in survived the sweep")
	}
}

func TestSignInUnsupportedFlowEnds(t *testing.T) {
	s := NewSignIns()
	si := &signin{userID: "u", flow: "nonsense"}
	si.ID, si.ExpiresAt = "x", time.Now().Add(time.Hour)
	s.m["x"] = si
	if _, err := s.Complete(context.Background(), "u", "x", ""); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("err = %v", err)
	}
	if _, ok := s.m["x"]; ok {
		t.Error("a failed sign-in stayed pending")
	}
}

// completeDevice finishes a device sign-in against a scripted provider.
func completeDevice(t *testing.T, provider string) (providers.Credential, error) {
	t.Helper()
	s := NewSignIns()
	p, err := s.Start(context.Background(), "u", provider)
	if err != nil {
		t.Fatal(err)
	}
	return s.Complete(context.Background(), "u", p.ID, "")
}

func TestChatGPTDeviceFailures(t *testing.T) {
	access := jwt(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"}})
	devOK := answer(200, map[string]string{"authorization_code": "c", "code_verifier": "v"})
	start := answer(200, map[string]string{"device_auth_id": "d", "user_code": "U"})
	for name, c := range map[string]struct {
		dev, tok func() (int, any)
		want     string
	}{
		"device refused":   {answer(500, map[string]string{}), nil, "failed (500)"},
		"half an answer":   {answer(200, map[string]string{"authorization_code": "c"}), nil, "incomplete"},
		"token refused":    {devOK, answer(400, map[string]string{"error": "invalid_grant"}), "invalid_grant"},
		"no refresh token": {devOK, answer(200, map[string]string{"access_token": access}), "no refresh token"},
		"no account":       {devOK, answer(200, map[string]string{"access_token": "opaque", "refresh_token": "r"}), "names no ChatGPT account"},
		"no access token":  {devOK, answer(200, map[string]string{"refresh_token": "r"}), "without an access_token"},
	} {
		t.Run(name, func(t *testing.T) {
			routes := map[string]func() (int, any){"/usercode": start, "/devtoken": c.dev}
			if c.tok != nil {
				routes["/token"] = c.tok
			}
			signinServer(t, routes)
			if _, err := completeDevice(t, "openai-chatgpt"); err == nil || errors.Is(err, ErrPending) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// A provider that cannot be reached mid-poll is not a failed sign-in.
func TestDevicePollingSurvivesAnOutage(t *testing.T) {
	ctx := context.Background()
	srv := signinServer(t, map[string]func() (int, any){
		"/usercode": answer(200, map[string]string{"device_auth_id": "d", "user_code": "U"}),
		"/gh/code":  answer(200, map[string]any{"device_code": "d", "user_code": "U", "verification_uri": "https://g", "expires_in": 60}),
		"/gh/token": answer(200, map[string]string{"error": "slow_down"}),
	})
	s := NewSignIns()
	gh, err := s.Start(ctx, "u", "github-copilot")
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(gh.ExpiresAt) > 2*time.Minute {
		t.Errorf("the provider's expiry was ignored: %v", gh.ExpiresAt)
	}
	if _, err := s.Complete(ctx, "u", gh.ID, ""); !errors.Is(err, ErrPending) {
		t.Errorf("slow_down = %v", err)
	}
	oa, err := s.Start(ctx, "u", "openai-chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	for name, id := range map[string]string{"chatgpt": oa.ID, "copilot": gh.ID} {
		if _, err := s.Complete(ctx, "u", id, ""); !errors.Is(err, ErrPending) {
			t.Errorf("%s while down = %v, want pending", name, err)
		}
	}
}

func TestCopilotRefusal(t *testing.T) {
	signinServer(t, map[string]func() (int, any){
		"/gh/code":  answer(200, map[string]any{"device_code": "d", "user_code": "U", "verification_uri": "https://g"}),
		"/gh/token": answer(200, map[string]string{"error": "access_denied", "error_description": "the person said no"}),
	})
	if _, err := completeDevice(t, "github-copilot"); err == nil || !strings.Contains(err.Error(), "the person said no") {
		t.Errorf("err = %v", err)
	}
}

func completePaste(t *testing.T, provider, pasted string) (providers.Credential, error) {
	t.Helper()
	s := NewSignIns()
	p, err := s.Start(context.Background(), "u", provider)
	if err != nil {
		t.Fatal(err)
	}
	return s.Complete(context.Background(), "u", p.ID, pasted)
}

func TestPasteFlowFailures(t *testing.T) {
	signinServer(t, map[string]func() (int, any){"/token": answer(200, map[string]string{"access_token": "a"})})
	for _, provider := range []string{"anthropic", "google-gemini-cli"} {
		if _, err := completePaste(t, provider, ""); err == nil || !strings.Contains(err.Error(), "paste the code") {
			t.Errorf("%s with nothing pasted = %v", provider, err)
		}
		if _, err := completePaste(t, provider, "code"); err == nil || !strings.Contains(err.Error(), "no refresh token") {
			t.Errorf("%s with no refresh token = %v", provider, err)
		}
	}
	signinServer(t, map[string]func() (int, any){"/token": answer(400, map[string]string{"error": "invalid_grant"})})
	for _, provider := range []string{"anthropic", "google-gemini-cli"} {
		if _, err := completePaste(t, provider, "code"); err == nil || !strings.Contains(err.Error(), "invalid_grant") {
			t.Errorf("%s refused = %v", provider, err)
		}
	}
	signinServer(t, map[string]func() (int, any){"/token": answer(200, map[string]string{"refresh_token": "r"})})
	if _, err := completePaste(t, "google-gemini-cli", "code"); err == nil || !strings.Contains(err.Error(), "without an access_token") {
		t.Errorf("no access token = %v", err)
	}
}

func geminiToken() func() (int, any) {
	return answer(200, map[string]any{"access_token": "a", "refresh_token": "r", "expires_in": 3600})
}

// Finding the Code Assist project: a current tier with one, a free tier to
// onboard, a paid tier that cannot be.
func TestGeminiProject(t *testing.T) {
	tier := func(id string) []map[string]any { return []map[string]any{{"id": id, "isDefault": true}} }
	for name, c := range map[string]struct {
		load, onboard func() (int, any)
		project, want string
	}{
		"current tier, no project": {load: answer(200, map[string]any{"currentTier": map[string]string{"id": "free-tier"}}), want: "needs a Cloud project"},
		"load refused":             {load: answer(500, map[string]any{}), want: "Gemini Code Assist:"},
		"no default tier":          {load: answer(200, map[string]any{"allowedTiers": []map[string]any{{"id": "free-tier"}}}), want: "legacy-tier"},
		"paid default":             {load: answer(200, map[string]any{"allowedTiers": tier("standard-tier")}), want: "standard-tier"},
		"onboarding refused":       {load: answer(200, map[string]any{"allowedTiers": tier("free-tier")}), onboard: answer(500, map[string]any{}), want: "onboarding"},
		"onboarded at once": {load: answer(200, map[string]any{"allowedTiers": tier("free-tier")}),
			onboard: answer(200, map[string]any{"done": true, "response": map[string]any{"cloudaicompanionProject": map[string]string{"id": "fresh"}}}), project: "fresh"},
		"onboarded with no project": {load: answer(200, map[string]any{"allowedTiers": tier("free-tier")}),
			onboard: answer(200, map[string]any{"done": true}), want: "did not provision"},
	} {
		t.Run(name, func(t *testing.T) {
			routes := map[string]func() (int, any){
				"/token":                     geminiToken(),
				"/userinfo":                  answer(500, map[string]string{}), // best effort: no email, no failure
				"/v1internal:loadCodeAssist": c.load,
			}
			if c.onboard != nil {
				routes["/v1internal:onboardUser"] = c.onboard
			}
			signinServer(t, routes)
			got, err := completePaste(t, "google-gemini-cli", "code")
			if c.want != "" {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Errorf("err = %v, want %q", err, c.want)
				}
				return
			}
			if err != nil || got.Get(providers.ExtraProjectID) != c.project {
				t.Errorf("project = %q, %v", got.Get(providers.ExtraProjectID), err)
			}
			if got.Get(providers.ExtraEmail) != "" {
				t.Errorf("email from a failed lookup = %q", got.Get(providers.ExtraEmail))
			}
		})
	}
}

// A long-running onboarding polls until the caller gives up.
func TestGeminiOnboardingStopsWithItsContext(t *testing.T) {
	signinServer(t, map[string]func() (int, any){
		"/v1internal:loadCodeAssist": answer(200, map[string]any{"allowedTiers": []map[string]any{{"id": "free-tier", "isDefault": true}}}),
		"/v1internal:onboardUser":    answer(200, map[string]any{"name": "operations/1"}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := NewSignIns().geminiProject(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestSignInRequestsWithABadURL(t *testing.T) {
	s := NewSignIns()
	ctx := context.Background()
	var out any
	if err := s.postJSON(ctx, "http://bad host/", map[string]string{"a": "b"}, nil, &out); err == nil {
		t.Error("postJSON accepted a bad URL")
	}
	if err := s.postForm(ctx, "http://bad host/", nil, nil, &out); err == nil {
		t.Error("postForm accepted a bad URL")
	}
	if err := s.getJSON(ctx, "http://bad host/", nil, &out); err == nil {
		t.Error("getJSON accepted a bad URL")
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	if err := s.getJSON(ctx, u, nil, &out); err == nil {
		t.Error("getJSON of a dead server reported nothing")
	}
}
