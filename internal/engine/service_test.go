package engine

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/event"
	"apothecary/internal/portrelay"
	"apothecary/internal/ports/envport"
	"apothecary/internal/resolution"
	"apothecary/internal/run"
	"apothecary/internal/runinput"
	"apothecary/internal/services"
)

// servicesFlowDoc brings the app up, lets an agent look at it, and — when
// the app does not come up — hands its output to an agent to fix it.
const servicesFlowDoc = `name: svc
version: 5
parameters:
  harness: fake
  model: fake/model
  app: { type: string, default: default, description: the project service the run brings up }
agents:
  - name: shooter
    prompt: look at the app
    inputs:
      - { name: app, type: link }
  - name: fixer
    prompt: fix the app
    inputs:
      - { name: failure, type: failure, optional: true }
main:
  - id: up
    type: service
    service: $app
    timeout: 2m
    on_fail: { to: fix, max: 1 }
  - id: look
    type: agent
    harness: $harness
    model: $model
    agent: shooter
    input:
      app: { from: run.service.$app, type: link }
    on_success: done
  - id: fix
    type: agent
    harness: $harness
    model: $model
    agent: fixer
    input:
      failure: { from: edge.failure, type: failure, optional: true }
    on_success: up
`

func serviceHarness(t *testing.T, doc string) (*harness, *fake.Services) {
	t.Helper()
	h := newHarness(t, mustFlow(t, doc))
	h.res.Services = []resolution.Service{
		{Name: "default", Start: "npm run dev", Health: resolution.ServiceHealth{HTTP: "http://localhost:3000/health", Timeout: 5 * time.Minute, Interval: 10 * time.Second},
			Ports: []resolution.ServicePort{{Port: 3000, Label: "app"}}},
		{Name: "db", Start: "docker compose up -d db", Stop: "docker compose stop db", Health: resolution.ServiceHealth{TCP: 5432, Timeout: time.Minute}},
	}
	svcs := fake.NewServices()
	h.envp.ExecFunc = svcs.Exec(h.envp.ExecFunc)
	return h, svcs
}

func promptOf(h *harness, agent string) []string {
	var out []string
	for _, c := range h.runner.SnapshotRunCalls() {
		if c.Spec.Agent == agent {
			out = append(out, c.In.Prompt)
		}
	}
	return out
}

// A service stage starts the app, the chain goes on once it is healthy —
// the next agent handed its address — and the chain's end stops it.
func TestServiceStageStartsTheApp(t *testing.T) {
	h, svcs := serviceHarness(t, servicesFlowDoc)
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "Show the app"}, "svc", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	if v := run.Reduce(evs); v.State != run.Completed {
		t.Fatalf("state = %s, events %v", v.State, eventTypes(evs))
	}
	calls := svcs.Snapshot()
	if len(calls) == 0 || calls[0] != "start default --wait" {
		t.Errorf("calls = %v", calls)
	}
	started, _ := findPayload[*event.ServiceStarted](evs)
	if started == nil || started.Service != "default" || started.By != event.ServiceByStage || started.StageID != "up" {
		t.Errorf("service_started = %+v", started)
	}
	ready, _ := findPayload[*event.ServiceReady](evs)
	if ready == nil || ready.URL != "http://localhost:3000/" {
		t.Errorf("service_ready = %+v", ready)
	}
	prompts := promptOf(h, "shooter")
	if len(prompts) != 1 || !strings.Contains(prompts[0], "# INPUT: app") || !strings.Contains(prompts[0], "http://localhost:3000/\n") {
		t.Errorf("shooter prompt = %q", prompts)
	}
	stopped, _ := findPayload[*event.ServiceStopped](evs)
	if stopped == nil || stopped.By != event.ServiceByEngine || stopped.Service != "default" {
		t.Errorf("service_stopped = %+v", stopped)
	}
	if st := svcs.State("default"); st.Phase != "stopped" {
		t.Errorf("after the chain: %s", st.Phase)
	}
	// The stop comes before the run's end.
	ty := eventTypes(evs)
	if i, j := indexOf(ty, string(event.TypeServiceStopped)), indexOf(ty, string(event.TypeRunCompleted)); i < 0 || i > j {
		t.Errorf("events = %v", ty)
	}
}

// A start that fails, then one never healthy: each fails the stage with the
// service's output as the failure, which feeds the agent that fixes it.
func TestServiceThatDoesNotComeUpFeedsTheFix(t *testing.T) {
	h, svcs := serviceHarness(t, servicesFlowDoc)
	svcs.Outcomes["default"] = []string{fake.OutcomeExits, fake.OutcomeNever}
	svcs.Log["default"] = "npm ERR! Missing script: dev\n"
	runID, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "Show the app"}, "svc", testProject)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	evs := h.events(t, runID)
	var fails []*event.StageFailed
	for _, e := range evs {
		if p, ok := e.Payload.(*event.StageFailed); ok && p.StageID == "up" {
			fails = append(fails, p)
		}
	}
	if len(fails) != 2 {
		t.Fatalf("up failed %d times, events %v", len(fails), eventTypes(evs))
	}
	if f := fails[0]; f.Reason != event.ReasonExitNonzero || f.ExitCode == nil || *f.ExitCode != 3 || !strings.Contains(f.Error, "npm ERR! Missing script") {
		t.Errorf("first failure = %+v", f)
	}
	if f := fails[1]; f.Reason != event.ReasonTimeout || !strings.Contains(f.Error, "not healthy within 2m") {
		t.Errorf("second failure = %+v", f)
	}
	prompts := promptOf(h, "fixer")
	if len(prompts) == 0 || !strings.Contains(prompts[0], "service default did not come up: its start exited with 3") ||
		!strings.Contains(prompts[0], "npm ERR! Missing script: dev") {
		t.Errorf("fixer prompt = %q", prompts)
	}
	if got := countType(evs, event.TypeServiceFailed); got != 2 {
		t.Errorf("service_failed = %d", got)
	}
	// max: 1 — the second failure has nowhere to go.
	if v := run.Reduce(evs); v.State != run.Abandoned {
		t.Errorf("state = %s", v.State)
	}
}

