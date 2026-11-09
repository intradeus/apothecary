// Package bootstrap prepares a fresh workspace for a run (§11): clone and
// branch, git identity and credentials, provider and harness secrets, agent
// definitions and skills. It is pure port calls and records no events, so the
// run loop, a rebuild and the standalone `apothecaryd envcheck` all run the
// same sequence; each caller maps a failure to its own outcome.
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/skills"
	"apothecary/internal/workspace/checkout"
)

// BootstrapSpec parameterizes the §11 bootstrap sequence. It is pure port
// calls (no events), so `apothecaryd envcheck` can run it standalone; the run
// loop wraps it and maps failures to env_failed.
type BootstrapSpec struct {
	RunID     string
	Token     string // per-run bearer token for the ask_human shim
	EngineURL string

	RepoURL string
	// SourceBranch is the branch the run's Branch forks off from
	// (`git switch -c <Branch> origin/<SourceBranch>`). Empty falls back to the
	// repo's default branch so standalone callers (envcheck) stay valid.
	SourceBranch string
	Branch       string
	// Fetch, when set, is the ref Branch starts from instead of forking
	// SourceBranch: the head of a merge request the run was created on.
	Fetch        string
	AgentsMDPath string

	// ProviderEnv are the provider API keys (§11 step 2).
	ProviderEnv map[string]string
	// Register, when set, is told every secret SeedAuth is about to write
	// (the run's redactor), before it is written.
	Register func(secret string)
	// SeedAuth, when set, writes harness-native auth material right after
	// provider.env (DESIGN2 §6.2, still step 2).
	SeedAuth func(ctx context.Context, env envport.EnvID, register func(secret string)) error
	// GitUser/GitPass form the credential-store entry for the repo host
	// (§11 step 3: the token never lands in .git/config); host-kind
	// specific (gitlab "oauth2", github "x-access-token"). Both empty =
	// public repo.
	GitUser string
	GitPass string

	// GitName/GitEmail set the commit author identity (§11 step 3 `git config
	// user.*`), sourced from the repository host integration. Either empty
	// falls back to the built-in "Apothecary"/"apothecary@local" default for
	// that field.
	GitName  string
	GitEmail string

	// Prepare, when set, runs after the clone (§11 step 4): harness config
	// generation for Agents (FLOWLANG §1, v3 — the flow carries them).
	Prepare func(ctx context.Context, env envport.EnvID, specs []*agentspec.Spec) error
	// Skills are the run's skills (TODO 38), laid down at skills.WorkspaceDir
	// one directory each, right before Prepare; SkillPins say which skillset
	// versions they came from (the manifest beside them). InstallSkills,
	// when set, then copies them into every harness's own directory.
	Skills        []skills.Tree
	SkillPins     []event.SkillsetPin
	InstallSkills func(ctx context.Context, env envport.EnvID, names []string) error
	// Agents are the flow's agent definitions, materialized by Prepare.
	Agents []*agentspec.Spec

	Timeout time.Duration

	// Extras are the run's additional repositories (multi-repo tasks),
	// cloned beside the primary and branched like it. Their hosts get
	// credential-store entries too.
	Extras []ExtraCheckout
}

// ExtraCheckout is one additional repository to check out: Dir is relative
// to the workspace home ("repos/<name>").
type ExtraCheckout struct {
	Name   string
	Dir    string
	URL    string
	Branch string
	Source string
	Fetch  string // as BootstrapSpec.Fetch
}

