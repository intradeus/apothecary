package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
)

// Dynamic envcheck (DESIGN2 §9): the workspace compliance gate. The check
// list is DERIVED from the project's resolution — adapters declare their
// egress domains, the harness its binary, the project its commands — so
// whatever the user customizes, this proves the contract deterministically.
// RunEnvCheck is standalone (CLI reuse); the Engine wraps it with an
// in-memory registry for the API.

type CheckStep struct {
	ID     string `json:"id"`
	Status string `json:"status"` // pass|fail|skip
	Detail string `json:"detail,omitempty"`
}

// CheckRun is an envcheck job snapshot (API shape).
type CheckRun struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"` // running|pass|fail|cancelled
	Keep      bool   `json:"keep"`
	// TearingDown: the verdict is in, the throwaway workspace is still being
	// destroyed (a destroy step follows). Another check can start meanwhile.
	TearingDown bool        `json:"tearing_down,omitempty"`
	Steps       []CheckStep `json:"steps"`
	StartedAt   time.Time   `json:"started_at"`
	FinishedAt  *time.Time  `json:"finished_at,omitempty"`
}

// EnvCheckOptions parameterize a check pass.
type EnvCheckOptions struct {
	EngineURL  string // written into run.env for the shim
	EnginePort int    // tunnel probe target (workspace-local loopback)
	Keep       bool   // keep the workspace for inspection
	Timeout    time.Duration
	// Agents are the agent definitions to materialize during the bootstrap
	// step, taken from the project's default flow (FLOWLANG §1, v3 — agents
	// belong to a flow, not a project). Empty simply generates no configs.
	Agents []*agentspec.Spec
	// Stages are the default flow's stages, for checking each agent stage's
	// model against what the agent in the workspace actually offers.
	Stages []flow.Stage
}

// RunEnvCheck provisions, bootstraps and verifies one throwaway
// environment, emitting steps as they complete, then tears it down. Returns
// the failure count.
func RunEnvCheck(ctx context.Context, res *Resolution, runID string, opts EnvCheckOptions, emit func(CheckStep)) int {
	failed, teardown := runEnvCheck(ctx, res, runID, opts, emit)
	return failed + teardown()
}

// envcheckProbes bounds the egress probes running at once: each is a curl
// of up to 10 s in the workspace, and a derived allowlist runs to dozens.
const envcheckProbes = 8

