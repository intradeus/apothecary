// apothecaryd is the Apothecary factory engine (DESIGN.md, DESIGN2.md).
//
//	apothecaryd serve [-dev]                    run the engine + HTTP API (env-var config, DESIGN2 §1)
//	apothecaryd keygen                          print a fresh APOTHECARY_MASTER_KEY
//	apothecaryd validate <flow.yaml>            parse and validate a flow file
//	apothecaryd rebuild-projections
//	apothecaryd envcheck -project <name|id>     environment compliance check against the live workspace
//	apothecaryd user <list|reset-password|set-admin>   accounts, from the terminal (a lockout's way back in)
//
// -dev registers the fake kinds and seeds a dev project so a full flow runs
// end-to-end with no real workspace/harness/repository host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"apothecary/internal/adapters/browser"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/adapters/pgstore"
	"apothecary/internal/flow"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/platform/config"
	"apothecary/internal/platform/logs"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/platform/telemetry"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
	"apothecary/internal/workspace/sshdoor"
	"apothecary/internal/workspace/wsinfo"
	wsassets "apothecary/workspace"
)

// version is stamped by the release build (deploy/release.sh, ldflags).
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Printf("apothecaryd %s\n", version)
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "keygen":
		fmt.Println(secrets.GenerateKey())
	case "rebuild-projections":
		err = cmdRebuild(os.Args[2:])
	case "envcheck":
		err = cmdEnvcheck(os.Args[2:])
	case "user":
		err = cmdUser(os.Args[2:])
	case "shim-hash":
		// The shim source hash a published shim image must carry (CI passes it
		// as SHIM_HASH; a Kubernetes sandbox's seeded shim is checked against it).
		fmt.Println(wsassets.ShimSourceHash())
	case "tools-hash":
		// Likewise for the tools bundle (TOOLS_HASH; a seeded bundle that
		// differs is only warned about — tool versions are not an ABI).
		fmt.Println(wsassets.ToolsHash())
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		msg := err.Error()
		if safe, ok := pgstore.RedactError(err); ok {
			msg = safe
		}
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: apothecaryd <command>

