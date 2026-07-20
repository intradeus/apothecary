// Package opencode adapts the AgentRunner port to the opencode CLI
// (DESIGN.md §5.2), executing inside workspaces through the EnvProvisioner.
//
// Verified against opencode 1.17.8 (drift from the design's sketch is
// recorded in IMPLEMENTATION_NOTES): invocation is
//
//	opencode run --format json --agent <name> -m <provider/model> "$(cat prompt)"
//
// There is no --dangerously-skip-permissions flag — permissions are granted
// via generated config — and no turn cap flag; max_turns is not enforceable
// per-invocation (budgets still bound the run engine-side).
//
// The --format json stream is NDJSON events {type, sessionID, part};
// step_finish parts carry tokens{input,output,...} and cost, which map to
// Usage; TurnsUsed = number of step_finish events.
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// Config is reserved for adapter-level knobs; auth material arrives per
// call via SeedAuthJSON (composed by the opencode kind, DESIGN2 §4).
type Config struct{}

type Adapter struct {
	envp ports.EnvProvisioner
	cfg  Config
}

func New(envp ports.EnvProvisioner, cfg Config) *Adapter {
	return &Adapter{envp: envp, cfg: cfg}
}

const (
	promptPath = "/apothecary/prompt.txt"
	replyPath  = "/apothecary/reply.txt"
	homeConfig = "/home/apothecary/.config/opencode"
	authPath   = "/home/apothecary/.local/share/opencode/auth.json"
)