// runEnvCheck runs every probe and returns their failure count together
// with the teardown still to do: the verdict does not wait on destroying
// the workspace. A cancelled (or timed-out) ctx stops the remaining probes;
// one step says why, and the rest are not reported. The teardown always
// runs on its own context, so a cancelled check still cleans up.
func runEnvCheck(ctx context.Context, res *Resolution, runID string, opts EnvCheckOptions, emit func(CheckStep)) (int, func() int) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var mu sync.Mutex // probes run concurrently; emit and the count are shared
	failed := 0
	stopped := false
	step := func(id string, err error, detail string) bool {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return false
		}
		if cerr := ctx.Err(); cerr != nil {
			// Report the stop once, not one failure per step it cut short.
			stopped = true
			failed++
			why := "cancelled"
			if errors.Is(cerr, context.DeadlineExceeded) {
				why = fmt.Sprintf("timed out after %s", opts.Timeout)
			}
			emit(CheckStep{ID: "stopped", Status: "fail", Detail: why + " during " + id})
			return false
		}
		s := CheckStep{ID: id, Status: "pass", Detail: detail}
		if err != nil {
			failed++
			s.Status = "fail"
			s.Detail = err.Error()
		}
		emit(s)
		return err == nil
	}
	skip := func(id, why string) {
		mu.Lock()
		defer mu.Unlock()
		if !stopped {
			emit(CheckStep{ID: id, Status: "skip", Detail: why})
		}
	}
	noTeardown := func() int { return 0 }

	envp := res.Envp
	branch := "apothecary/" + strings.ToLower(runID)

	name := workspaceName(branch, runID)
	env, err := envp.Provision(ctx, ports.EnvSpec{
		RunID:   runID,
		RepoURL: res.RepoRef.URL,
		Branch:  branch,
		Name:    name,
		Project: res.ProjectName,
		// The workspace a run would get: a repo-authored devcontainer
		// installs the harness itself, so without this the harness_binary
		// probe could only ever fail on images that don't ship it.
		HarnessInstall:       res.HarnessInstall,
		EgressDomains:        res.EgressDomains,
		EgressMode:           res.EgressMode,
		EgressDenylist:       res.EgressDenylist,
		EgressBlocked:        res.EgressBlocked,
		EgressAllowedCIDRs:   res.EgressAllowedCIDRs,
		EgressAllowedPorts:   res.EgressAllowedPorts,
		CodeServerExtensions: res.CodeServerExtensions,
		GitUser:              res.GitUser,
		GitPass:              res.GitPass,
	})
	if !step("provision", err, string(env)) {
		if opts.Keep {
			return failed, noTeardown
		}
		// A failed or cancelled provision returns no id, but may have built
		// half a workspace (a sandbox, a volume, an egress network) under
		// the name it was given — which is its identity. Remove by name.
		if env == "" {
			env = ports.EnvID(name)
		}
		return failed, func() int { return destroyEnv(envp, env, emit) }
	}
	teardown := func() int { return destroyEnv(envp, env, emit) }
	if opts.Keep {
		teardown = func() int {
			emit(CheckStep{ID: "destroy", Status: "skip", Detail: "kept for inspection"})
			return 0
		}
	}

	step("bootstrap", RunBootstrap(ctx, envp, env, BootstrapSpec{
		RunID:        runID,
		Token:        "envcheck-token",
		EngineURL:    opts.EngineURL,
		RepoURL:      res.RepoRef.URL,
		SourceBranch: res.RepoRef.DefaultBranch,
		Branch:       branch,
		AgentsMDPath: res.AgentsMDPath,
		ProviderEnv:  res.ProviderEnv,
		SeedAuth:     res.SeedAuth,
		GitUser:      res.GitUser,
		GitPass:      res.GitPass,
		GitName:      res.GitName,
		GitEmail:     res.GitEmail,
		Prepare:      res.Prepare,
		Agents:       opts.Agents,
		Setup:        res.Commands["setup"],
		Timeout:      10 * time.Minute,
		Extras:       envcheckExtras(res, branch),
	}), "clone + seed auth + prepare + setup")

	if verify, ok := res.Commands["verify"]; ok && verify != "" {
		step("verify", execExpect(ctx, envp, env, verify, "repo", true), "")
	} else {
		skip("verify", "project.commands.verify not configured")
	}

	// Egress negative: in allow mode a non-allowlisted host must be blocked by
	// the per-project gateway. In deny mode everything but the denylist is
	// allowed, so example.com is expected to be reachable — skip the negative.
	if res.EgressMode == "deny" {
		skip("egress_deny", "deny mode: only the denylist is blocked; a generic host is expected reachable")
	} else {
		step("egress_deny", execExpect(ctx, envp, env,
			"curl -m 8 -s -o /dev/null https://example.com", "", false), "non-allowlisted host blocked by gateway")
	}

	// Egress positives: one per derived allowlist entry. TCP+TLS connect is
	// the criterion — any HTTP status counts; connectivity, not auth.
	sem := make(chan struct{}, envcheckProbes)
	var wg sync.WaitGroup
	for _, domain := range res.EgressDomains {
		if strings.HasPrefix(domain, "*.") {
			// A pattern names no host to connect to; curl'ing it literally
			// fails as a malformed URL (exit 3), which is not an egress fact.
			skip("egress:"+domain, "wildcard pattern — no single host to probe")
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(domain string) {
			defer wg.Done()
			defer func() { <-sem }()
			step("egress:"+domain, execExpect(ctx, envp, env,
				fmt.Sprintf("curl -m 10 -s -o /dev/null https://%s", domain), "", true), "")
		}(domain)
	}
	wg.Wait()
	step("repo_reachable", execExpect(ctx, envp, env,
		"git ls-remote --heads origin >/dev/null", "repo", true), "")

	// Per-step harness: probe every harness in the set — any of them may run.
	binaries := res.HarnessBinaries
	if len(binaries) == 0 && res.HarnessBinary != "" {
		binaries = []string{res.HarnessBinary}
	}
	if len(binaries) == 0 {
		skip("harness_binary", "harness declares no binary")
	}
	for _, b := range binaries {
		if b == "" {
			continue
		}
		name := "harness_binary"
		if len(binaries) > 1 {
			name = "harness_binary:" + b
		}
		step(name, execExpect(ctx, envp, env, "command -v "+b, "", true), b)
	}
	// The agent's own model list is the authoritative one: the installed
	// harness may not know a model its provider still serves.
	if len(opts.Stages) == 0 {
		skip("agent_models", "no flow stages to check")
	} else {
		missing, unasked := agentModelCheck(ctx, res, env, opts.Stages)
		var merr error
		switch {
		case len(missing) > 0:
			merr = errors.New(strings.Join(missing, "; "))
		case len(unasked) > 0:
			merr = fmt.Errorf("could not ask the agent: %s", strings.Join(unasked, "; "))
		}
		step("agent_models", merr, "every agent stage's model is offered by its agent")
	}
	step("shim", execExpect(ctx, envp, env,
		"test -x /opt/apothecary/shim", "", true), "")
	// Loopback parity holds on every provisioner (coder: reverse tunnel;
	// devcontainer: in-container relay over the mounted unix socket), so
	// one probe fits all.
	step("tunnel", execExpect(ctx, envp, env,
		fmt.Sprintf("curl -m 10 -s -o /dev/null http://127.0.0.1:%d/", opts.EnginePort), "", true),
		"engine reachable from the workspace's own loopback")

	blob := []byte("round-trip \x00\x01\xfe binary ok\n")
	step("rw_absolute", rwRoundTrip(ctx, envp, env, "/apothecary/roundtrip.bin", blob), "")
	step("rw_relative", rwRoundTrip(ctx, envp, env, "artifacts/roundtrip.txt", []byte("hello apothecary\n")), "")

	links, lerr := envp.Links(ctx, env)
	detail := ""
	for _, l := range links {
		detail += l.Kind + " "
	}
	step("links", lerr, strings.TrimSpace(detail))

	mu.Lock()
	defer mu.Unlock()
	return failed, teardown
}