// WriteWorkspaceSecrets writes the credential material bootstrap step 2 owns:
// provider.env, the harnesses' native auth, and the git credential-store file.
// Split out of RunBootstrap because these are the files the engine WIPES when a
// run reaches a terminal state (ScrubWorkspaceSecrets) and must put back when a
// resumed run re-enters the same workspace — seeding and re-seeding have to
// stay one implementation or they drift.
func WriteWorkspaceSecrets(ctx context.Context, envp envport.EnvProvisioner, id envport.EnvID, spec BootstrapSpec) error {
	keys := make([]string, 0, len(spec.ProviderEnv))
	for k := range spec.ProviderEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var providers strings.Builder
	for _, k := range keys {
		// The file is sourced (`set -a; . provider.env`): values must be
		// shell-quoted or a key containing $, quotes or spaces breaks every
		// agent invocation.
		fmt.Fprintf(&providers, "%s=%s\n", k, shell.Quote(spec.ProviderEnv[k]))
	}
	if err := envp.WriteFile(ctx, id, "/apothecary/provider.env", []byte(providers.String()), 0o600); err != nil {
		return fmt.Errorf("write provider.env: %w", err)
	}
	if spec.SeedAuth != nil {
		register := spec.Register
		if register == nil {
			register = func(string) {}
		}
		if err := spec.SeedAuth(ctx, id, register); err != nil {
			return fmt.Errorf("seed harness auth: %w", err)
		}
	}
	if spec.GitPass != "" {
		// One store entry per distinct host: every repo of the run rides the
		// same repository host account.
		urls := []string{spec.RepoURL}
		for _, x := range spec.Extras {
			urls = append(urls, x.URL)
		}
		var lines []string
		seen := map[string]bool{}
		for _, u := range urls {
			cred, err := checkout.CredentialLine(u, spec.GitUser, spec.GitPass)
			if err != nil {
				return fmt.Errorf("git credentials: %w", err)
			}
			if !seen[cred] {
				seen[cred] = true
				lines = append(lines, cred)
			}
		}
		if err := envp.WriteFile(ctx, id, "/apothecary/git-credentials", []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			return fmt.Errorf("write git-credentials: %w", err)
		}
	}
	return nil
}

