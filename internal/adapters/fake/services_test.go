package fake

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"apothecary/internal/ports/envport"
	"apothecary/internal/workspace/sidecar"
)

// req is one service script: the verb's line, the request on a heredoc.
func svcScript(args, body string) envport.Command {
	return envport.Command{Script: "apothecary-shim service " + args + " <<'EOF'\n" + body + "\nEOF\n"}
}

func svcState(t *testing.T) func(envport.ExecResult, error) sidecar.ServiceState {
	return func(res envport.ExecResult, err error) sidecar.ServiceState {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var st sidecar.ServiceState
		if err := json.Unmarshal([]byte(res.Output), &st); err != nil {
			t.Fatalf("output %q: %v", res.Output, err)
		}
		return st
	}
}

func TestServicesPassThrough(t *testing.T) {
	s := NewServices()
	if res, err := s.Exec(nil)(nil, envport.Command{Script: "make test"}); err != nil || res.ExitCode != 0 {
		t.Errorf("no next = %+v, %v", res, err)
	}
	boom := errors.New("next")
	next := func(*Env, envport.Command) (envport.ExecResult, error) { return envport.ExecResult{}, boom }
	if _, err := s.Exec(next)(nil, envport.Command{Script: "make test"}); !errors.Is(err, boom) {
		t.Errorf("next = %v", err)
	}
	if res, err := s.Exec(nil)(nil, envport.Command{Script: "apothecary-shim service nonsense"}); err != nil || res.ExitCode != 2 {
		t.Errorf("unknown verb = %+v, %v", res, err)
	}
}

func TestServiceStartStates(t *testing.T) {
	s := NewServices()
	exec := s.Exec(nil)
	body := `{"name":"app","by":"human","owner":"u1","stage_id":"build","timeout":"30s"}`
	if got := s.State("app"); got.Phase != "stopped" {
		t.Errorf("never started = %+v", got)
	}
	st := svcState(t)(exec(nil, svcScript("start --wait", body)))
	if st.Phase != "healthy" || !st.Launched || st.By != "human" || st.Owner != "u1" || st.StageID != "build" || st.PID != 101 || !st.LastProbeOK || st.HealthyAt == nil {
		t.Errorf("started = %+v", st)
	}
	// A live service is left alone: the same answer, no new process.
	st = svcState(t)(exec(nil, svcScript("start --wait", body)))
	if st.Launched || st.PID != 101 {
		t.Errorf("second start = %+v", st)
	}
	st = svcState(t)(exec(nil, svcScript("start --wait --restart", body)))
	if !st.Launched || st.PID != 102 {
		t.Errorf("restart = %+v", st)
	}
	calls := strings.Join(s.Snapshot(), "|")
	if calls != "start app --wait|start app --wait|start app --wait --restart" {
		t.Errorf("calls = %s", calls)
	}
}

func TestServiceOutcomes(t *testing.T) {
	s := NewServices()
	s.Outcomes["app"] = []string{OutcomeExits, OutcomeNever}
	exec := s.Exec(nil)
	body := `{"name":"app","timeout":"5s"}`
	st := svcState(t)(exec(nil, svcScript("start --wait", body)))
	if st.Phase != "exited" || st.ExitCode == nil || *st.ExitCode != 3 || st.EndedAt == nil || !strings.Contains(st.LastError, "exited with 3") {
		t.Errorf("exits = %+v", st)
	}
	// An exited service is not live: the next start launches.
	st = svcState(t)(exec(nil, svcScript("start --wait", body)))
	if st.Phase != "unhealthy" || !strings.Contains(st.LastError, "not healthy within 5s") {
		t.Errorf("never = %+v", st)
	}
	// Unhealthy counts as retryable, even though it is live.
	st = svcState(t)(exec(nil, svcScript("start --wait", body)))
	if st.Phase != "healthy" || st.PID != 103 {
		t.Errorf("default after the list = %+v", st)
	}
}

func TestServiceStartWithoutWaitSettlesOnTheNextLook(t *testing.T) {
	s := NewServices()
	exec := s.Exec(nil)
	st := svcState(t)(exec(nil, svcScript("start", `{"name":"db"}`)))
	if st.Phase != "starting" {
		t.Errorf("no wait = %+v", st)
	}
	if got := s.State("db"); got.Phase != "healthy" {
		t.Errorf("settled = %+v", got)
	}
}

func TestServiceStatusLogsStop(t *testing.T) {
	s := NewServices()
	s.Log["app"] = "line1\n"
	exec := s.Exec(nil)
	exec(nil, svcScript("start --wait", `{"name":"app"}`))
	res, err := exec(nil, envport.Command{Script: "apothecary-shim service status"})
	if err != nil {
		t.Fatal(err)
	}
	var list []sidecar.ServiceState
	if err := json.Unmarshal([]byte(res.Output), &list); err != nil || len(list) != 1 || list[0].Name != "app" {
		t.Errorf("status = %s, %v", res.Output, err)
	}
	if res, _ := exec(nil, envport.Command{Script: "apothecary-shim service logs --tail 5 app"}); res.Output != "line1\n" {
		t.Errorf("logs = %q", res.Output)
	}
	st := svcState(t)(exec(nil, svcScript("stop", `{"name":"app"}`)))
	if st.Phase != "stopped" || st.EndedAt == nil {
		t.Errorf("stop = %+v", st)
	}
	if st := svcState(t)(exec(nil, svcScript("stop", `{"name":"ghost"}`))); st.Phase != "stopped" || st.Name != "ghost" {
		t.Errorf("stop of nothing = %+v", st)
	}
	if got := strings.Join(s.Snapshot(), "|"); !strings.Contains(got, "status|logs app|stop app|stop ghost") {
		t.Errorf("calls = %s", got)
	}
}

func TestServicesRunInFrontOfAnExecFunc(t *testing.T) {
	p, id := provisioned(t)
	s := NewServices()
	called := 0
	p.ExecFunc = s.Exec(func(*Env, envport.Command) (envport.ExecResult, error) {
		called++
		return envport.ExecResult{}, nil
	})
	p.Exec(bgCtx, id, svcScript("start --wait", `{"name":"app"}`))
	p.Exec(bgCtx, id, envport.Command{Script: "make"})
	if called != 1 || s.State("app").Phase != "healthy" {
		t.Errorf("called = %d, state = %+v", called, s.State("app"))
	}
}
