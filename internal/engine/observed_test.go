package engine

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports"
)

// scannerEnvp adds the SiblingPortScanner capability to the fake.
type scannerEnvp struct {
	*fake.EnvProvisioner
	siblings []ports.ObservedPort
}

func (s *scannerEnvp) ScanSiblingPorts(context.Context, ports.EnvID) []ports.ObservedPort {
	return s.siblings
}

func TestObservedPortsRoundTripAndTTL(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	clk := time.Unix(1000, 0)
	h.e.clock = func() time.Time { return clk }

	h.e.ReportObservedPorts("r1", []ports.ObservedPort{{Port: 3000, Process: "node"}})
	got := h.e.ObservedPorts("r1")
	if len(got) != 1 || got[0].Port != 3000 || got[0].Process != "node" {
		t.Fatalf("observed = %+v, want one {3000 node}", got)
	}

	// Past the TTL without a fresh heartbeat, the snapshot is considered dead.
	clk = clk.Add(observedTTL + time.Second)
	if got := h.e.ObservedPorts("r1"); got != nil {
		t.Errorf("stale snapshot must return nil, got %+v", got)
	}

	// forgetObserved drops it entirely (env destroyed).
	clk = time.Unix(1000, 0)
	h.e.ReportObservedPorts("r1", []ports.ObservedPort{{Port: 3000}})
	h.e.forgetObserved("r1")
	if got := h.e.ObservedPorts("r1"); got != nil {
		t.Errorf("after forget, observed = %+v, want nil", got)
	}
}

func TestObservedPortsMergesSiblingScan(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.res.Envp = &scannerEnvp{
		EnvProvisioner: h.envp,
		siblings:       []ports.ObservedPort{{Port: 5432, Service: "db"}, {Port: 3000, Service: "db"}},
	}
	ctx := context.Background()
	if _, err := h.store.Append(ctx, "r1",
		&event.RunCreated{ProjectID: testProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}

	// A shim report establishes liveness (drives the TTL); the workspace's own
	// :3000 must win over the sibling's :3000 on the merge.
	h.e.ReportObservedPorts("r1", []ports.ObservedPort{{Port: 3000, Process: "vite"}})
	h.e.refreshSiblingPorts("r1") // synchronous (bypass the async heartbeat trigger)

	got := h.e.ObservedPorts("r1")
	if len(got) != 2 {
		t.Fatalf("merged observed = %+v, want 3000 + 5432", got)
	}
	if got[0].Port != 3000 || got[0].Process != "vite" || got[0].Service != "" {
		t.Errorf("port 3000 should be the workspace's own (vite), got %+v", got[0])
	}
	if got[1].Port != 5432 || got[1].Service != "db" {
		t.Errorf("port 5432 should be sibling db, got %+v", got[1])
	}
}

func TestAutoExposeObservedCapAndDedupe(t *testing.T) {
	h := newHarness(t, mustFlow(t, loopFlowDoc))
	h.e.cfg.PortDiscoveryAutoExpose = true
	h.e.cfg.PortDiscoveryMaxAuto = 2

	// A workspace "app" the fake PortConn dials, so ExposeRunPort succeeds.
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
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(io.Discard, c) }(c)
		}
	}()
	h.res.Envp = &exposerEnvp{EnvProvisioner: h.envp, target: app.Addr().String()}

	ctx := context.Background()
	if _, err := h.store.Append(ctx, "r1",
		&event.RunCreated{ProjectID: testProject, FlowName: "loop"},
		&event.EnvReady{EnvID: "env1"},
	); err != nil {
		t.Fatal(err)
	}
	relay := portrelay.New("127.0.0.1")
	defer relay.CloseAll()
	h.e.SetPortRelay(relay)

	// Three discovered ports, cap of two.
	h.e.observed["r1"] = &observedRun{
		shim:        []ports.ObservedPort{{Port: 3000}, {Port: 3001}, {Port: 3002}},
		at:          h.e.clock(),
		autoExposed: map[int]bool{},
	}
	h.e.autoExposeObserved("r1")
	if n := len(relay.List("r1")); n != 2 {
		t.Fatalf("auto-exposed %d ports, want cap of 2", n)
	}

	// A second pass exposes nothing new (mark-once + already-Existing).
	h.e.autoExposeObserved("r1")
	if n := len(relay.List("r1")); n != 2 {
		t.Errorf("second pass changed exposures to %d, want stable 2", n)
	}
}
