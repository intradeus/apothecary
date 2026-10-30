package kinds

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/agentspec"
	"apothecary/internal/ports"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/reviewfile"
)

// Fake kinds back `serve -dev` and tests: the whole config plane works against
// them with no provisioner/harness/repository host. The fake harness honors §3.2
// output contracts by parsing the prompt's OUTPUT CONTRACT section (moved here
// from the v1 devAdapters in cmd/apothecaryd).

// RegisterFakes registers one "fake" kind per port.
func RegisterFakes(r *Registry) {
	r.RegisterRepoHost(FakeRepoHost{})
	r.RegisterProvisioner(FakeProvisioner{})
	r.RegisterHarness(FakeHarness{})
	r.RegisterTicketSource(FakeTicketSource{})
}

// --- RepoHost ---------------------------------------------------------------

type FakeRepoHost struct{}

func (FakeRepoHost) Descriptor() Descriptor {
	return Descriptor{Port: "repository", Kind: "fake", Label: "Fake repositories (dev)"}
}

type fakeAccount struct{}

func (fakeAccount) Test(context.Context) error { return nil }
func (fakeAccount) ListRepos(context.Context) ([]RepoInfo, error) {
	return []RepoInfo{{Name: "dev/fake-repo", URL: "https://git.local/dev/fake-repo.git", Locator: "1", DefaultBranch: "main"}}, nil
}
func (fakeAccount) ListBranches(context.Context, string) ([]BranchInfo, error) {
	return []BranchInfo{{Name: "main", Default: true}, {Name: "develop"}}, nil
}
func (fakeAccount) GitCredential() (string, string) { return "", "" }
func (fakeAccount) GitIdentity() (string, string)   { return "", "" }
func (fakeAccount) EgressDomains() []string         { return nil }

func (FakeRepoHost) Account(Config) (RepoAccount, error) { return fakeAccount{}, nil }

// fakeRepoHosts keeps one fake repository host per repository locator for the
// life of the process, so what a run opens on it is still there when the next
// resolution asks — the way a real repository host would remember it.
var fakeRepoHosts sync.Map // locator -> *fake.RepoHost

func (FakeRepoHost) RepoHost(_ Config, repo repoport.RepoRef) (repoport.RepoHost, error) {
	return FakeRepoHostOf(repo.Locator), nil
}

// FakeRepoHostOf is the fake repository host of a repository locator (tests
// script merge requests on it).
func FakeRepoHostOf(locator string) *fake.RepoHost {
	f, _ := fakeRepoHosts.LoadOrStore(locator, &fake.RepoHost{})
	return f.(*fake.RepoHost)
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

func (FakeHarness) New(_ Config, envp envport.EnvProvisioner) (Harness, error) {
	runner := &fake.AgentRunner{}
	runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		select {
		case <-time.After(200 * time.Millisecond): // a little realism
		case <-ctx.Done():
			return agentport.AgentResult{}, ctx.Err()
		}
		// An output with a shape contract gets a document that holds it —
		// a verdict, an empty review, a reply per thread, a commit message —
		// in the format its path says; every other declared file a stub.
		for _, out := range declaredOutputs(call.In.Prompt) {
			content, err := stubOutput(out, call)
			if err != nil {
				return agentport.AgentResult{}, err
			}
			if err := envp.WriteFile(ctx, call.Env, out.path, content, 0o644); err != nil {
				return agentport.AgentResult{}, err
			}
		}
		return agentport.AgentResult{TurnsUsed: 3, Usage: agentport.Usage{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.01}}, nil
	}
	return &fakeHarness{runner: runner}, nil
}

type fakeHarness struct{ runner *fake.AgentRunner }

func (h *fakeHarness) Run(ctx context.Context, env envport.EnvID, spec agentport.AgentSpec, in agentport.AgentInput) (agentport.AgentResult, error) {
	return h.runner.Run(ctx, env, spec, in)
}

func (h *fakeHarness) Resume(ctx context.Context, env envport.EnvID, s agentport.SessionID, in agentport.AgentInput) (agentport.AgentResult, error) {
	return h.runner.Resume(ctx, env, s, in)
}

