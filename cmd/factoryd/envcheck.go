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

	"factory/internal/config"
	"factory/internal/confstore"
	"factory/internal/engine"
	"factory/internal/pgstore"
	"factory/internal/ports"
	"factory/internal/resolve"
)

// cmdEnvcheck is the environment compliance gate (DESIGN2 §9) run from the
// CLI: resolve the project from the DB, provision, bootstrap, then verify
// the workspace contract. The egress checklist is DERIVED from the
// project's resolved configuration — adapters declare their domains, the
// check probes exactly those.
func cmdEnvcheck(args []string) error {
	fs := flag.NewFlagSet("envcheck", flag.ExitOnError)
	projectRef := fs.String("project", "", "project name or id")
	keep := fs.Bool("keep", false, "keep the workspace for inspection")
	_ = fs.Parse(args)
	if *projectRef == "" {
		return fmt.Errorf("usage: factoryd envcheck -project <name|id> [-keep]")
	}

	boot, err := config.FromEnv()
	if err != nil {
		return err
	}
	key, err := boot.Key()
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := pgstore.Open(ctx, boot.PostgresDSN)
	if err != nil {
		return err
	}
	defer store.Close()

	reg := registry(false)
	conf := confstore.New(store, key)
	conf.KindValid = reg.Valid

	project, err := conf.GetProjectByName(ctx, *projectRef)
	if err != nil {
		if project, err = conf.GetProject(ctx, *projectRef); err != nil {
			return fmt.Errorf("project %q not found (by name or id)", *projectRef)
		}
	}

	enginePort := 8080
	if _, p, err := splitHostPort(boot.Listen); err == nil {
		fmt.Sscanf(p, "%d", &enginePort)
	}
	resolver := resolve.New(conf, reg, boot.RunsDir, enginePort)
	defer resolver.Close()
	res, err := resolver.Resolve(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("resolve project %s: %w", project.Name, err)
	}

	runID := "envck" + strings.ToLower(ulid.Make().String()[16:])
	fmt.Printf("envcheck project %s, run id %s\n", project.Name, runID)

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
	envp := res.Envp
	env, err := envp.Provision(ctx, ports.EnvSpec{
		RunID:         runID,
		RepoURL:       res.RepoRef.URL,
		Branch:        "factory/" + runID,
		Image:         res.Image,
		EgressDomains: res.EgressDomains,
	})
	if err != nil {
		return fmt.Errorf("provision: %w", err)
	}
	fmt.Printf("workspace %s ready in %s\n", env, time.Since(start).Round(time.Second))
	if !*keep {
		defer func() {
			fmt.Println("destroying workspace...")
			if err := envp.Destroy(context.Background(), env); err != nil {
				fmt.Printf("WARN  destroy: %v\n", err)
			}
		}()
	}

	check("bootstrap (clone+setup)", engine.RunBootstrap(ctx, envp, env, engine.BootstrapSpec{
		RunID:         runID,
		Token:         "envcheck-token",
		EngineURL:     engineURL(boot.Listen),
		RepoURL:       res.RepoRef.URL,
		DefaultBranch: res.RepoRef.DefaultBranch,
		Branch:        "factory/" + runID,
		AgentsMDPath:  res.AgentsMDPath,
		ProviderEnv:   res.ProviderEnv,
		SeedAuth:      res.SeedAuth,
		GitUser:       res.GitUser,
		GitPass:       res.GitPass,
		Prepare:       res.Prepare,
		Setup:         res.Commands["setup"],
		Timeout:       10 * time.Minute,
	}))

	// The user's verify command on the real repo (§16 M2).
	if verify, ok := res.Commands["verify"]; ok {
		check("verify command", execExpect(ctx, envp, env, verify, "repo", true))
	} else {
		fmt.Println("skip  verify command (project.commands.verify not configured)")
	}

	// Firewall negatives (§9): fixed.
	check("firewall blocks example.com", execExpect(ctx, envp, env,
		"curl -m 8 -s -o /dev/null https://example.com", "", false))
	check("firewall blocks foreign DNS", execExpect(ctx, envp, env,
		"getent hosts example.com", "", false))
	check("firewall initialized", execExpect(ctx, envp, env,
		"test -e /run/factory-firewall.done", "", true))

	// Egress positives (§9): one per derived allowlist entry. TCP+TLS
	// connect is the criterion — any HTTP status counts (401/403 fine),
	// connectivity is under test, not auth.
	for _, domain := range res.EgressDomains {
		check("egress:"+domain, execExpect(ctx, envp, env,
			fmt.Sprintf("curl -m 10 -s -o /dev/null https://%s", domain), "", true))
	}
	check("repo host reachable", execExpect(ctx, envp, env,
		"git ls-remote --heads origin >/dev/null", "repo", true))

	// Harness binary present (§9).
	if res.HarnessBinary != "" {
		check("harness binary "+res.HarnessBinary, execExpect(ctx, envp, env,
			"command -v "+res.HarnessBinary, "", true))
	}
	check("shim present", execExpect(ctx, envp, env,
		"test -f /opt/factory/ask-human.mjs", "", true))

	// Engine reachability through the reverse tunnel: serve a marker on the
	// host's loopback, then fetch it from INSIDE the workspace at its own
	// 127.0.0.1 — the full path the ask_human shim uses.
	stopProbe, probeErr := startProbeListener(enginePort)
	if probeErr != nil {
		fmt.Printf("note  port %d busy (engine running?) — testing tunnel against it\n", enginePort)
	} else {
		defer stopProbe()
	}
	check("engine reachable via tunnel", execExpect(ctx, envp, env,
		fmt.Sprintf("curl -m 10 -s -o /dev/null http://127.0.0.1:%d/", enginePort), "", true))

	// ReadFile/WriteFile round trip: absolute and repo-relative.
	blob := []byte("round-trip \x00\x01\xfe binary ok\n")
	check("write/read absolute path", roundTrip(ctx, envp, env, "/factory/roundtrip.bin", blob))
	check("write/read repo-relative", roundTrip(ctx, envp, env, "artifacts/roundtrip.txt", []byte("hello factory\n")))

	if links, err := envp.Links(ctx, env); err == nil {
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

func execExpect(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, script, dir string, wantSuccess bool) error {
	res, err := envp.Exec(ctx, env, ports.Command{Script: script, Dir: dir, Timeout: 3 * time.Minute})
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

func roundTrip(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, path string, data []byte) error {
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
