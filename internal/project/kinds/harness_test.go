package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/envport"
)

// roundTrip answers http.DefaultClient's calls, which is what the probes and
// the ChatGPT model list go out on: their URLs are the vendors' own.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubVendors serves every outgoing DefaultClient request from answer, so no
// test here can reach a real vendor.
func stubVendors(t *testing.T, answer func(r *http.Request) (int, string)) {
	t.Helper()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		status, body := answer(r)
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

// vendorsDown fails every DefaultClient request at the transport.
func vendorsDown(t *testing.T, err error) {
	t.Helper()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, err })
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

func stubOmpCatalog(t *testing.T, status int, doc string) *atomic.Int32 {
	t.Helper()
	var reads atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(doc))
	}))
	old := ompCatalogURL
	ompCatalogURL = ts.URL + "/v%s/models.json"
	resetOmpCatalog()
	t.Cleanup(func() {
		ts.Close()
		ompCatalogURL = old
		resetOmpCatalog()
	})
	return &reads
}

func resetOmpCatalog() {
	ompCatalogs.Lock()
	ompCatalogs.byVersion = nil
	ompCatalogs.Unlock()
}

// breakCatalog leaves the models.dev catalog answering with a status.
func breakCatalog(t *testing.T, status int, body string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	old := modelsCatalogURL
	modelsCatalogURL = ts.URL
	reset := func() {
		modelsCache.Lock()
		modelsCache.catalog = nil
		modelsCache.Unlock()
	}
	reset()
	t.Cleanup(func() {
		ts.Close()
		modelsCatalogURL = old
		reset()
	})
}

func newEnv(t *testing.T) (*fake.EnvProvisioner, envport.EnvID) {
	t.Helper()
	envp := fake.NewEnvProvisioner()
	id, err := envp.Provision(context.Background(), envport.EnvSpec{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	return envp, id
}

func noRegister(string) {}

// --- codex ------------------------------------------------------------------

func TestCodexNeedsACredential(t *testing.T) {
	h, _ := Codex{}.New(Config{}, nil)
	if err := h.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "no model provider credentials") {
		t.Errorf("none = %v", err)
	}
	for _, c := range []providers.Credential{apiKey("openai", "k"), login("openai-chatgpt", "a", nil)} {
		h, _ := Codex{}.New(withCreds(c), nil)
		if err := h.Test(context.Background()); err != nil {
			t.Errorf("%s: %v", c.Provider, err)
		}
	}
}

func TestCodexCredentialRouting(t *testing.T) {
	envp, id := newEnv(t)
	ctx := context.Background()
	key, _ := Codex{}.New(withCreds(apiKey("openai", "sk-1")), envp)
	if got := key.EnvVars(); len(got) != 1 || got["OPENAI_API_KEY"] != "sk-1" {
		t.Errorf("env = %v", got)
	}
	if got := key.(SecretHolder).SecretValues(); got != nil {
		t.Errorf("a key is the env var's to track: %v", got)
	}
	if err := key.SeedAuth(ctx, id, func(string) { t.Error("a key writes no auth.json") }); err != nil {
		t.Error(err)
	}
	if key.Binary() != "codex-acp" {
		t.Errorf("binary = %q", key.Binary())
	}
	if got := key.EgressDomains(); !slices.Contains(got, "api.openai.com") || !slices.Contains(got, "chatgpt.com") {
		t.Errorf("egress = %v", got)
	}
	if got := key.(AuthLocator).AuthFiles(ctx, id); len(got) != 1 || !strings.HasSuffix(got[0], "/auth.json") {
		t.Errorf("auth files = %v", got)
	}

	withLogin, _ := Codex{}.New(withCreds(login("openai-chatgpt", "acc", map[string]string{"id_token": "idt"})), envp)
	if got := withLogin.EnvVars(); got != nil {
		t.Errorf("a login rides no env var: %v", got)
	}
	got := withLogin.(SecretHolder).SecretValues()
	for _, want := range []string{"acc", "idt"} {
		if !slices.Contains(got, want) {
			t.Errorf("secrets %v lack %s", got, want)
		}
	}
}

func TestCodexSeedsTheFreshLogin(t *testing.T) {
	envp, id := newEnv(t)
	ctx := context.Background()
	stale := login("openai-chatgpt", "stale", map[string]string{"id_token": "idt"})
	current := login("openai-chatgpt", "current", map[string]string{"id_token": "idt"})
	cfg := Config{Credentials: Credentials{Snapshot: []providers.Credential{stale},
		Fresh: func(context.Context) ([]providers.Credential, error) {
			return []providers.Credential{apiKey("openai", "sk"), current}, nil
		}}}
	h, _ := Codex{}.New(cfg, envp)
	var registered []string
	if err := h.SeedAuth(ctx, id, func(s string) { registered = append(registered, s) }); err != nil {
		t.Fatal(err)
	}
	data, err := envp.ReadFile(ctx, id, h.(AuthLocator).AuthFiles(ctx, id)[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "current") || strings.Contains(string(data), "stale") {
		t.Errorf("auth.json = %s", data)
	}
	if !slices.Contains(registered, "current") {
		t.Errorf("registered = %v", registered)
	}

	boom := errors.New("keeper down")
	broken, _ := Codex{}.New(Config{Credentials: Credentials{Snapshot: []providers.Credential{stale},
		Fresh: func(context.Context) ([]providers.Credential, error) { return nil, boom }}}, envp)
	if err := broken.SeedAuth(ctx, id, noRegister); !errors.Is(err, boom) {
		t.Errorf("keeper failure = %v", err)
	}

	// The login gone since the snapshot: nothing to seed, and no failure.
	gone, _ := Codex{}.New(Config{Credentials: Credentials{Snapshot: []providers.Credential{stale},
		Fresh: func(context.Context) ([]providers.Credential, error) { return nil, nil }}}, envp)
	if err := gone.SeedAuth(ctx, id, func(string) { t.Error("nothing was seeded") }); err != nil {
		t.Errorf("gone = %v", err)
	}
}

func TestCodexPrepareAndSkills(t *testing.T) {
	envp, id := newEnv(t)
	ctx := context.Background()
	h, _ := Codex{}.New(withCreds(apiKey("openai", "k")), envp)
	if err := h.Prepare(ctx, id, []*agentspec.Spec{{Name: "builder", Prompt: "Build."}}); err != nil {
		t.Fatal(err)
	}
	if err := h.InstallSkills(ctx, id, []string{"pdf"}); err != nil {
		t.Fatal(err)
	}
	calls := envp.Execs()
	if len(calls) == 0 || !strings.Contains(calls[len(calls)-1].Cmd.Script, "/.agents/skills/pdf") {
		t.Errorf("execs = %+v", calls)
	}
}

func TestChatgptModelsFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		refused bool
		want    string
	}{
		{"unauthorized", 401, ``, true, "sign in again"},
		{"forbidden", 403, ``, true, "sign in again"},
		{"down", 503, ``, false, "ChatGPT models: 503"},
		{"malformed", 200, `{"models":`, false, "ChatGPT models:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubVendors(t, func(*http.Request) (int, string) { return tc.status, tc.body })
			_, err := chatgptModels(ctx, "acc-secret", "")
			var refused *loginRefused
			if err == nil || errors.As(err, &refused) != tc.refused || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
			if strings.Contains(err.Error(), "acc-secret") {
				t.Errorf("the token is in the error: %v", err)
			}
		})
	}

	var account string
	stubVendors(t, func(r *http.Request) (int, string) {
		account = r.Header.Get("chatgpt-account-id")
		return 200, `{"models":[{"slug":"b","visibility":"list"},{"slug":"a"},{"slug":"","visibility":"list"},{"slug":"gone","visibility":"none"}]}`
	})
	got, err := chatgptModels(ctx, "acc", "")
	if err != nil || !slices.Equal(got, []string{"a", "b"}) || account != "" {
		t.Errorf("models = %v, %v, account %q", got, err, account)
	}

	old := chatgptModelsURL
	chatgptModelsURL = "http://bad host/"
	defer func() { chatgptModelsURL = old }()
	if _, err := chatgptModels(ctx, "acc", ""); err == nil {
		t.Error("an unparsable URL is an error")
	}
}

