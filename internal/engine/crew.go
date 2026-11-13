package engine

import (
	"encoding/json"
	"slices"
	"sync"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/project/resolution"
)

// Crew is the agent on station in one run's agent stage right now: which
// stage, which harness and agent, and what it has in flight. Like the live
// deltas and the resource window it is observation, never history — it
// lives in memory only, and an engine restart empties it until the loop is
// back in the stage.
type Crew struct {
	StageID   string
	Iteration int
	// Harness is the harness kind running the stage: the stage's own, or
	// the resolution's fallback for a stage that pins none. Never empty.
	Harness string
	// Agent is the stage's agent binding name.
	Agent string
	// Since is when the agent came on station (the invocation began).
	Since time.Time
	// TurnActive: the agent is in a turn (between turn_start and turn_end).
	TurnActive bool
	// Tool is the title of the latest tool call still in flight, scrubbed.
	Tool string
	// Subagents are the subagent spawn calls in flight, oldest first.
	Subagents []Subagent
}

// Subagent is one tool call of the agent that spawned subagents and has
// not finished.
type Subagent struct {
	ToolCallID string
	// Title is the call's title, scrubbed.
	Title string
	// Count is how many subagents the call runs (a batch spawn runs many).
	Count int
	Since time.Time
}

// Drones is how many subagents are in flight across every spawn call.
func (c Crew) Drones() int {
	n := 0
	for _, s := range c.Subagents {
		n += s.Count
	}
	return n
}

// station is a boarded run: its presence plus the id of the tool call
// Tool names, so that call finishing clears it.
type station struct {
	crew   Crew
	toolID string
}

// crewBoard is every run's agent on station. The transcript writes it from
// the run loop; HTTP reads it — hence its own lock. The zero value is ready.
type crewBoard struct {
	mu   sync.Mutex
	runs map[string]*station
}

// board puts a run's agent on station, replacing any earlier presence.
func (b *crewBoard) board(runID string, c Crew) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runs == nil {
		b.runs = map[string]*station{}
	}
	b.runs[runID] = &station{crew: c}
}

// leave takes a run's agent off station.
func (b *crewBoard) leave(runID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.runs, runID)
}

// turn records a turn starting or ending. A turn that ends takes whatever
// it still had in flight with it: a cancelled turn never reports its
// pending calls finished.
func (b *crewBoard) turn(runID string, active bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.runs[runID]
	if s == nil {
		return
	}
	s.crew.TurnActive = active
	if !active {
		s.crew.Tool, s.toolID, s.crew.Subagents = "", "", nil
	}
}

// tool folds one tool call's merged state in: in flight it is the latest
// tool (and a subagent spawn when spawns > 0); finished it is dropped.
func (b *crewBoard) tool(runID, id, title, status string, spawns int, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.runs[runID]
	if s == nil {
		return
	}
	i := slices.IndexFunc(s.crew.Subagents, func(a Subagent) bool { return a.ToolCallID == id })
	if status == "completed" || status == "failed" {
		if i >= 0 {
			s.crew.Subagents = slices.Delete(s.crew.Subagents, i, i+1)
		}
		if s.toolID == id {
			s.crew.Tool, s.toolID = "", ""
		}
		return
	}
	s.crew.Tool, s.toolID = title, id
	switch {
	case spawns <= 0 && i >= 0:
		s.crew.Subagents = slices.Delete(s.crew.Subagents, i, i+1)
	case spawns > 0 && i >= 0:
		s.crew.Subagents[i].Title, s.crew.Subagents[i].Count = title, spawns
	case spawns > 0:
		s.crew.Subagents = append(s.crew.Subagents, Subagent{ToolCallID: id, Title: title, Count: spawns, Since: at})
	}
}

// snapshot copies the board: callers may keep or change what they get.
func (b *crewBoard) snapshot() map[string]Crew {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]Crew, len(b.runs))
	for id, s := range b.runs {
		c := s.crew
		c.Subagents = slices.Clone(c.Subagents)
		out[id] = c
	}
	return out
}

// Crew returns every run's agent on station now, by run id — a copy.
func (e *Engine) Crew() map[string]Crew {
	return e.crew.snapshot()
}

// stationHarness is the harness kind a stage runs on: its own, or the
// resolution's fallback when it pins none (Resolution.RunnerFor).
func stationHarness(st *flow.Stage, res *resolution.Resolution) string {
	if st.Harness != "" {
		return st.Harness
	}
	return res.HarnessKind
}

// subagentSpawns is how many subagents a tool call runs, read from the
// shape of its title and raw input (agentport.AgentTool) — ACP has no notion
// of a subagent, so the harnesses' own spawn tools are recognised as they
// arrive (IMPLEMENTATION_NOTES, "War room"). 0 for an ordinary tool, for
// input that is not (or no longer, once truncated) a JSON object, and for
// any shape not known here: a drone is never counted on a guess.
func subagentSpawns(title, input string) int {
	// codex's collab tool, titled by its camelCase name; its siblings
	// (wait, sendInput, closeAgent) spawn nothing.
	if title == "spawnAgent" {
		return 1
	}
	var in map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &in) != nil {
		return 0
	}
	// A batch: {tasks: [{task, ...}, ...]} (omp's task tool), one subagent
	// per assignment.
	if raw, ok := in["tasks"]; ok {
		var tasks []map[string]json.RawMessage
		if json.Unmarshal(raw, &tasks) != nil {
			return 0
		}
		n := 0
		for _, t := range tasks {
			if _, ok := t["task"]; ok {
				n++
			}
		}
		return n
	}
	_, prompt := in["prompt"]
	_, subagentType := in["subagent_type"]
	_, task := in["task"]
	_, solutionSpace := in["solutionSpace"]
	switch {
	case prompt && subagentType: // opencode's task tool
		return 1
	case task && solutionSpace: // omp's task tool, one assignment
		return 1
	}
	return 0
}
