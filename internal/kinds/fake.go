package kinds

import (
	"context"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
)

// Fake kinds back `serve -dev` and tests: the whole config plane works
// against them with no Coder/harness/forge. The fake harness honors §3.2
// output contracts by parsing the prompt's OUTPUT CONTRACT section (moved
// here from the v1 devAdapters in cmd/apothecaryd).

// RegisterFakes registers one "fake" kind per port.
func RegisterFakes(r *Registry) {
	r.RegisterForge(FakeForge{})
	r.RegisterProvisioner(FakeProvisioner{})
	r.RegisterHarness(FakeHarness{})
	r.RegisterTicketSource(FakeTicketSource{})
}

// --- forge ------------------------------------------------------------------

type FakeForge struct{}

func (FakeForge) Descriptor() Descriptor {
	return Descriptor{Port: "forge", Kind: "fake", Label: "Fake forge (dev)"}
}

type fakeAccount struct{}

func (fakeAccount) Test(context.Context) error { return nil }
func (fakeAccount) ListRepos(context.Context) ([]RepoInfo, error) {
	return []RepoInfo{{Name: "dev/fake-repo", URL: "https://forge.local/dev/fake-repo.git", Locator: "1", DefaultBranch: "main"}}, nil
}
func (fakeAccount) ListBranches(context.Context, string) ([]BranchInfo, error) {
	return []BranchInfo{{Name: "main", Default: true}, {Name: "develop"}}, nil
}
func (fakeAccount) GitCredential() (string, string) { return "", "" }
func (fakeAccount) GitIdentity() (string, string)   { return "", "" }
func (fakeAccount) EgressDomains() []string         { return nil }

func (FakeForge) Account(Config) (ForgeAccount, error) { return fakeAccount{}, nil }

func (FakeForge) Forge(Config, ports.RepoRef) (ports.Forge, error) {
	return &fake.Forge{}, nil
}

// --- provisioner ------------------------------------------------------------

type FakeProvisioner struct{}

func (FakeProvisioner) Descriptor() Descriptor {
	return Descriptor{Port: "provisioner", Kind: "fake", Label: "Fake workspaces (dev)"}
}

type fakeProvisioner struct{ *fake.EnvProvisioner }

func (fakeProvisioner) Test(context.Context) error { return nil }
func (fakeProvisioner) Close()                     {}

func (FakeProvisioner) New(Config, ProvisionerDeps) (Provisioner, error) {
	return fakeProvisioner{fake.NewEnvProvisioner()}, nil
}

// --- ticket source ----------------------------------------------------------

type FakeTicketSource struct{}

func (FakeTicketSource) Descriptor() Descriptor {
	return Descriptor{Port: "ticketsource", Kind: "fake", Label: "Fake tracker (dev)"}
}

// New serves a tiny fixed corpus so the import flow (search → pick → copy,
// attachments included) is exercisable in -dev with no Jira.
func (FakeTicketSource) New(Config) (ports.TicketSource, error) {
	return &fake.TicketSource{
		Tickets: []ports.SourceTicket{
			{
				Key: "DEV-1", Title: "Add a Farewell function",
				Body:   "The greeter only says hello.\n\n- [ ] add `Farewell(name string) string`\n- [ ] cover it in the tests",
				URL:    "https://tracker.local/browse/DEV-1",
				Status: "To Do", Type: "Story", Assignee: "Dev User",
				Updated: time.Now().Add(-2 * time.Hour),
				Attachments: []ports.SourceAttachment{
					{ID: "att-1", Filename: "spec.md", Mime: "text/markdown", Size: 42, URL: "https://tracker.local/att/1"},
				},
			},
			{
				Key: "DEV-2", Title: "Flaky test in the router suite",
				Body:   "`TestRoutes/redirect` fails about one run in five on CI.",
				URL:    "https://tracker.local/browse/DEV-2",
				Status: "In Progress", Type: "Bug", Assignee: "Dev User",
				Updated: time.Now().Add(-30 * time.Minute),
			},
		},
		Blobs: map[string][]byte{
			"att-1": []byte("# Farewell\n\nMirror Greet: `Farewell(name) => \"Goodbye, <name>!\"`.\n"),
		},
		// A small board, so a flow's tracker actions have somewhere to move
		// the issue: To Do → In Progress → In Review → Done (and back).
		Reachable: map[string][]string{
			"To Do":       {"In Progress"},
			"In Progress": {"In Review", "To Do"},
			"In Review":   {"Done", "In Progress"},
			"Done":        {"To Do"},
		},
	}, nil
}

