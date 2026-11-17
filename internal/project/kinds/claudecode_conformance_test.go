//go:build conformance

package kinds

// The pinned claude and claude-agent-acp against what the engine hands them:
// the two credential faces — an Anthropic API key riding provider.env, a
// Claude login rendered into .credentials.json with its refresh token
// withheld and rewritten as the keeper refreshes it — and the ACP bridge the
// sidecar drives, through to model calls at a fake Anthropic Messages API
// (ANTHROPIC_BASE_URL). How these tests run: conformance_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/account/providers"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
)

func TestClaudeCodeConformance(t *testing.T) {
	wantVersion(t, toolsBin(t, "claude"), ClaudeCodeVersion)
	wantVersion(t, toolsBin(t, "claude-agent-acp"), ClaudeAgentACPVersion)
	t.Logf("running as uid %d: bypassPermissions must hold for root too (IS_SANDBOX)", os.Geteuid())
	shim := buildShim(t)
	api := newFakeAnthropic(t)

	const key = "sk-ant-conformance"
	specs := []*agentspec.Spec{{Name: "builder", Description: "Builds it", AllowBash: true, Prompt: "Build it."}}
	stage := agentport.AgentSpec{Agent: "builder", Harness: providers.ClaudeCode}

	// workspace is a workspace home holding what the engine writes for cfg's
	// credentials, the provider.env it hands the agent, and the stage's role
	// preamble as the runner prepends it.
	type workspace struct {
		h        *claudeCodeHarness
		seed     func() // the engine seeding, and again the keeper's push
		env      []string
		preamble string
	}
	prepared := func(t *testing.T, cfg Config) workspace {
		t.Helper()
		envp, id, home := workspaceHome(t)
		hh, err := ClaudeCode{}.New(cfg, envp)
		if err != nil {
			t.Fatal(err)
		}
		w := workspace{h: hh.(*claudeCodeHarness)}
		if err := w.h.Prepare(t.Context(), id, specs); err != nil {
			t.Fatal(err)
		}
		w.seed = func() {
			t.Helper()
			if err := w.h.SeedAuth(t.Context(), id, func(string) {}); err != nil {
				t.Fatal(err)
			}
			t.Logf("the engine wrote %v", materialize(t, envp, id, home))
		}
		w.seed()
		env := []string{"ANTHROPIC_BASE_URL=" + api.URL}
		for k, v := range w.h.EnvVars() {
			env = append(env, k+"="+v)
		}
		w.env = harnessEnv(home, env...)
		if w.preamble, err = w.h.adapter.Profile().Preamble(t.Context(), id, stage); err != nil {
			t.Fatal(err)
		}
		return w
	}

	// open starts the bridge as the sidecar does and opens a session on a
	// repository in the profile's mode (or its read-only one), with ask_human
	// riding it — the shim built from this tree standing in for the
	// workspace's /opt/apothecary/shim. The model is the bridge's default.
	open := func(t *testing.T, w workspace, readOnly bool) (*acpClient, acpSession, string) {
		t.Helper()
		profile := w.h.adapter.Profile()
		repo := gitRepo(t)
		cl := startACP(t, profile.Command, w.env, repo)
		cl.initialize()
		var servers []any
		if !readOnly {
			for _, m := range profile.MCPServers {
				var menv []map[string]string
				for k, v := range m.Env {
					menv = append(menv, map[string]string{"name": k, "value": v})
				}
				servers = append(servers, map[string]any{"name": m.Name, "command": shim, "args": m.Command[1:], "env": menv})
			}
		}
		var s acpSession
		res := cl.mustCall("session/new", map[string]any{"cwd": repo, "mcpServers": servers}, 2*time.Minute)
		if err := json.Unmarshal(res, &s); err != nil || s.SessionID == "" {
			t.Fatalf("session/new = %s (%v)", res, err)
		}
		// bypassPermissions is offered only where the bridge will honour it:
		// as root, only with IS_SANDBOX.
		mode := profile.Mode(stage)
		for _, m := range []string{mode, profile.ReadOnlyMode} {
			if !slices.Contains(s.offered("mode"), m) {
				t.Errorf("claude-agent-acp offers no mode %q: %v", m, s.offered("mode"))
			}
		}
		if readOnly {
			mode = profile.ReadOnlyMode
			cl.refuse.Store(true)
		}
		cl.set(s, "mode", mode)
		return cl, s, repo
	}

	t.Run("api key", func(t *testing.T) {
		w := prepared(t, withCreds(apiKey("anthropic", key)))
		cl, s, repo := open(t, w, false)
		from := api.mark()
		stop, said := cl.prompt(s.SessionID, w.preamble+"Say pong.")
		turn := api.turn(from, "Say pong.")
		t.Logf("fake Anthropic saw %v; stop %q, said %q", api.seen(from), stop, said)
		if turn == nil {
			t.Fatalf("the turn never reached the model")
		}
		if turn.key != key {
			t.Errorf("the turn called the model with x-api-key %q, want the engine's key", turn.key)
		}
		if !strings.Contains(turn.body, "Build it.") || !strings.Contains(turn.body, "# ASKING THE HUMAN") {
			t.Errorf("the turn did not carry the role preamble:\n%s", tail(turn.body, 3000))
		}
		// Loaded, not deferred behind ToolSearch: the role names the tool,
		// and a deferred one fails when called by that name.
		if !slices.Contains(turn.tools, "mcp__ask_human__ask_human") {
			t.Errorf("ask_human is not among the turn's tools: %v", turn.tools)
		}
		if stop != "end_turn" || !strings.Contains(said, "pong") {
			t.Errorf("the turn ended %q saying %q, want end_turn and the model's pong", stop, said)
		}
		if !cl.reportedCost() {
			t.Error("the turn reported no USD cost: budgets would count nothing")
		}

		// A flow's model, in the catalog's anthropic/<id> form, reaches the
		// model call as <id>. The id is the one the default just ran: no
		// model literal in the tree.
		cl.set(s, "model", "anthropic/"+turn.model)
		from = api.mark()
		cl.prompt(s.SessionID, "Say pong with the flow's model.")
		if again := api.turn(from, "Say pong with the flow's model."); again == nil || again.model != turn.model {
			t.Errorf("the flow's model anthropic/%s ran as %+v", turn.model, again)
		}

		// The stage's mode runs a write without asking anyone: the control
		// for the read-only session below.
		target := filepath.Join(repo, "written.txt")
		cl.prompt(s.SessionID, writeFile(target))
		if _, err := os.Stat(target); err != nil {
			t.Errorf("a stage session could not write: the read-only check proves nothing (%v)", err)
		}
	})

	t.Run("read-only", func(t *testing.T) {
		// Plan mode asks before a write, and the sidecar refuses every
		// request of a read-only session that is not a read.
		w := prepared(t, withCreds(apiKey("anthropic", key)))
		cl, s, repo := open(t, w, true)
		from := api.mark()
		target := filepath.Join(repo, "written.txt")
		stop, said := cl.prompt(s.SessionID, writeFile(target))
		t.Logf("fake Anthropic saw %v; stop %q, said %q", api.seen(from), stop, said)
		if api.turn(from, writeFile(target)) == nil {
			t.Fatal("the read-only turn never reached the model")
		}
		if _, err := os.Stat(target); err == nil {
			t.Error("a read-only (plan mode) session wrote to the repository")
		}
	})

	t.Run("login", func(t *testing.T) {
		// The keeper refreshes engine-side and pushes the new access token
		// by seeding again, while the old one is still valid; the running
		// Claude Code must take the new one from the rewritten file on its
		// own — with no refresh token, it cannot refresh itself.
		first := login("anthropic", "oat-first", nil)
		current := first
		w := prepared(t, Config{Credentials: Credentials{Snapshot: []providers.Credential{first},
			Fresh: func(context.Context) ([]providers.Credential, error) { return []providers.Credential{current}, nil }}})
		if slices.ContainsFunc(w.env, func(kv string) bool { return strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") }) {
			t.Fatalf("a login rides no key: %v", w.env)
		}
		cl, s, _ := open(t, w, false)
		from := api.mark()
		stop, said := cl.prompt(s.SessionID, "Say pong.")
		turn := api.turn(from, "Say pong.")
		t.Logf("fake Anthropic saw %v; stop %q, said %q", api.seen(from), stop, said)
		if turn == nil || turn.bearer != first.Access {
			t.Fatalf("the login's turn did not call the model with Bearer %s: %+v", first.Access, turn)
		}

		current = login("anthropic", "oat-second", nil)
		w.seed()
		from = api.mark()
		stop, said = cl.prompt(s.SessionID, "Say pong again.")
		t.Logf("fake Anthropic saw %v; stop %q, said %q", api.seen(from), stop, said)
		if turn := api.turn(from, "Say pong again."); turn == nil || turn.bearer != current.Access {
			t.Errorf("after the keeper's push, the next turn did not use the new token: %+v", turn)
		}
		if stop != "end_turn" || !strings.Contains(said, "pong") {
			t.Errorf("the turn after the push ended %q saying %q", stop, said)
		}
	})
}

// reportedCost says whether a usage_update carried a USD cost.
func (c *acpClient) reportedCost() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.updates {
		var n struct {
			Update struct {
				Kind string `json:"sessionUpdate"`
				Cost *struct {
					Amount   float64 `json:"amount"`
					Currency string  `json:"currency"`
				} `json:"cost"`
			} `json:"update"`
		}
		if json.Unmarshal(u, &n) == nil && n.Update.Kind == "usage_update" && n.Update.Cost != nil &&
			n.Update.Cost.Currency == "USD" && n.Update.Cost.Amount > 0 {
			return true
		}
	}
	return false
}