// A login that cannot list falls back to the catalog; so does a key.
func TestCodexModelsFromTheCatalog(t *testing.T) {
	ctx := context.Background()
	stubCatalog(t, `{"openai":{"name":"OpenAI","models":{"zeta":{"name":""},"alpha":{"name":"Alpha"}}}}`)
	stubVendors(t, func(*http.Request) (int, string) { return 503, `` })
	for _, c := range []providers.Credential{apiKey("openai", "k"), login("openai-chatgpt", "a", nil)} {
		h, _ := Codex{}.New(withCreds(c), nil)
		got, err := h.Models(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := []ModelInfo{{ID: "openai/alpha", Name: "OpenAI · Alpha"}, {ID: "openai/zeta", Name: "OpenAI · zeta"}}
		if !slices.Equal(got, want) {
			t.Errorf("%s: %+v", c.Provider, got)
		}
	}
}

func TestCodexModelsCatalogFailures(t *testing.T) {
	ctx := context.Background()
	h, _ := Codex{}.New(withCreds(apiKey("openai", "k")), nil)
	stubCatalog(t, `{"other":{"name":"Other"}}`)
	if _, err := h.Models(ctx); err == nil || !strings.Contains(err.Error(), "no openai provider") {
		t.Errorf("no openai = %v", err)
	}
	breakCatalog(t, 500, ``)
	if _, err := h.Models(ctx); err == nil || !strings.Contains(err.Error(), "model catalog: 500") {
		t.Errorf("catalog down = %v", err)
	}
}

func TestCodexCheckCredentials(t *testing.T) {
	ctx := context.Background()
	ok := func(*http.Request) (int, string) { return 200, `{"models":[]}` }
	for _, tc := range []struct {
		name       string
		creds      []providers.Credential
		problems   map[string]string
		answer     func(*http.Request) (int, string)
		rejected   string
		unverified string
	}{
		{name: "key accepted", creds: []providers.Credential{apiKey("openai", "sk")}, answer: ok},
		{name: "key rejected", creds: []providers.Credential{apiKey("openai", "sk")},
			answer:   func(*http.Request) (int, string) { return 401, `{"error":{"message":"Incorrect API key"}}` },
			rejected: "codex OpenAI key: credential rejected (401 Incorrect API key)"},
		{name: "key unprobeable", creds: []providers.Credential{apiKey("openai", "sk")},
			answer:     func(*http.Request) (int, string) { return 503, `` },
			unverified: "codex OpenAI key: inconclusive (503)"},
		{name: "key rejected, login stands", creds: []providers.Credential{apiKey("openai", "sk"), login("openai-chatgpt", "a", nil)},
			answer: func(r *http.Request) (int, string) {
				if strings.Contains(r.URL.Host, "openai") {
					return 401, ``
				}
				return 200, `{"models":[]}`
			},
			unverified: "the ChatGPT login may still work"},
		{name: "login refused, key stands", creds: []providers.Credential{apiKey("openai", "sk"), login("openai-chatgpt", "a", nil)},
			answer: func(r *http.Request) (int, string) {
				if strings.Contains(r.URL.Host, "openai") {
					return 200, ``
				}
				return 401, ``
			},
			unverified: "codex ChatGPT login: ChatGPT refused"},
		{name: "login unprobeable", creds: []providers.Credential{login("openai-chatgpt", "a", nil)},
			answer: func(*http.Request) (int, string) { return 503, `` }, unverified: "ChatGPT models: 503"},
		{name: "a disabled credential is a verdict", creds: []providers.Credential{apiKey("openai", "sk")},
			problems: map[string]string{"c1": "credential c1 is disabled"}, answer: ok, rejected: "codex: credential c1 is disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubVendors(t, tc.answer)
			h, _ := Codex{}.New(Config{Credentials: Credentials{Snapshot: tc.creds, Problems: tc.problems}}, nil)
			res := h.(AuthChecker).CheckCredentials(ctx)
			checkResult(t, res, tc.rejected, tc.unverified)
		})
	}

	boom := errors.New("network is down")
	vendorsDown(t, boom)
	h, _ := Codex{}.New(withCreds(apiKey("openai", "sk")), nil)
	if res := h.(AuthChecker).CheckCredentials(ctx); len(res.Unverified) != 1 || !strings.Contains(res.Unverified[0], "network is down") {
		t.Errorf("transport = %+v", res)
	}
}

