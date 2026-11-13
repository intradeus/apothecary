package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/flow"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run/runinput"
)

// The harnesses' spawn tools as their ACP adapters send them (the pinned
// versions; IMPLEMENTATION_NOTES, "War room").
func TestSubagentSpawns(t *testing.T) {
	cases := []struct {
		name, title, input string
		want               int
	}{
		{"opencode task", "Find auth code", `{"description":"Find auth code","prompt":"Locate the auth code","subagent_type":"explore"}`, 1},
		{"opencode task, pending with no input yet", "task", `{}`, 0},
		{"codex spawn_agent", "spawnAgent", `{"prompt":"Investigate failing test","senderThreadId":"019a","receiverThreadIds":[],"agentsStates":{},"model":null,"reasoningEffort":null,"status":"inProgress"}`, 1},
		{"codex wait", "wait", `{"prompt":null,"senderThreadId":"019a","receiverThreadIds":["019b"],"agentsStates":{}}`, 0},
		{"omp task, one assignment", "Map auth flow", `{"agent":"explore","task":"Map auth flow","solutionSpace":"read-only"}`, 1},
		{"omp task, a batch", "Two scouts", `{"context":"# Goal","tasks":[{"name":"A","agent":"task","task":"a","solutionSpace":"x"},{"name":"B","agent":"scout","task":"b","solutionSpace":"y"},{"name":"C","agent":"scout","task":"c","solutionSpace":"z"}]}`, 3},
		{"a batch of something else", "plan", `{"tasks":[{"title":"x"},{"title":"y"}]}`, 0},
		{"tasks that are not a list", "plan", `{"tasks":"many"}`, 0},
		{"read", "Read go.mod", `{"path":"go.mod"}`, 0},
		{"edit", "Edit a.go", `{"path":"a.go","old":"x","new":"y"}`, 0},
		{"execute", "go test ./...", `{"command":"go test ./..."}`, 0},
		{"truncated batch", "Two scouts", `{"context":"# Goal","tasks":[{"name":"A","agent":"task","task":"a","solu`, 0},
		{"not an object", "task", `["prompt","subagent_type"]`, 0},
		{"no input", "task", ``, 0},
	}
	for _, c := range cases {
		if got := subagentSpawns(c.title, c.input); got != c.want {
			t.Errorf("%s: %d subagents, want %d", c.name, got, c.want)
		}
	}
}

func TestStationHarnessFallsBackToTheResolution(t *testing.T) {
	res := &resolution.Resolution{HarnessKind: "opencode"}
	if got := stationHarness(&flow.Stage{Harness: "omp"}, res); got != "omp" {
		t.Errorf("pinned stage = %q", got)
	}
	if got := stationHarness(&flow.Stage{}, res); got != "opencode" {
		t.Errorf("unpinned stage = %q, want the resolution's kind", got)
	}
}

func TestCrewBoard(t *testing.T) {
	var b crewBoard
	at := time.Unix(100, 0)
	b.tool("r1", "t1", "Read", "in_progress", 0, at) // not boarded: ignored
	b.turn("r1", true)
	if len(b.snapshot()) != 0 {
		t.Fatal("an unboarded run reached the board")
	}
	b.board("r1", Crew{StageID: "build", Iteration: 1, Harness: "fake", Agent: "builder", Since: at})
	b.turn("r1", true)
	b.tool("r1", "t1", "Read go.mod", "in_progress", 0, at)
	b.tool("r1", "s1", "task", "pending", 0, at)              // a spawn tool before its input arrived
	b.tool("r1", "s1", "Scouts", "in_progress", 2, at)        // ...and once it did
	b.tool("r1", "s2", "Explore", "in_progress", 1, at)       // a second spawn
	b.tool("r1", "s2", "Explore again", "in_progress", 1, at) // an update renames it
	c := b.snapshot()["r1"]
	if !c.TurnActive || c.Tool != "Explore again" || len(c.Subagents) != 2 || c.Drones() != 3 ||
		c.Subagents[0].Title != "Scouts" || c.Subagents[1].Title != "Explore again" {
		t.Fatalf("crew = %+v", c)
	}

	// Snapshots are copies.
	c.Subagents[0].Title = "changed"
	if b.snapshot()["r1"].Subagents[0].Title != "Scouts" {
		t.Fatal("a snapshot aliased the board")
	}

	b.tool("r1", "s2", "", "failed", 0, at)
	b.tool("r1", "t1", "", "completed", 0, at) // not the latest tool: the latest stays
	c = b.snapshot()["r1"]
	if c.Drones() != 2 || c.Tool != "" {
		t.Fatalf("after s2 failed: %+v", c)
	}
	b.tool("r1", "s1", "Scouts", "in_progress", 0, at) // no longer reads as a spawn
	if got := b.snapshot()["r1"].Drones(); got != 0 {
		t.Fatalf("drones = %d", got)
	}

	// The end of a turn takes what is still in flight with it.
	b.tool("r1", "s3", "Explore", "pending", 1, at)
	b.turn("r1", false)
	if c = b.snapshot()["r1"]; c.TurnActive || c.Tool != "" || len(c.Subagents) != 0 {
		t.Fatalf("after turn_end: %+v", c)
	}
	b.leave("r1")
	if _, ok := b.snapshot()["r1"]; ok {
		t.Fatal("left, still on the board")
	}
}

