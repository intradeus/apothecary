package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"go.opentelemetry.io/otel"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/logins"
	"apothecary/internal/adapters/browser"
	discordadapter "apothecary/internal/adapters/discord"
	matrixadapter "apothecary/internal/adapters/matrix"
	"apothecary/internal/adapters/pgstore"
	slackadapter "apothecary/internal/adapters/slack"
	"apothecary/internal/analytics"
	"apothecary/internal/engine"
	"apothecary/internal/engine/ask"
	"apothecary/internal/engine/authsink"
	"apothecary/internal/engine/browsermcp"
	"apothecary/internal/engine/egress"
	"apothecary/internal/engine/mrs"
	"apothecary/internal/engine/telemetry"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/httpapi"
	"apothecary/internal/notices"
	"apothecary/internal/platform/config"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/project/automation"
	"apothecary/internal/project/channels"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/integrations"
	"apothecary/internal/project/tickets"
	"apothecary/internal/run"
	"apothecary/internal/skills/skillstore"
	"apothecary/internal/workspace/envcheck"
	"apothecary/internal/workspace/envstate"
	"apothecary/internal/workspace/portrelay"
	"apothecary/internal/workspace/resources"
	"apothecary/internal/workspace/runports"
	"apothecary/internal/workspace/services"
	"apothecary/internal/workspace/sshdoor"
	"apothecary/internal/workspace/wsimage"
	"apothecary/internal/workspace/wsinfo"
)

// The services extracted from the engine declare what they consume; these
// assertions check, at the composition root, that what main hands them
// satisfies it.
var (
	_ automation.Runs    = (*engine.Engine)(nil)
	_ automation.MRs     = (*mrs.Service)(nil)
	_ envcheck.Flows     = (*engine.Engine)(nil)
	_ engine.EnvTeardown = (*egress.Service)(nil)
	_ engine.EnvTeardown = (*runports.Service)(nil)
	_ engine.RunPorts    = (*runports.Service)(nil)
	_ engine.EnvTeardown = (*browsermcp.Service)(nil)
	_ services.Events    = (*engine.Engine)(nil)
	_ services.Exposer   = (*runports.Service)(nil)
	_ egress.AuditStore  = (*pgstore.Store)(nil)
	_ ask.Engine         = (*engine.Engine)(nil)
	_ ask.Store          = (*pgstore.Store)(nil)
	_ analytics.Store    = (*pgstore.Store)(nil)
	_ notices.Store      = (*pgstore.Store)(nil)
)

// app is the daemon, built: what serving and the background loops need.
type app struct {
	log         *slog.Logger
	store       *pgstore.Store
	conf        *confstore.Service
	resolver    *integrations.Resolver
	chreg       *channels.Registry
	e           *engine.Engine
	srv         *http.Server
	keeper      *logins.Keeper
	attachments *filestore.Attachments
	blobs       *filestore.Blobs
	envSt       *envstate.Store
	sampler     *resources.Sampler
	egressSvc   *egress.Service
	mrSvc       *mrs.Service
	syncer      *tickets.Syncer
	notes       *notices.Service
}

