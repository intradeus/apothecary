// factoryd is the Apothecary factory engine (DESIGN.md).
//
//	factoryd validate <flow.yaml>          parse and validate a flow file
//	factoryd serve -config <path> [-dev]   run the engine + HTTP API
//	factoryd rebuild-projections -config <path>
//
// -dev wires the in-memory fake adapters (workspaces, harness, forge) so a
// full flow can be driven end-to-end before the real adapters land (M2-M4).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"factory/internal/adapters/fake"
	"factory/internal/channels"
	"factory/internal/config"
	"factory/internal/engine"
	"factory/internal/flow"
	"factory/internal/httpapi"
	"factory/internal/pgstore"
	"factory/internal/ports"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "rebuild-projections":
		err = cmdRebuild(os.Args[2:])
	case "envcheck":
		err = cmdEnvcheck(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: factoryd <command>

commands:
  validate <flow.yaml>                     parse and validate a flow file
  serve -config <factory.yaml> [-dev]      run the engine + HTTP API
  rebuild-projections -config <factory.yaml>
  envcheck -config <factory.yaml> [-keep]  M2 acceptance against live Coder`)
}

func cmdValidate(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: factoryd validate <flow.yaml>")
	}
	f, err := flow.Load(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("flow %q valid: %d stages, hash %s\n", f.Name, len(f.Stages), f.Hash[:12])
	return nil
}

func cmdRebuild(args []string) error {
	fs := flag.NewFlagSet("rebuild-projections", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/factory/factory.yaml", "engine config file")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := pgstore.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer store.Close()
	n, err := store.RebuildProjections(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("rebuilt projections for %d runs\n", n)
	return nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/factory/factory.yaml", "engine config file")
	dev := fs.Bool("dev", false, "use in-memory fake adapters (no Coder/opencode/GitLab)")
	_ = fs.Parse(args)

	log := slog.Default()
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ctx := context.Background()

	store, err := pgstore.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer store.Close()

	flows, err := loadFlows(cfg.FlowsDir)
	if err != nil {
		return err
	}
	log.Info("flows loaded", "dir", cfg.FlowsDir, "count", len(flows))

	registry := channels.NewRegistry()
	registry.Persist(store)
	rows, err := store.LoadChannels(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := registry.Restore(r.ID, r.Kind, r.Config); err != nil {
			return err
		}
	}

	var (
		envp   ports.EnvProvisioner
		runner ports.AgentRunner
		forge  ports.Forge
	)
	if *dev {
		log.Warn("dev mode: in-memory fake adapters (no real workspaces, harness, or forge)")
		envp, runner, forge = devAdapters()
	} else {
		return errors.New("real adapters (Coder/opencode/GitLab) are not implemented yet — run with -dev (M2-M4 pending)")
	}

	ecfg := engine.Config{
		RepoURL:       cfg.Repo.URL,
		DefaultBranch: cfg.Repo.DefaultBranch,
		AgentsMDPath:  cfg.Repo.AgentsMDPath,
		RepoRef: ports.RepoRef{
			URL:           cfg.Repo.URL,
			DefaultBranch: cfg.Repo.DefaultBranch,
			ProjectID:     cfg.GitLab.ProjectID,
		},
		Commands: cfg.Project.Commands,
		ProviderEnv: providerEnv(map[string]string{
			"ANTHROPIC_API_KEY": cfg.Providers.AnthropicEnv,
			"OPENAI_API_KEY":    cfg.Providers.OpenAIEnv,
		}),
		EngineURL: engineURL(cfg.Listen),
		RunsDir:   cfg.RunsDir,
		Limits: engine.Limits{
			MaxConcurrentRuns: cfg.Limits.MaxConcurrentRuns,
			MaxEnvironments:   cfg.Limits.MaxEnvironments,
		},
	}
	e := engine.New(ecfg, store, envp, runner, forge, []ports.NotificationChannel{registry}, flows)
	if err := e.Recover(ctx); err != nil {
		return fmt.Errorf("crash recovery: %w", err)
	}

	api := httpapi.New(e, store, registry, cfg.RunsDir)
	srv := &http.Server{Addr: cfg.Listen, Handler: api.Handler()}

	reaperCtx, stopReaper := context.WithCancel(ctx)
	defer stopReaper()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := e.ReapOnce(reaperCtx, cfg.Reaper.DestroyCompletedAfter); err != nil {
					log.Error("reaper pass failed", "err", err)
				}
			case <-reaperCtx.Done():
				return
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		log.Info("factoryd listening", "addr", cfg.Listen, "dev", *dev)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutting down", "signal", s.String())
	case err := <-errCh:
		return err
	}

	shutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	e.Shutdown() // runs stay non-terminal; Recover resumes them next boot
	return nil
}

func loadFlows(dir string) (map[string]*flow.Flow, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no *.yaml flows found in %s", dir)
	}
	flows := map[string]*flow.Flow{}
	for _, p := range paths {
		f, err := flow.Load(p)
		if err != nil {
			return nil, err
		}
		if _, dup := flows[f.Name]; dup {
			return nil, fmt.Errorf("duplicate flow name %q (%s)", f.Name, p)
		}
		flows[f.Name] = f
	}
	return flows, nil
}

// providerEnv resolves env-var names from config into actual secret values;
// unset providers are simply omitted (§11: only keys the flow needs).
func providerEnv(names map[string]string) map[string]string {
	out := map[string]string{}
	for key, envName := range names {
		if v := config.ResolveEnv(envName); v != "" {
			out[key] = v
		}
	}
	return out
}

// engineURL derives the workspace-visible engine URL from the listen
// address. Workspaces reach the engine at their own localhost through the
// Coder adapter's reverse tunnel (deviation from §8's host.docker.internal
// path — no host firewall exposure needed).
func engineURL(listen string) string {
	_, port, err := splitHostPort(listen)
	if err != nil {
		port = "8080"
	}
	return "http://127.0.0.1:" + port
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("no port in %q", addr)
	}
	return addr[:i], addr[i+1:], nil
}

// devAdapters returns fakes good enough to drive full flows through the real
// engine, API and Postgres: agents honor output contracts by parsing the
// prompt's OUTPUT CONTRACT section, reviewers approve.
func devAdapters() (ports.EnvProvisioner, ports.AgentRunner, ports.Forge) {
	envp := fake.NewEnvProvisioner()
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
	return envp, runner, &fake.Forge{}
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