func checkResult(t *testing.T, res AuthResult, rejected, unverified string) {
	t.Helper()
	one := func(list []string, want string) bool {
		if want == "" {
			return len(list) == 0
		}
		return len(list) == 1 && strings.Contains(list[0], want)
	}
	if !one(res.Rejected, rejected) || !one(res.Unverified, unverified) {
		t.Errorf("result = %+v; want rejected %q, unverified %q", res, rejected, unverified)
	}
}

func TestCodexCheckAuth(t *testing.T) {
	ctx := context.Background()
	calls := 0
	stubVendors(t, func(*http.Request) (int, string) { calls++; return 200, `{"models":[{"slug":"offered"}]}` })

	// A flow with no model on this harness probes nothing.
	h, _ := Codex{}.New(withCreds(login("openai-chatgpt", "a", nil)), nil)
	if res := h.(AuthChecker).CheckAuth(ctx, nil); len(res.Rejected)+len(res.Unverified) != 0 || calls != 0 {
		t.Errorf("no models = %+v, %d calls", res, calls)
	}
	// A key reaches the whole catalog: the plan's list does not limit it.
	both, _ := Codex{}.New(withCreds(apiKey("openai", "k"), login("openai-chatgpt", "a", nil)), nil)
	if res := both.(AuthChecker).CheckAuth(ctx, []string{"openai/anything"}); len(res.Rejected) != 0 {
		t.Errorf("key and login = %+v", res)
	}
	// A model with no provider part is the model check's to refuse.
	if res := h.(AuthChecker).CheckAuth(ctx, []string{"bare"}); len(res.Rejected) != 0 {
		t.Errorf("bare model = %+v", res)
	}
	// The plan could not be read: CheckCredentials said why, once.
	stubVendors(t, func(*http.Request) (int, string) { return 503, `` })
	res := h.(AuthChecker).CheckAuth(ctx, []string{"openai/offered"})
	checkResult(t, res, "", "ChatGPT models: 503")
}

// --- standalone credential check ---------------------------------------------

func TestCheckCredential(t *testing.T) {
	ctx := context.Background()
	if _, err := CheckCredential(ctx, providers.Credential{Provider: "nobody"}); err == nil || !strings.Contains(err.Error(), "not one apothecary manages") {
		t.Errorf("unknown provider = %v", err)
	}

	stubCatalog(t, `{"anthropic":{"name":"Anthropic","models":{}},
	  "opencode":{"name":"Zen","api":"https://opencode.example/zen/v1","models":{"b":{},"a":{}}},
	  "groq":{"name":"Groq"}}`)

	for _, tc := range []struct {
		name       string
		cred       providers.Credential
		answer     func(*http.Request) (int, string)
		unverified string
		refused    string
	}{
		{name: "login with no list to ask", cred: login("github-copilot", "a", nil),
			answer: func(*http.Request) (int, string) { t.Error("a login is the keeper's to prove"); return 500, `` }},
		{name: "login accepted", cred: login("openai-chatgpt", "a", nil), answer: func(*http.Request) (int, string) { return 200, `{"models":[]}` }},
		{name: "login refused", cred: login("openai-chatgpt", "a", nil), answer: func(*http.Request) (int, string) { return 401, `` }, refused: "sign in again"},
		{name: "login unreadable", cred: login("openai-chatgpt", "a", nil), answer: func(*http.Request) (int, string) { return 503, `` }, unverified: "ChatGPT models: 503"},
		{name: "key accepted", cred: apiKey("anthropic", "sk"), answer: func(*http.Request) (int, string) { return 200, `{}` }},
		{name: "key refused", cred: apiKey("anthropic", "sk"), answer: func(*http.Request) (int, string) { return 401, `` }, refused: "Anthropic: credential rejected (401)"},
		{name: "key unreadable", cred: apiKey("anthropic", "sk"), answer: func(*http.Request) (int, string) { return 503, `` }, unverified: "inconclusive (503)"},
		{name: "no probe", cred: apiKey("groq", "sk"), answer: func(*http.Request) (int, string) { return 200, `` }, unverified: "no zero-token probe"},
		// The gateway refused the model it was handed before looking at the
		// key: that model, the first of the catalog's, is named.
		{name: "borrowed model refused", cred: apiKey("opencode-zen", "sk"), answer: func(r *http.Request) (int, string) {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"model":"a"`) {
				t.Errorf("probe body = %s", body)
			}
			return 400, `{"error":{"message":"Model is unavailable"}}`
		}, unverified: "probe model a model refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubVendors(t, tc.answer)
			got, err := CheckCredential(ctx, tc.cred)
			if got != "" && tc.unverified == "" || !strings.Contains(got, tc.unverified) {
				t.Errorf("unverified = %q", got)
			}
			if tc.refused == "" && err != nil || tc.refused != "" && (err == nil || !strings.Contains(err.Error(), tc.refused)) {
				t.Errorf("err = %v", err)
			}
		})
	}

	breakCatalog(t, 500, ``)
	if got, err := CheckCredential(ctx, apiKey("anthropic", "sk")); err != nil || !strings.Contains(got, "model catalog: 500") {
		t.Errorf("catalog down = %q, %v", got, err)
	}
}

// --- probes ------------------------------------------------------------------

func TestProviderAuthProbe(t *testing.T) {
	withAPI := catalogProvider{API: "https://gw.example/v1/"}
	for _, tc := range []struct {
		provider string
		p        catalogProvider
		url      string
		kind     probeKind
		header   string
	}{
		{"anthropic", catalogProvider{}, "https://api.anthropic.com/v1/models?limit=1", probeMetadata, "x-api-key"},
		{"openai", catalogProvider{}, "https://api.openai.com/v1/models", probeMetadata, "Authorization"},
		{"openrouter", catalogProvider{}, "https://openrouter.ai/api/v1/key", probeMetadata, "Authorization"},
		{"opencode", withAPI, "https://gw.example/v1/chat/completions", probeEmptyChat, "Authorization"},
		{"opencode-go", withAPI, "https://gw.example/v1/chat/completions", probeEmptyChat, "x-opencode-session"},
		{"groq", withAPI, "https://gw.example/v1/models", probeMetadata, "Authorization"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			pr, ok := providerAuthProbe(tc.provider, tc.p, "sk", "m1")
			if !ok || pr.url != tc.url || pr.kind != tc.kind || pr.header[tc.header] == "" {
				t.Errorf("probe = %+v, %v", pr, ok)
			}
			if tc.kind == probeEmptyChat {
				var body map[string]any
				if err := json.Unmarshal([]byte(pr.body), &body); err != nil {
					t.Fatal(err)
				}
				if body["model"] != "m1" || body["max_tokens"] != float64(1) || len(body["messages"].([]any)) != 0 {
					t.Errorf("a probe must not run inference: %v", body)
				}
			}
		})
	}
	for _, provider := range []string{"opencode", "opencode-go", "groq"} {
		if _, ok := providerAuthProbe(provider, catalogProvider{}, "sk", "m"); ok {
			t.Errorf("%s: no base URL, no probe", provider)
		}
	}
}

func TestAuthProbeRun(t *testing.T) {
	ctx := context.Background()
	var got *http.Request
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key sk-secret"}}`))
	}))
	defer ts.Close()
	pr := authProbe{kind: probeEmptyChat, method: http.MethodPost, url: ts.URL, header: map[string]string{"X-Key": "sk-secret"}, body: `{"a":1}`}
	rejected, reason, err := pr.run(ctx, "sk-secret")
	if err != nil || !rejected || reason != "credential rejected (401 bad key ***)" {
		t.Errorf("run = %v, %q, %v", rejected, reason, err)
	}
	if got.Header.Get("X-Key") != "sk-secret" || gotBody != `{"a":1}` {
		t.Errorf("request = %v, %q", got.Header, gotBody)
	}

	bad := authProbe{method: "BAD METHOD", url: ts.URL}
	if _, _, err := bad.run(ctx, "sk"); err == nil {
		t.Error("an invalid request is an error")
	}

	// A key that sits in the URL stays out of the transport's error.
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL + "/models?key=sk-secret"
	dead.Close()
	_, _, err = authProbe{method: http.MethodGet, url: u}.run(ctx, "sk-secret")
	if err == nil || strings.Contains(err.Error(), "sk-secret") || !strings.Contains(err.Error(), "***") {
		t.Errorf("transport err = %v", err)
	}
}

