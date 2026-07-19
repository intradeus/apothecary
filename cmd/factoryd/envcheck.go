package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"factory/internal/adapters/coder"
	"factory/internal/config"
	"factory/internal/engine"
	"factory/internal/ports"
)

// cmdEnvcheck drives the M2 acceptance criteria (DESIGN.md §16) against a
// live Coder: provision → bootstrap (incl. the user setup command) → verify
// command on the real repo → firewall checks → ReadFile/WriteFile round
// trip. Exit 0 only if every check passes.
func cmdEnvcheck(args []string) error {
	fs := flag.NewFlagSet("envcheck", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/factory/factory.yaml", "engine config file")
	keep := fs.Bool("keep", false, "keep the workspace for inspection")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	enginePort := 8080
	if _, p, err := splitHostPort(cfg.Listen); err == nil {
		fmt.Sscanf(p, "%d", &enginePort)
	}
	adapter := coder.New(coder.Config{
		URL:        cfg.Coder.URL,
		Token:      config.ResolveEnv(cfg.Coder.TokenEnv),
		Template:   cfg.Coder.Template,
		RunsDir:    cfg.RunsDir,
		EnginePort: enginePort,
	})
	defer adapter.Close()
	ctx := context.Background()
	runID := "envck" + strings.ToLower(ulid.Make().String()[16:])
	fmt.Printf("envcheck run id: %s\n", runID)

	failed := 0
	check := func(name string, err error) {
		if err != nil {
			failed++
			fmt.Printf("FAIL  %-28s %v\n", name, err)
		} else {
			fmt.Printf("ok    %s\n", name)
		}
	}

	fmt.Println("provisioning workspace (this builds the container)...")
	start := time.Now()
	env, err := adapter.Provision(ctx, ports.EnvSpec{
		RunID:   runID,
		RepoURL: cfg.Repo.URL,
		Branch:  "factory/" + runID,
	})
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	fmt.Printf("workspace %s ready in %s\n", env, time.Since(start).Round(time.Second))
	if !*keep {
		defer func() {
			fmt.Println("destroying workspace...")
			if err := adapter.Destroy(context.Background(), env); err != nil {
				fmt.Printf("WARN  destroy: %v\n", err)
			}
		}()
	}

	check("bootstrap (clone+setup)", engine.RunBootstrap(ctx, adapter, env, engine.BootstrapSpec{
		RunID:         runID,
		Token:         "envcheck-token",
		EngineURL:     engineURL(cfg.Listen),
		RepoURL:       cfg.Repo.URL,
		DefaultBranch: cfg.Repo.DefaultBranch,
		Branch:        "factory/" + runID,
		AgentsMDPath:  cfg.Repo.AgentsMDPath,
		GitToken:      config.ResolveEnv(cfg.GitLab.TokenEnv),
		Setup:         cfg.Project.Commands["setup"],
		Timeout:       10 * time.Minute,
	}))

	// The user's verify command on the real repo (§16 M2).
	if verify, ok := cfg.Project.Commands["verify"]; ok {
		check("verify command", execExpect(ctx, adapter, env, verify, "repo", true))
	} else {
		fmt.Println("skip  verify command (project.commands.verify not configured)")
	}

	// Firewall (§11): denied egress fails, provider APIs reachable, DNS dead.
	check("firewall blocks example.com", execExpect(ctx, adapter, env,
		"curl -m 8 -s -o /dev/null https://example.com", "", false))
	check("firewall blocks foreign DNS", execExpect(ctx, adapter, env,
		"getent hosts example.com", "", false))
	check("anthropic API reachable", execExpect(ctx, adapter, env,
		"curl -m 10 -s -o /dev/null https://api.anthropic.com", "", true))
	check("openai API reachable", execExpect(ctx, adapter, env,
		"curl -m 10 -s -o /dev/null https://api.openai.com", "", true))
	check("repo host reachable", execExpect(ctx, adapter, env,
		"git ls-remote --heads origin >/dev/null", "repo", true))
	check("firewall initialized", execExpect(ctx, adapter, env,
		"test -e /run/factory-firewall.done", "", true))

	// Engine reachability through the reverse tunnel: serve a marker on the
	// host's loopback, then fetch it from INSIDE the workspace at its own
	// 127.0.0.1 — the full path the ask_human shim uses. If the bind fails,
	// the real engine is likely listening; any HTTP response proves the
	// tunnel either way.
	stopProbe, probeErr := startProbeListener(enginePort)
	if probeErr != nil {
		fmt.Printf("note  port %d busy (engine running?) — testing tunnel against it\n", enginePort)
	} else {
		defer stopProbe()
	}
	check("engine reachable via tunnel", execExpect(ctx, adapter, env,
		fmt.Sprintf("curl -m 10 -s -o /dev/null http://127.0.0.1:%d/", enginePort), "", true))

	// ReadFile/WriteFile round trip (§16 M2): absolute and repo-relative.
	blob := []byte("round-trip \x00\x01\xfe binary ok\n")
	check("write/read absolute path", roundTrip(ctx, adapter, env, "/factory/roundtrip.bin", blob))
	check("write/read repo-relative", roundTrip(ctx, adapter, env, "artifacts/roundtrip.txt", []byte("hello factory\n")))

	if links, err := adapter.Links(ctx, env); err == nil {
		for _, l := range links {
			fmt.Printf("link  %-8s %s\n", l.Kind, l.URL)
		}
	} else {
		fmt.Printf("WARN  links: %v\n", err)
	}

	if failed > 0 {
		return fmt.Errorf("envcheck: %d check(s) failed", failed)
	}
	fmt.Println("envcheck: all checks passed")
	return nil
}

func execExpect(ctx context.Context, a *coder.Adapter, env ports.EnvID, script, dir string, wantSuccess bool) error {
	res, err := a.Exec(ctx, env, ports.Command{Script: script, Dir: dir, Timeout: 3 * time.Minute})
	if err != nil {
		return err
	}
	if wantSuccess && res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, tailStr(res.Output, 400))
	}
	if !wantSuccess && res.ExitCode == 0 {
		return fmt.Errorf("succeeded but should have been blocked")
	}
	return nil
}

func roundTrip(ctx context.Context, a *coder.Adapter, env ports.EnvID, path string, data []byte) error {
	if err := a.WriteFile(ctx, env, path, data, 0o600); err != nil {
		return err
	}
	back, err := a.ReadFile(ctx, env, path)
	if err != nil {
		return err
	}
	if !bytes.Equal(back, data) {
		return fmt.Errorf("content mismatch: wrote %d bytes, read %d", len(data), len(back))
	}
	return nil
}

// startProbeListener serves 204s on the host loopback for the tunnel check.
func startProbeListener(port int) (func(), error) {
	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, err
	}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close() }, nil
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
