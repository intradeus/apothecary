package wsinfo

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/envstate"
)

const fixtureProject = "p1"

// portConn dials a fixed address.
type portConn struct {
	addr string
	err  error
}

func (p portConn) Dial(ctx context.Context) (net.Conn, error) {
	if p.err != nil {
		return nil, p.err
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", p.addr)
}
func (p portConn) Close() error { return nil }

// fullEnvp adds the optional capabilities the fake leaves out.
type fullEnvp struct {
	*fake.EnvProvisioner
	sidecar    envport.PortConn
	sidecarErr error
	extensions []string
	cache      envport.BuildCacheInfo
	cacheErr   error
	cleared    []string
	askedEnv   envport.EnvID
}

func (f *fullEnvp) Sidecar(_ context.Context, env envport.EnvID) (envport.PortConn, error) {
	f.askedEnv = env
	return f.sidecar, f.sidecarErr
}

func (f *fullEnvp) StartEditor(context.Context, envport.EnvID, int, envport.EditorSettings) error {
	return nil
}

func (f *fullEnvp) EditorExtensions(context.Context, envport.EnvID, envport.EditorSettings) []string {
	return f.extensions
}

func (f *fullEnvp) BuildCache(_ context.Context, project string) (envport.BuildCacheInfo, error) {
	f.cleared = append(f.cleared, "read:"+project)
	return f.cache, f.cacheErr
}

func (f *fullEnvp) ClearBuildCache(_ context.Context, project string) error {
	f.cleared = append(f.cleared, "clear:"+project)
	return f.cacheErr
}

// plainEnvp hides every optional capability of the fake.
type plainEnvp struct{ envport.EnvProvisioner }

type fixture struct {
	store  *event.MemStore
	fake   *fake.EnvProvisioner
	envp   *fullEnvp
	res    *resolution.Resolution
	state  *envstate.Store
	s      *Service
	closed int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := &fixture{store: event.NewMemStore(), fake: fake.NewEnvProvisioner()}
	h.envp = &fullEnvp{EnvProvisioner: h.fake}
	h.res = &resolution.Resolution{ProjectID: fixtureProject, Envp: h.envp, Release: func() { h.closed++ }}
	reader := &run.Reader{Store: h.store}
	resolver := resolution.StaticResolver{R: h.res}
	h.state = envstate.New(reader, resolver, nil, nil)
	h.s = New(reader, resolver, h.state)
	return h
}

// addRun creates a run with a live env, its primary checkout on source.
func (h *fixture) addRun(t *testing.T, runID, source string, extra ...event.Payload) envport.EnvID {
	t.Helper()
	ctx := context.Background()
	id, err := h.fake.Provision(ctx, envport.EnvSpec{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	payloads := []event.Payload{
		&event.RunCreated{TicketTitle: "t", FlowName: "loop", ProjectID: fixtureProject, UserID: "owner-1",
			Checkouts: []event.Checkout{
				{Name: event.PrimaryCheckoutName, Source: source},
				{Name: "lib", Dir: "repos/lib", Source: "develop"},
			}},
		&event.EnvRequested{}, &event.EnvReady{EnvID: string(id)},
	}
	if _, err := h.store.Append(ctx, runID, append(payloads, extra...)...); err != nil {
		t.Fatal(err)
	}
	return id
}

func (h *fixture) createOnly(t *testing.T, runID string, projectID string) {
	t.Helper()
	if _, err := h.store.Append(context.Background(), runID,
		&event.RunCreated{TicketTitle: "t", FlowName: "loop", ProjectID: projectID}); err != nil {
		t.Fatal(err)
	}
}

// sidecarServing points the provisioner at an HTTP handler over loopback.
func (h *fixture) sidecarServing(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	h.envp.sidecar = portConn{addr: strings.TrimPrefix(srv.URL, "http://")}
}

func TestCheckouts(t *testing.T) {
	h := newFixture(t)
	h.addRun(t, "r1", "main")
	got, err := h.s.Checkouts(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != event.PrimaryCheckoutName || got[1].Name != "lib" {
		t.Fatalf("checkouts = %+v", got)
	}
	if _, err := h.s.Checkouts(context.Background(), "nope"); err == nil {
		t.Fatal("an unknown run must be an error")
	}
}

func TestWorkspaceResolvesTheCheckout(t *testing.T) {
	h := newFixture(t)
	env := h.addRun(t, "r1", "dev")
	h.res.RepoRef.DefaultBranch = "main"
	h.sidecarServing(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"boot":"b1"}`) })

	client, co, err := h.s.Workspace(context.Background(), "r1", "")
	if err != nil {
		t.Fatal(err)
	}
	if co.Name != event.PrimaryCheckoutName || co.Base != "origin/dev" {
		t.Errorf("checkout = %+v", co)
	}
	if h.envp.askedEnv != env {
		t.Errorf("asked sidecar of %q, want %q", h.envp.askedEnv, env)
	}
	hl, err := client.Health(context.Background())
	if err != nil || hl.Boot != "b1" {
		t.Fatalf("the client does not reach the sidecar: %+v, %v", hl, err)
	}
	if h.closed != 1 {
		t.Errorf("resolution closed %d times", h.closed)
	}

	_, co, err = h.s.Workspace(context.Background(), "r1", "lib")
	if err != nil || co.Base != "origin/develop" || co.Dir != "repos/lib" {
		t.Fatalf("named checkout = %+v, %v", co, err)
	}
}

func TestWorkspaceBaseFallsBack(t *testing.T) {
	h := newFixture(t)
	h.addRun(t, "r1", "")
	h.sidecarServing(t, func(http.ResponseWriter, *http.Request) {})
	h.res.RepoRef.DefaultBranch = "trunk"
	if _, co, err := h.s.Workspace(context.Background(), "r1", ""); err != nil || co.Base != "origin/trunk" {
		t.Fatalf("no source: %+v, %v", co, err)
	}
	h.res.RepoRef.DefaultBranch = ""
	if _, co, err := h.s.Workspace(context.Background(), "r1", ""); err != nil || co.Base != "" {
		t.Fatalf("no default: %+v, %v", co, err)
	}
}

func TestWorkspaceRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("unknown run", func(t *testing.T) {
		h := newFixture(t)
		if _, _, err := h.s.Workspace(ctx, "nope", ""); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("no workspace yet", func(t *testing.T) {
		h := newFixture(t)
		h.createOnly(t, "r1", fixtureProject)
		if _, _, err := h.s.Workspace(ctx, "r1", ""); !errors.Is(err, ErrNoSidecar) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no project", func(t *testing.T) {
		h := newFixture(t)
		h.createOnly(t, "r1", "")
		if _, _, err := h.s.Workspace(ctx, "r1", ""); !errors.Is(err, ErrNoSidecar) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("archived", func(t *testing.T) {
		h := newFixture(t)
		h.addRun(t, "r1", "main", &event.RunArchived{})
		_, _, err := h.s.Workspace(ctx, "r1", "")
		if !errors.Is(err, ErrNoSidecar) || !strings.Contains(err.Error(), "archived") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no such checkout", func(t *testing.T) {
		h := newFixture(t)
		h.addRun(t, "r1", "main")
		if _, _, err := h.s.Workspace(ctx, "r1", "ghost"); !errors.Is(err, ErrNoSuchCheckout) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unresolvable project", func(t *testing.T) {
		h := newFixture(t)
		h.addRun(t, "r1", "main")
		h.s.Resolver = resolution.StaticResolver{}
		if _, _, err := h.s.Workspace(ctx, "r1", ""); !errors.Is(err, resolution.ErrUnresolvable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("provisioner without a sidecar", func(t *testing.T) {
		h := newFixture(t)
		h.addRun(t, "r1", "main")
		h.res.Envp = plainEnvp{h.fake}
		if _, _, err := h.s.Workspace(ctx, "r1", ""); !errors.Is(err, ErrNoSidecar) {
			t.Fatalf("err = %v", err)
		}
		if h.closed != 1 {
			t.Errorf("resolution leaked: closed %d", h.closed)
		}
	})
	t.Run("the sidecar will not start", func(t *testing.T) {
		h := newFixture(t)
		h.addRun(t, "r1", "main")
		h.envp.sidecarErr = errors.New("no shim")
		_, _, err := h.s.Workspace(ctx, "r1", "")
		if err == nil || !strings.Contains(err.Error(), "workspace sidecar: no shim") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLogTail(t *testing.T) {
	ctx := context.Background()
	h := newFixture(t)
	env := h.addRun(t, "r1", "main")
	if _, err := h.fake.Exec(ctx, env, envport.Command{Script: "echo marker"}); err != nil {
		t.Fatal(err)
	}
	got, err := h.s.LogTail(ctx, "r1", 1024)
	if err != nil || !strings.Contains(string(got), "echo marker") {
		t.Fatalf("LogTail = %q, %v", got, err)
	}

	if _, err := h.s.LogTail(ctx, "nope", 1); err == nil {
		t.Error("an unknown run must be an error")
	}
	h.createOnly(t, "no-project", "")
	if _, err := h.s.LogTail(ctx, "no-project", 1); !errors.Is(err, ErrNoLiveLog) {
		t.Errorf("err = %v", err)
	}
	h.res.Envp = plainEnvp{h.fake}
	if _, err := h.s.LogTail(ctx, "r1", 1); !errors.Is(err, ErrNoLiveLog) {
		t.Errorf("err = %v", err)
	}
	h.s.Resolver = resolution.StaticResolver{}
	if _, err := h.s.LogTail(ctx, "r1", 1); !errors.Is(err, resolution.ErrUnresolvable) {
		t.Errorf("err = %v", err)
	}
}

func TestBuildCache(t *testing.T) {
	ctx := context.Background()
	h := newFixture(t)
	h.envp.cache = envport.BuildCacheInfo{Bytes: 42}
	info, err := h.s.BuildCache(ctx, "ignored")
	if err != nil || info.Bytes != 42 {
		t.Fatalf("BuildCache = %+v, %v", info, err)
	}
	if err := h.s.ClearBuildCache(ctx, "ignored"); err != nil {
		t.Fatal(err)
	}
	// Keyed by the resolved project: the same id Provision stamped.
	if got := strings.Join(h.envp.cleared, ","); got != "read:"+fixtureProject+",clear:"+fixtureProject {
		t.Errorf("calls = %s", got)
	}
	if h.closed != 2 {
		t.Errorf("released %d, want 2", h.closed)
	}

	h.res.Envp = plainEnvp{h.fake}
	if _, err := h.s.BuildCache(ctx, "p"); !errors.Is(err, ErrBuildCacheOff) {
		t.Errorf("err = %v", err)
	}
	if err := h.s.ClearBuildCache(ctx, "p"); !errors.Is(err, ErrBuildCacheOff) {
		t.Errorf("err = %v", err)
	}
	if h.closed != 4 {
		t.Errorf("an unsupported provisioner leaked its resolution: closed %d", h.closed)
	}
	h.s.Resolver = resolution.StaticResolver{}
	if _, err := h.s.BuildCache(ctx, "p"); !errors.Is(err, resolution.ErrUnresolvable) {
		t.Errorf("err = %v", err)
	}
	if err := h.s.ClearBuildCache(ctx, "p"); !errors.Is(err, resolution.ErrUnresolvable) {
		t.Errorf("err = %v", err)
	}
}

// envLookupStore is a store that keeps the env index.
type envLookupStore struct {
	*event.MemStore
	view  run.View
	found bool
	err   error
	asked string
}

func (e *envLookupStore) RunByEnv(_ context.Context, envID string) (run.View, bool, error) {
	e.asked = envID
	return e.view, e.found, e.err
}

func TestSSHTargetResolvesAndDials(t *testing.T) {
	ctx := context.Background()
	h := newFixture(t)
	env := h.addRun(t, "r1", "main")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer c.Close()
			_, _ = c.Write([]byte("SSH-2.0-sidecar\r\n"))
		}
	}()
	h.fake.SSHDial = func(got envport.EnvID) (net.Conn, error) {
		if got != env {
			t.Errorf("dialed %s, want %s", got, env)
		}
		return net.Dial("tcp", ln.Addr().String())
	}
	h.s.SetDoorKey("ssh-ed25519 AAAA door")

	tg, err := h.s.SSHTarget(ctx, string(env))
	if err != nil {
		t.Fatal(err)
	}
	if tg.RunID != "r1" || tg.OwnerID != "owner-1" || tg.EnvID != string(env) {
		t.Fatalf("target = %+v", tg)
	}
	c, err := tg.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The door's key went in before the dial, and only the door's.
	got, err := h.fake.ReadFile(ctx, env, sshDoorKeyPath)
	if err != nil || string(got) != "ssh-ed25519 AAAA door" {
		t.Fatalf("door key = %q, %v", got, err)
	}
}

func TestSSHTargetRefusesDeadWorkspaces(t *testing.T) {
	ctx := context.Background()
	t.Run("a name no run carries", func(t *testing.T) {
		h := newFixture(t)
		h.addRun(t, "r1", "main")
		if _, err := h.s.SSHTarget(ctx, "ghost"); !errors.Is(err, ErrNoWorkspace) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an archived run", func(t *testing.T) {
		h := newFixture(t)
		env := h.addRun(t, "r1", "main", &event.RunArchived{})
		if _, err := h.s.SSHTarget(ctx, string(env)); !errors.Is(err, ErrNoWorkspace) {
			t.Fatalf("err = %v", err)
		}
	})
	for _, st := range []envport.EnvStatus{envport.EnvStopped, envport.EnvAbsent} {
		t.Run("a workspace known "+string(st), func(t *testing.T) {
			h := newFixture(t)
			env := h.addRun(t, "r1", "main")
			h.state.SetStatus("r1", st)
			if _, err := h.s.SSHTarget(ctx, string(env)); !errors.Is(err, ErrNoWorkspace) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("a live status does not refuse", func(t *testing.T) {
		h := newFixture(t)
		env := h.addRun(t, "r1", "main")
		h.state.SetStatus("r1", envport.EnvRunning)
		if _, err := h.s.SSHTarget(ctx, string(env)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSSHTargetDialFaults(t *testing.T) {
	ctx := context.Background()
	target := func(t *testing.T, h *fixture) SSHTarget {
		t.Helper()
		env := h.addRun(t, "r1", "main")
		tg, err := h.s.SSHTarget(ctx, string(env))
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}
	t.Run("no door key: never touches the workspace", func(t *testing.T) {
		h := newFixture(t)
		tg := target(t, h)
		h.s.Resolver = resolution.StaticResolver{}
		if _, err := tg.Dial(ctx); err == nil || !strings.Contains(err.Error(), "no SSH door key") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unresolvable project", func(t *testing.T) {
		h := newFixture(t)
		h.s.SetDoorKey("k")
		tg := target(t, h)
		h.s.Resolver = resolution.StaticResolver{}
		if _, err := tg.Dial(ctx); !errors.Is(err, resolution.ErrUnresolvable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("provisioner without ssh", func(t *testing.T) {
		h := newFixture(t)
		h.s.SetDoorKey("k")
		tg := target(t, h)
		h.res.Envp = plainEnvp{h.fake}
		if _, err := tg.Dial(ctx); !errors.Is(err, ErrNoSidecar) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the key cannot be written", func(t *testing.T) {
		h := newFixture(t)
		h.s.SetDoorKey("k")
		tg := target(t, h)
		if err := h.fake.Destroy(ctx, envport.EnvID(tg.EnvID)); err != nil {
			t.Fatal(err)
		}
		if _, err := tg.Dial(ctx); err == nil || !strings.Contains(err.Error(), "writing the door key") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the workspace serves no ssh", func(t *testing.T) {
		h := newFixture(t)
		h.s.SetDoorKey("k")
		tg := target(t, h)
		if _, err := tg.Dial(ctx); err == nil || !strings.Contains(err.Error(), "serves no ssh") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunByEnvUsesTheStoreIndex(t *testing.T) {
	ctx := context.Background()
	h := newFixture(t)
	store := &envLookupStore{MemStore: h.store, found: true,
		view: run.View{RunID: "indexed", ProjectID: fixtureProject, EnvID: "env-9", UserID: "u"}}
	h.s.Runs = &run.Reader{Store: store}
	tg, err := h.s.SSHTarget(ctx, "env-9")
	if err != nil {
		t.Fatal(err)
	}
	if tg.RunID != "indexed" || store.asked != "env-9" {
		t.Fatalf("target = %+v, asked %q", tg, store.asked)
	}

	// The index keeps a stale row for a reused name: it must still match.
	store.view.EnvID = "env-other"
	if _, err := h.s.SSHTarget(ctx, "env-9"); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("err = %v", err)
	}
	store.found = false
	if _, err := h.s.SSHTarget(ctx, "env-9"); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("err = %v", err)
	}
	boom := errors.New("db down")
	store.err = boom
	if _, err := h.s.SSHTarget(ctx, "env-9"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

type failingRunIDs struct {
	*event.MemStore
	err error
}

func (f failingRunIDs) RunIDs(context.Context) ([]string, error) { return nil, f.err }

func TestRunByEnvScanFailureSurfaces(t *testing.T) {
	h := newFixture(t)
	boom := errors.New("db down")
	h.s.Runs = &run.Reader{Store: failingRunIDs{h.store, boom}}
	if _, err := h.s.SSHTarget(context.Background(), "env-1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestSSHInfo(t *testing.T) {
	ctx := context.Background()
	t.Run("the folder is the workspace folder", func(t *testing.T) {
		h := newFixture(t)
		env := h.addRun(t, "r1", "main")
		h.envp.extensions = []string{"golang.go"}
		h.sidecarServing(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"repo":"/home/u/repo","workspace":"/workspaces/app"}`)
		})
		info, err := h.s.SSHInfo(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		if info.Workspace != string(env) || info.Path != "/workspaces/app" || len(info.Extensions) != 1 {
			t.Fatalf("info = %+v", info)
		}
	})
	t.Run("falling back to the checkout", func(t *testing.T) {
		h := newFixture(t)
		h.addRun(t, "r1", "main")
		h.sidecarServing(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"repo":"/home/u/repo"}`) })
		info, err := h.s.SSHInfo(ctx, "r1")
		if err != nil || info.Path != "/home/u/repo" {
			t.Fatalf("info = %+v, %v", info, err)
		}
	})
	t.Run("a sidecar that cannot say still gives the name", func(t *testing.T) {
		h := newFixture(t)
		env := h.addRun(t, "r1", "main")
		h.envp.sidecarErr = errors.New("no shim")
		info, err := h.s.SSHInfo(ctx, "r1")
		if err != nil || info.Workspace != string(env) || info.Path != "" {
			t.Fatalf("info = %+v, %v", info, err)
		}
		h.envp.sidecarErr = nil
		h.envp.sidecar = portConn{err: errors.New("refused")}
		info, err = h.s.SSHInfo(ctx, "r1")
		if err != nil || info.Path != "" {
			t.Fatalf("info = %+v, %v", info, err)
		}
	})
	t.Run("a provisioner without an editor", func(t *testing.T) {
		h := newFixture(t)
		h.addRun(t, "r1", "main")
		h.res.Envp = plainEnvp{h.fake}
		info, err := h.s.SSHInfo(ctx, "r1")
		if err != nil || info.Extensions != nil {
			t.Fatalf("info = %+v, %v", info, err)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		h := newFixture(t)
		if _, err := h.s.SSHInfo(ctx, "nope"); err == nil {
			t.Error("an unknown run must be an error")
		}
		h.createOnly(t, "no-env", fixtureProject)
		if _, err := h.s.SSHInfo(ctx, "no-env"); !errors.Is(err, ErrNoWorkspace) {
			t.Errorf("err = %v", err)
		}
		h.addRun(t, "gone", "main", &event.RunArchived{})
		if _, err := h.s.SSHInfo(ctx, "gone"); !errors.Is(err, ErrNoWorkspace) {
			t.Errorf("err = %v", err)
		}
	})
}