// A long body echoing the key must not leave any of it behind: what is clipped
// first cannot be scrubbed after.
func TestAuthProbeScrubsAKeyAcrossTheClip(t *testing.T) {
	key := "sk-secretsecretsecret"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(strings.Repeat("x", 190) + key))
	}))
	defer ts.Close()
	_, reason, err := authProbe{method: http.MethodGet, url: ts.URL}.run(context.Background(), key)
	if err != nil || strings.Contains(reason, key[:8]) {
		t.Errorf("reason = %q, %v", reason, err)
	}
}

func TestJudgeExtraVerdicts(t *testing.T) {
	for _, tc := range []struct {
		kind     probeKind
		status   int
		msg      string
		rejected bool
		reason   string
		isErr    bool
	}{
		{probeMetadata, 200, "", false, "", false},
		{probeMetadata, 204, "", false, "", false},
		{probeMetadata, 402, "out of credit", true, "no credit left (402 out of credit)", false},
		{probeMetadata, 403, "", true, "credential rejected (403)", false},
		{probeMetadata, 400, "", false, "", true},
		{probeMetadata, 429, "", false, "", true},
		{probeEmptyChat, 422, "bad body", false, "", false},
		{probeEmptyChat, 400, "MODEL is Unavailable", true, "model refused (400 MODEL is Unavailable)", false},
		{probeEmptyChat, 500, "", false, "", true},
	} {
		rejected, reason, err := judge(tc.kind, tc.status, tc.msg)
		if rejected != tc.rejected || reason != tc.reason || (err != nil) != tc.isErr {
			t.Errorf("judge(%d, %q) = %v, %q, %v", tc.status, tc.msg, rejected, reason, err)
		}
	}
}

