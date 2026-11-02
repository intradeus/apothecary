//go:build conformance

package kinds

// The pinned codex and codex-acp against what the engine hands them: the
// config.toml the adapter generates, the two credential faces — an OpenAI API
// key riding provider.env, a ChatGPT login rendered into auth.json with its
// refresh token withheld — and the ACP bridge the sidecar drives, through to
// a model call at a fake OpenAI. How these tests run: conformance_test.go.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
)

func TestCodexConformance(t *testing.T) {
	codex := toolsBin(t, "codex")
	wantVersion(t, codex, CodexVersion)
	wantVersion(t, toolsBin(t, "codex-acp"), CodexACPVersion)

	const (
		key     = "sk-openai-conformance"
		account = "acct-conformance"
	)
	access := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	idToken := fakeJWT(map[string]any{
		"exp": time.Now().Add(time.Hour).Unix(), "email": "c@example.test",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": account},
	})
	specs := []*agentspec.Spec{{Name: "builder", Description: "Builds it", AllowBash: true, Prompt: "Build it."}}

	// prepared is a workspace home holding what the engine writes for cred,
	// and the provider.env it hands the agent.
	prepared := func(t *testing.T, cred providers.Credential) (*codexHarness, string, []string) {
		t.Helper()
		envp, id, home := workspaceHome(t)
		h, err := Codex{}.New(withCreds(cred), envp)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Prepare(t.Context(), id, specs); err != nil {
			t.Fatal(err)
		}
		if err := h.SeedAuth(t.Context(), id, func(string) {}); err != nil {
			t.Fatal(err)
		}
		t.Logf("the engine wrote %v", materialize(t, envp, id, home))
		var env []string
		for k, v := range h.EnvVars() {
			env = append(env, k+"="+v)
		}
		return h.(*codexHarness), home, env
	}

	t.Run("config", func(t *testing.T) {
		// codex ignores a config.toml key it does not know: what it parsed is
		// what counts. The ask_human server must reach it whole — its
		// env_vars pass the agent marker, its timeout outlasts a human.
		_, home, _ := prepared(t, apiKey("openai", key))
		out, err := runBin(t, harnessEnv(home), home, codex, "mcp", "get", "ask_human", "--json")
		if err != nil {
			t.Fatalf("codex rejects the engine's config.toml: %v\n%s", err, out)
		}
		var got struct {
			Enabled   bool `json:"enabled"`
			Transport struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
				EnvVars []string `json:"env_vars"`
			} `json:"transport"`
			ToolTimeoutSec float64 `json:"tool_timeout_sec"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &got); err != nil {
			t.Fatalf("codex mcp get: %v\n%s", err, out)
		}
		if !got.Enabled || got.Transport.Type != "stdio" || got.Transport.Command != "/opt/apothecary/shim" ||
			!slices.Equal(got.Transport.Args, []string{"ask-human"}) || !slices.Equal(got.Transport.EnvVars, []string{"APOTHECARY_AGENT"}) ||
			got.ToolTimeoutSec < 3600 {
			t.Errorf("codex reads the ask_human server as %+v:\n%s", got, out)
		}
	})

	t.Run("login status", func(t *testing.T) {
		// The ChatGPT face, as far as it can be checked offline: codex takes
		// the rendered auth.json (refresh_token "", the id_token's claims) as
		// a ChatGPT login. No further: on session/new codex's app-server runs
		// workspace routing discovery against chatgpt.com — a private
		// endpoint no env or CODEX_CONFIG setting redirects — so a session
		// on this login cannot open without the real backend and a real login.
		_, home, _ := prepared(t, login("openai-chatgpt", access, map[string]string{providers.ExtraIDToken: idToken, providers.ExtraAccountID: account}))
		out, err := runBin(t, harnessEnv(home), home, codex, "login", "status")
		if err != nil || !strings.Contains(out, "Logged in using ChatGPT") {
			t.Errorf("codex does not take the engine's auth.json as a ChatGPT login: %v\n%s", err, out)
		}
	})

	t.Run("acp api key", func(t *testing.T) {
		// The sidecar's path: codex-acp under the engine's provider.env, a
		// session in the engine's mode, one turn — reaching a fake OpenAI
		// with the key. codex ignores OPENAI_BASE_URL; the fake is the
		// session config's openai_base_url, which codex-acp passes on from
		// CODEX_CONFIG.
		h, home, provEnv := prepared(t, apiKey("openai", key))
		api, seen := fakeOpenAI(t)
		env := harnessEnv(home, append(provEnv, `CODEX_CONFIG={"openai_base_url":"`+api.URL+`/v1"}`)...)
		profile := h.adapter.Profile()
		repo := gitRepo(t)
		cl := startACP(t, profile.Command, env, repo)
		cl.initialize()
		s := cl.newSession(repo)
		mode := profile.Mode(agentport.AgentSpec{Agent: "builder", Harness: "codex"})
		for _, m := range []string{mode, profile.ReadOnlyMode} {
			if !slices.Contains(s.offered("mode"), m) {
				t.Errorf("codex-acp offers no mode %q: %v", m, s.offered("mode"))
			}
		}
		cl.set(s, "mode", mode)
		stop, said := cl.prompt(s.SessionID, "Say pong.")
		reqs := seen()
		t.Logf("fake OpenAI saw %v; stop %q, said %q", reqs, stop, said)
		if want := "POST /v1/responses bearer=" + key; !slices.Contains(reqs, want) {
			t.Errorf("codex never called the model with the engine's key (%s): %v", want, reqs)
		}
		if stop != "end_turn" || !strings.Contains(said, "pong") {
			t.Errorf("the turn ended %q saying %q, want end_turn and the model's pong", stop, said)
		}
	})
}

// fakeOpenAI serves a streamed Responses API turn saying "pong" to every
// model call, and lists every request as "METHOD path bearer=…". Anything
// else is a 404 — codex's WebSocket attempt included, after which it falls
// back to HTTPS.
func fakeOpenAI(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" bearer="+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses"):
			w.Header().Set("Content-Type", "text/event-stream")
			msg := `{"type":"message","role":"assistant","id":"msg_1","content":[{"type":"output_text","text":"pong"}]}`
			for _, ev := range []string{
				`{"type":"response.created","response":{"id":"resp_1"}}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","id":"msg_1","content":[]}}`,
				`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"pong"}`,
				`{"type":"response.output_item.done","output_index":0,"item":` + msg + `}`,
				`{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"input_tokens_details":null,"output_tokens":1,"output_tokens_details":null,"total_tokens":2}}}`,
			} {
				var typ struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal([]byte(ev), &typ)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ.Type, ev)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}