// RunBootstrap executes the ordered §11 steps against a ready environment.
// Errors carry the step name and the output tail — the caller decides what
// they mean (the engine: env_failed).
func RunBootstrap(ctx context.Context, envp envport.EnvProvisioner, id envport.EnvID, spec BootstrapSpec) error {
	runEnv := fmt.Sprintf("APOTHECARY_RUN_ID=%s\nAPOTHECARY_RUN_TOKEN=%s\nAPOTHECARY_ENGINE_URL=%s\n",
		spec.RunID, spec.Token, spec.EngineURL)
	if err := envp.WriteFile(ctx, id, "/apothecary/run.env", []byte(runEnv), 0o600); err != nil {
		return fmt.Errorf("write run.env: %w", err)
	}

	if err := WriteWorkspaceSecrets(ctx, envp, id, spec); err != nil {
		return err
	}

	type step struct{ name, dir, script string }
	var steps []step
	if spec.GitPass != "" {
		// The helper POINTER lives in ~/.gitconfig, which survives as long as
		// the container does — only the credential FILE is re-written when a
		// resumed run needs its secrets back.
		steps = append(steps, step{"git-credential-helper", "",
			`git config --global credential.helper "store --file=/apothecary/git-credentials"`})
	}
	steps = append(steps,
		// Idempotent for crash recovery: a restart mid-bootstrap re-runs the
		// whole sequence against the same workspace. An existing clone is
		// kept (it may hold agent commits); an existing branch is re-entered,
		// never recreated.
		step{"clone", "", fmt.Sprintf("[ -e repo/.git ] || git clone %q repo",
			spec.RepoURL)},
		// The provisioner ran the same step before the devcontainer build
		// (checkout.BranchScript); here it re-enters the branch.
		step{"branch", "repo", checkout.BranchScript(spec.Branch, spec.SourceBranch, spec.Fetch)},
		// The AGENTS.md symlink is engine policy (§1); agent configs are
		// generated by Harness.Prepare from the DB library (DESIGN3 §1).
		step{"agent-configs", "repo", agentConfigScript(spec.AgentsMDPath)},
		step{"git-config", "repo", gitConfigScript(spec.GitName, spec.GitEmail)},
	)
	// Additional repositories: siblings under ~/repos, each on the run's
	// branch forked from its own source. Idempotent like the primary.
	for _, x := range spec.Extras {
		steps = append(steps,
			step{"clone:" + x.Name, "", fmt.Sprintf("mkdir -p %s && { [ -e %s/.git ] || git clone %q %s; }",
				shell.Quote(filepath.Dir(x.Dir)), shell.Quote(x.Dir), x.URL, shell.Quote(x.Dir))},
			step{"branch:" + x.Name, x.Dir, checkout.BranchScript(x.Branch, x.Source, x.Fetch)},
			step{"git-config:" + x.Name, x.Dir, gitConfigScript(spec.GitName, spec.GitEmail)},
		)
	}
	// Egress is filtered externally by the per-project gateway, so bootstrap
	// steps need no proxy env — HTTP(S) just leaves and the gateway allows/denies
	// by hostname.
	for _, s := range steps {
		res, err := envp.Exec(ctx, id, envport.Command{Script: s.script, Dir: s.dir, Timeout: spec.Timeout})
		if err != nil {
			return fmt.Errorf("bootstrap %s: %w", s.name, err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("bootstrap %s: exit %d: %s", s.name, res.ExitCode, shell.Tail(res.Output, 8<<10))
		}
	}

	if err := writeSkills(ctx, envp, id, spec); err != nil {
		return fmt.Errorf("bootstrap skills: %w", err)
	}
	if spec.Prepare != nil {
		if err := spec.Prepare(ctx, id, spec.Agents); err != nil {
			return fmt.Errorf("bootstrap harness-config: %w", err)
		}
	}
	if spec.InstallSkills != nil && len(spec.Skills) > 0 {
		names := make([]string, 0, len(spec.Skills))
		for _, t := range spec.Skills {
			names = append(names, t.Name)
		}
		if err := spec.InstallSkills(ctx, id, names); err != nil {
			return fmt.Errorf("bootstrap skills: %w", err)
		}
	}
	return nil
}

// skillsManifest is what bootstrap writes beside the skill trees: which
// skillset versions the run pinned and which skills they amounted to.
type skillsManifest struct {
	Skillsets []event.SkillsetPin `json:"skillsets"`
	Skills    []string            `json:"skills"`
}

// writeSkills replaces skills.WorkspaceDir with the run's skill trees — the
// neutral copy every harness installs from — plus a manifest. Always
// starts from an empty directory (a re-bootstrap of the same container
// must not keep a previous selection); with no skills that is all it does.
func writeSkills(ctx context.Context, envp envport.EnvProvisioner, id envport.EnvID, spec BootstrapSpec) error {
	q := shell.Quote(skills.WorkspaceDir)
	res, err := envp.Exec(ctx, id, envport.Command{Script: "rm -rf " + q + " && mkdir -p " + q, Timeout: spec.Timeout})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("reset %s: exit %d: %s", skills.WorkspaceDir, res.ExitCode, shell.Tail(res.Output, 1<<10))
	}
	if len(spec.Skills) == 0 {
		return nil
	}
	m := skillsManifest{Skillsets: spec.SkillPins, Skills: []string{}}
	for _, t := range spec.Skills {
		if err := envp.WriteTree(ctx, id, skills.WorkspaceDir+"/"+t.Name, t.Files); err != nil {
			return fmt.Errorf("skill %s: %w", t.Name, err)
		}
		m.Skills = append(m.Skills, t.Name)
	}
	if m.Skillsets == nil {
		m.Skillsets = []event.SkillsetPin{}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := envp.WriteFile(ctx, id, skills.WorkspaceDir+"/manifest.json", append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	return nil
}

// SourceBranchOr returns the run's per-run source branch, or the repo default
// when the run predates SourceBranch (pre-source runs project it as "").
func SourceBranchOr(runSource, repoDefault string) string {
	if runSource != "" {
		return runSource
	}
	return repoDefault
}

func agentConfigScript(agentsMDPath string) string {
	// artifacts/ = the agent's own output dir; .apothecary/ = human-attached
	// artifacts (goal + ping answers) delivered by the engine. Both are
	// git-excluded so `git add -A` never stages them; kept SEPARATE so a
	// builder that cleans artifacts/ can't delete the human's inputs.
	script := "mkdir -p artifacts .apothecary/attachments" +
		" && { grep -qx artifacts/ .git/info/exclude 2>/dev/null || echo artifacts/ >> .git/info/exclude; }" +
		" && { grep -qx .apothecary/ .git/info/exclude 2>/dev/null || echo .apothecary/ >> .git/info/exclude; }"
	if agentsMDPath != "AGENTS.md" {
		script += fmt.Sprintf(" && ln -sf %q AGENTS.md && { grep -qx AGENTS.md .git/info/exclude 2>/dev/null || echo AGENTS.md >> .git/info/exclude; }", agentsMDPath)
	}
	return script
}

// gitConfigScript stamps the commit author identity. name/email come from the
// repository host integration; each empty field falls back to the generic
// default so a run always has a valid identity (a commit with no user.email
// fails).
func gitConfigScript(name, email string) string {
	if name == "" {
		name = "Apothecary"
	}
	if email == "" {
		email = "apothecary@local"
	}
	return fmt.Sprintf("git config user.name %s && git config user.email %s",
		shell.Quote(name), shell.Quote(email))
}

// encodeEnvFile renders a KEY=value env file (sorted keys, shell-quoted
// values) — the sourced /apothecary/*.env format. Shared so bootstrap and
// the reuse-path refresh produce byte-identical files.
func encodeEnvFile(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, shell.Quote(env[k]))
	}
	return b.String()
}