// envcheckExtras clones the project's extra repos too: a run would, and a
// clone the token cannot reach is exactly what this check is for.
func envcheckExtras(res *Resolution, branch string) []ExtraCheckout {
	var out []ExtraCheckout
	for _, x := range res.ExtraRepos {
		out = append(out, ExtraCheckout{Name: x.Name, Dir: "repos/" + x.Name, URL: x.URL, Branch: branch, Source: x.DefaultBranch})
	}
	return out
}

// destroyEnv removes a check's workspace on a context of its own: a
// cancelled check must still clean up after itself.
func destroyEnv(envp ports.EnvProvisioner, env ports.EnvID, emit func(CheckStep)) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	s := CheckStep{ID: "destroy", Status: "pass"}
	if err := envp.Destroy(ctx, env); err != nil {
		s.Status, s.Detail = "fail", err.Error()
		emit(s)
		return 1
	}
	emit(s)
	return 0
}

func execExpect(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, script, dir string, wantSuccess bool) error {
	res, err := envp.Exec(ctx, env, ports.Command{Script: script, Dir: dir, Timeout: 3 * time.Minute})
	if err != nil {
		return err
	}
	if wantSuccess && res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, tail(res.Output, 400))
	}
	if !wantSuccess && res.ExitCode == 0 {
		return fmt.Errorf("succeeded but should have been blocked")
	}
	return nil
}

func rwRoundTrip(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, path string, data []byte) error {
	if err := envp.WriteFile(ctx, env, path, data, 0o600); err != nil {
		return err
	}
	back, err := envp.ReadFile(ctx, env, path)
	if err != nil {
		return err
	}
	if !bytes.Equal(back, data) {
		return fmt.Errorf("content mismatch: wrote %d bytes, read %d", len(data), len(back))
	}
	return nil
}

// --- engine-side job registry (API surface, §9) -----------------------------

type envCheckState struct {
	snap   CheckRun
	cancel context.CancelFunc
}

