package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/event"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
)

// editorEnvp adds the EditorStarter capability on top of exposerEnvp, so
// OpenEditor's start-then-expose path can be driven end to end.
type editorEnvp struct {
	*exposerEnvp
	starts   int32
	startErr error
}

func (e *editorEnvp) StartEditor(_ context.Context, _ ports.EnvID, _ int, _ ports.EditorSettings) error {
	atomic.AddInt32(&e.starts, 1)
	return e.startErr
}

func TestOpenEditor(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()

	// The "code-server": an echo listener the fake PortConn dials.
	app, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	go func() {
		for {
			c, err := app.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); io.Copy(io.Discard, c) }(c)
		}
	}()
	ed := &editorEnvp{exposerEnvp: &exposerEnvp{EnvProvisioner: h.envp, target: app.Addr().String()}}
	h.res.Envp = ed

	if _, err := h.store.Append(ctx, "r1",
		&event.RunCreated{ProjectID: testProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}

	// No relay → honest 503-shaped error.
	if _, err := h.e.OpenEditor(ctx, "r1"); !errors.Is(err, ErrPortsOff) {
		t.Fatalf("want ErrPortsOff, got %v", err)
	}
	relay := portrelay.New("127.0.0.1")
	defer relay.CloseAll()
	h.e.SetPortRelay(relay)

	u, err := h.e.OpenEditor(ctx, "r1")
	if err != nil || u == "" {
		t.Fatalf("OpenEditor = %q, %v", u, err)
	}
	if ed.starts != 1 {
		t.Errorf("StartEditor called %d times, want 1", ed.starts)
	}
	// The editor rode the code-server port.
	if l := h.e.RunPorts("r1"); len(l) != 1 || l[0].Port != ports.CodeServerPort {
		t.Errorf("exposed ports = %v, want [%d]", l, ports.CodeServerPort)
	}

	// Idempotent: same URL — and code-server is started again if it is not
	// listening (StartEditor is the adapter's idempotent probe-then-start),
	// since an exposure can outlive the code-server behind it.
	if u2, err := h.e.OpenEditor(ctx, "r1"); err != nil || u2 != u {
		t.Errorf("second OpenEditor = %q, %v (want %q)", u2, err, u)
	}
	if ed.starts != 2 {
		t.Errorf("StartEditor called %d times over two opens, want 2", ed.starts)
	}
	if l := h.e.RunPorts("r1"); len(l) != 1 {
		t.Errorf("second open exposed again: %v", l)
	}
}

// rebuildEditorEnvp is a rebuildable workspace with exposable ports and an
// editor, counting re-exposures.
type rebuildEditorEnvp struct {
	*editorEnvp
	exposes int32
}

func (r *rebuildEditorEnvp) Rebuild(context.Context, ports.EnvSpec) error { return nil }
func (r *rebuildEditorEnvp) ExposePort(ctx context.Context, id ports.EnvID, port int) (ports.PortConn, error) {
	atomic.AddInt32(&r.exposes, 1)
	return r.editorEnvp.ExposePort(ctx, id, port)
}

// A rebuild recreates the container: the relays behind the run's exposures
// and the code-server die with it. Every exposure comes back on its old URL,
// through a fresh relay, and an editor that was open is started again.
func TestRebuildRestoresExposuresAndEditor(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	id := completedWithEnv(t, h)
	ctx := context.Background()
	relay := portrelay.New("127.0.0.1")
	defer relay.CloseAll()
	h.e.SetPortRelay(relay)
	env := &rebuildEditorEnvp{editorEnvp: &editorEnvp{exposerEnvp: &exposerEnvp{EnvProvisioner: h.envp, target: "127.0.0.1:1"}}}
	h.res.Envp = env

	u, err := h.e.OpenEditor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.e.RebuildEnv(ctx, id, false); err != nil {
		t.Fatalf("RebuildEnv: %v", err)
	}
	if env.exposes != 2 {
		t.Errorf("ExposePort called %d times, want 2 (the rebuild re-exposes)", env.exposes)
	}
	if env.starts != 2 {
		t.Errorf("StartEditor called %d times, want 2 (the rebuild restarts it)", env.starts)
	}
	if got := relay.Existing(id, ports.CodeServerPort); got != u {
		t.Errorf("editor URL after rebuild = %q, want the old %q", got, u)
	}
}

func TestOpenEditorUnsupported(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	ctx := context.Background()
	h.e.SetPortRelay(portrelay.New("127.0.0.1"))
	if _, err := h.store.Append(ctx, "r1",
		&event.RunCreated{ProjectID: testProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}
	// Plain fake implements neither EditorStarter nor WorkspacePortExposer.
	_, err := h.e.OpenEditor(ctx, "r1")
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("want unsupported error, got %v", err)
	}
}