commands:
  serve [-dev]                     run the engine + HTTP API (config via APOTHECARY_* env vars)
  keygen                           print a fresh APOTHECARY_MASTER_KEY
  validate <flow.yaml>             parse and validate a flow file
  rebuild-projections
  envcheck -project <name|id> [-keep]
  user list | user reset-password <handle> | user set-admin <handle> [-off]
  shim-hash                        print the shim source hash (for publishing shim images)
  tools-hash                       print the tools bundle hash (for publishing tools images)
  version`)
}

func cmdValidate(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: apothecaryd validate <flow.yaml>")
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
// Model auth is harness-owned (post-M8 amendment) — there is no provider
// port.
func registry(dev bool, boot *config.Boot) *kinds.Registry {
	reg := kinds.NewRegistry()
	reg.RegisterRepoHost(kinds.GitLab{})
	reg.RegisterRepoHost(kinds.GitHub{})
	reg.RegisterProvisioner(kinds.Kubernetes{Defaults: kubernetesDefaults(boot)})
	reg.RegisterHarness(kinds.Opencode{})
	reg.RegisterHarness(kinds.Codex{})
	reg.RegisterHarness(kinds.Omp{})
	reg.RegisterTicketSource(kinds.Jira{})
	reg.RegisterTicketSource(kinds.GitHubIssues{})
	reg.RegisterTicketSource(kinds.GitLabIssues{})
	if dev {
		kinds.RegisterFakes(reg)
	}
	return reg
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dev := fs.Bool("dev", false, "register fake kinds and seed a dev project (no real workspace, opencode or repository host)")
	_ = fs.Parse(args)

	boot, err := config.FromEnv()
	if err != nil {
		return err
	}
	// Read once into boot; no child process (devcontainer, docker, git,
	// kubectl) may inherit them — a repo's devcontainer.json could print them.
	os.Unsetenv("APOTHECARY_MASTER_KEY")
	os.Unsetenv("APOTHECARY_POSTGRES_DSN")
	ctx := context.Background()
	prov, err := telemetry.Setup(ctx, version)
	if err != nil {
		return err
	}
	// Registered before cleanup's defer, so it runs after it: the providers
	// flush what the app's last moments emitted.
	defer func() {
		fctx, cancel := context.WithTimeout(context.Background(), otelFlushFor)
		defer cancel()
		if err := prov.Shutdown(fctx); err != nil {
			slog.Warn("otel shutdown", "err", err)
		}
	}()
	// A driver error can quote a stored value (a secret's ciphertext
	// included): log lines show it cut down to its SQLSTATE.
	log, err := logs.New(boot.LogFormat, boot.LogLevel, os.Stderr, prov, pgstore.RedactError)
	if err != nil {
		return err
	}
	slog.SetDefault(log)

	a, cleanup, err := buildApp(ctx, boot, *dev, log)
	if err != nil {
		return err
	}
	defer cleanup()
	reaperCtx, stopReaper := context.WithCancel(ctx)
	defer stopReaper()
	a.background(reaperCtx)

	errCh := make(chan error, 2)
	go func() {
		log.Info("apothecaryd listening", "addr", boot.Listen, "dev", *dev)
		if err := a.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	var debugSrv *http.Server
	if boot.DebugAddr != "" {
		debugSrv = &http.Server{Addr: boot.DebugAddr, Handler: pprofMux(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			log.Info("pprof listening", "addr", boot.DebugAddr)
			if err := debugSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("debug server: %w", err)
			}
		}()
	}

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
	if err := a.srv.Shutdown(shutCtx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	if debugSrv != nil {
		if err := debugSrv.Shutdown(shutCtx); err != nil {
			log.Warn("debug server shutdown", "err", err)
		}
	}
	a.e.Shutdown() // runs stay non-terminal; Recover resumes them next boot
	return nil
}

// otelFlushFor is how long a shutdown waits on the telemetry collector.
var otelFlushFor = 5 * time.Second

// pprofMux serves net/http/pprof on an explicit mux, never the default one
// the package registers itself on, so profiles stay off the API listener.
func pprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// devFlowYAML is the -dev sandbox flow: plan-gated build with an MR and a
// final gate — enough to exercise every stage type against the fakes.
// Dev-mode only; production flows are user-defined in the UI.
const devFlowYAML = `name: dev
version: 5
description: dev-mode sandbox flow (fake adapters)
parameters: { harness: fake, model: fake/model }
agents:
  - name: planner
    description: plans the work
    read_only: true
    prompt: |
      You are the planning agent of the dev sandbox. Write a short plan.
    inputs:
      - { name: ticket, type: ticket, description: "the ticket to plan for" }
    outputs:
      - { name: plan, type: file, description: "the plan" }
  - name: builder
    description: implements the plan
    allow_bash: true
    prompt: |
      You are the building agent of the dev sandbox. Implement the plan.
    inputs:
      - { name: plan, type: file, description: "the plan to implement" }
main:
  - { id: plan, type: agent, harness: $harness, model: $model, agent: planner,
      input: { ticket: { from: run.ticket, type: ticket } },
      output: { plan: { to: artifacts/plan.md, type: file } } }
  - { id: plan_gate, type: human_gate, question: "Approve the plan?",
      input: { doc: { from: artifacts/plan.md, type: file } },
      on_reject: { to: plan, max: 3 } }
  - { id: build, type: agent, harness: $harness, model: $model, agent: builder,
      input: { plan: { from: artifacts/plan.md, type: file } } }
  - { id: test, type: command, run_ref: verify, on_fail: { to: build, max: 5 } }
  - { id: mr, type: action, action: create_mr,
      input: { body: { from: artifacts/plan.md, type: file } } }
  - { id: final_gate, type: human_gate, question: "Merge when satisfied.",
      input: { mr: { from: run.mr.url, type: link } } }