func TestErrorMessage(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"error":{"message":"nope"}}`, "nope"},
		{`{"error":"nope"}`, "nope"},
		{`{"error":{"code":7}}`, `{"error":{"code":7}}`},
		{`{"error":""}`, `{"error":""}`},
		{`{"other":1}`, `{"other":1}`},
		{`  plain text  `, "plain text"},
		{``, ""},
	} {
		if got := errorMessage([]byte(tc.raw)); got != tc.want {
			t.Errorf("errorMessage(%q) = %q", tc.raw, got)
		}
	}
	long := errorMessage([]byte(strings.Repeat("x", 500)))
	if !strings.HasSuffix(long, "…") || len(long) != 200+len("…") {
		t.Errorf("clipped to %d", len(long))
	}
}

func TestScrubKey(t *testing.T) {
	plain := errors.New("no key here")
	if got := scrubKey(plain, "sk"); got != plain {
		t.Errorf("untouched = %v", got)
	}
	if got := scrubKey(plain, ""); got != plain {
		t.Errorf("no key = %v", got)
	}
	if got := scrubKey(errors.New("dial sk-1 and sk-1"), "sk-1"); got.Error() != "dial *** and ***" {
		t.Errorf("scrubbed = %v", got)
	}
}

// --- opencode ----------------------------------------------------------------

func TestOpencodeNeedsACredential(t *testing.T) {
	h, _ := Opencode{}.New(Config{}, nil)
	if err := h.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "no model provider credentials") {
		t.Errorf("test = %v", err)
	}
	if _, err := h.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "pick some") {
		t.Errorf("models = %v", err)
	}
	if h.Binary() != "opencode" || h.EnvVars() != nil {
		t.Errorf("binary %q env %v", h.Binary(), h.EnvVars())
	}
}

func TestUniqueSorted(t *testing.T) {
	if got := uniqueSorted([]string{"b", "", "a", "b"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("hosts = %v", got)
	}
}

func TestOpencodeEgressIsWhatItsCredentialsReach(t *testing.T) {
	h, _ := Opencode{}.New(withCreds(apiKey("anthropic", "k")), nil)
	got := h.EgressDomains()
	for _, want := range []string{"models.dev", "registry.npmjs.org", "api.anthropic.com"} {
		if !slices.Contains(got, want) {
			t.Errorf("egress = %v", got)
		}
	}
	if slices.Contains(got, "api.openai.com") {
		t.Errorf("no OpenAI credential, no OpenAI egress: %v", got)
	}
}

func TestOpencodeSeedAuth(t *testing.T) {
	envp, id := newEnv(t)
	ctx := context.Background()
	h, _ := Opencode{}.New(withCreds(apiKey("anthropic", "sk-a")), envp)
	var registered []string
	if err := h.SeedAuth(ctx, id, func(s string) { registered = append(registered, s) }); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(registered, "sk-a") {
		t.Errorf("registered = %v", registered)
	}
	if got := h.(SecretHolder).SecretValues(); !slices.Contains(got, "sk-a") {
		t.Errorf("secrets = %v", got)
	}
	data, err := envp.ReadFile(ctx, id, h.(AuthLocator).AuthFiles(ctx, id)[0])
	if err != nil || !strings.Contains(string(data), `"key":"sk-a"`) {
		t.Errorf("auth.json = %s, %v", data, err)
	}
	boom := errors.New("keeper down")
	broken, _ := Opencode{}.New(Config{Credentials: Credentials{Fresh: func(context.Context) ([]providers.Credential, error) { return nil, boom }}}, envp)
	if err := broken.SeedAuth(ctx, id, noRegister); !errors.Is(err, boom) {
		t.Errorf("keeper failure = %v", err)
	}
	if err := h.Prepare(ctx, id, nil); err != nil {
		t.Errorf("prepare = %v", err)
	}
	if err := h.InstallSkills(ctx, id, []string{"pdf"}); err != nil {
		t.Errorf("skills = %v", err)
	}
}

// A login's refresh token stays with the keeper; Copilot is the one that
// reuses its access token there, and its extras travel.
func TestRenderOpencodeAuth(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	chatgpt := login("openai-chatgpt", "acc", map[string]string{"account_id": "acct"})
	chatgpt.Expires = at
	copilot := login("github-copilot", "gho", map[string]string{"enterprise_url": "https://ghe.example"})
	doc, secrets, err := renderOpencodeAuth(bind(providers.Opencode, []providers.Credential{chatgpt, copilot}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	o := got["openai"]
	if o["type"] != "oauth" || o["access"] != "acc" || o["refresh"] != "" || o["expires"] != float64(1_700_000_000_000) || o["accountId"] != "acct" {
		t.Errorf("chatgpt = %v", o)
	}
	c := got["github-copilot"]
	if c["refresh"] != "gho" || c["expires"] != float64(0) || c["enterpriseUrl"] != "https://ghe.example" {
		t.Errorf("copilot = %v", c)
	}
	if strings.Contains(string(doc), "refresh-acc") {
		t.Errorf("a refresh token reached the document: %s", doc)
	}
	if !slices.Contains(secrets, "acc") || !slices.Contains(secrets, "gho") {
		t.Errorf("secrets = %v", secrets)
	}
}

func TestEntryOfGivesALoginNoKey(t *testing.T) {
	if got := entryOf(bound{Credential: apiKey("openai", "k")}); got != (authEntry{Type: "api", Key: "k"}) {
		t.Errorf("key = %+v", got)
	}
	if got := entryOf(bound{Credential: login("openai-chatgpt", "a", nil)}); got != (authEntry{Type: "oauth"}) {
		t.Errorf("login = %+v", got)
	}
}

func TestFetchCatalogFailuresAndCache(t *testing.T) {
	ctx := context.Background()
	breakCatalog(t, 200, `{"broken`)
	if _, err := fetchCatalog(ctx); err == nil || !strings.Contains(err.Error(), "model catalog:") {
		t.Errorf("malformed = %v", err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	modelsCatalogURL = dead.URL
	dead.Close()
	if _, err := fetchCatalog(ctx); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("dead = %v", err)
	}
	modelsCatalogURL = "http://bad host/"
	if _, err := fetchCatalog(ctx); err == nil {
		t.Error("an unparsable URL is an error")
	}

	var reads atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = w.Write([]byte(`{"p":{"name":"P"}}`))
	}))
	defer ts.Close()
	modelsCatalogURL = ts.URL
	for range 2 {
		if c, err := fetchCatalog(ctx); err != nil || c["p"].Name != "P" {
			t.Fatalf("catalog = %v, %v", c, err)
		}
	}
	if reads.Load() != 1 {
		t.Errorf("read %d times within the hour", reads.Load())
	}
	// Past the hour it is fetched anew.
	modelsCache.Lock()
	modelsCache.fetchedAt = time.Now().Add(-2 * time.Hour)
	modelsCache.Unlock()
	if _, err := fetchCatalog(ctx); err != nil || reads.Load() != 2 {
		t.Errorf("stale catalog: %d reads, %v", reads.Load(), err)
	}
}

func TestOpencodeModelsSkipsWhatTheCatalogLacks(t *testing.T) {
	ctx := context.Background()
	stubCatalog(t, `{"anthropic":{"name":"Anthropic","models":{"m":{"name":""}}}}`)
	h, _ := Opencode{}.New(withCreds(apiKey("anthropic", "k"), apiKey("groq", "k")), nil)
	got, err := h.Models(ctx)
	if err != nil || !slices.Equal(got, []ModelInfo{{ID: "anthropic/m", Name: "Anthropic · m"}}) {
		t.Errorf("models = %+v, %v", got, err)
	}
	breakCatalog(t, 500, ``)
	if _, err := h.Models(ctx); err == nil {
		t.Error("a catalog failure is an error")
	}
}

