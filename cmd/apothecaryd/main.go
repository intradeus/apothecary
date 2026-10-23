// apothecaryd is the Apothecary factory engine (DESIGN.md, DESIGN2.md).
//
//	apothecaryd serve [-dev]                    run the engine + HTTP API (env-var config, DESIGN2 §1)
//	apothecaryd keygen                          print a fresh APOTHECARY_MASTER_KEY
//	apothecaryd validate <flow.yaml>            parse and validate a flow file
//	apothecaryd rebuild-projections
//	apothecaryd envcheck -project <name|id>     environment compliance check against the live workspace
//
// -dev registers the fake kinds and seeds a dev project so a full flow runs
// end-to-end with no real workspace/harness/forge.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	matrixadapter "apothecary/internal/adapters/matrix"
	slackadapter "apothecary/internal/adapters/slack"
	"apothecary/internal/builtin"
	"apothecary/internal/channels"
	"apothecary/internal/config"
	"apothecary/internal/confstore"
	"apothecary/internal/engine"
	"apothecary/internal/filestore"
	"apothecary/internal/flow"
	"apothecary/internal/httpapi"
	"apothecary/internal/kinds"
	"apothecary/internal/pgstore"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
	"apothecary/internal/resolve"
	"apothecary/internal/secrets"
	"apothecary/internal/telemetry"
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
	fmt.Fprintln(os.Stderr, `usage: apothecaryd <command>

commands:
  serve [-dev]                     run the engine + HTTP API (config via APOTHECARY_* env vars)
  keygen                           print a fresh APOTHECARY_MASTER_KEY
  validate <flow.yaml>             parse and validate a flow file
  rebuild-projections
  envcheck -project <name|id> [-keep]
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
func registry(dev bool) *kinds.Registry {
	reg := kinds.NewRegistry()
	reg.RegisterForge(kinds.GitLab{})
	reg.RegisterForge(kinds.GitHub{})
	reg.RegisterProvisioner(kinds.Devcontainer{})
	reg.RegisterHarness(kinds.Opencode{})
	reg.RegisterHarness(kinds.Codex{})
	reg.RegisterTicketSource(kinds.Jira{})
	reg.RegisterTicketSource(kinds.GitHubIssues{})
	if dev {
		kinds.RegisterFakes(reg)
	}
	return reg
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dev := fs.Bool("dev", false, "register fake kinds and seed a dev project (no real workspace/opencode/forge)")
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

	// No built-in flows: flows are user-defined, created in the UI (Lu,
	// post-M8). Only -dev seeds one, for its fake sandbox.
	if *dev {
		log.Warn("dev mode: fake kinds registered (no real workspaces, harness, or forge)")
		if err := seedDevProject(ctx, conf); err != nil {
			return fmt.Errorf("seed dev project: %w", err)
		}
	}

	chreg := channels.NewRegistry()
	chreg.Register("matrix", matrixadapter.Factory)
	chreg.Register("slack", slackadapter.Factory)
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

	// Egress filtering now lives in the per-project gateway the devcontainer
	// provisioner brings up (internal/egressgw) — no engine-side proxy.
	resolver := resolve.New(conf, reg, boot.RunsDir, enginePort, boot.SandboxRuntime)
	defer resolver.Close()

	ecfg := engine.Config{
		EngineURL:               engineURL(boot.Listen),
		RunsDir:                 boot.RunsDir,
		PortDiscoveryAutoExpose: boot.PortAutoExpose,
	}
	// Flows resolve DB-first with the compiled-in defaults underneath
	// (internal/builtin): an empty database still runs the "default" flow.
	e := engine.New(ecfg, store, resolver, builtin.FlowSource(conf.FlowSource()), resolve.Settings{Conf: conf}, []ports.NotificationChannel{chreg})
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
	// Workspace-port publisher: exposed app ports bind the CPUI's own
	// interface (dynamic host ports; loopback by default). Before Recover —
	// recovery re-creates the exposures. Persist() attaches durable storage
	// (same handle as channels) so a run's ports come back on the same host
	// port after a restart.
	relayHost := boot.Listen
	if h, _, err := net.SplitHostPort(boot.Listen); err == nil {
		relayHost = h
	}
	portRelay := portrelay.New(relayHost)
	defer portRelay.CloseAll()
	if err := portRelay.Persist(store); err != nil {
		return fmt.Errorf("load persisted port exposures: %w", err)
	}
	e.SetPortRelay(portRelay)

	// File stores (DESIGN-FILES): one content-addressed blob store, two typed
	// stores over it — attachments (upload policy) and snapshots (capture
	// policy, its rows a projection of snapshot_captured).
	blobs := filestore.NewBlobs(boot.RunsDir)
	attachments := filestore.NewAttachments(store, blobs, boot.UploadMaxBytes, boot.UploadMimeAllow)
	snapshots := filestore.NewSnapshots(store, blobs, boot.SnapshotMaxBytes)
	e.SetFiles(attachments, snapshots)
	e.SetEgressAudit(store) // durable egress audit trail (pgstore)
	// The durable ping projection. A run parked on a human has no loop and no
	// in-memory ping, so this is what makes its question listable and
	// answerable — the SQL view, rather than the engine folding every run's
	// log to find one row.
	e.SetPingLookup(store)

	if err := e.Recover(ctx); err != nil {
		return fmt.Errorf("crash recovery: %w", err)
	}

	api := httpapi.New(e, store, chreg, boot.RunsDir)
	api.Conf = conf
	api.Kinds = reg
	api.Ops = resolver
	api.Sources = resolver // ticket sources for the import factory (Jira)
	api.Pings = store      // durable request/response projection (PLAN4 M15)
	api.Attachments = attachments
	api.Snapshots = snapshots
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
				// Uploads never claimed by a ticket/run/answer age out
				// (retention.orphan_uploads_after; rows only — bytes go in
				// the blob sweep below).
				if n, err := attachments.GCOrphans(reaperCtx, set.OrphanWindow()); err != nil {
					log.Error("reaper: upload GC failed", "err", err)
				} else if n > 0 {
					log.Info("reaper: swept unclaimed uploads", "count", n)
				}
				// Blob sweep: delete bytes no live row references. The mark
				// set honors snapshot content retention — rows older than
				// retention.snapshots_after stop pinning their blobs while
				// the rows/events keep the record (DESIGN-FILES §6).
				keepAfter := time.Time{}
				if window, ok := set.SnapshotWindow(); ok {
					keepAfter = time.Now().Add(-window)
				}
				live, err := store.LiveBlobSHAs(reaperCtx, keepAfter)
				if err != nil {
					log.Error("reaper: live blob set failed", "err", err)
				} else if n, err := blobs.Sweep(live); err != nil {
					log.Error("reaper: blob sweep failed", "err", err)
				} else if n > 0 {
					log.Info("reaper: swept unreferenced blobs", "count", n)
				}
			case <-reaperCtx.Done():
				return
			}
		}
	}()

	// Live-workspace reconciler: refreshes each run's real container status
	// (running/stopped/removed) so the workspace badge reflects reality, not
	// just the event-sourced env_id. Runs far more often than the reaper —
	// its whole job is catching out-of-band container changes promptly. An
	// eager first pass populates the badge without waiting a full interval.
	go func() {
		if err := e.ReconcileEnvsOnce(reaperCtx); err != nil && reaperCtx.Err() == nil {
			log.Error("env reconcile pass failed", "err", err)
		}
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := e.ReconcileEnvsOnce(reaperCtx); err != nil {
					log.Error("env reconcile pass failed", "err", err)
				}
			case <-reaperCtx.Done():
				return
			}
		}
	}()

	// Resource sampler: one docker-stats reading of every active run's sandbox
	// per pass, feeding the run-page cpu/ram graphs. Same honesty rule as
	// the reconciler: a failed sample is a gap in the series, never a
	// carried-forward value.
	go func() {
		if err := e.SampleResourcesOnce(reaperCtx); err != nil && reaperCtx.Err() == nil {
			log.Error("resource sample pass failed", "err", err)
		}
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := e.SampleResourcesOnce(reaperCtx); err != nil && reaperCtx.Err() == nil {
					log.Error("resource sample pass failed", "err", err)
				}
			case <-reaperCtx.Done():
				return
			}
		}
	}()

	// Egress audit ingest: persist each ACTIVE run's audit trail (from its
	// per-project gateway) into the DB near-live, so the trail is durable BEFORE
	// the UI reads it (the single audit path: gateway file → engine → DB → UI).
	// Cheap — only active runs, not full history — so it can run often.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := e.IngestEgressAuditOnce(reaperCtx); err != nil {
					log.Error("egress audit ingest failed", "err", err)
				}
			case <-reaperCtx.Done():
				return
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		log.Info("apothecaryd listening", "addr", boot.Listen, "dev", *dev)
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

// devFlowYAML is the -dev sandbox flow: plan-gated build with an MR and a
// final gate — enough to exercise every stage type against the fakes.
// Dev-mode only; production flows are user-defined in the UI.
const devFlowYAML = `name: dev
version: 4
description: dev-mode sandbox flow (fake adapters)
parameters: { harness: fake, model: anthropic/claude-sonnet-4-6 }
agents:
  - name: planner
    description: plans the work
    read_only: true
    prompt: |
      You are the planning agent of the dev sandbox. Write a short plan.
    outputs:
      - name: plan
        description: the plan
        path: artifacts/plan.md
  - name: builder
    description: implements the plan
    allow_bash: true
    prompt: |
      You are the building agent of the dev sandbox. Implement the plan.
    inputs:
      - name: plan
        description: the plan to implement
stages:
  - { id: plan,       type: agent, agent: planner }
  - { id: plan_gate,  type: human_gate, question: "Approve the plan?", input: { doc: artifacts/plan.md },
      on_reject: { to: plan, max: 3 } }
  - { id: build,      type: agent, agent: builder, input: { plan: artifacts/plan.md } }
  - { id: test,       type: command, run_ref: verify, on_fail: { to: build, max: 5 } }
  - { id: mr,         type: action, action: create_mr, input: { body: artifacts/plan.md } }
  - { id: final_gate, type: human_gate, question: "Merge when satisfied.", input: { mr: $MR_URL },
      auto_approve: mr_merged }
`

// seedDevProject creates a dev flow, fake integrations and a "dev" project
// on first -dev boot, so tickets can be filed immediately (compose smoke).
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
	// A fake tracker so the "import a ticket" flow is exercisable in -dev.
	if _, err := mk(confstore.PortTicketSource, "dev-tracker"); err != nil {
		return err
	}
	p, err := conf.CreateProject(ctx, confstore.Project{
		Name:          "dev",
		ForgeID:       forgeID,
		ProvisionerID: provID,
		HarnessIDs:    []string{harnessID},
		Repo: confstore.RepoConfig{
			URL: "https://forge.local/dev/fake-repo.git", Locator: "1",
			DefaultBranch: "main", AgentsMDPath: "AGENTS.md",
		},
		Commands: map[string]string{"setup": "true", "verify": "true"},
		// The project starts on the built-in template: its own flows cannot
		// exist before it does, now that a flow belongs to a project.
		DefaultFlow: builtin.DefaultFlowName,
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
