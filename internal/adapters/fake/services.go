package fake

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"apothecary/internal/ports/envport"
	"apothecary/internal/sidecar"
)

// Services stands in for the workspace's service supervisor — the
// `apothecary-shim service start|stop|status|logs` scripts the engine execs
// (workspace/shim/service.go) — in memory. Install it in front of whatever
// ExecFunc a test has: p.ExecFunc = svcs.Exec(p.ExecFunc).
type Services struct {
	mu     sync.Mutex
	states map[string]*sidecar.ServiceState
	starts map[string]int
	// pending is what a start without --wait turns into on the next look.
	pending map[string]string

	// Outcomes scripts successive starts of a service: OutcomeHealthy (the
	// default once the list runs out), OutcomeExits (its start exits 3),
	// OutcomeNever (not healthy within its timeout, still running).
	Outcomes map[string][]string
	// Log is each service's output, served by logs.
	Log map[string]string
	// Calls records each command: "start app --wait", "stop app", "status".
	Calls []string
}

const (
	OutcomeHealthy = "healthy"
	OutcomeExits   = "exits"
	OutcomeNever   = "never"
)

func NewServices() *Services {
	return &Services{states: map[string]*sidecar.ServiceState{}, starts: map[string]int{},
		pending: map[string]string{}, Outcomes: map[string][]string{}, Log: map[string]string{}}
}

// State is the service's current state (stopped when never started).
func (s *Services) State(name string) sidecar.ServiceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settle()
	if st, ok := s.states[name]; ok {
		return *st
	}
	return sidecar.ServiceState{Name: name, Phase: "stopped"}
}

// Snapshot is a copy of the calls so far.
func (s *Services) Snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Calls...)
}

// Exec answers the service scripts and hands everything else to next.
func (s *Services) Exec(next func(*Env, envport.Command) (envport.ExecResult, error)) func(*Env, envport.Command) (envport.ExecResult, error) {
	return func(env *Env, cmd envport.Command) (envport.ExecResult, error) {
		first, rest, _ := strings.Cut(cmd.Script, "\n")
		args, ok := strings.CutPrefix(first, "apothecary-shim service ")
		if !ok {
			if next == nil {
				return envport.ExecResult{}, nil
			}
			return next(env, cmd)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		args, _, _ = strings.Cut(args, " <<") // the request rides a heredoc
		f := strings.Fields(args)
		var req struct {
			Name    string `json:"name"`
			By      string `json:"by"`
			Owner   string `json:"owner"`
			StageID string `json:"stage_id"`
			Timeout string `json:"timeout"`
		}
		if body, _, ok := strings.Cut(rest, "\n"); ok {
			_ = json.Unmarshal([]byte(body), &req)
		}
		verb := f[0]
		has := func(flag string) bool { return strings.Contains(args, flag) }
		switch verb {
		case "status":
			s.Calls = append(s.Calls, "status")
			s.settle()
			out := []sidecar.ServiceState{}
			for _, st := range s.states {
				out = append(out, *st)
			}
			return jsonResult(out)
		case "logs":
			name := f[len(f)-1]
			s.Calls = append(s.Calls, "logs "+name)
			return envport.ExecResult{Output: s.Log[name]}, nil
		case "stop":
			s.Calls = append(s.Calls, "stop "+req.Name)
			st := s.states[req.Name]
			if st == nil {
				return jsonResult(sidecar.ServiceState{Name: req.Name, Phase: "stopped"})
			}
			now := time.Now().UTC()
			st.Phase, st.EndedAt = "stopped", &now
			delete(s.pending, req.Name)
			return jsonResult(*st)
		case "start":
			call := "start " + req.Name
			for _, fl := range f[1:] {
				call += " " + fl
			}
			s.Calls = append(s.Calls, call)
			s.settle()
			cur := s.states[req.Name]
			if cur != nil && live(cur.Phase) && !has("--restart") && cur.Phase != "unhealthy" {
				return jsonResult(*cur)
			}
			s.starts[req.Name]++
			outcome := OutcomeHealthy
			if o := s.Outcomes[req.Name]; len(o) >= s.starts[req.Name] {
				outcome = o[s.starts[req.Name]-1]
			}
			now := time.Now().UTC()
			st := &sidecar.ServiceState{Name: req.Name, Phase: "starting", By: req.By, Owner: req.Owner,
				StageID: req.StageID, StartedAt: &now, PID: 100 + s.starts[req.Name]}
			s.states[req.Name] = st
			if has("--wait") {
				apply(st, outcome, req.Timeout)
			} else {
				s.pending[req.Name] = outcome
			}
			out := *st
			out.Launched = true
			return jsonResult(out)
		}
		return envport.ExecResult{ExitCode: 2, Output: "usage"}, nil
	}
}

// settle lets starts made without --wait come up (or not).
func (s *Services) settle() {
	for name, outcome := range s.pending {
		if st := s.states[name]; st != nil {
			apply(st, outcome, "")
		}
		delete(s.pending, name)
	}
}

func apply(st *sidecar.ServiceState, outcome, timeout string) {
	now := time.Now().UTC()
	switch outcome {
	case OutcomeExits:
		code := 3
		st.Phase, st.ExitCode, st.EndedAt = "exited", &code, &now
		st.LastError = "its start exited with 3"
	case OutcomeNever:
		st.Phase = "unhealthy"
		st.LastError = fmt.Sprintf("not healthy within %s: dial tcp: connection refused", timeout)
	default:
		st.Phase, st.HealthyAt, st.LastProbeOK = "healthy", &now, true
	}
}

func live(phase string) bool {
	return phase == "starting" || phase == "healthy" || phase == "unhealthy" || phase == "stopping"
}

func jsonResult(v any) (envport.ExecResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return envport.ExecResult{}, err
	}
	return envport.ExecResult{Output: string(data) + "\n"}, nil
}