func TestLiveModelsFailures(t *testing.T) {
	ctx := context.Background()
	key := authEntry{Type: "api", Key: "sk"}
	if _, err := liveModels(ctx, catalogProvider{}, key); err == nil {
		t.Error("no API, no live list")
	}
	if _, err := liveModels(ctx, catalogProvider{API: "https://x"}, authEntry{Type: "oauth"}); err == nil {
		t.Error("no key, no live list")
	}
	if _, err := liveModels(ctx, catalogProvider{API: "http://bad host"}, key); err == nil {
		t.Error("an unparsable API is an error")
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	u := dead.URL
	dead.Close()
	if _, err := liveModels(ctx, catalogProvider{API: u}, key); err == nil {
		t.Error("a dead provider is an error")
	}
	for _, tc := range []struct {
		status int
		body   string
		want   []string
		isErr  bool
	}{
		{500, ``, nil, true},
		{200, `{"data":`, nil, true},
		{200, `{"data":[{"id":"a"},{"id":""},{"id":"b"}]}`, []string{"a", "b"}, false},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		got, err := liveModels(ctx, catalogProvider{API: ts.URL + "/"}, key)
		ts.Close()
		if (err != nil) != tc.isErr || !slices.Equal(got, tc.want) {
			t.Errorf("%d %s: %v, %v", tc.status, tc.body, got, err)
		}
	}
}

func TestOpencodeCheckAuthVerdicts(t *testing.T) {
	ctx := context.Background()
	stubCatalog(t, `{
	  "anthropic":{"name":"Anthropic","models":{"m":{}}},
	  "opencode":{"name":"Zen","api":"https://zen.example/v1","models":{"z":{}}},
	  "groq":{"name":"Groq"}}`)
	for _, tc := range []struct {
		name       string
		creds      []providers.Credential
		models     []string
		problems   map[string]string
		answer     func(*http.Request) (int, string)
		rejected   string
		unverified string
	}{
		{name: "accepted", creds: []providers.Credential{apiKey("anthropic", "k")}, models: []string{"anthropic/m"},
			answer: func(*http.Request) (int, string) { return 200, `{}` }},
		{name: "rejected", creds: []providers.Credential{apiKey("anthropic", "k")}, models: []string{"anthropic/m"},
			answer:   func(*http.Request) (int, string) { return 401, `` },
			rejected: "opencode provider anthropic: credential rejected (401) — fix the Anthropic API key"},
		{name: "inconclusive", creds: []providers.Credential{apiKey("anthropic", "k")}, models: []string{"anthropic/m"},
			answer: func(*http.Request) (int, string) { return 500, `` }, unverified: "opencode provider anthropic: inconclusive"},
		{name: "no probe", creds: []providers.Credential{apiKey("groq", "k")}, models: []string{"groq/m"},
			answer: func(*http.Request) (int, string) { return 200, `` }, unverified: "no zero-token probe"},
		{name: "a provider the harness lacks is not probed", creds: []providers.Credential{apiKey("anthropic", "k")}, models: []string{"openai/x", "bare"},
			answer: func(*http.Request) (int, string) { t.Error("nothing to probe"); return 200, `` }},
		{name: "a login is the keeper's", creds: []providers.Credential{login("openai-chatgpt", "a", nil)}, models: []string{"openai/x"},
			answer: func(*http.Request) (int, string) { t.Error("no probe for a login"); return 200, `` }},
		{name: "one probe per provider", creds: []providers.Credential{apiKey("anthropic", "k")}, models: []string{"anthropic/m", "anthropic/n"},
			answer: func(*http.Request) (int, string) { return 401, `` }, rejected: "opencode provider anthropic"},
		{name: "gateway refuses the chosen model", creds: []providers.Credential{apiKey("opencode-zen", "k")}, models: []string{"opencode/retired"},
			answer:   func(*http.Request) (int, string) { return 400, `{"error":{"message":"Model is unavailable"}}` },
			rejected: "opencode model opencode/retired: model refused (400 Model is unavailable) — pick another model in the flow"},
		{name: "problems are verdicts", creds: nil, models: nil, problems: map[string]string{"a": "credential a was deleted"},
			answer: func(*http.Request) (int, string) { return 200, `` }, rejected: "opencode: credential a was deleted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubVendors(t, tc.answer)
			h, _ := Opencode{}.New(Config{Credentials: Credentials{Snapshot: tc.creds, Problems: tc.problems}}, nil)
			checkResult(t, h.(AuthChecker).CheckAuth(ctx, tc.models), tc.rejected, tc.unverified)
		})
	}

	// A key and a login for one native id: the key is what is probed.
	var probed atomic.Int32
	stubVendors(t, func(*http.Request) (int, string) { probed.Add(1); return 200, `{}` })
	h, _ := Opencode{}.New(withCreds(login("openai-chatgpt", "a", nil), apiKey("openai", "k")), nil)
	h.(AuthChecker).CheckAuth(ctx, []string{"openai/x"})
	if probed.Load() != 1 {
		t.Errorf("probes = %d", probed.Load())
	}

	breakCatalog(t, 500, ``)
	res := h.(AuthChecker).CheckAuth(ctx, []string{"anthropic/m"})
	checkResult(t, res, "", "opencode: model catalog: 500")
}

// --- omp ---------------------------------------------------------------------

func TestOmpNeedsACredential(t *testing.T) {
	h, _ := Omp{}.New(Config{}, nil)
	if err := h.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "no model provider credentials") {
		t.Errorf("test = %v", err)
	}
	if got := h.EnvVars(); got != nil {
		t.Errorf("env = %v", got)
	}
	if got := h.EgressDomains(); !slices.Equal(got, ompInfra) {
		t.Errorf("egress = %v", got)
	}
}

func TestOmpSecretsAndPrepare(t *testing.T) {
	envp, id := newEnv(t)
	ctx := context.Background()
	h, _ := Omp{}.New(withCreds(login("anthropic", "acc", nil)), envp)
	if got := h.EnvVars(); got != nil {
		t.Errorf("a login rides no env var: %v", got)
	}
	if got := h.(SecretHolder).SecretValues(); !slices.Contains(got, "acc") {
		t.Errorf("secrets = %v", got)
	}
	if err := h.Prepare(ctx, id, nil); err != nil {
		t.Errorf("prepare = %v", err)
	}
	if err := h.InstallSkills(ctx, id, []string{"pdf"}); err != nil {
		t.Errorf("skills = %v", err)
	}
}

func TestFetchOmpCatalogFailuresAndCache(t *testing.T) {
	ctx := context.Background()
	reads := stubOmpCatalog(t, 200, `{"anthropic":{"m":{"name":"M"}}}`)
	for range 2 {
		c, err := fetchOmpCatalog(ctx, "1.2.3")
		if err != nil || c["anthropic"]["m"].Name != "M" {
			t.Fatalf("catalog = %v, %v", c, err)
		}
	}
	if reads.Load() != 1 {
		t.Errorf("a release's catalog never changes: read %d times", reads.Load())
	}
	if _, err := fetchOmpCatalog(ctx, "1.2.4"); err != nil || reads.Load() != 2 {
		t.Errorf("another version is another catalog: %d reads, %v", reads.Load(), err)
	}

	stubOmpCatalog(t, 404, ``)
	if _, err := fetchOmpCatalog(ctx, "9.9.9"); err == nil || !strings.Contains(err.Error(), "(v9.9.9): 404") {
		t.Errorf("missing = %v", err)
	}
	stubOmpCatalog(t, 200, `{"broken`)
	if _, err := fetchOmpCatalog(ctx, "9.9.9"); err == nil || !strings.Contains(err.Error(), "omp model catalog:") {
		t.Errorf("malformed = %v", err)
	}
	// A failure is not cached: the next asks again.
	reads = stubOmpCatalog(t, 500, ``)
	_, _ = fetchOmpCatalog(ctx, "9.9.9")
	_, _ = fetchOmpCatalog(ctx, "9.9.9")
	if reads.Load() != 2 {
		t.Errorf("reads = %d", reads.Load())
	}
	old := ompCatalogURL
	defer func() { ompCatalogURL = old }()
	ompCatalogURL = "http://bad host/%s"
	if _, err := fetchOmpCatalog(ctx, "9.9.9"); err == nil {
		t.Error("an unparsable URL is an error")
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	ompCatalogURL = dead.URL + "/%s"
	dead.Close()
	if _, err := fetchOmpCatalog(ctx, "9.9.9"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("dead = %v", err)
	}
}

func TestOmpModelsCatalogFailure(t *testing.T) {
	stubOmpCatalog(t, 500, ``)
	h, _ := Omp{}.New(withCreds(apiKey("anthropic", "k")), nil)
	if _, err := h.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v", err)
	}
}

