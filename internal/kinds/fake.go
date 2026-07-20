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
func (fakeAccount) GitCredential() (string, string) { return "", "" }
func (fakeAccount) EgressDomains() []string         { return nil }

func (FakeForge) Account(Config) (ForgeAccount, error) { return fakeAccount{}, nil }

func (FakeForge) Forge(Config, ports.RepoRef) (ports.Forge, error) {
	return &fake.Forge{
		// A legacy repo spec so the DESIGN3 §1.3 import path is exercisable
		// in dev mode and tests.
		Files: map[string][]byte{
			".apothecary/agents/sample.yaml": []byte(
				"name: sample\ndescription: imported sample\nread_only: true\nallow_bash: false\nprompt: |\n  You are the imported sample agent.\n"),
		},
	}, nil
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
		if path, ok := declaredOutput(call.In.Prompt); ok {
			content := fmt.Sprintf("# Dev stub: %s\n\nWritten by the dev-mode agent %q.\n", path, call.Spec.Agent)
			if err := envp.WriteFile(ctx, call.Env, path, []byte(content), 0o644); err != nil {
				return ports.AgentResult{}, err
			}
		}
		if strings.Contains(call.In.Prompt, "artifacts/verdict.json") {
			v := []byte(`{"verdict":"APPROVE","comments":[]}`)
			if err := envp.WriteFile(ctx, call.Env, "artifacts/verdict.json", v, 0o644); err != nil {
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

func (h *fakeHarness) Resume(ctx context.Context, env ports.EnvID, s ports.SessionID, reply string) (ports.AgentResult, error) {
	return h.runner.Resume(ctx, env, s, reply)
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
func (h *fakeHarness) Models(context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "fake/fast-1", Name: "Fake · Fast 1"},
		{ID: "fake/smart-1", Name: "Fake · Smart 1"},
	}, nil
}

// declaredOutput extracts the artifact path from the §3.2 contract line
// "Before finishing you MUST write <path> (relative to repo root)."
func declaredOutput(prompt string) (string, bool) {
	_, rest, ok := strings.Cut(prompt, "you MUST write ")
	if !ok {
		return "", false
	}
	path, _, ok := strings.Cut(rest, " (relative to repo root)")
	if !ok || strings.ContainsAny(path, " \n") {
		return "", false
	}
	return path, true
}