// The board as an agent's turn plays out: who is on station, the spawns in
// flight, secrets out of the titles, and nobody once the attempt is over.
func TestCrewFollowsTheAgent(t *testing.T) {
	const token = "sk-test" // the harness's provider key
	h := newHarness(t, mustFlow(t, askFlowDoc))
	step := make(chan string)
	next := make(chan struct{})
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		emit := call.In.Events
		at := func(what string) { step <- what; <-next }
		emit(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s1"})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "t"})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTool, Turn: "t", Tool: &agentport.AgentTool{ID: "c1", Title: "Scout with " + token,
			Kind: "other", Status: "in_progress", Input: `{"context":"c","tasks":[{"task":"a","solutionSpace":"x"},{"task":"b","solutionSpace":"y"}]}`}})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTool, Turn: "t", Tool: &agentport.AgentTool{ID: "c2", Title: "Read go.mod",
			Kind: "read", Status: "in_progress", Input: `{"path":"go.mod"}`}})
		at("spawned")
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTool, Turn: "t", Tool: &agentport.AgentTool{ID: "c1", Status: "completed"}})
		at("landed")
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTool, Turn: "t", Tool: &agentport.AgentTool{ID: "c3", Title: "spawnAgent", Status: "pending"}})
		emit(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "t"})
		at("ended")
		return agentport.AgentResult{TurnsUsed: 1, Session: "s1"}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	crew := func() (Crew, bool) { c, ok := h.e.Crew()[id]; return c, ok }

	if got := <-step; got != "spawned" {
		t.Fatalf("step %q", got)
	}
	waitUntil(t, "two drones and the read in flight", func() bool {
		c, _ := crew()
		return c.Drones() == 2 && c.Tool == "Read go.mod"
	})
	c, _ := crew()
	if c.StageID != "build" || c.Iteration != 1 || c.Harness != "fake" || c.Agent != "builder" || !c.TurnActive || c.Since.IsZero() {
		t.Errorf("on station = %+v", c)
	}
	if title := c.Subagents[0].Title; strings.Contains(title, token) || !strings.HasPrefix(title, "Scout with ") {
		t.Errorf("subagent title = %q, want it scrubbed", title)
	}
	next <- struct{}{}

	if got := <-step; got != "landed" {
		t.Fatalf("step %q", got)
	}
	waitUntil(t, "the spawn landing", func() bool { c, _ := crew(); return c.Drones() == 0 })
	next <- struct{}{}

	if got := <-step; got != "ended" {
		t.Fatalf("step %q", got)
	}
	waitUntil(t, "the turn's end clearing a pending spawn", func() bool {
		c, _ := crew()
		return !c.TurnActive && len(c.Subagents) == 0 && c.Tool == ""
	})
	next <- struct{}{}

	h.e.Wait(id)
	if _, ok := crew(); ok {
		t.Error("the run is still on the board after its attempt returned")
	}
}

// An attempt parked on a question has nobody on station.
func TestCrewLeavesWhenTheAttemptParks(t *testing.T) {
	h := newHarness(t, mustFlow(t, askFlowDoc))
	runIDCh := make(chan string, 1)
	onStation := make(chan bool, 1)
	h.runner.RunFunc = func(_ context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		id := <-runIDCh
		_, ok := h.e.Crew()[id]
		onStation <- ok
		if _, err := h.e.RaisePing(id, "clarification", "one endpoint or two?", "", nil, nil); err != nil {
			return agentport.AgentResult{}, err
		}
		return agentport.AgentResult{TurnsUsed: 1, Session: "sess-1"}, nil
	}
	id, err := h.e.CreateRun(context.Background(), runinput.Ticket{Title: "T"}, "ask", testProject)
	if err != nil {
		t.Fatal(err)
	}
	runIDCh <- id
	if !<-onStation {
		t.Error("the agent was not on station while its turn ran")
	}
	h.waitPing(t, "clarification")
	waitUntil(t, "the parked run leaving the board", func() bool { _, ok := h.e.Crew()[id]; return !ok })
}