func TestOmpModelNamesFallBackAndMarkLogins(t *testing.T) {
	stubOmpCatalog(t, 200, `{"anthropic":{"m":{"name":""}}}`)
	h, _ := Omp{}.New(withCreds(login("anthropic", "a", nil)), nil)
	got, err := h.Models(context.Background())
	if err != nil || !slices.Equal(got, []ModelInfo{{ID: "anthropic/m", Name: "anthropic (login) · m"}}) {
		t.Errorf("models = %+v, %v", got, err)
	}
}

func TestOmpCheckAuth(t *testing.T) {
	ctx := context.Background()
	stubOmpCatalog(t, 200, `{
	  "anthropic":{"m":{"baseUrl":"https://api.anthropic.com"}},
	  "opencode-zen":{"z2":{"baseUrl":"https://zen.example/v1"},"z1":{"baseUrl":"https://zen.example/v1"}},
	  "deepseek":{}}`)
	for _, tc := range []struct {
		name       string
		cred       providers.Credential
		models     []string
		answer     func(*http.Request) (int, string)
		rejected   string
		unverified string
	}{
		{name: "accepted", cred: apiKey("anthropic", "k"), models: []string{"anthropic/m"}, answer: func(*http.Request) (int, string) { return 200, `{}` }},
		{name: "rejected", cred: apiKey("anthropic", "k"), models: []string{"anthropic/m"}, answer: func(*http.Request) (int, string) { return 401, `` },
			rejected: "omp provider anthropic: credential rejected (401) — fix the Anthropic API key"},
		{name: "inconclusive", cred: apiKey("anthropic", "k"), models: []string{"anthropic/m"}, answer: func(*http.Request) (int, string) { return 503, `` },
			unverified: "omp provider anthropic: inconclusive"},
		{name: "a provider the flow does not use", cred: apiKey("anthropic", "k"), models: []string{"openai/x", "bare"},
			answer: func(*http.Request) (int, string) { t.Error("nothing to probe"); return 200, `` }},
		{name: "no base URL to probe", cred: apiKey("deepseek", "k"), models: []string{"deepseek/m"},
			answer: func(*http.Request) (int, string) { return 200, `` }, unverified: "omp provider deepseek: no zero-token probe"},
		{name: "the flow's model is retired", cred: apiKey("opencode-zen", "k"), models: []string{"opencode-zen/z9"},
			answer:   func(*http.Request) (int, string) { return 400, `{"error":"Model is unavailable"}` },
			rejected: "pick another model in the flow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubVendors(t, tc.answer)
			h, _ := Omp{}.New(withCreds(tc.cred), nil)
			checkResult(t, h.(AuthChecker).CheckAuth(ctx, tc.models), tc.rejected, tc.unverified)
		})
	}

	// With no flow in hand the model is borrowed, first of the catalog's, and
	// its being refused says nothing about the key.
	stubVendors(t, func(r *http.Request) (int, string) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"model":"z1"`)) {
			t.Errorf("probe body = %s", body)
		}
		return 400, `{"error":"Model is unavailable"}`
	})
	h, _ := Omp{}.New(Config{Credentials: Credentials{Snapshot: []providers.Credential{apiKey("opencode-zen", "k")},
		Problems: map[string]string{"x": "credential x was deleted"}}}, nil)
	res := h.(AuthChecker).CheckCredentials(ctx)
	checkResult(t, res, "omp: credential x was deleted", "probe model z1 model refused")

	stubOmpCatalog(t, 500, ``)
	checkResult(t, h.(AuthChecker).CheckAuth(ctx, []string{"opencode-zen/z1"}), "omp: credential x was deleted", "omp: omp model catalog")
}

// --- omp broker --------------------------------------------------------------

func brokerOf(t *testing.T, creds Credentials) http.Handler {
	t.Helper()
	h, err := Omp{}.New(Config{Credentials: creds}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return h.(AuthBrokered).Broker(noRegister)
}

func call(h http.Handler, method, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestBrokerRoutes(t *testing.T) {
	b := brokerOf(t, Credentials{})
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{http.MethodGet, "/v1/healthz", 200, `"ok":true`},
		{http.MethodGet, "/v1/usage", 200, `"reports":[]`},
		{http.MethodGet, "/v1/usage/history", 200, `"entries":[]`},
		{http.MethodGet, "/v1/usage/clients", 200, `"clients":[]`},
		{http.MethodPost, "/v1/usage/stale", 200, `"ok":true`},
		{http.MethodGet, "/v1/credentials/disabled", 200, `"disabled":[]`},
		{http.MethodPost, "/v1/credential/7/block", 200, `"ok":true`},
		{http.MethodDelete, "/v1/credential/7/blocks", 200, `"ok":true`},
		{http.MethodPost, "/v1/credential/x/block", 404, `not found`},
		{http.MethodPut, "/v1/usage", 404, `not found`},
		{http.MethodPost, "/v1/credential", 403, `managed in apothecary`},
	} {
		w := call(b, tc.method, tc.path)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s %s = %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: a broker answer must not be cached", tc.path)
		}
	}
}

func TestParseGeneration(t *testing.T) {
	for in, want := range map[string]int64{`"7"`: 7, `W/"7"`: 7, ` 7 `: 7, `"0"`: 0} {
		if got, ok := parseGeneration(in); !ok || got != want {
			t.Errorf("parseGeneration(%q) = %d, %v", in, got, ok)
		}
	}
	for _, in := range []string{``, `"x"`, `"-1"`} {
		if _, ok := parseGeneration(in); ok {
			t.Errorf("parseGeneration(%q) ok", in)
		}
	}
}

func TestBrokerEntriesAndGeneration(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	now := at.Add(-time.Hour)
	google := login("google-gemini-cli", "g-acc", map[string]string{"email": "me@example.org", "project_id": "proj-1"})
	google.Expires = at
	copilot := login("github-copilot", "gho", map[string]string{"enterprise_url": "https://ghe.example", "api_endpoint": "https://api.ghe.example"})
	var registered []string
	b := &ompBroker{creds: Credentials{Snapshot: []providers.Credential{apiKey("anthropic", "k"), google, copilot}},
		register: func(s string) { registered = append(registered, s) }, now: func() time.Time { return now }}
	es, err := b.entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("entries = %+v", es)
	}
	byProvider := map[string]brokerEntry{}
	for _, e := range es {
		byProvider[e.Provider] = e
	}
	g := byProvider["google-gemini-cli"]
	if g.IdentityKey == nil || *g.IdentityKey != "me@example.org" || g.Credential.ProjectID != "proj-1" || g.Credential.Email != "me@example.org" {
		t.Errorf("google = %+v", g)
	}
	if g.Credential.Expires != at.UnixMilli() {
		t.Errorf("expires = %d", g.Credential.Expires)
	}
	c := byProvider["github-copilot"]
	if c.Credential.EnterpriseURL != "https://ghe.example" || c.Credential.APIEndpoint != "https://api.ghe.example" {
		t.Errorf("copilot = %+v", c)
	}
	// A token that never expires needs a number all the same.
	if want := now.Add(brokerFarFuture).UnixMilli(); c.Credential.Expires != want {
		t.Errorf("no expiry = %d, want %d", c.Credential.Expires, want)
	}
	if !slices.Contains(registered, "g-acc") || !slices.Contains(registered, "gho") {
		t.Errorf("registered = %v", registered)
	}
	if slices.Contains(registered, "k") {
		t.Errorf("a key is not a broker token: %v", registered)
	}

	// Any token moving moves the generation.
	g1 := generation(es)
	es[0].Credential.Expires += 60_000
	if generation(es) == g1 {
		t.Error("a refreshed token must read as a new generation")
	}
	if generation(nil) != 0 {
		t.Error("nothing is generation 0")
	}
	if got := generation([]brokerEntry{{}, {}}); got != 2 {
		t.Errorf("count = %d", got)
	}
}

func TestBrokerFailures(t *testing.T) {
	boom := errors.New("keeper down")
	b := brokerOf(t, Credentials{Fresh: func(context.Context) ([]providers.Credential, error) { return nil, boom }})
	if w := call(b, http.MethodGet, "/v1/snapshot"); w.Code != 503 || !strings.Contains(w.Body.String(), "keeper down") {
		t.Errorf("snapshot = %d %s", w.Code, w.Body)
	}
	if w := call(b, http.MethodPost, "/v1/credential/1/refresh"); w.Code != 500 || !strings.Contains(w.Body.String(), "keeper down") {
		t.Errorf("refresh = %d %s", w.Code, w.Body)
	}
}

func TestBrokerSnapshotOfNothingIsAnEmptyList(t *testing.T) {
	b := brokerOf(t, Credentials{})
	w := call(b, http.MethodGet, "/v1/snapshot")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"credentials":[]`) {
		t.Errorf("snapshot = %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("ETag"); got == "" {
		t.Error("no ETag")
	}
}