// buildShim builds the workspace shim from this tree: its ask-human server is
// what the session's ask_human runs.
func buildShim(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "shim")
	if b, err := exec.Command("go", "build", "-buildvcs=false", "-o", out, "apothecary/workspace/shim").CombinedOutput(); err != nil {
		t.Fatalf("build the shim: %v\n%s", err, b)
	}
	return out
}

// writeFile is the prompt the fake answers with a Write of path.
func writeFile(path string) string { return "Write the file " + path + " now." }

// fakeAnthropic is a Messages API: every model call is answered with a
// streamed "pong", but a turn asking to write a file (writeFile), which gets
// a Write of it (once: not again after its result).
type fakeAnthropic struct {
	*httptest.Server
	mu    sync.Mutex
	calls []anthropicCall
}

type anthropicCall struct {
	path, key, bearer, model, body string
	tools                          []string
}

func newFakeAnthropic(t *testing.T) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAnthropic) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Model string `json:"model"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(raw, &body)
	c := anthropicCall{path: r.Method + " " + r.URL.Path, key: r.Header.Get("x-api-key"),
		bearer: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), model: body.Model, body: string(raw)}
	for _, tool := range body.Tools {
		c.tools = append(c.tools, tool.Name)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/messages") {
		http.NotFound(w, r)
		return
	}
	turn := anthropicText("pong")
	if i := strings.Index(c.body, "Write the file "); i >= 0 && !strings.Contains(c.body, `"tool_result"`) && slices.Contains(c.tools, "Write") {
		target := strings.Fields(c.body[i+len("Write the file "):])[0]
		in, _ := json.Marshal(map[string]string{"file_path": target, "content": "written\n"})
		turn = anthropicToolUses([][2]string{{"Write", string(in)}})
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range turn {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev[0], ev[1])
	}
}

// mark is where the calls from now on start.
func (f *fakeAnthropic) mark() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// seen lists the calls since from, as "METHOD path model auth".
func (f *fakeAnthropic) seen(from int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls[from:] {
		auth := "x-api-key=" + c.key
		if c.bearer != "" {
			auth = "bearer=" + c.bearer
		}
		out = append(out, c.path+" "+c.model+" "+auth)
	}
	return out
}

// turn is the last model call since from carrying prompt with the agent's
// tools: the session's turn, not a side call (a title, say).
func (f *fakeAnthropic) turn(from int, prompt string) *anthropicCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= from; i-- {
		c := f.calls[i]
		if strings.HasSuffix(c.path, "/v1/messages") && strings.Contains(c.body, prompt) && len(c.tools) > 0 {
			return &c
		}
	}
	return nil
}
