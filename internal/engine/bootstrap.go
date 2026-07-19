package engine

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"factory/internal/ports"
)

// BootstrapSpec parameterizes the §11 bootstrap sequence. It is pure port
// calls (no events), so `factoryd envcheck` can run it standalone; the run
// loop wraps it and maps failures to env_failed.
type BootstrapSpec struct {
	RunID     string
	Token     string // per-run bearer token for the ask_human shim
	EngineURL string

	RepoURL       string
	DefaultBranch string
	Branch        string
	AgentsMDPath  string

	// ProviderEnv are the provider API keys (§11 step 2).
	ProviderEnv map[string]string
	// GitToken, when set, is written as a credential-store entry for the
	// repo host (§11 step 3: the token never lands in .git/config).
	GitToken string

	// Prepare, when set, runs after the clone (§11 step 4): harness config
	// generation from .factory/agents/ (ports.HarnessPreparer).
	Prepare func(ctx context.Context, env ports.EnvID) error

	Setup   string // project.commands.setup (§11 step 6)
	Timeout time.Duration
}

// RunBootstrap executes the ordered §11 steps against a ready environment.
// Errors carry the step name and the output tail — the caller decides what
// they mean (the engine: env_failed).
func RunBootstrap(ctx context.Context, envp ports.EnvProvisioner, id ports.EnvID, spec BootstrapSpec) error {
	runEnv := fmt.Sprintf("FACTORY_RUN_ID=%s\nFACTORY_RUN_TOKEN=%s\nFACTORY_ENGINE_URL=%s\n",
		spec.RunID, spec.Token, spec.EngineURL)
	if err := envp.WriteFile(ctx, id, "/factory/run.env", []byte(runEnv), 0o600); err != nil {
		return fmt.Errorf("write run.env: %w", err)
	}

	keys := make([]string, 0, len(spec.ProviderEnv))
	for k := range spec.ProviderEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var providers strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&providers, "%s=%s\n", k, spec.ProviderEnv[k])
	}
	if err := envp.WriteFile(ctx, id, "/factory/provider.env", []byte(providers.String()), 0o600); err != nil {
		return fmt.Errorf("write provider.env: %w", err)
	}

	type step struct{ name, dir, script string }
	var steps []step
	if spec.GitToken != "" {
		cred, err := credentialLine(spec.RepoURL, spec.GitToken)
		if err != nil {
			return fmt.Errorf("git credentials: %w", err)
		}
		if err := envp.WriteFile(ctx, id, "/factory/git-credentials", []byte(cred+"\n"), 0o600); err != nil {
			return fmt.Errorf("write git-credentials: %w", err)
		}
		steps = append(steps, step{"git-credential-helper", "",
			`git config --global credential.helper "store --file=/factory/git-credentials"`})
	}
	steps = append(steps,
		step{"clone", "", fmt.Sprintf("git clone %q repo && cd repo && git switch -c %q origin/%s",
			spec.RepoURL, spec.Branch, spec.DefaultBranch)},
		// Harness config generation from .factory/agents/ lands with the
		// opencode adapter (M3); the AGENTS.md symlink is engine policy (§1).
		step{"agent-configs", "repo", agentConfigScript(spec.AgentsMDPath)},
		step{"git-config", "repo", `git config user.name "Factory" && git config user.email "factory@local"`},
	)
	for _, s := range steps {
		res, err := envp.Exec(ctx, id, ports.Command{Script: s.script, Dir: s.dir, Timeout: spec.Timeout})
		if err != nil {
			return fmt.Errorf("bootstrap %s: %w", s.name, err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("bootstrap %s: exit %d: %s", s.name, res.ExitCode, tail(res.Output, 8<<10))
		}
	}

	if spec.Prepare != nil {
		if err := spec.Prepare(ctx, id); err != nil {
			return fmt.Errorf("bootstrap harness-config: %w", err)
		}
	}

	res, err := envp.Exec(ctx, id, ports.Command{Script: spec.Setup, Dir: "repo", Timeout: spec.Timeout})
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("setup: exit %d: %s", res.ExitCode, tail(res.Output, 8<<10))
	}
	return nil
}

// credentialLine builds the git credential-store entry for the repo host.
func credentialLine(repoURL, token string) (string, error) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("cannot parse repo url %q", repoURL)
	}
	return fmt.Sprintf("%s://oauth2:%s@%s", u.Scheme, url.QueryEscape(token), u.Host), nil
}

func agentConfigScript(agentsMDPath string) string {
	script := "mkdir -p artifacts && echo artifacts/ >> .git/info/exclude"
	if agentsMDPath != "AGENTS.md" {
		script += fmt.Sprintf(" && ln -sf %q AGENTS.md && echo AGENTS.md >> .git/info/exclude", agentsMDPath)
	}
	return script
}