// The long poll: a client up to date waits; a rotated token ends the wait with
// the new snapshot; a client going away ends it with nothing.
func TestBrokerSnapshotLongPoll(t *testing.T) {
	cred := login("openai-chatgpt", "first", nil)
	var calls atomic.Int32
	creds := Credentials{Fresh: func(context.Context) ([]providers.Credential, error) {
		c := cred
		if calls.Add(1) > 1 {
			c.Access = "second"
			c.Expires = cred.Expires.Add(time.Hour)
		}
		return []providers.Credential{c}, nil
	}}
	b := brokerOf(t, creds)
	first := call(b, http.MethodGet, "/v1/snapshot")
	etag := first.Header().Get("ETag")
	calls.Store(0)

	w := call(b, http.MethodGet, "/v1/snapshot?wait=10000", "If-None-Match", etag)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"access":"second"`) {
		t.Errorf("rotated = %d %s", w.Code, w.Body)
	}
	if w.Header().Get("ETag") == etag {
		t.Error("the new snapshot carries a new generation")
	}

	// The first read answers the same generation; the second fails.
	calls.Store(0)
	failing := brokerOf(t, Credentials{Fresh: func(context.Context) ([]providers.Credential, error) {
		if calls.Add(1) > 1 {
			return nil, errors.New("keeper down")
		}
		return []providers.Credential{cred}, nil
	}})
	if w := call(failing, http.MethodGet, "/v1/snapshot?wait=10000", "If-None-Match", etag); w.Code != 503 {
		t.Errorf("failed mid-wait = %d %s", w.Code, w.Body)
	}

	steady := brokerOf(t, Credentials{Snapshot: []providers.Credential{cred}})
	req := httptest.NewRequest(http.MethodGet, "/v1/snapshot?wait=10000", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = req.WithContext(ctx)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	steady.ServeHTTP(rec, req)
	if rec.Body.Len() != 0 {
		t.Errorf("a client gone gets nothing: %s", rec.Body)
	}
}

func TestBrokerRefreshAnswersTheEntry(t *testing.T) {
	cred := login("openai-chatgpt", "acc", map[string]string{"account_id": "acct"})
	b := brokerOf(t, Credentials{Snapshot: []providers.Credential{cred}})
	id := strconv.FormatInt(brokerID(cred.ID), 10)
	w := call(b, http.MethodPost, "/v1/credential/"+id+"/refresh")
	var got struct {
		Entry brokerRefreshEntry `json:"entry"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.Entry.Credential.Access != "acc" || got.Entry.Credential.Refresh != remoteRefreshSentinel {
		t.Errorf("refresh = %d %+v", w.Code, got)
	}
	if strings.Contains(w.Body.String(), cred.Refresh) {
		t.Errorf("the refresh token left the engine: %s", w.Body)
	}
	if w := call(b, http.MethodPost, fmt.Sprintf("/v1/credential/%d/refresh", brokerID("other"))); w.Code != 404 {
		t.Errorf("unknown = %d", w.Code)
	}
}
