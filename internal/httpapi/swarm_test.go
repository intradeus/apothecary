package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
)

// samplingEnvp adds the OPTIONAL ResourceSampler capability to the fake.
type samplingEnvp struct{ *fake.EnvProvisioner }

func (samplingEnvp) SampleResources(context.Context, envport.EnvID) (envport.ResourceSample, error) {
	return envport.ResourceSample{CPUPerc: 150, CPUs: 4, MemUsed: 1 << 30, MemLimit: 4 << 30, PIDs: 42}, nil
}

func TestGetSwarm(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.res.Envp = samplingEnvp{x.envp}
	ctx := context.Background()

	// The store's clock, shiftable into the past for a run that ended long ago.
	var shift atomic.Int64
	x.store.Clock = func() time.Time { return time.Now().Add(-time.Duration(shift.Load())) }
	seed := func(runID, user, project string, tail ...event.Payload) {
		t.Helper()
		ps := append([]event.Payload{&event.RunCreated{TicketTitle: runID, FlowName: "loop", ProjectID: project, UserID: user}}, tail...)
		if _, err := x.store.Append(ctx, runID, ps...); err != nil {
			t.Fatal(err)
		}
	}
	shift.Store(int64(10 * time.Minute))
	seed("r-old", testUser, testProject, &event.RunCompleted{})
	shift.Store(0)
	seed("r-ended", testUser, testProject, &event.RunFailed{Reason: "boom"})
	seed("r-other-workshop", testUser, "prj-second", &event.EnvRequested{}, &event.EnvReady{EnvID: "env-x"},
		&event.PingRaised{PingID: "p1", Type: "clarification", Question: "?"})
	seed("r-not-mine", "usr-someone-else", testProject)
	x.srv.EnvState.SetStatus("r-other-workshop", envport.EnvStopped)

	// A run of the caller's with an agent mid-turn and a subagent out.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	spawned := make(chan struct{})
	x.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "t1"})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTool, Turn: "t1", Tool: &agentport.AgentTool{ID: "c1",
			Title: "Explore", Kind: "other", Status: "in_progress", Input: `{"agent":"explore","task":"map it","solutionSpace":"open"}`}})
		close(spawned)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return agentport.AgentResult{TurnsUsed: 1}, nil
	}
	live := x.createTicket(t, "Live one")
	<-spawned
	if err := x.srv.Resources.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}

	var got Swarm
	var body []byte
	waitFor(t, "the live run's subagent on the board", func() bool {
		var res *http.Response
		res, body = x.do(t, "GET", "/api/swarm", nil, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/swarm = %d: %s", res.StatusCode, body)
		}
		got = Swarm{}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		for _, u := range got.Units {
			if u.RunID == live && u.Crew != nil && len(u.Crew.Subagents) == 1 {
				return true
			}
		}
		return false
	})
	if got.At.IsZero() {
		t.Error("no snapshot time")
	}
	units := map[string]SwarmUnit{}
	for _, u := range got.Units {
		units[u.RunID] = u
	}
	if _, ok := units["r-not-mine"]; ok {
		t.Error("another user's run is on the board")
	}
	if _, ok := units["r-old"]; ok {
		t.Error("a run that ended ten minutes ago is still on the board")
	}
	if u, ok := units["r-ended"]; !ok || u.State != "failed" {
		t.Errorf("a run that just ended = %+v (on the board: %v), want it with its outcome", u, ok)
	}
	// A run with no sample and no agent carries neither, not empty ones.
	var raw struct {
		Units []map[string]json.RawMessage `json:"units"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, ru := range raw.Units {
		if string(ru["run_id"]) != `"r-ended"` {
			continue
		}
		if _, ok := ru["resources"]; ok {
			t.Errorf("the ended run has resources: %s", body)
		}
		if _, ok := ru["crew"]; ok {
			t.Errorf("the ended run has a crew: %s", body)
		}
	}
	other, ok := units["r-other-workshop"]
	if !ok || other.ProjectID != "prj-second" || other.EnvStatus != string(envport.EnvStopped) || other.OpenPings != 1 {
		t.Errorf("the other workshop's run = %+v (on the board: %v)", other, ok)
	}

	u := units[live]
	if u.ProjectID != testProject || u.TicketTitle != "Live one" || u.FlowName != "loop" || u.CurrentStage != "build" {
		t.Errorf("live unit = %+v", u)
	}
	if u.Resources == nil || u.Resources.CPUPerc != 150 || u.Resources.CPUs != 4 || u.Resources.At.IsZero() {
		t.Errorf("live unit resources = %+v", u.Resources)
	}
	c := u.Crew
	if c.StageID != "build" || c.Iteration != 1 || c.Harness != "fake" || c.Agent != "builder" || !c.TurnActive ||
		c.Tool != "Explore" || c.Subagents[0].ToolCallID != "c1" || c.Subagents[0].Count != 1 {
		t.Errorf("crew = %+v", c)
	}
	// The flow's model never leaves through the war room.
	if strings.Contains(string(body), "model") {
		t.Errorf("a model field in the payload: %s", body)
	}
}

func TestGetSwarmNeedsASignedInUser(t *testing.T) {
	x := newEnv(t, engine.Config{})
	res, _ := x.do(t, "GET", "/api/swarm", nil, map[string]string{"Authorization": "none"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous GET /api/swarm = %d, want 401", res.StatusCode)
	}
}
