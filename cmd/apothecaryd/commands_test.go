package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/adapters/pgstore/pgtest"
	"apothecary/internal/flow/builtin"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/envstate"
	"apothecary/internal/workspace/wsinfo"
)

const deadDSN = "postgres://x@127.0.0.1:1/x?connect_timeout=1"

// stdout captures what fn prints.
func stdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	func() {
		defer func() { os.Stdout = old; w.Close() }()
		fn()
	}()
	return <-done
}

// feedStdin makes text what the process reads next.
func feedStdin(t *testing.T, text string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, text); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
}

// mainWith runs main in this process, for a command that returns.
func mainWith(args ...string) {
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = append([]string{"apothecaryd"}, args...)
	main()
}

func TestMainInProcess(t *testing.T) {
	if got := stdout(t, func() { mainWith("version") }); got != "apothecaryd dev\n" {
		t.Errorf("version = %q", got)
	}
	if got := stdout(t, func() { mainWith("keygen") }); strings.TrimSpace(got) == "" {
		t.Error("keygen printed nothing")
	}
	a, b := stdout(t, func() { mainWith("shim-hash") }), stdout(t, func() { mainWith("tools-hash") })
	if len(a) < 16 || len(b) < 16 || a == b {
		t.Errorf("hashes = %q %q", a, b)
	}
	path := filepath.Join(t.TempDir(), "f.yaml")
	if err := os.WriteFile(path, []byte(devFlowYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stdout(t, func() { mainWith("validate", path) }); !strings.Contains(got, `flow "dev" valid`) {
		t.Errorf("validate = %q", got)
	}
}

func TestUsageNamesEveryCommand(t *testing.T) {
	r := runMain(t, nil, "")
	for _, cmd := range []string{"serve", "keygen", "validate", "rebuild-projections", "envcheck", "user", "shim-hash", "tools-hash", "version"} {
		if !strings.Contains(r.stderr, cmd) {
			t.Errorf("usage does not mention %s:\n%s", cmd, r.stderr)
		}
	}
}

// userEnv is a migrated database with accounts, addressed by the environment
// the CLI reads.
func userEnv(t *testing.T) *auth.Service {
	t.Helper()
	dsn := pgtest.Empty(t)
	t.Setenv("APOTHECARY_POSTGRES_DSN", dsn)
	t.Setenv("APOTHECARY_MASTER_KEY", secrets.GenerateKey())
	t.Setenv("APOTHECARY_RUNS_DIR", runsDir(t))
	store := openDSN(t, dsn)
	return auth.New(store)
}

func TestUserCommands(t *testing.T) {
	svc := userEnv(t)
	ctx := context.Background()

	if got := stdout(t, func() { mainWith("user", "list") }); !strings.Contains(got, "no accounts yet") {
		t.Errorf("empty list = %q", got)
	}
	lu, err := svc.CreateUser(ctx, auth.NewUser{Handle: "lu", Email: "lu@example.test", Name: "Lu", Password: "correct horse", Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := svc.CreateUser(ctx, auth.NewUser{Handle: "bob", Email: "bob@example.test", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetDisabled(ctx, bob.ID, true); err != nil {
		t.Fatal(err)
	}
	got := stdout(t, func() { mainWith("user", "list") })
	if !strings.Contains(got, lu.ID+"\tlu\tlu@example.test\tLu admin\n") || !strings.Contains(got, "bob@example.test\tbob disabled\n") {
		t.Errorf("list = %q", got)
	}

	// A password by flag, and every session goes with the old one.
	raw, _, err := svc.NewSession(ctx, lu.ID, "ua")
	if err != nil {
		t.Fatal(err)
	}
	got = stdout(t, func() { mainWith("user", "reset-password", "-password", "a new password", "lu") })
	if !strings.Contains(got, "password set for lu") {
		t.Errorf("reset = %q", got)
	}
	if _, err := svc.AuthenticateSession(ctx, raw); !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("session after a reset = %v", err)
	}
	if _, _, err := svc.SignIn(ctx, auth.KindPassword, "lu", "a new password", "tests"); err != nil {
		t.Errorf("login with the new password = %v", err)
	}

	// By prompt, from a pipe, and by email.
	feedStdin(t, "piped password\n")
	stdout(t, func() { mainWith("user", "reset-password", "lu@example.test") })
	if _, _, err := svc.SignIn(ctx, auth.KindPassword, "lu", "piped password", "tests"); err != nil {
		t.Errorf("login with the piped password = %v", err)
	}

	// Admin on and off.
	if got := stdout(t, func() { mainWith("user", "set-admin", "bob") }); !strings.Contains(got, "bob is an admin") {
		t.Errorf("set-admin = %q", got)
	}
	if u, _ := svc.GetUser(ctx, bob.ID); !u.Admin {
		t.Error("bob is not an admin")
	}
	if got := stdout(t, func() { mainWith("user", "set-admin", "-off", "bob") }); !strings.Contains(got, "no longer an admin") {
		t.Errorf("set-admin -off = %q", got)
	}
	// The last enabled admin stays.
	if err := cmdUser([]string{"set-admin", "-off", "lu"}); err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Errorf("last admin = %v", err)
	}

	feedStdin(t, "")
	if err := cmdUser([]string{"reset-password", "lu"}); err == nil {
		t.Error("a reset with nothing on stdin went through")
	}

	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"unknown command":   {[]string{"nonsense"}, "unknown user command"},
		"reset, no handle":  {[]string{"reset-password"}, "usage"},
		"reset, two":        {[]string{"reset-password", "a", "b"}, "usage"},
		"reset, nobody":     {[]string{"reset-password", "-password", "x", "nobody"}, "not found"},
		"reset, weak":       {[]string{"reset-password", "-password", "x", "lu"}, "at least"},
		"set-admin, none":   {[]string{"set-admin"}, "usage"},
		"set-admin, nobody": {[]string{"set-admin", "nobody"}, "not found"},
	} {
		if err := cmdUser(c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s = %v, want %q", name, err, c.want)
		}
	}
}

func TestPasswordPrompt(t *testing.T) {
	for name, c := range map[string]struct {
		in, want string
		bad      bool
	}{
		"a line":         {"secret\n", "secret", false},
		"crlf":           {"secret\r\n", "secret", false},
		"no newline":     {"secret", "secret", false},
		"an empty line":  {"\n", "", true},
		"nothing at all": {"", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			feedStdin(t, c.in)
			got, err := promptPassword()
			if (err != nil) != c.bad || got != c.want {
				t.Errorf("got %q, %v", got, err)
			}
		})
	}
}

// Commands that need the database report an outage, and a missing config.
func TestCommandsWithoutADatabase(t *testing.T) {
	t.Setenv("APOTHECARY_POSTGRES_DSN", deadDSN)
	t.Setenv("APOTHECARY_MASTER_KEY", secrets.GenerateKey())
	for name, err := range map[string]error{
		"rebuild":  cmdRebuild(nil),
		"user":     cmdUser([]string{"list"}),
		"envcheck": cmdEnvcheck([]string{"-project", "x"}),
	} {
		if err == nil || !strings.Contains(err.Error(), "failed to connect") {
			t.Errorf("%s = %v", name, err)
		}
	}
	t.Setenv("APOTHECARY_POSTGRES_DSN", "")
	for name, err := range map[string]error{
		"rebuild":  cmdRebuild(nil),
		"user":     cmdUser([]string{"list"}),
		"envcheck": cmdEnvcheck([]string{"-project", "x"}),
		"serve":    cmdServe(nil),
	} {
		if err == nil || !strings.Contains(err.Error(), "APOTHECARY_POSTGRES_DSN") {
			t.Errorf("%s = %v", name, err)
		}
	}
	t.Setenv("APOTHECARY_POSTGRES_DSN", deadDSN)
	t.Setenv("APOTHECARY_MASTER_KEY", "")
	if err := cmdEnvcheck([]string{"-project", "x"}); err == nil || !strings.Contains(err.Error(), "APOTHECARY_MASTER_KEY") {
		t.Errorf("envcheck without a key = %v", err)
	}
	if err := cmdEnvcheck(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("envcheck without a project = %v", err)
	}
	if err := cmdUser(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("user with nothing = %v", err)
	}
}

func TestRebuildProjections(t *testing.T) {
	dsn := pgtest.Empty(t)
	t.Setenv("APOTHECARY_POSTGRES_DSN", dsn)
	store := openDSN(t, dsn)
	if _, err := store.Append(context.Background(), "r1", &event.RunCreated{ProjectID: "p", UserID: "u", FlowName: "f"}); err != nil {
		t.Fatal(err)
	}
	if got := stdout(t, func() { mainWith("rebuild-projections") }); got != "rebuilt projections for 1 runs\n" {
		t.Errorf("output = %q", got)
	}
}

// envEnv is a database with a project on real kinds, the provisioner one the
// empty PATH leaves with no docker.
func envEnv(t *testing.T) (*confstore.Service, confstore.Project) {
	t.Helper()
	noHostTools(t)
	dsn := pgtest.Empty(t)
	raw := secrets.GenerateKey()
	t.Setenv("APOTHECARY_POSTGRES_DSN", dsn)
	t.Setenv("APOTHECARY_MASTER_KEY", raw)
	t.Setenv("APOTHECARY_RUNS_DIR", runsDir(t))
	t.Setenv("APOTHECARY_LISTEN", freePort(t))
	key, err := secrets.ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	conf := confstore.New(openDSN(t, dsn), key)
	conf.KindValid = registry(false, nil).Valid
	ctx := context.Background()
	mk := func(port, kind string, cfg map[string]any, secret map[string]string) string {
		in, err := conf.CreateIntegration(ctx, confstore.Integration{Port: port, Kind: kind, Name: kind, Config: cfg, Secrets: secret})
		if err != nil {
			t.Fatalf("%s integration: %v", kind, err)
		}
		return in.ID
	}
	p, err := conf.CreateProject(ctx, confstore.Project{
		Name:          "web",
		RepoAccountID: mk(confstore.PortRepository, "gitlab", map[string]any{"base_url": "http://127.0.0.1:1"}, map[string]string{"token": "t"}),
		ProvisionerID: mk(confstore.PortProvisioner, "docker", nil, nil),
		HarnessIDs:    []string{mk(confstore.PortHarness, "opencode", nil, nil)},
		Repo:          confstore.RepoConfig{URL: "http://127.0.0.1:1/g/p.git", Locator: "1", DefaultBranch: "main"},
		DefaultFlow:   builtin.FlowNames()[0],
	})
	if err != nil {
		t.Fatal(err)
	}
	return conf, p
}

// The check runs against the live provisioner; with no docker to provision on
// it fails, says so step by step, and exits with the count.
func TestEnvcheckCommand(t *testing.T) {
	envEnv(t)
	var err error
	got := stdout(t, func() { err = cmdEnvcheck([]string{"-project", "web"}) })
	if err == nil || !strings.Contains(err.Error(), "check(s) failed") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(got, "envcheck project web, run id envck") || !strings.Contains(got, "FAIL") {
		t.Errorf("output = %q", got)
	}
	if err := cmdEnvcheck([]string{"-project", "nope"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("no such project = %v", err)
	}
}

// The tunnel target: its own listener when the port is free, the running
// engine when it is not. By id as well as by name.
func TestEnvcheckWithAnEngineOnThePort(t *testing.T) {
	_, p := envEnv(t)
	ln, err := net.Listen("tcp", os.Getenv("APOTHECARY_LISTEN"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := stdout(t, func() { _ = cmdEnvcheck([]string{"-project", p.ID}) })
	if !strings.Contains(got, "busy (engine running?)") || !strings.Contains(got, "envcheck project web") {
		t.Errorf("output = %q", got)
	}
}

// A project on a kind this build does not carry cannot be checked: the CLI has
// no fakes.
func TestEnvcheckOfAnUnresolvableProject(t *testing.T) {
	noHostTools(t)
	dsn := pgtest.Empty(t)
	raw := secrets.GenerateKey()
	t.Setenv("APOTHECARY_POSTGRES_DSN", dsn)
	t.Setenv("APOTHECARY_MASTER_KEY", raw)
	t.Setenv("APOTHECARY_RUNS_DIR", runsDir(t))
	key, err := secrets.ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	conf := confstore.New(openDSN(t, dsn), key)
	conf.KindValid = registry(true, nil).Valid
	if err := seedDevProject(context.Background(), conf); err != nil {
		t.Fatal(err)
	}
	if err := cmdEnvcheck([]string{"-project", "dev"}); err == nil || !strings.Contains(err.Error(), "resolve project") {
		t.Errorf("err = %v", err)
	}
}

func TestProbeListener(t *testing.T) {
	addr := freePort(t)
	_, port, _ := net.SplitHostPort(addr)
	n := 0
	for _, c := range port {
		n = n*10 + int(c-'0')
	}
	stop, err := startProbeListener(n)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.Get("http://" + addr + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("probe = %d", res.StatusCode)
	}
	if _, err := startProbeListener(n); err == nil {
		t.Error("a second listener on a busy port")
	}
	stop()
	if _, err := http.Get("http://" + addr + "/"); err == nil {
		t.Error("the probe outlived its stop")
	}
}

// serve, end to end: boots from the environment, answers on its port, and a
// signal shuts it down cleanly.
func TestServeUntilSignalled(t *testing.T) {
	for name, args := range map[string][]string{"plain": nil, "dev": {"-dev"}} {
		t.Run(name, func(t *testing.T) {
			noHostTools(t)
			addr := freePort(t)
			t.Setenv("APOTHECARY_POSTGRES_DSN", pgtest.Empty(t))
			t.Setenv("APOTHECARY_MASTER_KEY", secrets.GenerateKey())
			t.Setenv("APOTHECARY_RUNS_DIR", runsDir(t))
			t.Setenv("APOTHECARY_LISTEN", addr)

			// Ours too, so a signal sent before serve is listening cannot
			// take the test binary down.
			held := make(chan os.Signal, 8)
			signal.Notify(held, syscall.SIGTERM)
			defer signal.Stop(held)

			done := make(chan error, 1)
			go func() {
				mainWith(append([]string{"serve"}, args...)...)
				done <- nil
			}()

			waitFor(t, 30*time.Second, "serve to answer", func() bool {
				select {
				case err := <-done:
					t.Fatalf("serve returned early: %v", err)
				default:
				}
				res, err := http.Get("http://" + addr + "/api/auth/status")
				if err != nil {
					return false
				}
				defer res.Body.Close()
				return res.StatusCode == http.StatusOK
			})

			// serve registers for the signal just after it starts answering,
			// so repeat it until one lands.
			deadline := time.After(30 * time.Second)
			for {
				if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("serve = %v", err)
					}
					if _, err := http.Get("http://" + addr + "/"); err == nil {
						t.Error("the port still answers after shutdown")
					}
					return
				case <-time.After(20 * time.Millisecond):
				case <-deadline:
					t.Fatal("serve ignored the signal")
				}
			}
		})
	}
}

func TestServeOnABusyPort(t *testing.T) {
	noHostTools(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("APOTHECARY_POSTGRES_DSN", pgtest.Empty(t))
	t.Setenv("APOTHECARY_MASTER_KEY", secrets.GenerateKey())
	t.Setenv("APOTHECARY_RUNS_DIR", runsDir(t))
	t.Setenv("APOTHECARY_LISTEN", ln.Addr().String())
	if err := cmdServe(nil); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("err = %v", err)
	}
}

// The reaper pass, and the automation pass, on a real boot: each owner's
// retention applies to that owner, and bytes nothing references go.
func TestBackgroundPasses(t *testing.T) {
	oldReap, oldAuto := reapEvery, automationEvery
	reapEvery, automationEvery = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { reapEvery, automationEvery = oldReap, oldAuto })

	boot := testBoot(t, pgtest.Empty(t))
	a, stop := booted(t, boot, true)
	ctx := context.Background()

	// One owner keeps uploads a millisecond, another the default day.
	set := confstore.DefaultSettings()
	set.OrphanUploadsAfter = "1ms"
	set.DestroyCompletedAfter = "1ms"
	set.SnapshotsAfter = "1h"
	if err := a.conf.PutSettings(ctx, "hasty", set); err != nil {
		t.Fatal(err)
	}
	save := func(user string) filestore.Attachment {
		t.Helper()
		at, err := a.attachments.Save(ctx, user, "a.txt", "text/plain", strings.NewReader("from "+user))
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	hasty, patient := save("hasty"), save("patient")

	// Bytes no row references, old enough for the sweep, and a run for the
	// reaper to look at.
	orphan, err := a.blobs.Put([]byte("nothing points here"))
	if err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(boot.RunsDir, "blobs", orphan[:2], orphan)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(blobPath, old, old); err != nil {
		t.Fatal(err)
	}
	dev, err := a.conf.GetProjectByName(ctx, "", "dev")
	if err != nil {
		t.Fatal(err)
	}
	finished := func(id, owner string) {
		t.Helper()
		if _, err := a.store.Append(ctx, id, &event.RunCreated{ProjectID: dev.ID, UserID: owner, FlowName: "dev"},
			&event.EnvReady{EnvID: "env-" + id}, &event.RunCompleted{}); err != nil {
			t.Fatal(err)
		}
	}
	finished("hasty-run", "hasty")
	finished("patient-run", "patient")

	// An automation the watcher will poll.
	all, err := a.conf.ListIntegrations(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	tracker := ""
	for _, in := range all {
		if in.Port == confstore.PortTicketSource {
			tracker = in.ID
		}
	}
	auto, err := a.conf.CreateAutomation(ctx, confstore.Automation{
		ProjectID: dev.ID, Name: "inbox", Kind: confstore.AutomationTrackerQuery, SourceID: tracker,
		Query: "matches-nothing-at-all", Flow: "dev", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	bg, cancel := context.WithCancel(ctx)
	a.background(bg)
	defer cancel()

	waitFor(t, 10*time.Second, "the hasty owner's upload to go", func() bool {
		_, err := a.attachments.Get(ctx, hasty.ID)
		return err != nil
	})
	waitFor(t, 10*time.Second, "the unreferenced blob to go", func() bool {
		_, err := os.Stat(blobPath)
		return os.IsNotExist(err)
	})
	waitFor(t, 10*time.Second, "the automation to be polled", func() bool {
		got, err := a.conf.GetAutomation(ctx, auto.ID)
		return err == nil && got.LastPolledAt != nil
	})
	waitFor(t, 10*time.Second, "the hasty owner's workspace to go", func() bool {
		v, err := (&run.Reader{Store: a.store}).View(ctx, "hasty-run")
		return err == nil && v.EnvID == ""
	})
	// Someone else's window is someone else's: the patient owner's upload,
	// its bytes and its workspace stay.
	if v, err := (&run.Reader{Store: a.store}).View(ctx, "patient-run"); err != nil || v.EnvID == "" {
		t.Errorf("the patient owner's run = %+v, %v", v, err)
	}
	if _, err := a.attachments.Get(ctx, patient.ID); err != nil {
		t.Errorf("the patient owner's upload went: %v", err)
	}
	if data, _, err := a.attachments.Bytes(ctx, patient.ID); err != nil || string(data) != "from patient" {
		t.Errorf("its bytes = %q, %v", data, err)
	}
	cancel()
	stop()
}

// seedFails fails the store call a seed step needs.
type seedFails struct {
	confstore.DB
	list         error
	integrations int // UpsertIntegration calls to let through; -1: all
	project      int // UpsertProject calls to let through; -1: all
	flow         error
}

func (f *seedFails) ListProjects(ctx context.Context, u string) ([]confstore.Project, error) {
	if f.list != nil {
		return nil, f.list
	}
	return f.DB.ListProjects(ctx, u)
}

func (f *seedFails) UpsertIntegration(ctx context.Context, in confstore.StoredIntegration) error {
	if f.integrations == 0 {
		return errors.New("store refused the integration")
	}
	f.integrations--
	return f.DB.UpsertIntegration(ctx, in)
}

func (f *seedFails) UpsertProject(ctx context.Context, p confstore.Project) error {
	if f.project == 0 {
		return errors.New("store refused the project")
	}
	f.project--
	return f.DB.UpsertProject(ctx, p)
}

func (f *seedFails) UpsertFlow(ctx context.Context, row confstore.FlowRow) error {
	if f.flow != nil {
		return f.flow
	}
	return f.DB.UpsertFlow(ctx, row)
}

// A seed that cannot finish says so; serve -dev would otherwise start on half
// a sandbox.
func TestSeedDevProjectFailures(t *testing.T) {
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	reg := registry(true, nil)
	for name, f := range map[string]*seedFails{
		"listing projects": {list: errors.New("store is down"), integrations: -1, project: -1},
		"the repository":   {integrations: 0, project: -1},
		"the provisioner":  {integrations: 1, project: -1},
		"the harness":      {integrations: 2, project: -1},
		"the tracker":      {integrations: 3, project: -1},
		"the project":      {integrations: -1, project: 0},
		"the default flow": {integrations: -1, project: 1},
	} {
		f.DB = confstore.NewMem()
		conf := confstore.New(f, key)
		conf.KindValid = reg.Valid
		if err := seedDevProject(context.Background(), conf); err == nil {
			t.Errorf("%s: the seed claimed success", name)
		}
	}
}

// The SSH door's targets: a username is a workspace, and only a live one.
func TestSSHTargets(t *testing.T) {
	store := event.NewMemStore()
	ctx := context.Background()
	if _, err := store.Append(ctx, "r1", &event.RunCreated{ProjectID: "p", UserID: "u", FlowName: "f"}, &event.EnvReady{EnvID: "apo-x"}); err != nil {
		t.Fatal(err)
	}
	runs := &run.Reader{Store: store}
	var resolver resolution.Resolver = resolution.StaticResolver{R: &resolution.Resolution{}}
	s := sshTargets{wsinfo.New(runs, resolver, envstate.New(runs, resolver, nil, nil))}
	tg, err := s.Lookup(ctx, "apo-x")
	if err != nil || tg.RunID != "r1" || tg.OwnerID != "u" || tg.Dial == nil {
		t.Errorf("target = %+v, %v", tg, err)
	}
	if _, err := s.Lookup(ctx, "nope"); !errors.Is(err, wsinfo.ErrNoWorkspace) {
		t.Errorf("unknown workspace = %v", err)
	}
}
