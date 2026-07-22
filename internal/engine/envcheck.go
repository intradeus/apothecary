package engine

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

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
	ID         string      `json:"id"`
	ProjectID  string      `json:"project_id"`
	Status     string      `json:"status"` // running|pass|fail
	Keep       bool        `json:"keep"`
	Steps      []CheckStep `json:"steps"`
	StartedAt  time.Time   `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
}

// EnvCheckOptions parameterize a check pass.
type EnvCheckOptions struct {
	EngineURL  string // written into run.env for the shim
	EnginePort int    // tunnel probe target (workspace-local loopback)
	Keep       bool   // keep the workspace for inspection
	Timeout    time.Duration
	// ProxyEnv is the workspace's HTTP(S)_PROXY environment when the engine
	// runs in egress-proxy mode (M18): written to proxy.env so the check's
	// clone + egress probes route through the proxy exactly as a real run
	// does. Empty ⇒ allowlist mode (direct egress through the pinned
	// firewall). The caller must also install the proxy allowlist policy for
	// this check's run id.
	ProxyEnv map[string]string
}

// RunEnvCheck provisions, bootstraps and verifies one throwaway
// environment, emitting steps as they complete. Returns the failure count.
// The named return matters: the deferred destroy step must be able to count
// its own failure.
func RunEnvCheck(ctx context.Context, res *Resolution, runID string, opts EnvCheckOptions, emit func(CheckStep)) (failed int) {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	step := func(id string, err error, detail string) bool {
		s := CheckStep{ID: id, Status: "pass", Detail: detail}
		if err != nil {
			failed++
			s.Status = "fail"
			s.Detail = err.Error()
		}
		emit(s)
		return err == nil
	}
	skip := func(id, why string) { emit(CheckStep{ID: id, Status: "skip", Detail: why}) }

	envp := res.Envp
	branch := "apothecary/" + strings.ToLower(runID)

	// Image pipeline (§8) — only on the managed-template path.
	if res.EnsureImage != nil {
		cached, err := res.EnsureImage(ctx)
		if !step("image", err, fmt.Sprintf("%s (cached=%v)", res.Image, cached)) {
			return failed
		}
	} else {
		skip("image", "custom template: the template bakes its own image")
	}

	env, err := envp.Provision(ctx, ports.EnvSpec{
		RunID:         runID,
		RepoURL:       res.RepoRef.URL,
		Branch:        branch,
		Name:          workspaceName(branch, runID),
		Image:         res.Image,
		EgressDomains: res.EgressDomains,
		GitUser:       res.GitUser,
		GitPass:       res.GitPass,
	})
	if !step("provision", err, string(env)) {
		return failed
	}
	if !opts.Keep {
		defer func() {
			derr := envp.Destroy(context.WithoutCancel(ctx), env)
			step("destroy", derr, "")
		}()
	} else {
		defer skip("destroy", "kept for inspection")
	}

	step("bootstrap", RunBootstrap(ctx, envp, env, BootstrapSpec{
		RunID:         runID,
		Token:         "envcheck-token",
		EngineURL:     opts.EngineURL,
		RepoURL:       res.RepoRef.URL,
		DefaultBranch: res.RepoRef.DefaultBranch,
		Branch:        branch,
		AgentsMDPath:  res.AgentsMDPath,
		ProviderEnv:   res.ProviderEnv,
		// In proxy mode the firewall drops all direct egress; the clone (and
		// every egress probe below) must route through the proxy, exactly as
		// a real run does — without this the check firewalls its own clone.
		ProxyEnv: opts.ProxyEnv,
		SeedAuth: res.SeedAuth,
		GitUser:  res.GitUser,
		GitPass:  res.GitPass,
		Prepare:  res.Prepare,
		Setup:    res.Commands["setup"],
		Timeout:  10 * time.Minute,
	}), "clone + seed auth + prepare + setup")

	if verify, ok := res.Commands["verify"]; ok && verify != "" {
		step("verify", execExpect(ctx, envp, env, verify, "repo", true), "")
	} else {
		skip("verify", "project.commands.verify not configured")
	}

	// Firewall negatives: fixed — but a provisioner may report (per env)
	// that THIS workspace runs without the apothecary firewall (devcontainer
	// on a compose config or with the firewall turned off). Skip honestly
	// there instead of failing three invariants that do not exist.
	egressOpen := false
	if r, ok := envp.(ports.EgressReporter); ok {
		if open, err := r.EgressOpen(ctx, env); err == nil {
			egressOpen = open
		}
	}
	proxyMode := len(opts.ProxyEnv) > 0
	if egressOpen {
		for _, id := range []string{"firewall_deny_http", "firewall_deny_dns", "firewall_initialized"} {
			skip(id, "provisioner reports open egress for this workspace: firewall not present (engine-side proxy filtering only)")
		}
	} else {
		// example.com is not on the allowlist: in allowlist mode the firewall
		// drops it; in proxy mode the proxy refuses the CONNECT. Either way a
		// non-allowed host must NOT be reachable.
		step("firewall_deny_http", execExpect(ctx, envp, env,
			"curl -m 8 -s -o /dev/null https://example.com", "", false), "example.com blocked")
		// DNS: allowlist mode blocks the resolver (names resolve only via the
		// pinned /etc/hosts); proxy mode INTENTIONALLY leaves the loopback
		// resolver up (all filtering is by hostname at the proxy), so the
		// "foreign DNS dead" invariant only applies to allowlist mode.
		if proxyMode {
			skip("firewall_deny_dns", "proxy mode: DNS resolves locally by design; egress is filtered by hostname at the proxy, not by blocking DNS")
		} else {
			step("firewall_deny_dns", execExpect(ctx, envp, env,
				"getent hosts example.com", "", false), "foreign DNS dead")
		}
		step("firewall_initialized", execExpect(ctx, envp, env,
			"test -e /run/apothecary-firewall.done", "", true), "")
	}

	// Egress positives: one per derived allowlist entry. TCP+TLS connect is
	// the criterion — any HTTP status counts; connectivity, not auth.
	for _, domain := range res.EgressDomains {
		step("egress:"+domain, execExpect(ctx, envp, env,
			fmt.Sprintf("curl -m 10 -s -o /dev/null https://%s", domain), "", true), "")
	}
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

	return failed
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
	snap CheckRun
}

// StartEnvCheck launches an envcheck for the project; one at a time per
// project. Returns the check id immediately (202 semantics).
func (e *Engine) StartEnvCheck(ctx context.Context, projectID string, keep bool) (string, error) {
	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return "", err
	}
	id := e.newID()
	st := &envCheckState{snap: CheckRun{
		ID: id, ProjectID: projectID, Status: "running", Keep: keep, StartedAt: e.clock(),
	}}
	// One lock section for check + insert: two concurrent calls must not
	// both pass the duplicate guard.
	e.mu.Lock()
	for _, other := range e.envchecks {
		if other.snap.ProjectID == projectID && other.snap.Status == "running" {
			e.mu.Unlock()
			res.release()
			return "", fmt.Errorf("an envcheck for project %s is already running", projectID)
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
	// Proxy mode (M18): the check's workspace is firewalled exactly like a
	// real run, so its egress must go through the proxy. Install the
	// allowlist policy (derived egress domains) for this check's run id and
	// hand RunEnvCheck the matching proxy.env; both empty ⇒ allowlist mode.
	if e.egress != nil {
		e.egress.SetPolicy(checkRunID, res.EgressDomains)
	}
	proxyEnv := e.proxyEnvFor(checkRunID)
	go func() {
		defer res.release()
		if e.egress != nil {
			defer e.egress.DropPolicy(checkRunID)
		}
		failed := RunEnvCheck(context.Background(), res, checkRunID,
			EnvCheckOptions{EngineURL: e.cfg.EngineURL, EnginePort: enginePort, Keep: keep, ProxyEnv: proxyEnv},
			func(s CheckStep) {
				e.mu.Lock()
				st.snap.Steps = append(st.snap.Steps, s)
				e.mu.Unlock()
			})
		now := e.clock()
		e.mu.Lock()
		st.snap.FinishedAt = &now
		if failed > 0 {
			st.snap.Status = "fail"
		} else {
			st.snap.Status = "pass"
		}
		e.mu.Unlock()
	}()
	return id, nil
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