`

// seedDevProject creates a dev flow, fake integrations and a "dev" project
// on first -dev boot, so tickets can be filed immediately (compose smoke).
func seedDevProject(ctx context.Context, conf *confstore.Service) error {
	if _, err := conf.GetProjectByName(ctx, "", "dev"); err == nil {
		return nil
	}
	projects, err := conf.ListProjects(ctx, "")
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
	repoAccountID, err := mk(confstore.PortRepository, "dev-repos")
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
	// A fake tracker so the "import a ticket" flow is exercisable in -dev.
	if _, err := mk(confstore.PortTicketSource, "dev-tracker"); err != nil {
		return err
	}
	p, err := conf.CreateProject(ctx, confstore.Project{
		Name:          "dev",
		RepoAccountID: repoAccountID,
		ProvisionerID: provID,
		HarnessIDs:    []string{harnessID},
		Repo: confstore.RepoConfig{
			URL: "https://git.local/dev/fake-repo.git", Locator: "1",
			DefaultBranch: "main", AgentsMDPath: "AGENTS.md",
		},
		Commands: map[string]string{"verify": "true"},
		// A project needs a default flow that resolves, and its own flows
		// cannot exist before it does: it starts on whichever template is
		// listed first and moves to its dev flow just below.
		DefaultFlow: builtin.FlowNames()[0],
	})
	if err != nil {
		return err
	}
	// Now the project exists, its dev flow can. It carries its own agents
	// (FLOWLANG §1, v3), so there is nothing to seed beyond the document.
	if _, err := conf.SaveFlow(ctx, p.ID, "dev", devFlowYAML, nil); err != nil {
		return fmt.Errorf("dev flow: %w", err)
	}
	p.DefaultFlow = "dev"
	if _, err := conf.UpdateProject(ctx, p.ID, p); err != nil {
		return fmt.Errorf("dev project default flow: %w", err)
	}
	return nil
}

// engineURL derives the workspace-visible engine URL from the listen
// address. Workspaces reach the engine at their own localhost through the
// devcontainer bridge's in-container shim relay (loopback parity).
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

// kubernetesDefaults are the boot env's answers for a Kubernetes integration;
// images default to this engine's own release.
func kubernetesDefaults(boot *config.Boot) kinds.KubernetesDefaults {
	if boot == nil {
		return kinds.KubernetesDefaults{}
	}
	k := boot.K8s
	img := func(set, name string) string {
		if set != "" {
			return set
		}
		return config.DefaultImage(name, version)
	}
	return kinds.KubernetesDefaults{
		Namespace:       k.Namespace,
		EngineNamespace: k.EngineNamespace,
		EngineURL:       k.EngineURL,
		Kubeconfig:      k.Kubeconfig,
		SandboxImage:    img(k.SandboxImage, "sandbox"),
		ShimImage:       img(k.ShimImage, "shim"),
		ToolsImage:      img(k.ToolsImage, "tools"),
		GatewayImage:    img(k.GatewayImage, "egress-gw"),
		PullPolicy:      k.PullPolicy,
		ProtectCIDRs:    k.ProtectCIDRs,
		Transport:       k.Transport,
		RuntimeClass:    k.RuntimeClass,
		Privileged:      k.Privileged,
	}
}

// k8sBrowserHost runs project browsers as pods in the workspaces namespace,
// their routing proxies on one engine listener.
func k8sBrowserHost(boot *config.Boot) (browser.Host, func(), error) {
	k := boot.K8s
	advertise := k.BrowserProxyAddr
	if advertise == "" && k.EngineURL != "" {
		if u, err := url.Parse(k.EngineURL); err == nil && u.Hostname() != "" {
			_, port, _ := net.SplitHostPort(k.BrowserProxyListen)
			advertise = net.JoinHostPort(u.Hostname(), port)
		}
	}
	kubeconfig, _, err := kube.Kubeconfig(filepath.Join(boot.RunsDir, "k8s"), k.Kubeconfig)
	if err != nil {
		return nil, nil, fmt.Errorf("browser kubeconfig: %w", err)
	}
	mux, err := browser.NewSessionMux(k.BrowserProxyListen, advertise)
	if err != nil {
		return nil, nil, fmt.Errorf("browser session listener %s: %w", k.BrowserProxyListen, err)
	}
	shim := k.ShimImage
	if shim == "" {
		shim = config.DefaultImage("shim", version)
	}
	return &browser.K8sHost{
		Kube:            &kube.Client{Kubeconfig: kubeconfig, Namespace: k.Namespace},
		ShimImage:       shim,
		Mux:             mux,
		EngineNamespace: k.EngineNamespace,
		PullPolicy:      k.PullPolicy,
	}, func() { mux.Close() }, nil
}

// sshTargets adapts the workspace service for the SSH door: a username is a
// workspace, and the service says whose run it is and how to reach its sidecar.
type sshTargets struct{ ws *wsinfo.Service }

func (s sshTargets) Lookup(ctx context.Context, workspace string) (sshdoor.Target, error) {
	tg, err := s.ws.SSHTarget(ctx, workspace)
	if err != nil {
		return sshdoor.Target{}, err
	}
	return sshdoor.Target{RunID: tg.RunID, OwnerID: tg.OwnerID, Dial: tg.Dial}, nil
}
