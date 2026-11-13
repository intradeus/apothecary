package httpapi

import (
	"net/http"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
)

// The war room: one live picture of every sandbox the caller has in flight,
// across all their workshops. Observation only — the run views, the
// reconciled workspace badges, the latest resource sample and the engine's
// crew board, assembled per poll; nothing here is history.

// swarmAfterglow is how long a run that ended stays on the board, so the
// war room can show how it ended before it leaves.
const swarmAfterglow = 5 * time.Minute

// Swarm is the war room's snapshot.
type Swarm struct {
	At    time.Time   `json:"at"`
	Units []SwarmUnit `json:"units"`
}

// SwarmUnit is one run's sandbox on the board.
type SwarmUnit struct {
	RunID        string    `json:"run_id"`
	ProjectID    string    `json:"project_id"`
	TicketTitle  string    `json:"ticket_title"`
	FlowName     string    `json:"flow_name"`
	State        run.State `json:"state"`
	CurrentStage string    `json:"current_stage,omitempty"`
	Phase        string    `json:"phase,omitempty"`
	// EnvStatus is the reconciled workspace status (run.View.EnvStatus).
	EnvStatus string    `json:"env_status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// OpenPings counts the questions waiting on a human; Held says a human
	// has taken the agent's session over.
	OpenPings int  `json:"open_pings"`
	Held      bool `json:"held"`
	// Resources is the newest workspace sample; absent when none was taken.
	Resources *envport.ResourceSample `json:"resources,omitempty"`
	// Crew is the agent on station; absent outside an agent's attempt.
	Crew *CrewPresence `json:"crew,omitempty"`
}

// CrewPresence is the agent on station in a run (engine.Crew).
type CrewPresence struct {
	StageID    string    `json:"stage_id"`
	Iteration  int       `json:"iteration"`
	Harness    string    `json:"harness"`
	Agent      string    `json:"agent"`
	Since      time.Time `json:"since"`
	TurnActive bool      `json:"turn_active"`
	// Tool is the title of the latest tool call in flight.
	Tool      string          `json:"tool,omitempty"`
	Subagents []SwarmSubagent `json:"subagents"`
}

// SwarmSubagent is one spawn call in flight; Count is how many subagents it
// runs.
type SwarmSubagent struct {
	ToolCallID string    `json:"tool_call_id"`
	Title      string    `json:"title"`
	Count      int       `json:"count"`
	Since      time.Time `json:"since"`
}

func (s *Server) getSwarm(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	envStatus := s.EnvState.Badges()
	crew := s.Runs.Crew()
	// The caller's runs only (the store's per-user query) — never the
	// instance's.
	views, err := (&run.Reader{Store: s.Store}).List(r.Context(), run.ListFilter{UserID: auth.UserID(r.Context())})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := Swarm{At: now, Units: []SwarmUnit{}}
	for _, v := range views {
		if v.State == run.Archived || v.State.Terminal() && now.Sub(v.UpdatedAt) > swarmAfterglow {
			continue
		}
		u := SwarmUnit{
			RunID: v.RunID, ProjectID: v.ProjectID, TicketTitle: v.TicketTitle, FlowName: v.FlowName,
			State: v.State, CurrentStage: v.CurrentStage, Phase: v.Phase, EnvStatus: v.EnvStatus,
			CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, OpenPings: len(v.OpenPings), Held: v.Hold != nil,
		}
		if st, ok := envStatus[v.RunID]; ok {
			u.EnvStatus = st
		}
		if sample, ok := s.Resources.Latest(v.RunID); ok {
			u.Resources = &sample
		}
		if c, ok := crew[v.RunID]; ok {
			u.Crew = crewPresence(c)
		}
		out.Units = append(out.Units, u)
	}
	writeJSON(w, http.StatusOK, out)
}

func crewPresence(c engine.Crew) *CrewPresence {
	p := &CrewPresence{StageID: c.StageID, Iteration: c.Iteration, Harness: c.Harness, Agent: c.Agent,
		Since: c.Since, TurnActive: c.TurnActive, Tool: c.Tool, Subagents: make([]SwarmSubagent, 0, len(c.Subagents))}
	for _, a := range c.Subagents {
		p.Subagents = append(p.Subagents, SwarmSubagent{ToolCallID: a.ToolCallID, Title: a.Title, Count: a.Count, Since: a.Since})
	}
	return p
}

// swarmRoutes is the war room's feed.
func (s *Server) swarmRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/swarm", ID: "getSwarm", Tag: tagSwarm,
			Summary: "Every sandbox one has in flight, across all one's workshops",
			Doc: "The war room's live snapshot: the caller's non-terminal runs, plus those that ended in the last five minutes, " +
				"each with its workspace status, newest resource sample and the agent on station with its subagents in flight.",
			Handler: s.getSwarm, Returns: Swarm{}},
	}
}