// StartEnvCheck launches an envcheck for the project. One runs at a time per
// project: starting another SUPERSEDES the one running (it is cancelled and
// cleans up after itself) rather than making the caller wait it out — the
// usual reason to start a check is that the project just changed. Returns
// the check id immediately (202 semantics).
func (e *Engine) StartEnvCheck(ctx context.Context, projectID string, keep bool) (string, error) {
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return "", err
	}
	// Agents ride the project's default flow (FLOWLANG §1, v3), so the probe
	// generates the same harness configs a real run would. A flow that no
	// longer resolves is not this check's business — it probes the
	// environment, so fall through with no agents rather than refusing.
	var agents []*agentspec.Spec
	var stages []flow.Stage
	if fl, ferr := e.flows.Get(ctx, res.ProjectID, res.DefaultFlow); ferr == nil {
		agents = fl.AgentSpecs()
		stages = fl.Stages
	}
	id := e.newID()
	st := &envCheckState{snap: CheckRun{
		ID: id, ProjectID: projectID, Status: "running", Keep: keep, StartedAt: e.clock(),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	st.cancel = cancel
	// One lock section for supersede + insert: two concurrent calls must not
	// both leave a running check behind.
	e.mu.Lock()
	for _, other := range e.envchecks {
		if other.snap.ProjectID == projectID && other.snap.Status == "running" {
			e.cancelEnvCheckLocked(other, "superseded by a newer check")
		}
	}
	e.envchecks[id] = st
	e.envcheckOrder = append(e.envcheckOrder, id)
	// Keep the last 20 finished (§9: diagnostics, not history) — never evict
	// a running job: pollers would 404 mid-run and the duplicate guard above
	// would stop seeing it.
	for i := 0; len(e.envcheckOrder) > 20 && i < len(e.envcheckOrder); {
		cand := e.envcheckOrder[i]
		if e.envchecks[cand].snap.Status == "running" {
			i++
			continue
		}
		delete(e.envchecks, cand)
		e.envcheckOrder = append(e.envcheckOrder[:i], e.envcheckOrder[i+1:]...)
	}
	e.mu.Unlock()

	enginePort := enginePortOf(e.cfg.EngineURL)
	checkRunID := "envck" + strings.ToLower(id[max(0, len(id)-11):])
	// Egress is enforced by the per-project gateway the provisioner brings up at
	// Provision time (from the EnvSpec egress fields) — nothing to prime here.
	go func() {
		defer res.release()
		defer cancel()
		emit := func(s CheckStep) {
			e.mu.Lock()
			st.snap.Steps = append(st.snap.Steps, s)
			e.mu.Unlock()
		}
		failed, teardown := runEnvCheck(ctx, res, checkRunID,
			EnvCheckOptions{EngineURL: e.cfg.EngineURL, EnginePort: enginePort, Keep: keep, Agents: agents, Stages: stages}, emit)
		// The verdict is published now; destroying the workspace can take
		// minutes and says nothing about the project.
		now := e.clock()
		e.mu.Lock()
		if st.snap.Status == "running" {
			st.snap.FinishedAt = &now
			if failed > 0 {
				st.snap.Status = "fail"
			} else {
				st.snap.Status = "pass"
			}
		}
		st.snap.TearingDown = true
		e.mu.Unlock()
		teardown()
		e.mu.Lock()
		st.snap.TearingDown = false
		e.mu.Unlock()
	}()
	return id, nil
}

// cancelEnvCheckLocked stops a running check (e.mu held). Its goroutine
// winds down and still destroys its workspace.
func (e *Engine) cancelEnvCheckLocked(st *envCheckState, why string) {
	if st.snap.Status != "running" {
		return
	}
	now := e.clock()
	st.snap.Status = "cancelled"
	st.snap.FinishedAt = &now
	st.snap.Steps = append(st.snap.Steps, CheckStep{ID: "cancelled", Status: "skip", Detail: why})
	if st.cancel != nil {
		st.cancel()
	}
}

// CancelEnvCheck stops a running check. ok=false: no such check.
func (e *Engine) CancelEnvCheck(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.envchecks[id]
	if !ok {
		return false
	}
	e.cancelEnvCheckLocked(st, "cancelled")
	return true
}

// LatestEnvCheck is the project's most recent check (running or not), so a
// page opened mid-check can follow it.
func (e *Engine) LatestEnvCheck(projectID string) (CheckRun, bool) {
	e.mu.Lock()
	var id string
	for i := len(e.envcheckOrder) - 1; i >= 0; i-- {
		if st := e.envchecks[e.envcheckOrder[i]]; st != nil && st.snap.ProjectID == projectID {
			id = e.envcheckOrder[i]
			break
		}
	}
	e.mu.Unlock()
	if id == "" {
		return CheckRun{}, false
	}
	return e.EnvCheck(id)
}

// enginePortOf extracts the port from the engine URL, honoring scheme
// defaults ("https://engine.example.com" → 443, not the scheme colon).
func enginePortOf(engineURL string) int {
	u, err := url.Parse(engineURL)
	if err != nil {
		return 8080
	}
	if p := u.Port(); p != "" {
		n := 0
		fmt.Sscanf(p, "%d", &n)
		if n > 0 {
			return n
		}
	}
	switch u.Scheme {
	case "https":
		return 443
	case "http":
		return 80
	}
	return 8080
}

// EnvCheck returns a snapshot of a check run.
func (e *Engine) EnvCheck(id string) (CheckRun, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.envchecks[id]
	if !ok {
		return CheckRun{}, false
	}
	snap := st.snap
	snap.Steps = append([]CheckStep(nil), st.snap.Steps...)
	return snap, true
}