// PrepareEnv generates harness-native config from .apothecary/agents/ (§5.2):
// one agent markdown per spec plus the ask_human MCP registration. Config
// lands in the workspace user's config dir — outside the repo, so nothing
// needs git-excluding, regenerated every provision.
func (a *Adapter) PrepareEnv(ctx context.Context, env ports.EnvID) error {
	specs, err := a.readSpecs(ctx, env)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return fmt.Errorf("no agent specs found in .apothecary/agents/")
	}

	for _, s := range specs {
		md := agentMarkdown(s)
		p := path.Join(homeConfig, "agent", s.Name+".md")
		if err := a.envp.WriteFile(ctx, env, p, []byte(md), 0o644); err != nil {
			return fmt.Errorf("write agent config %s: %w", s.Name, err)
		}
	}

	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		// Non-interactive runs must never hang on a permission prompt;
		// restriction happens per-agent via tools:false.
		"permission": map[string]any{"edit": "allow", "bash": "allow", "webfetch": "allow"},
		"mcp": map[string]any{
			"ask_human": map[string]any{
				"type":    "local",
				"command": []string{"node", "/opt/apothecary/ask-human.mjs"},
				"enabled": true,
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := a.envp.WriteFile(ctx, env, path.Join(homeConfig, "opencode.json"), data, 0o644); err != nil {
		return fmt.Errorf("write opencode.json: %w", err)
	}

	return nil
}

// SeedAuthJSON writes opencode's auth.json (DESIGN2 §4 Harness.SeedAuth,
// bootstrap step 2). The caller (the opencode kind) composes the content
// from the harness's stored auth_json secret and provider integrations;
// empty data is a no-op.
func (a *Adapter) SeedAuthJSON(ctx context.Context, env ports.EnvID, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if err := a.envp.WriteFile(ctx, env, authPath, data, 0o600); err != nil {
		return fmt.Errorf("seed auth.json: %w", err)
	}
	return nil
}

// readSpecs cats every spec file out of the repo in one exec.
func (a *Adapter) readSpecs(ctx context.Context, env ports.EnvID) ([]*agentspec.Spec, error) {
	const marker = "===APOTHECARY-SPEC "
	script := `for f in .apothecary/agents/*.yaml .apothecary/agents/*.yml; do [ -e "$f" ] || continue; printf '\n` + marker + `%s\n' "$f"; cat "$f"; done`
	res, err := a.envp.Exec(ctx, env, ports.Command{Script: script, Dir: "repo"})
	if err != nil {
		return nil, fmt.Errorf("list agent specs: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("list agent specs: exit %d: %s", res.ExitCode, res.Output)
	}
	var specs []*agentspec.Spec
	// Line-anchored split: a spec whose body merely contains the marker text
	// mid-line must not corrupt parsing.
	chunks := strings.Split("\n"+res.Output, "\n"+marker)
	for _, chunk := range chunks[min(1, len(chunks)):] {
		file, body, ok := strings.Cut(chunk, "\n")
		if !ok {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(path.Base(strings.TrimSpace(file)), ".yaml"), ".yml")
		s, err := agentspec.Parse(stem, []byte(body))
		if err != nil {
			return nil, err
		}
		specs = append(specs, s)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

// agentMarkdown renders one opencode agent definition (frontmatter + prompt).
func agentMarkdown(s *agentspec.Spec) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "description: %q\n", s.Description)
	b.WriteString("mode: primary\n")
	b.WriteString("tools:\n")
	if s.ReadOnly {
		// read_only denies edit/patch but keeps write: §5.3's read-only
		// agents (planner, reviewer, redactor) must still create their
		// artifact outputs. write must be re-enabled EXPLICITLY — in
		// opencode, edit:false disables the whole file-modification tool
		// group. See IMPLEMENTATION_NOTES.
		b.WriteString("  edit: false\n  patch: false\n  write: true\n")
	}
	fmt.Fprintf(&b, "  bash: %v\n", s.AllowBash)
	b.WriteString("---\n")
	b.WriteString(s.Prompt)
	return b.String()
}

// --- AgentRunner ------------------------------------------------------------

func (a *Adapter) Run(ctx context.Context, env ports.EnvID, spec ports.AgentSpec, in ports.AgentInput) (ports.AgentResult, error) {
	if err := a.envp.WriteFile(ctx, env, promptPath, []byte(in.Prompt), 0o600); err != nil {
		return ports.AgentResult{}, fmt.Errorf("write prompt: %w", err)
	}
	// shellQuote, not %q: Go's %q double-quotes, inside which the shell
	// still expands $ and backticks (a model string like `openai/$(cmd)`
	// would execute).
	script := fmt.Sprintf(
		`set -a; [ -f /apothecary/provider.env ] && . /apothecary/provider.env; set +a
opencode run --format json --agent %s -m %s "$(cat %s)"`,
		shellQuote(spec.Agent), shellQuote(spec.Model), shellQuote(promptPath))
	return a.invoke(ctx, env, script)
}

func (a *Adapter) Resume(ctx context.Context, env ports.EnvID, session ports.SessionID, reply string) (ports.AgentResult, error) {
	if session == "" {
		return ports.AgentResult{}, fmt.Errorf("opencode resume: no session recorded for this stage")
	}
	if err := a.envp.WriteFile(ctx, env, replyPath, []byte(reply), 0o600); err != nil {
		return ports.AgentResult{}, fmt.Errorf("write reply: %w", err)
	}
	script := fmt.Sprintf(
		`set -a; [ -f /apothecary/provider.env ] && . /apothecary/provider.env; set +a
opencode run --format json --session %s "$(cat %s)"`,
		shellQuote(string(session)), shellQuote(replyPath))
	return a.invoke(ctx, env, script)
}

// shellQuote single-quotes s for POSIX sh (inert $, backticks, spaces).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (a *Adapter) invoke(ctx context.Context, env ports.EnvID, script string) (ports.AgentResult, error) {
	res, err := a.envp.Exec(ctx, env, ports.Command{Script: script, Dir: "repo"})
	if err != nil {
		return ports.AgentResult{}, err
	}
	result := parseEvents(res.Output)
	if res.ExitCode != 0 {
		// Failed invocations often produce no text events at all (startup
		// errors: unknown model, bad auth …) — the stage log and the
		// on_fail feed must carry the RAW output, not an empty transcript
		// (found live: a failed plan stage logged nothing).
		result.Output = res.Output
		return result, fmt.Errorf("opencode exit %d: %s", res.ExitCode, tail(res.Output, 2048))
	}
	return result, nil
}

// event mirrors the --format json NDJSON stream (opencode 1.17.8).
type event struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Type   string  `json:"type"`
		Text   string  `json:"text"`
		Cost   float64 `json:"cost"`
		Tokens struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
			Cache     struct {
				Write int `json:"write"`
				Read  int `json:"read"`
			} `json:"cache"`
		} `json:"tokens"`
	} `json:"part"`
}

// parseEvents folds the NDJSON stream into an AgentResult. Non-JSON lines
// (stderr noise appended by the exec transport) are skipped.
func parseEvents(output string) ports.AgentResult {
	var r ports.AgentResult
	var texts []string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.SessionID != "" && r.Session == "" {
			// First session id wins: late events can carry child/subtask
			// sessions, and Resume must target the primary one.
			r.Session = ports.SessionID(ev.SessionID)
		}
		switch ev.Type {
		case "step_finish":
			r.TurnsUsed++
			r.Usage.CostUSD += ev.Part.Cost
			// Cache writes were genuinely sent this turn; cache reads were
			// not — counting them would inflate input several-fold on long
			// sessions.
			r.Usage.InputTokens += ev.Part.Tokens.Input + ev.Part.Tokens.Cache.Write
			r.Usage.OutputTokens += ev.Part.Tokens.Output + ev.Part.Tokens.Reasoning
		case "text":
			if ev.Part.Text != "" {
				texts = append(texts, ev.Part.Text)
			}
		}
	}
	r.Output = strings.Join(texts, "\n")
	return r
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var _ ports.AgentRunner = (*Adapter)(nil)
