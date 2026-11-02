//go:build conformance

package kinds

// The pinned opencode against what the engine hands it: the config and agent
// files the adapter generates (opencode validates its config strictly and
// refuses an unknown key), the auth.json SeedAuth renders (opencode drops an
// entry its schema rejects, silently), and the ACP face the sidecar drives.
// How these tests run: conformance_test.go.

import (
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
	opencodeadapter "apothecary/internal/adapters/opencode"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/ports/agentport"
)

func TestOpencodeConformance(t *testing.T) {
	bin := toolsBin(t, "opencode")
	wantVersion(t, bin, OpencodeVersion)
	ctx := t.Context()

	// One credential of each face opencode's auth.json carries: an API key,
	// a login with its refresh withheld, a Copilot login.
	const anthropicKey = "sk-ant-conformance"
	envp, id, home := workspaceHome(t)
	h, err := Opencode{}.New(withCreds(
		apiKey("anthropic", anthropicKey),
		login("openai-chatgpt", fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), map[string]string{providers.ExtraAccountID: "acct-conformance"}),
		login("github-copilot", "ghu_conformance", nil),
	), envp)
	if err != nil {
		t.Fatal(err)
	}
	// The agents as flow.WorkspaceAgents hands them over: a read-only one
	// carries the paths its stages write (a file, and a files directory).
	specs := []*agentspec.Spec{
		{Name: "builder", Description: "Builds it", AllowBash: true, MCP: []string{"docs"}, Prompt: "Build it."},
		{Name: "reviewer", Description: "Reviews it", ReadOnly: true, Prompt: "Review it.",
			Outputs:     []agentspec.OutputDecl{{Name: "review", Type: agentspec.TypeFile}, {Name: "shots", Type: agentspec.TypeFiles}},
			OutputPaths: []string{"artifacts/review.md", "artifacts/shots/"}},
		agentspec.Ask(),
	}
	if err := h.Prepare(ctx, id, specs); err != nil {
		t.Fatal(err)
	}
	if err := h.SeedAuth(ctx, id, func(string) {}); err != nil {
		t.Fatal(err)
	}
	files := materialize(t, envp, id, home)
	t.Logf("the engine wrote %v", files)
	authFile := h.(AuthLocator).AuthFiles(ctx, id)[0]
	authDoc, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatalf("no auth.json at %s: %v", authFile, err)
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(authDoc, &entries); err != nil || len(entries) != 3 {
		t.Fatalf("auth.json = %s (%v)", authDoc, err)
	}

	// The models catalog: opencode's bundled snapshot, never models.dev — the
	// providers it lists come from the credentials, not from the network.
	env := harnessEnv(home, "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_AUTOUPDATE=1")
	// The workspace's repository: opencode scopes a session (and matches
	// its permission paths) to the git worktree it runs in.
	project := gitRepo(t)

	t.Run("config", func(t *testing.T) {
		// opencode refuses a config with a key its schema lacks; the resolved
		// config is every agent markdown parsed into its own terms.
		out, err := runBin(t, env, project, bin, "debug", "config")
		if err != nil {
			t.Fatalf("opencode rejects the engine's config: %v\n%s", err, tail(out, 3000))
		}
		var cfg struct {
			MCP   map[string]json.RawMessage `json:"mcp"`
			Agent map[string]struct {
				Mode       string                     `json:"mode"`
				Prompt     string                     `json:"prompt"`
				Permission map[string]json.RawMessage `json:"permission"`
			} `json:"agent"`
		}
		if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &cfg); err != nil {
			t.Fatalf("opencode debug config: %v\n%s", err, tail(out, 3000))
		}
		if cfg.MCP["ask_human"] == nil {
			t.Errorf("opencode dropped the ask_human MCP server: %v", cfg.MCP)
		}
		for _, s := range specs {
			a, ok := cfg.Agent[s.Name]
			if !ok {
				t.Errorf("opencode has no agent %s: %s", s.Name, tail(out, 3000))
				continue
			}
			if a.Mode != "primary" || !strings.Contains(a.Prompt, s.Prompt) {
				t.Errorf("agent %s: mode %q, prompt %q", s.Name, a.Mode, tail(a.Prompt, 200))
			}
		}
		// What the frontmatter turns into: no shell where none is allowed, an
		// unlisted MCP server's tools off, the Ask agent asks no human and
		// edits nothing. (Which files a read-only agent with outputs may
		// write is proven by doing it, below.)
		perm := func(agent, tool string) string {
			var s string
			_ = json.Unmarshal(cfg.Agent[agent].Permission[tool], &s)
			return s
		}
		for _, c := range []struct{ agent, tool, want string }{
			{"ask", "edit", "deny"}, {"ask", "bash", "deny"}, {"ask", "ask_human_*", "deny"},
			{"reviewer", "bash", "deny"}, {"reviewer", "docs_*", "deny"},
			{"builder", "bash", "allow"}, {"builder", "docs_*", ""}, {"builder", "edit", ""},
		} {
			if got := perm(c.agent, c.tool); got != c.want {
				t.Errorf("agent %s: %s is %q, want %q (permissions %s)", c.agent, c.tool, got, c.want, cfg.Agent[c.agent].Permission)
			}
		}
	})

	t.Run("auth", func(t *testing.T) {
		// Every entry the engine wrote is one opencode keeps.
		out, err := runBin(t, env, project, bin, "auth", "list")
		if err != nil {
			t.Fatalf("opencode auth list: %v\n%s", err, out)
		}
		if want := fmt.Sprintf("%d credentials", len(entries)); !strings.Contains(out, want) {
			t.Errorf("opencode keeps fewer credentials than the engine wrote (%s), want %s:\n%s", authDoc, want, out)
		}
		// Each provider is enabled by its entry: a control without auth.json
		// lists none of them.
		models, err := runBin(t, env, project, bin, "models")
		if err != nil {
			t.Fatalf("opencode models: %v\n%s", err, tail(models, 2000))
		}
		none, err := runBin(t, harnessEnv(t.TempDir(), "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_AUTOUPDATE=1"), project, bin, "models")
		if err != nil {
			t.Fatalf("opencode models without auth: %v\n%s", err, tail(none, 2000))
		}
		for native := range entries {
			if !strings.Contains("\n"+models, "\n"+native+"/") {
				t.Errorf("opencode lists no %s model: it did not accept the engine's %s entry\n%s", native, native, tail(models, 2000))
			}
			if strings.Contains("\n"+none, "\n"+native+"/") {
				t.Errorf("opencode lists %s models with no auth.json: the check above proves nothing", native)
			}
		}
	})

	t.Run("acp", func(t *testing.T) {
		// The sidecar's path: `opencode acp`, a session whose mode is the
		// stage's agent, a model, a turn. The model is a fake Anthropic,
		// reached through this repository's own config — which a repo may
		// carry. Its calls must carry the key from the engine's auth.json and
		// the prompt of the agent the mode selected. It answers the read-only
		// reviewer by writing its two outputs and three files it may not —
		// and then, the control, the builder by writing those three.
		writes := map[string]bool{ // repo path → may the reviewer write it
			"artifacts/review.md":         true,
			"artifacts/shots/nested/a.md": true,
			"artifacts/other.md":          false,
			"artifacts/shots.md":          false,
			"src/main.go":                 false,
		}
		var mu sync.Mutex
		var calls []string
		var bodies strings.Builder
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			calls = append(calls, r.Method+" "+r.URL.Path+" x-api-key="+r.Header.Get("x-api-key"))
			bodies.Write(body)
			mu.Unlock()
			if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			turn := anthropicText("pong")
			agentWrites := map[string]bool{"Review it.": true, "Build it.": false}
			for marker, all := range agentWrites {
				if !strings.Contains(string(body), marker) || strings.Contains(string(body), `"tool_result"`) {
					continue
				}
				var uses [][2]string
				for p, allowed := range writes {
					if all || !allowed {
						in, _ := json.Marshal(map[string]string{"filePath": filepath.Join(project, p), "content": "written by " + marker + "\n"})
						uses = append(uses, [2]string{"write", string(in)})
					}
				}
				// And an edit of a file the repository already holds.
				in, _ := json.Marshal(map[string]string{"filePath": filepath.Join(project, "README.md"), "oldString": "original", "newString": "edited by " + marker})
				uses = append(uses, [2]string{"edit", string(in)})
				slices.SortFunc(uses, func(a, b [2]string) int { return strings.Compare(a[1], b[1]) })
				turn = anthropicToolUses(uses)
			}
			for _, ev := range turn {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev[0], ev[1])
			}
		}))
		defer api.Close()
		repoCfg, _ := json.Marshal(map[string]any{"provider": map[string]any{"anthropic": map[string]any{
			"options": map[string]any{"baseURL": api.URL + "/v1"},
			"models":  map[string]any{"acme-1": map[string]any{"name": "Acme 1", "tool_call": true}},
		}}})
		if err := os.WriteFile(filepath.Join(project, "opencode.json"), repoCfg, 0o644); err != nil {
			t.Fatal(err)
		}
		readme := filepath.Join(project, "README.md")
		if err := os.WriteFile(readme, []byte("original\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		readmeHas := func(want string) bool { b, _ := os.ReadFile(readme); return strings.Contains(string(b), want) }

		profile := opencodeadapter.Profile()
		c := startACP(t, profile.Command, env, project)
		c.initialize()
		s := c.newSession(project)
		mode := func(agent string) string { return profile.Mode(agentport.AgentSpec{Agent: agent, Harness: "opencode"}) }
		for _, spec := range specs {
			if !slices.Contains(s.offered("mode"), mode(spec.Name)) {
				t.Errorf("opencode offers no mode %q for agent %s: %v", mode(spec.Name), spec.Name, s.offered("mode"))
			}
		}
		c.set(s, "mode", mode("reviewer"))
		c.set(s, "model", "anthropic/acme-1")
		stop, said := c.prompt(s.SessionID, "Review the change.")
		mu.Lock()
		t.Logf("fake Anthropic saw %v; stop %q, said %q", calls, stop, said)
		if !slices.ContainsFunc(calls, func(k string) bool { return strings.HasSuffix(k, "/messages x-api-key="+anthropicKey) }) {
			t.Errorf("opencode never called the provider with the engine's key: %v", calls)
		}
		if sent := bodies.String(); !strings.Contains(sent, "Review it.") || strings.Contains(sent, "Build it.") {
			t.Errorf("the turn did not run the reviewer agent the mode selected:\n%s", tail(sent, 3000))
		}
		mu.Unlock()
		if stop != "end_turn" {
			t.Errorf("the reviewer's turn ended %q saying %q, want end_turn", stop, said)
		}
		for p, allowed := range writes {
			_, err := os.Stat(filepath.Join(project, p))
			switch {
			case allowed && err != nil:
				t.Errorf("the read-only reviewer could not write its output %s: %v", p, err)
			case !allowed && err == nil:
				t.Errorf("the read-only reviewer wrote %s, which is not its output", p)
			}
		}
		if !readmeHas("original") {
			t.Error("the read-only reviewer edited README.md, which is not its output")
		}
		// The control: the builder, not read-only, writes what the reviewer
		// could not — so the reviewer's refusals were its permission's.
		b := c.newSession(project)
		c.set(b, "mode", mode("builder"))
		c.set(b, "model", "anthropic/acme-1")
		if stop, said := c.prompt(b.SessionID, "Build the change."); stop != "end_turn" {
			t.Errorf("the builder's turn ended %q saying %q, want end_turn", stop, said)
		}
		for p, allowed := range writes {
			if _, err := os.Stat(filepath.Join(project, p)); !allowed && err != nil {
				t.Errorf("the builder could not write %s either: the reviewer's refusal proves nothing (%v)", p, err)
			}
		}
		if !readmeHas("edited by Build it.") {
			t.Error("the builder could not edit README.md either: the reviewer's refusal proves nothing")
		}
	})
}

// gitRepo is a repository with one commit, as a workspace's checkout is.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=c", "-c", "user.email=c@example.test", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// anthropicText is one streamed Messages API reply saying text.
func anthropicText(text string) [][2]string {
	return [][2]string{
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"acme-1","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text)},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
}

// anthropicToolUses is one streamed reply calling each {tool, input JSON}.
func anthropicToolUses(uses [][2]string) [][2]string {
	evs := [][2]string{{"message_start", `{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"acme-1","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`}}
	for i, u := range uses {
		evs = append(evs,
			[2]string{"content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"toolu_%d","name":%q,"input":{}}}`, i, i, u[0])},
			[2]string{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%q}}`, i, u[1])},
			[2]string{"content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i)},
		)
	}
	return append(evs,
		[2]string{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":1}}`},
		[2]string{"message_stop", `{"type":"message_stop"}`},
	)
}