// gateFlowDoc parks on a gate showing the app, so a human can act on the
// run's services while its chain is live.
const gateFlowDoc = `name: gate
version: 5
parameters: { harness: fake, model: fake/model }
main:
  - { id: up, type: service, service: default }
  - { id: ok, type: human_gate, question: "Does it look right?", input: { app: { from: run.service.default, type: link } } }
`

// The chain's end stops what its stage started, never what a human did;
// the gate hands the human the app's preview link.
func TestChainEndStopsStageServicesOnly(t *testing.T) {
	h, svcs := serviceHarness(t, gateFlowDoc)
	app, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h.res.Envp = &exposerEnvp{EnvProvisioner: h.envp, target: app.Addr().String()}
	relay := portrelay.New("127.0.0.1")
	defer relay.CloseAll()
	h.setRelay(relay)
	every := services.WatchEvery
	services.WatchEvery = time.Millisecond
	t.Cleanup(func() { services.WatchEvery = every })
	ctx := context.Background()

	runID, err := h.e.CreateRun(ctx, runinput.Ticket{Title: "Look"}, "gate", testProject)
	if err != nil {
		t.Fatal(err)
	}
	p := h.waitPing(t, PingGate)
	if !strings.HasPrefix(p.Link, "http://127.0.0.1:") {
		t.Errorf("gate link = %q, want the app's preview", p.Link)
	}

	info, err := h.services().StartService(ctx, runID, "db", false)
	if err != nil || info.By != event.ServiceByHuman {
		t.Fatalf("StartService = %+v, %v", info, err)
	}
	waitFor(t, func() bool { return countType(h.events(t, runID), event.TypeServiceReady) == 2 })
	list, err := h.services().RunServices(ctx, runID)
	if err != nil || !list.Workspace || len(list.Services) != 2 {
		t.Fatalf("RunServices = %+v, %v", list, err)
	}
	if s := list.Services[0]; !s.Healthy || s.By != event.ServiceByStage || s.StageID != "up" || len(s.Ports) != 1 ||
		s.Ports[0].Address != "http://localhost:3000/" || !strings.HasPrefix(s.Ports[0].URL, "http://127.0.0.1:") {
		t.Errorf("default = %+v", s)
	}
	if s := list.Services[1]; !s.Healthy || s.By != event.ServiceByHuman || s.Probe != "tcp 5432" {
		t.Errorf("db = %+v", s)
	}

	if err := h.e.AnswerPing(p.ID, PingAnswer{Answer: "approve"}); err != nil {
		t.Fatal(err)
	}
	h.e.Wait(runID)
	if st := svcs.State("default"); st.Phase != "stopped" {
		t.Errorf("the stage's service after the chain: %s", st.Phase)
	}
	if st := svcs.State("db"); st.Phase != "healthy" {
		t.Errorf("the human's service after the chain: %s", st.Phase)
	}

	// A human stops it; stopping it again is no new history.
	if info, err := h.services().StopService(ctx, runID, "db"); err != nil || info.State != "stopped" {
		t.Fatalf("StopService = %+v, %v", info, err)
	}
	if _, err := h.services().StopService(ctx, runID, "db"); err != nil {
		t.Fatal(err)
	}
	var byHuman int
	for _, e := range h.events(t, runID) {
		if p, ok := e.Payload.(*event.ServiceStopped); ok && p.By == event.ServiceByHuman {
			byHuman++
		}
	}
	if byHuman != 1 {
		t.Errorf("human stops recorded = %d", byHuman)
	}
	if _, err := h.services().StartService(ctx, runID, "nope", false); !errors.Is(err, services.ErrNoSuchService) {
		t.Errorf("unknown service = %v", err)
	}
	if log, err := h.services().ServiceLog(ctx, runID, "db", 50); err != nil || log != "" {
		t.Errorf("ServiceLog = %q, %v", log, err)
	}
}

// Run creation refuses a flow naming a service the project does not have,
// naming the ones it does.
func TestPreflightRefusesAnUnknownService(t *testing.T) {
	h, _ := serviceHarness(t, strings.Replace(gateFlowDoc, "service: default }", "service: web }", 1))
	_, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "Look"}, "gate", testProject)
	if err == nil || !strings.Contains(err.Error(), `service "web" is not one of the project's services (default, db)`) {
		t.Errorf("CreateRun = %v", err)
	}
	h.res.Services = nil
	_, err = h.e.CreateRun(context.Background(), runinput.Ticket{Title: "Look"}, "gate", testProject)
	if err == nil || !strings.Contains(err.Error(), "none — declare it") {
		t.Errorf("CreateRun with no services = %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

var _ envport.EnvProvisioner = (*exposerEnvp)(nil)