var _ TicketSourceKind = FakeTicketSource{}

// --- harness ----------------------------------------------------------------

type FakeHarness struct{}

func (FakeHarness) Descriptor() Descriptor {
	return Descriptor{Port: "harness", Kind: "fake", Label: "Fake harness (dev)"}
}

func (FakeHarness) New(_ Config, envp ports.EnvProvisioner) (Harness, error) {
	runner := &fake.AgentRunner{}
	runner.RunFunc = func(ctx context.Context, call fake.RunCall) (ports.AgentResult, error) {
		select {
		case <-time.After(200 * time.Millisecond): // a little realism
		case <-ctx.Done():
			return ports.AgentResult{}, ctx.Err()
		}
		// A verdict output has a shape contract, so it needs real JSON rather
		// than the stub markdown every other declared file gets.
		verdict := declaredVerdict(call.In.Prompt)
		for _, path := range declaredOutputs(call.In.Prompt) {
			if path == verdict {
				continue
			}
			content := fmt.Sprintf("# Dev stub: %s\n\nWritten by the dev-mode agent %q.\n", path, call.Spec.Agent)
			if err := envp.WriteFile(ctx, call.Env, path, []byte(content), 0o644); err != nil {
				return ports.AgentResult{}, err
			}
		}
		if verdict != "" {
			v := []byte(`{"verdict":"APPROVE","comments":[]}`)
			if err := envp.WriteFile(ctx, call.Env, verdict, v, 0o644); err != nil {
				return ports.AgentResult{}, err
			}
		}
		return ports.AgentResult{TurnsUsed: 3, Usage: ports.Usage{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.01}}, nil
	}
	return &fakeHarness{runner: runner}, nil
}

type fakeHarness struct{ runner *fake.AgentRunner }

func (h *fakeHarness) Run(ctx context.Context, env ports.EnvID, spec ports.AgentSpec, in ports.AgentInput) (ports.AgentResult, error) {
	return h.runner.Run(ctx, env, spec, in)
}

func (h *fakeHarness) Resume(ctx context.Context, env ports.EnvID, s ports.SessionID, in ports.AgentInput) (ports.AgentResult, error) {
	return h.runner.Resume(ctx, env, s, in)
}

func (h *fakeHarness) Test(context.Context) error                  { return nil }
func (h *fakeHarness) EnvVars() map[string]string                  { return nil }
func (h *fakeHarness) Binary() string                              { return "true" }
func (h *fakeHarness) InstallLayer() []string                      { return nil }
func (h *fakeHarness) EgressDomains() []string                     { return nil }
func (h *fakeHarness) SeedAuth(context.Context, ports.EnvID) error { return nil }
func (h *fakeHarness) Prepare(_ context.Context, _ ports.EnvID, specs []*agentspec.Spec) error {
	h.runner.RecordPrepare(specs)
	return nil
}

// AcceptsAnyModel: the fake runs any model string; its Models are examples.
func (h *fakeHarness) AcceptsAnyModel() bool { return true }

func (h *fakeHarness) Models(context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "fake/fast-1", Name: "Fake · Fast 1"},
		{ID: "fake/smart-1", Name: "Fake · Smart 1"},
	}, nil
}

// declaredVerdict extracts the verdict output's path from the OUTPUT CONTRACT
// (FLOWLANG §4), which names it rather than hardcoding one — so a stage that
// rebinds the path still gets a valid verdict from the dev fake. "" when the
// stage's agent declares no verdict.
func declaredVerdict(prompt string) string {
	before, _, ok := strings.Cut(prompt, ` must contain exactly {"verdict"`)
	if !ok {
		return ""
	}
	// The path is the last token before the sentence.
	if i := strings.LastIndexAny(before, " \n"); i >= 0 {
		return before[i+1:]
	}
	return before
}

// declaredOutputs extracts the artifact paths an agent must write, parsed from
// the "# OUTPUT CONTRACT" bullet list (FLOWLANG §4). Each bullet is "- <path>"
// optionally followed by " — <name>: <desc>". Empty when the stage declares no
// file outputs.
func declaredOutputs(prompt string) []string {
	_, rest, ok := strings.Cut(prompt, "you MUST write these files (relative to repo root):\n")
	if !ok {
		return nil
	}
	var paths []string
	for line := range strings.SplitSeq(rest, "\n") {
		if !strings.HasPrefix(line, "- ") {
			break // the bullet list ends at the first non-bullet line
		}
		// The path is the first whitespace-delimited token after "- ".
		path, _, _ := strings.Cut(strings.TrimPrefix(line, "- "), " ")
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}
