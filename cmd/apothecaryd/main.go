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
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	discordadapter "apothecary/internal/adapters/discord"
	matrixadapter "apothecary/internal/adapters/matrix"
	slackadapter "apothecary/internal/adapters/slack"
	"apothecary/internal/ask"
	"apothecary/internal/auth"
	"apothecary/internal/authsink"
	"apothecary/internal/automation"
	"apothecary/internal/browser"
	"apothecary/internal/builtin"
	"apothecary/internal/channels"
	"apothecary/internal/config"
	"apothecary/internal/confstore"
	"apothecary/internal/egress"
	"apothecary/internal/engine"
	"apothecary/internal/envcheck"
	"apothecary/internal/envstate"
	"apothecary/internal/filestore"
	"apothecary/internal/flow"
	"apothecary/internal/httpapi"
	"apothecary/internal/kinds"
	"apothecary/internal/kube"
	"apothecary/internal/logins"
	"apothecary/internal/mrs"
	"apothecary/internal/pgstore"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
	"apothecary/internal/resolve"
	"apothecary/internal/resources"
	"apothecary/internal/run"
	"apothecary/internal/runports"
	"apothecary/internal/secrets"
	"apothecary/internal/skillstore"
	"apothecary/internal/sshdoor"
	"apothecary/internal/telemetry"
	"apothecary/internal/tickets"
	"apothecary/internal/wsimage"
	"apothecary/internal/wsinfo"
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
	reg.RegisterProvisioner(kinds.Docker{})
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

	reg := registry(*dev, boot)
	conf := confstore.New(store, key)
	conf.KindValid = reg.Valid

	// No built-in flows: flows are user-defined, created in the UI (Lu,
	// post-M8). Only -dev seeds one, for its fake sandbox.
	if *dev {
		log.Warn("dev mode: fake kinds registered (no real workspaces, harness, or repository host)")
		if err := seedDevProject(ctx, conf); err != nil {
			return fmt.Errorf("seed dev project: %w", err)
		}
	}

	chreg := channels.NewRegistry()
	chreg.Register("matrix", matrixadapter.Factory)
	chreg.Register("slack", slackadapter.Factory)
	chreg.Register("discord", discordadapter.Factory)
	chreg.Persist(store)
	rows, err := store.LoadChannels(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := chreg.Restore(r.ID, r.UserID, r.Kind, r.Config); err != nil {
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
	// The keeper is the one party that redeems provider refresh tokens
	// (docs/model-providers-plan.md): harness credentials are opened through
	// it, so what a workspace is seeded with is always fresh.
	keeper := logins.New(conf, log)
	resolver.SetLogins(keeper)
	if boot.K8s.Namespace == "" {
		// A docker host builds the tools bundle itself; start now rather
		// than inside the first provision after an upgrade.
		resolver.WarmImages(ctx)
	}

	ecfg := engine.Config{
		EngineURL: engineURL(boot.Listen),
		RunsDir:   boot.RunsDir,
	}
	// The capacity caps are an admin's setting (System → Limits), stored in
	// the database and read on every admission: a change applies to the next
	// run, no restart. Unset = the engine's defaults.
	limits := engine.StoredLimits{Store: store}
	// Flows resolve DB-first with the compiled-in templates underneath
	// (internal/builtin): an empty database still has flows to run.
	e := engine.New(ecfg, store, resolver, builtin.FlowSource(conf.FlowSource()), limits, []ports.NotificationChannel{chreg})
	runs := &run.Reader{Store: store}
	mrSvc := mrs.New(resolver, runs)
	sampler := resources.New(runs, resolver, nil, log)
	envSt := envstate.New(runs, resolver, nil, log)
	e.SetEnvState(envSt)
	wsSvc := wsinfo.New(runs, resolver, envSt)
	checks := envcheck.NewRegistry(resolver, e, ecfg.EngineURL)
	egressSvc := egress.New(runs, resolver, store) // durable egress audit trail (pgstore)
	egressSvc.ActiveRuns = e.ActiveRunIDs
	egressSvc.Log = log
	askSvc := ask.New(runs, resolver, e, store)
	askSvc.Log = log
	// A project's parameter bindings are laid over a flow's defaults at run
	// creation; the config store holds them beside the flow.
	e.SetFlowBindings(conf)
	// A refreshed login reaches the live workspaces that hold it; omp's
	// per-run broker tokens derive from an instance secret, so they survive
	// restarts.
	brokerKey, err := conf.InstanceSecret(ctx, "auth-broker")
	if err != nil {
		return fmt.Errorf("auth broker key: %w", err)
	}
	sink := authsink.New(runs, resolver, brokerKey, log)
	e.SetAuthSink(sink)
	keeper.OnRefresh = sink.Push
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
	if boot.AppsDomain != "" {
		// Behind a Service and an Ingress a listener per port is unreachable:
		// exposures become <port>-<run>.<apps domain>, served by this server.
		portRelay.RouteByHost(boot.AppsDomain, boot.AppsScheme)
	}
	if err := portRelay.Persist(store); err != nil {
		return fmt.Errorf("load persisted port exposures: %w", err)
	}
	portsSvc := runports.New(runs, resolver, portRelay, runports.Config{AutoExpose: boot.PortAutoExpose}, log)
	e.SetRunPorts(portsSvc)
	// What a destroyed workspace takes with it, in this order: its exposures
	// and discovered ports, then a last flush of its egress audit.
	e.AddEnvTeardown(portsSvc)
	e.AddEnvTeardown(egressSvc)

	// File stores (DESIGN-FILES): one content-addressed blob store, two typed
	// stores over it — attachments (upload policy) and snapshots (capture
	// policy, its rows a projection of snapshot_captured).
	blobs := filestore.NewBlobs(boot.RunsDir)
	attachments := filestore.NewAttachments(store, blobs, boot.UploadMaxBytes, boot.UploadMimeAllow)
	snapshots := filestore.NewSnapshots(store, blobs, boot.SnapshotMaxBytes)
	e.SetFiles(attachments, snapshots)
	skillLib := skillstore.New(store, blobs)
	e.SetSkills(skillLib)
	// The durable ping projection. A run parked on a human has no loop and no
	// in-memory ping, so this is what makes its question listable and
	// answerable — the SQL view, rather than the engine folding every run's
	// log to find one row.
	e.SetPingLookup(store)
	// The project browser (the builtin `browser` MCP server): one container
	// per project, a Chromium per agent session. Agent stages re-read the
	// project's secrets their MCP servers name when they open a session.
	e.SetProjectSecrets(conf)
	bcfg := browser.Config{
		RunsDir: boot.RunsDir,
		Image:   wsimage.New(boot.RunsDir).EnsureBrowserImage,
		Tag:     wsimage.BrowserImageTag,
		Log:     log,
	}
	if boot.K8s.Namespace != "" {
		// On Kubernetes the engine builds nothing: the browser is the release
		// image, its pods live beside the sandboxes, and sessions reach the
		// routing proxy over the engine's session mux.
		host, closeMux, err := k8sBrowserHost(boot)
		if err != nil {
			return err
		}
		defer closeMux()
		img := boot.K8s.BrowserImage
		if img == "" {
			img = config.DefaultImage("browser", version)
		}
		bcfg.Host = host
		bcfg.Image = func(context.Context) (string, error) { return img, nil }
		bcfg.Tag = func() string { return img }
	}
	browsers := browser.New(bcfg)
	browsers.Start(ctx)
	defer browsers.Close()
	e.SetBrowser(browsers)

	if err := e.Recover(ctx); err != nil {
		return fmt.Errorf("crash recovery: %w", err)
	}

	// Accounts (TODO 25): sessions, tokens, the setup of a fresh instance.
	authsvc := auth.New(store)

	api := httpapi.New(e, store, chreg, boot.RunsDir)
	api.Auth = authsvc
	api.MRs = mrSvc
	api.Resources = sampler
	api.EnvState = envSt
	api.Workspaces = wsSvc
	api.EnvCheck = checks
	api.Egress = egressSvc
	api.Ask = askSvc
	api.AuthSink = sink
	api.Ports = portsSvc
	// The SSH door (TODO 46): one listener people ssh to over their VPN —
	// desktop VS Code included — routed by username to the run's workspace.
	// Both of its keys derive from instance secrets: the host key so a person
	// verifies one fingerprint for good, the door key so every workspace can
	// admit it across restarts.
	if boot.SSHListen != "" {
		advertise, err := boot.SSHAdvertiseAddr()
		if err != nil {
			return err
		}
		hostSeed, err := conf.InstanceSecret(ctx, "ssh-door-host")
		if err != nil {
			return fmt.Errorf("ssh door host key: %w", err)
		}
		doorSeed, err := conf.InstanceSecret(ctx, "ssh-door-client")
		if err != nil {
			return fmt.Errorf("ssh door key: %w", err)
		}
		hostKey, err := sshdoor.KeyFromSeed(hostSeed)
		if err != nil {
			return err
		}
		doorKey, err := sshdoor.KeyFromSeed(doorSeed)
		if err != nil {
			return err
		}
		door, err := sshdoor.New(sshdoor.Config{HostKey: hostKey, DoorKey: doorKey, Keys: authsvc, Targets: sshTargets{wsSvc}, Advertise: advertise, Log: log})
		if err != nil {
			return err
		}
		wsSvc.SetDoorKey(door.DoorPublicKey())
		sshLn, err := net.Listen("tcp", boot.SSHListen)
		if err != nil {
			return fmt.Errorf("ssh door listen %s: %w", boot.SSHListen, err)
		}
		go func() {
			if err := door.Serve(sshLn); err != nil {
				log.Error("ssh door stopped", "err", err)
			}
		}()
		defer door.Close()
		api.SSH = door
		log.Info("ssh door listening", "listen", boot.SSHListen, "advertise", advertise, "fingerprint", door.Fingerprint())
	}
	api.Limits = limits
	api.Conf = conf
	api.Kinds = reg
	api.Ops = resolver
	api.Sources = resolver // ticket sources for the import factory (Jira)
	api.Pings = store      // durable request/response projection (PLAN4 M15)
	api.Attachments = attachments
	api.Skills = skillLib
	api.SkillImports = &skillstore.Importer{Store: skillLib, Fetch: skillstore.Fetch}
	api.Snapshots = snapshots
	api.Keeper = keeper
	api.SignIns = logins.NewSignIns()
	api.CPUIDir = boot.CPUIDir
	if boot.AppsDomain != "" {
		api.Apps = httpapi.NewApps(portRelay, boot.PublicURL)
	}
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
				// Settings are per user: each owner's windows apply to their
				// runs and uploads. One read per owner per pass, then every
				// decision looks its owner up (defaults for an unknown one).
				owners, err := store.SettingsOwners(reaperCtx)
				if err != nil {
					log.Error("reaper: owners read failed", "err", err)
					continue
				}
				settings := map[string]confstore.Settings{}
				for _, uid := range owners {
					set, err := conf.Settings(reaperCtx, uid)
					if err != nil {
						log.Error("reaper: settings read failed", "user", uid, "err", err)
						set = confstore.DefaultSettings()
					}
					settings[uid] = set
				}
				settingsOf := func(uid string) confstore.Settings {
					if set, ok := settings[uid]; ok {
						return set
					}
					return confstore.DefaultSettings()
				}
				if err := e.ReapOnce(reaperCtx, func(uid string) time.Duration {
					return settingsOf(uid).CompletedWindow()
				}); err != nil {
					log.Error("reaper pass failed", "err", err)
				}
				// Uploads never claimed by a ticket/run/answer age out
				// (retention.orphan_uploads_after; rows only — bytes go in
				// the blob sweep below).
				for _, uid := range owners {
					if n, err := attachments.GCOrphans(reaperCtx, uid, settingsOf(uid).OrphanWindow()); err != nil {
						log.Error("reaper: upload GC failed", "user", uid, "err", err)
					} else if n > 0 {
						log.Info("reaper: swept unclaimed uploads", "user", uid, "count", n)
					}
				}
				// Blob sweep: delete bytes no live row references. The mark
				// set honors each owner's snapshot content retention — rows
				// older than their retention.snapshots_after stop pinning
				// blobs while the rows/events keep the record (DESIGN-FILES §6).
				cutoffs := map[string]time.Time{}
				for _, uid := range owners {
					if window, ok := settingsOf(uid).SnapshotWindow(); ok {
						cutoffs[uid] = time.Now().Add(-window)
					}
				}
				live, err := store.LiveBlobSHAs(reaperCtx, cutoffs)
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

	// The login keeper: renews every provider login before it expires, idle
	// or not, and pushes each renewal into the live workspaces using it.
	go keeper.Run(reaperCtx, logins.DefaultEvery)

	// Live-workspace reconciler: refreshes each run's real container status
	// (running/stopped/removed) so the workspace badge reflects reality, not
	// just the event-sourced env_id. Runs far more often than the reaper —
	// its whole job is catching out-of-band container changes promptly. An
	// eager first pass populates the badge without waiting a full interval.
	go envSt.Run(reaperCtx, 30*time.Second)

	// Resource sampler: one docker-stats reading of every active run's sandbox
	// per pass, feeding the run-page cpu/ram graphs. Same honesty rule as
	// the reconciler: a failed sample is a gap in the series, never a
	// carried-forward value.
	go sampler.Run(reaperCtx, 10*time.Second)

	// Egress audit ingest: persist each ACTIVE run's audit trail (from its
	// per-project gateway) into the DB near-live, so the trail is durable BEFORE
	// the UI reads it (the single audit path: gateway file → engine → DB → UI).
	// Cheap — only active runs, not full history — so it can run often.
	go egressSvc.Run(reaperCtx, 5*time.Second)

	// Automations: tracker queries polled into tickets and runs, schedules
	// firing runs — the ways a run begins without a person (flow v4). A
	// pass walks every enabled automation; each one's failure is recorded
	// on it, never on the pass.
	watcher := &automation.Watcher{
		Runs: e, MRs: mrSvc, Conf: conf, Notify: chreg, Log: log,
		Tickets: &tickets.Factory{Conf: conf, Attachments: attachments, Sources: resolver, Log: log},
	}
	go func() {
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := watcher.RunOnce(reaperCtx); err != nil && reaperCtx.Err() == nil {
					log.Error("automation pass failed", "err", err)
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
      input: { mr: { from: run.mr.url, type: link } },
      auto_approve: mr_merged }
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
	return &browser.K8sHost{
		Kube:            &kube.Client{Kubeconfig: kubeconfig, Namespace: k.Namespace},
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
