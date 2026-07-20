package engine

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"factory/internal/ports"
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
}

// RunEnvCheck provisions, bootstraps and verifies one throwaway
// environment, emitting steps as they complete. Returns the failure count.
func RunEnvCheck(ctx context.Context, res *Resolution, runID string, opts EnvCheckOptions, emit func(CheckStep)) int {
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	failed := 0
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
	branch := "factory/" + strings.ToLower(runID)

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
		Image:         res.Image,
		EgressDomains: res.EgressDomains,
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
		SeedAuth:      res.SeedAuth,
		GitUser:       res.GitUser,
		GitPass:       res.GitPass,
		Prepare:       res.Prepare,
		Setup:         res.Commands["setup"],
		Timeout:       10 * time.Minute,
	}), "clone + seed auth + prepare + setup")

	if verify, ok := res.Commands["verify"]; ok && verify != "" {
		step("verify", execExpect(ctx, envp, env, verify, "repo", true), "")
	} else {
		skip("verify", "project.commands.verify not configured")
	}

	// Firewall negatives: fixed.
	step("firewall_deny_http", execExpect(ctx, envp, env,
		"curl -m 8 -s -o /dev/null https://example.com", "", false), "example.com blocked")
	step("firewall_deny_dns", execExpect(ctx, envp, env,
		"getent hosts example.com", "", false), "foreign DNS dead")
	step("firewall_initialized", execExpect(ctx, envp, env,
		"test -e /run/factory-firewall.done", "", true), "")

	// Egress positives: one per derived allowlist entry. TCP+TLS connect is
	// the criterion — any HTTP status counts; connectivity, not auth.
	for _, domain := range res.EgressDomains {
		step("egress:"+domain, execExpect(ctx, envp, env,
			fmt.Sprintf("curl -m 10 -s -o /dev/null https://%s", domain), "", true), "")
	}
	step("repo_reachable", execExpect(ctx, envp, env,
		"git ls-remote --heads origin >/dev/null", "repo", true), "")

	if res.HarnessBinary != "" {
		step("harness_binary", execExpect(ctx, envp, env,
			"command -v "+res.HarnessBinary, "", true), res.HarnessBinary)
	} else {
		skip("harness_binary", "harness declares no binary")
	}
	step("shim", execExpect(ctx, envp, env,
		"test -f /opt/factory/ask-human.mjs", "", true), "")
	step("tunnel", execExpect(ctx, envp, env,
		fmt.Sprintf("curl -m 10 -s -o /dev/null http://127.0.0.1:%d/", opts.EnginePort), "", true),
		"engine reachable from the workspace's own loopback")

	blob := []byte("round-trip \x00\x01\xfe binary ok\n")
	step("rw_absolute", rwRoundTrip(ctx, envp, env, "/factory/roundtrip.bin", blob), "")
	step("rw_relative", rwRoundTrip(ctx, envp, env, "artifacts/roundtrip.txt", []byte("hello factory\n")), "")

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
	e.mu.Lock()
	for _, st := range e.envchecks {
		if st.snap.ProjectID == projectID && st.snap.Status == "running" {
			e.mu.Unlock()
			return "", fmt.Errorf("an envcheck for project %s is already running", projectID)
		}
	}
	e.mu.Unlock()

	res, err := e.resolver.Resolve(ctx, projectID)
	if err != nil {
		return "", err
	}
	id := e.newID()
	st := &envCheckState{snap: CheckRun{
		ID: id, ProjectID: projectID, Status: "running", Keep: keep, StartedAt: e.clock(),
	}}
	e.mu.Lock()
	e.envchecks[id] = st
	e.envcheckOrder = append(e.envcheckOrder, id)
	for len(e.envcheckOrder) > 20 { // keep the last 20 (§9: diagnostics, not history)
		delete(e.envchecks, e.envcheckOrder[0])
		e.envcheckOrder = e.envcheckOrder[1:]
	}
	e.mu.Unlock()

	enginePort := 8080
	if i := strings.LastIndex(e.cfg.EngineURL, ":"); i >= 0 {
		fmt.Sscanf(e.cfg.EngineURL[i+1:], "%d", &enginePort)
	}
	go func() {
		defer res.release()
		failed := RunEnvCheck(context.Background(), res, "envck"+strings.ToLower(id[max(0, len(id)-11):]),
			EnvCheckOptions{EngineURL: e.cfg.EngineURL, EnginePort: enginePort, Keep: keep},
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