func (h *fakeHarness) Test(context.Context) error                                  { return nil }
func (h *fakeHarness) EnvVars() map[string]string                                  { return nil }
func (h *fakeHarness) Binary() string                                              { return "true" }
func (h *fakeHarness) EgressDomains() []string                                     { return nil }
func (h *fakeHarness) SeedAuth(context.Context, envport.EnvID, func(string)) error { return nil }
func (h *fakeHarness) Prepare(_ context.Context, _ envport.EnvID, specs []*agentspec.Spec) error {
	h.runner.RecordPrepare(specs)
	return nil
}

func (h *fakeHarness) InstallSkills(_ context.Context, _ envport.EnvID, names []string) error {
	h.runner.RecordInstallSkills(names)
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

// declaredOutput is one file the OUTPUT CONTRACT names: its path and, for a
// structured one, its type.
type declaredOutput struct{ path, typ string }

// declaredOutputs extracts the files an agent must write, parsed from the
// "# OUTPUT CONTRACT" bullet list (FLOWLANG §4). Each bullet is "- <path>"
// optionally followed by " — <name> (<type>): <desc>" — the type only for a
// structured file, so the dev fake writes one that holds its shape wherever
// a stage binds it, never a hardcoded path. Empty when the stage declares no
// file outputs.
func declaredOutputs(prompt string) []declaredOutput {
	_, rest, ok := strings.Cut(prompt, "you MUST write these files (relative to repo root):\n")
	if !ok {
		return nil
	}
	var outs []declaredOutput
	for line := range strings.SplitSeq(rest, "\n") {
		if !strings.HasPrefix(line, "- ") {
			break // the bullet list ends at the first non-bullet line
		}
		// The path is the first whitespace-delimited token after "- "; the
		// type the parenthesised word after the role.
		path, after, _ := strings.Cut(strings.TrimPrefix(line, "- "), " ")
		if path == "" {
			continue
		}
		out := declaredOutput{path: path}
		if _, paren, ok := strings.Cut(after, " ("); ok {
			if typ, _, ok := strings.Cut(paren, ")"); ok && agentspec.IsStructuredType(typ) {
				out.typ = typ
			}
		}
		outs = append(outs, out)
	}
	return outs
}

// stubOutput is what the dev fake writes for one declared output: a
// document holding the type's shape, in the format the path says.
func stubOutput(out declaredOutput, call fake.RunCall) ([]byte, error) {
	switch out.typ {
	case agentspec.TypeVerdict:
		return reviewfile.Encode(out.path, map[string]any{"verdict": "APPROVE", "comments": []string{}})
	case agentspec.TypeReviewThreads:
		return reviewfile.Encode(out.path, reviewfile.Review{Summary: "Dev stub review: nothing to note."})
	case agentspec.TypeReviewReplies:
		// One reply per thread of the mr_threads input the prompt inlines —
		// the contract says every thread is answered.
		replies := []reviewfile.Reply{}
		for _, id := range promptThreadIDs(call.In.Prompt) {
			replies = append(replies, reviewfile.Reply{Thread: id, Did: reviewfile.DidAnswered, Reply: "Dev stub reply."})
		}
		return reviewfile.Encode(out.path, replies)
	case agentspec.TypeCommitMessage:
		return reviewfile.Encode(out.path, map[string]any{"subject": "dev stub: " + call.Spec.Agent})
	}
	return []byte(fmt.Sprintf("# Dev stub: %s\n\nWritten by the dev-mode agent %q.\n", out.path, call.Spec.Agent)), nil
}

// promptThreadIDs reads the thread ids of every mr_threads file a prompt
// inlines: each "# INPUT: … (file: <path>.yaml)" section is tried as one.
func promptThreadIDs(prompt string) []string {
	var ids []string
	sections := strings.Split(prompt, "\n# ")
	for _, sec := range sections {
		head, body, ok := strings.Cut(sec, "\n")
		head = strings.TrimPrefix(head, "# ") // the first section has no newline before it
		if !ok || !strings.HasPrefix(head, "INPUT: ") {
			continue
		}
		_, path, ok := strings.Cut(head, "(file: ")
		if !ok {
			continue
		}
		path = strings.TrimSuffix(path, ")")
		if th, err := reviewfile.ParseThreads(path, []byte(body)); err == nil {
			ids = append(ids, th.IDs()...)
		}
	}
	return ids
}
