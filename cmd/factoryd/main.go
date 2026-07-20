// factoryd is the Apothecary factory engine (DESIGN.md, DESIGN2.md).
//
//	factoryd serve [-dev]                    run the engine + HTTP API (env-var config, DESIGN2 §1)
//	factoryd keygen                          print a fresh FACTORY_MASTER_KEY
//	factoryd import-config -config <path>    one-shot v1 factory.yaml -> DB migration (DESIGN2 §14)
//	factoryd validate <flow.yaml>            parse and validate a flow file
//	factoryd rebuild-projections
//	factoryd envcheck -project <name|id>     environment compliance check against live Coder
//
// -dev registers the fake kinds and seeds a dev project so a full flow runs
// end-to-end with no Coder/harness/forge.
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
	"strings"
	"syscall"
	"time"

	matrixadapter "factory/internal/adapters/matrix"
	"factory/internal/channels"
	"factory/internal/config"
	"factory/internal/confstore"
	"factory/internal/engine"
	"factory/internal/flow"
	"factory/internal/httpapi"
	"factory/internal/kinds"
	"factory/internal/pgstore"
	"factory/internal/ports"
	"factory/internal/resolve"
	"factory/internal/secrets"
	"factory/internal/seed"
	"factory/internal/telemetry"
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
	case "keygen":
		fmt.Println(secrets.GenerateKey())
	case "import-config":
		err = cmdImport(os.Args[2:])
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
  serve [-dev]                     run the engine + HTTP API (config via FACTORY_* env vars)
  keygen                           print a fresh FACTORY_MASTER_KEY
  import-config -config <path>     one-shot v1 factory.yaml -> DB migration
  validate <flow.yaml>             parse and validate a flow file
  rebuild-projections
  envcheck -project <name|id> [-keep]`)
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
	_ = fs.Parse(args)

	boot, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := pgstore.Open(ctx, boot.PostgresDSN)
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

// registry builds the kind registry: real kinds always, fakes with -dev.
func registry(dev bool) *kinds.Registry {
	reg := kinds.NewRegistry()
	reg.RegisterForge(kinds.GitLab{})
	reg.RegisterProvisioner(kinds.Coder{})
	reg.RegisterHarness(kinds.Opencode{})
	for _, pk := range kinds.ProviderKinds() {
		reg.RegisterProvider(pk)
	}
	if dev {
		kinds.RegisterFakes(reg)
	}
	return reg
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dev := fs.Bool("dev", false, "register fake kinds and seed a dev project (no Coder/opencode/forge)")
	_ = fs.Parse(args)

	log := slog.Default()
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

	reg := registry(*dev)
	conf := confstore.New(store, key)
	conf.KindValid = reg.Valid

	if err := seed.Flows(ctx, conf); err != nil {
		return fmt.Errorf("seed flows: %w", err)
	}
	if *dev {
		log.Warn("dev mode: fake kinds registered (no real workspaces, harness, or forge)")
		if err := seedDevProject(ctx, conf); err != nil {
			return fmt.Errorf("seed dev project: %w", err)
		}
	}

	chreg := channels.NewRegistry()
	chreg.Register("matrix", matrixadapter.Factory)
	chreg.Persist(store)
	rows, err := store.LoadChannels(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := chreg.Restore(r.ID, r.Kind, r.Config); err != nil {
			return err
		}
	}

	enginePort := 8080
	if _, p, err := splitHostPort(boot.Listen); err == nil {
		fmt.Sscanf(p, "%d", &enginePort)
	}
	resolver := resolve.New(conf, reg, boot.RunsDir, enginePort)
	defer resolver.Close()

	ecfg := engine.Config{
		EngineURL: engineURL(boot.Listen),
		RunsDir:   boot.RunsDir,
	}
	e := engine.New(ecfg, store, resolver, conf.FlowSource(), resolve.Settings{Conf: conf}, []ports.NotificationChannel{chreg})
	if boot.OtelEndpoint != "" {
		tel, err := telemetry.New(ctx, boot.OtelEndpoint)
		if err != nil {
			return fmt.Errorf("otel: %w", err)
		}
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tel.Shutdown(sctx)
		}()
		e.SetSink(tel)
		log.Info("otel telemetry enabled", "endpoint", boot.OtelEndpoint)
	}
	if err := e.Recover(ctx); err != nil {
		return fmt.Errorf("crash recovery: %w", err)
	}

	api := httpapi.New(e, store, chreg, boot.RunsDir)
	api.Conf = conf
	api.Kinds = reg
	api.Ops = resolver
	api.CPUIDir = boot.CPUIDir
	if boot.CPUIDir != "" {
		log.Info("serving CPUI", "dir", boot.CPUIDir)
	}
	srv := &http.Server{Addr: boot.Listen, Handler: api.Handler()}

	reaperCtx, stopReaper := context.WithCancel(ctx)
	defer stopReaper()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				set, err := conf.Settings(reaperCtx)
				if err != nil {
					log.Error("reaper: settings read failed", "err", err)
					continue
				}
				if err := e.ReapOnce(reaperCtx, set.CompletedWindow()); err != nil {
					log.Error("reaper pass failed", "err", err)
				}
			case <-reaperCtx.Done():
				return
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		log.Info("factoryd listening", "addr", boot.Listen, "dev", *dev)
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

// seedDevProject creates fake integrations and a "dev" project on first
// -dev boot, so tickets can be filed immediately (M7 compose smoke test).
func seedDevProject(ctx context.Context, conf *confstore.Service) error {
	if _, err := conf.GetProjectByName(ctx, "dev"); err == nil {
		return nil
	}
	projects, err := conf.ListProjects(ctx)
	if err != nil {
		return err
	}
	if len(projects) > 0 {
		return nil // a real config exists; don't pollute it
	}
	mk := func(port, name string) (string, error) {
		in, err := conf.CreateIntegration(ctx, confstore.Integration{
			Port: port, Kind: "fake", Name: name,
		})
		if err != nil {
			return "", err
		}
		return in.ID, nil
	}
	forgeID, err := mk(confstore.PortForge, "dev-forge")
	if err != nil {
		return err
	}
	provID, err := mk(confstore.PortProvisioner, "dev-workspaces")
	if err != nil {
		return err
	}
	harnessID, err := mk(confstore.PortHarness, "dev-harness")
	if err != nil {
		return err
	}
	providerID, err := mk(confstore.PortProvider, "dev-provider")
	if err != nil {
		return err
	}
	_, err = conf.CreateProject(ctx, confstore.Project{
		Name:          "dev",
		ForgeID:       forgeID,
		ProvisionerID: provID,
		HarnessID:     harnessID,
		ProviderIDs:   []string{providerID},
		Repo: confstore.RepoConfig{
			URL: "https://forge.local/dev/fake-repo.git", Locator: "1",
			DefaultBranch: "main", AgentsMDPath: "AGENTS.md",
		},
		Commands:    map[string]string{"setup": "true", "verify": "true"},
		Workspace:   confstore.WorkspaceConfig{Template: "dev"},
		DefaultFlow: "v1",
	})
	return err
}

// engineURL derives the workspace-visible engine URL from the listen
// address. Workspaces reach the engine at their own localhost through the
// Coder adapter's reverse tunnel (see IMPLEMENTATION_NOTES).
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