// buildApp opens the store and builds every adapter, service, the engine
// and the API over them, in dependency order, then recovers the runs a
// previous process left (after all wiring; the SSH door's key after that).
// cleanup closes what it opened, in reverse; on error what was opened so
// far is closed before it returns.
func buildApp(ctx context.Context, boot *config.Boot, dev bool, log *slog.Logger) (a *app, cleanup func(), err error) {
	var closers []func()
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	defer func() {
		if err != nil {
			closeAll()
		}
	}()
	key, err := boot.Key()
	if err != nil {
		return nil, nil, err
	}

	store, err := pgstore.Open(ctx, boot.PostgresDSN)
	if err != nil {
		return nil, nil, err
	}
	closers = append(closers, func() { store.Close() })

	reg := registry(dev, boot)
	conf := confstore.New(store, key)
	conf.KindValid = reg.Valid

	// No built-in flows: flows are user-defined, created in the UI (Lu,
	// post-M8). Only -dev seeds one, for its fake sandbox.
	if dev {
		log.Warn("dev mode: fake kinds registered (no real workspaces, harness, or repository host)")
		if err := seedDevProject(ctx, conf); err != nil {
			return nil, nil, fmt.Errorf("seed dev project: %w", err)
		}
	}
	// Which flow features and automation kinds are in use, as gauges (after
	// the dev seed, so its first snapshot sees the seeded flow).
	stopUsage, err := startUsageMetrics(otel.Meter("apothecaryd"), store, log)
	if err != nil {
		return nil, nil, fmt.Errorf("feature usage metrics: %w", err)
	}
	closers = append(closers, stopUsage)

	chreg := channels.NewRegistry()
	chreg.Register("matrix", matrixadapter.Factory)
	chreg.Register("slack", slackadapter.Factory)
	chreg.Register("discord", discordadapter.Factory)
	chreg.Persist(store)
	rows, err := store.LoadChannels(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if err := chreg.Restore(r.ID, r.UserID, r.Kind, r.Config); err != nil {
			return nil, nil, err
		}
	}
	// Notices (TODO 51): setup problems a person must act on, raised by the
	// producers wired below and mirrored to the recipient's channels.
	notes := &notices.Service{Store: store, Mirror: chreg, PublicURL: boot.PublicURL, Log: log,
		Admins: func(ctx context.Context) ([]string, error) {
			users, err := store.ListUsers(ctx)
			var ids []string
			for _, u := range users {
				if u.Admin && !u.Disabled() {
					ids = append(ids, u.ID)
				}
			}
			return ids, err
		}}
	conf.Notices = notes
	chreg.Notices = notes

	enginePort := 8080
	if _, p, err := splitHostPort(boot.Listen); err == nil {
		fmt.Sscanf(p, "%d", &enginePort)
	}

	// Egress filtering now lives in the per-project gateway the devcontainer
	// provisioner brings up (internal/adapters/egressgw) — no engine-side proxy.
	resolver := integrations.New(conf, reg, boot.RunsDir, enginePort, boot.SandboxRuntime)
	closers = append(closers, func() { resolver.Close() })
	// The keeper is the one party that redeems provider refresh tokens
	// (docs/model-providers-plan.md): harness credentials are opened through
	// it, so what a workspace is seeded with is always fresh.
	keeper := logins.New(conf, log)
	resolver.SetLogins(keeper)
	resolver.SetNotices(notes)
	if boot.K8s.Namespace == "" {
		// A docker host builds the tools bundle itself; start now rather
		// than inside the first provision after an upgrade.
		resolver.WarmImages(ctx, func(err error) { notes.ToolsImage(ctx, err) })
	}

	ecfg := engine.Config{
		EngineURL: engineURL(boot.Listen),
		RunsDir:   boot.RunsDir,
	}
	// The capacity caps are an admin's setting (System → Limits), stored in
	// the database and read on every admission: a change applies to the next
	// run, no restart. Unset = the engine's defaults.
	limits := engine.StoredLimits{Store: store}
	runs := &run.Reader{Store: store}
	mrSvc := mrs.New(resolver, runs)
	sampler := resources.New(runs, resolver, nil, log)
	envSt := envstate.New(runs, resolver, nil, log)
	egressSvc := egress.New(runs, resolver, store) // durable egress audit trail (pgstore)
	egressSvc.Log = log
	// A refreshed login reaches the live workspaces that hold it; omp's
	// per-run broker tokens derive from an instance secret, so they survive
	// restarts.
	brokerKey, err := conf.InstanceSecret(ctx, "auth-broker")
	if err != nil {
		return nil, nil, fmt.Errorf("auth broker key: %w", err)
	}
	sink := authsink.New(runs, resolver, brokerKey, log)
	keeper.OnRefresh = sink.Push
	keeper.Notices = notes
	// The sink is always wired, on the global providers: no-op unless
	// telemetry.Setup found OTEL_* configuration.
	tel, err := telemetry.New(otel.GetTracerProvider(), otel.GetMeterProvider())
	if err != nil {
		return nil, nil, fmt.Errorf("otel: %w", err)
	}
	// Ends the open spans; main's provider shutdown, after cleanup, flushes them.
	closers = append(closers, func() { tel.Shutdown(context.Background()) })
	var evSink engine.EventSink = tel
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
	closers = append(closers, func() { portRelay.CloseAll() })
	if boot.AppsDomain != "" {
		// Behind a Service and an Ingress a listener per port is unreachable:
		// exposures become <port>-<run>.<apps domain>, served by this server.
		portRelay.RouteByHost(boot.AppsDomain, boot.AppsScheme)
	}
	if err := portRelay.Persist(store); err != nil {
		return nil, nil, fmt.Errorf("load persisted port exposures: %w", err)
	}
	portsSvc := runports.New(runs, resolver, portRelay, runports.Config{AutoExpose: boot.PortAutoExpose}, log)

	// File stores (DESIGN-FILES): one content-addressed blob store, two typed
	// stores over it — attachments (upload policy) and snapshots (capture
	// policy, its rows a projection of snapshot_captured).
	blobs := filestore.NewBlobs(boot.RunsDir)
	attachments := filestore.NewAttachments(store, blobs, boot.UploadMaxBytes, boot.UploadMimeAllow)
	snapshots := filestore.NewSnapshots(store, blobs, boot.SnapshotMaxBytes)
	skillLib := skillstore.New(store, blobs)
	// The project browser (the builtin `browser` MCP server): one container
	// per project, a Chromium per agent session, from Microsoft's Playwright
	// MCP image with the engine's shim.
	browserImg := boot.BrowserImage
	if browserImg == "" {
		browserImg = browser.DefaultImage
	}
	bcfg := browser.Config{
		RunsDir: boot.RunsDir,
		Image:   browser.Pulled(browserImg, nil),
		Tag:     func() string { return browserImg },
		Shim:    wsimage.New(boot.RunsDir).EnsureShim,
		Log:     log,
	}
	// A failed browser-image pull is the admins' to fix (docker host only:
	// on Kubernetes the kubelet pulls it).
	pull := bcfg.Image
	bcfg.Image = func(ctx context.Context) (string, error) {
		ref, err := pull(ctx)
		notes.BrowserImage(ctx, err)
		return ref, err
	}
	if boot.K8s.Namespace != "" {
		// On Kubernetes the engine pulls nothing: the browser's pods live
		// beside the sandboxes, seed the release's shim, and sessions reach
		// the routing proxy over the engine's session mux.
		host, closeMux, err := k8sBrowserHost(boot)
		if err != nil {
			return nil, nil, err
		}
		closers = append(closers, func() { closeMux() })
		bcfg.Host = host
		bcfg.Image = func(context.Context) (string, error) { return browserImg, nil }
	}
	browsers := browser.New(bcfg)
	browsers.Start(ctx)
	closers = append(closers, func() { browsers.Close() })
	browserSvc := browsermcp.New(runs, resolver, browsers)
	// The ticket sync (docs/ticket-sync-plan.md § Sync): one Syncer, so the
	// poll, the API's refreshes and the engine's are single-flight together.
	// It copies the files an issue gained into the same attachment store.
	syncer := &tickets.Syncer{Conf: conf, Sources: resolver, Attachments: attachments, Log: log, Notices: notes}

	e := engine.New(engine.Deps{
		Config:   ecfg,
		Store:    store,
		Resolver: resolver,
		// Flows resolve DB-first with the compiled-in templates underneath
		// (internal/flow/builtin): an empty database still has flows to run.
		Flows:    builtin.FlowSource(conf.FlowSource()),
		Settings: limits,
		Channels: []notifyport.NotificationChannel{chreg},
		// A project's parameter bindings are laid over a flow's defaults at
		// run creation; the config store holds them beside the flow.
		Bindings:    conf,
		Sink:        evSink,
		Attachments: attachments,
		Snapshots:   snapshots,
		Skills:      skillLib,
		// A rerun starts from its ticket's current row, not the old run's
		// copy.
		Tickets: &tickets.Factory{Conf: conf, Attachments: attachments, Sources: resolver, Log: log},
		// A ticket mirroring a remote issue is re-read after a ticket stage
		// wrote to it and when a run starts on it.
		Refresher: syncer,
		// The durable ping projection. A run parked on a human has no loop
		// and no in-memory ping, so this is what makes its question listable
		// and answerable — the SQL view, rather than the engine folding every
		// run's log to find one row.
		PingLookup: store,
		// Agent stages re-read the project's secrets their MCP servers name
		// when they open a session.
		ProjectSecrets: conf,
		Browser:        browsers,
		RunPorts:       portsSvc,
		EnvState:       envSt,
		AuthSink:       sink,
		// What a destroyed workspace takes with it, in this order: its
		// exposures and discovered ports, the browser's exposures, then a
		// last flush of its egress audit.
		Teardown: []engine.EnvTeardown{portsSvc, browserSvc, egressSvc},
		Notices:  notes,
		Log:      log,
	})
	egressSvc.ActiveRuns = e.ActiveRunIDs
	wsSvc := wsinfo.New(runs, resolver, envSt)
	checks := envcheck.NewRegistry(resolver, e, ecfg.EngineURL)
	askSvc := ask.New(runs, resolver, e, store)
	askSvc.Log = log

	if err := e.Recover(ctx); err != nil {
		return nil, nil, fmt.Errorf("crash recovery: %w", err)
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
	api.Analytics = &analytics.Service{Store: store}
	api.Notices = notes
	api.AuthSink = sink
	api.Ports = portsSvc
	api.Browser = browserSvc
	api.Services = services.New(runs, resolver, e, portsSvc, log)
	// The SSH door (TODO 46): one listener people ssh to over their VPN —
	// desktop VS Code included — routed by username to the run's workspace.
	// Both of its keys derive from instance secrets: the host key so a person
	// verifies one fingerprint for good, the door key so every workspace can
	// admit it across restarts.
	if boot.SSHListen != "" {
		advertise, err := boot.SSHAdvertiseAddr()
		if err != nil {
			return nil, nil, err
		}
		hostSeed, err := conf.InstanceSecret(ctx, "ssh-door-host")
		if err != nil {
			return nil, nil, fmt.Errorf("ssh door host key: %w", err)
		}
		doorSeed, err := conf.InstanceSecret(ctx, "ssh-door-client")
		if err != nil {
			return nil, nil, fmt.Errorf("ssh door key: %w", err)
		}
		hostKey, err := sshdoor.KeyFromSeed(hostSeed)
		if err != nil {
			return nil, nil, err
		}
		doorKey, err := sshdoor.KeyFromSeed(doorSeed)
		if err != nil {
			return nil, nil, err
		}
		door, err := sshdoor.New(sshdoor.Config{HostKey: hostKey, DoorKey: doorKey, Keys: authsvc, Targets: sshTargets{wsSvc}, Advertise: advertise, Log: log})
		if err != nil {
			return nil, nil, err
		}
		wsSvc.SetDoorKey(door.DoorPublicKey())
		sshLn, err := net.Listen("tcp", boot.SSHListen)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh door listen %s: %w", boot.SSHListen, err)
		}
		go func() {
			if err := door.Serve(sshLn); err != nil {
				log.Error("ssh door stopped", "err", err)
				notes.SSHDoor(context.Background(), err)
			}
		}()
		closers = append(closers, func() { door.Close() })
		api.SSH = door
		log.Info("ssh door listening", "listen", boot.SSHListen, "advertise", advertise, "fingerprint", door.Fingerprint())
		notes.SSHDoor(ctx, nil)
	}
	api.Limits = limits
	api.Conf = conf
	api.Kinds = reg
	api.Ops = resolver
	api.Sources = resolver // ticket sources for the import factory (Jira)
	api.Trackers = resolver
	api.Syncer = syncer
	api.Pings = store // durable request/response projection (PLAN4 M15)
	api.DB = store    // the readiness probe's database check
	api.GrafanaURL = boot.GrafanaURL
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
	// Shutting down ends the API's event streams and ask_human holds rather
	// than waiting them out (a browser reconnects, the shim re-polls).
	srv.RegisterOnShutdown(api.Drain)
	return &app{
		log: log, store: store, conf: conf, resolver: resolver, chreg: chreg, e: e, srv: srv,
		keeper: keeper, attachments: attachments, blobs: blobs,
		envSt: envSt, sampler: sampler, egressSvc: egressSvc, mrSvc: mrSvc, syncer: syncer, notes: notes,
	}, closeAll, nil
}
