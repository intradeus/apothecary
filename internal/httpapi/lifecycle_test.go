package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

func (x *env) runState(t *testing.T, id string) run.State {
	t.Helper()
	evs, err := x.store.Events(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return run.Reduce(evs).State
}

// Pause parks a run in place and play picks it up, the same id throughout; a
// run with nothing to pause or play says so.
func TestPauseAndPlay(t *testing.T) {
	x := newEnv(t, engine.Config{})
	release := make(chan struct{})
	x.runner.RunFunc = blockUntil(release)
	id := x.createTicket(t, "T")
	waitFor(t, "the run to start", func() bool { return x.e.RunToken(id) != "" })

	if res, body := x.do(t, "POST", "/api/runs/"+id+"/play", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("play while active = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/pause", nil, nil); res.StatusCode != http.StatusAccepted {
		t.Fatalf("pause = %d %s", res.StatusCode, body)
	}
	x.e.Wait(id)
	if st := x.runState(t, id); st != run.Paused {
		t.Fatalf("state after pause = %s", st)
	}
	close(release)
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/play", nil, nil); res.StatusCode != http.StatusAccepted {
		t.Fatalf("play = %d %s", res.StatusCode, body)
	}
	x.e.Wait(id)
	if st := x.runState(t, id); st != run.Completed {
		t.Errorf("state after play = %s", st)
	}

	// A finished run is neither pausable nor playable, nor abortable.
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/pause", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("pause of a finished run = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/play", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("play of a finished run = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/abort", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("abort of a finished run = %d %s", res.StatusCode, body)
	}

	// An id that went away between the guard and the verb.
	gone := map[string]string{"id": "gone"}
	for name, h := range map[string]http.HandlerFunc{"play": x.srv.postPlay, "abort": x.srv.postAbort, "retry": x.srv.postRetry, "archive": x.srv.postArchive} {
		if w := call(h, "POST", "/", "", testUser, gone); w.Code != http.StatusNotFound {
			t.Errorf("%s of nothing = %d %s", name, w.Code, w.Body)
		}
	}
}

// Retry re-enters the failed stage in the same workspace, under the same id.
func TestRetryAFailedRun(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.runner.RunFunc = func(context.Context, fake.RunCall) (agentport.AgentResult, error) {
		return agentport.AgentResult{}, errors.New("the model refused")
	}
	id := x.createTicket(t, "T")
	x.e.Wait(id)
	if st := x.runState(t, id); !st.Terminal() || st == run.Completed {
		t.Fatalf("setup: state = %s", st)
	}

	x.runner.RunFunc = nil
	res, body := x.do(t, "POST", "/api/runs/"+id+"/retry", nil, nil)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("retry = %d %s", res.StatusCode, body)
	}
	x.e.Wait(id)
	if st := x.runState(t, id); st != run.Completed {
		t.Errorf("state after retry = %s", st)
	}
	// Completed is not a failure to try again.
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/retry", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("retry of a completed run = %d %s", res.StatusCode, body)
	}
}

func TestRetryAndArchiveWhileActive(t *testing.T) {
	x := newEnv(t, engine.Config{})
	release := make(chan struct{})
	defer close(release)
	x.runner.RunFunc = blockUntil(release)
	id := x.createTicket(t, "T")
	for _, verb := range []string{"retry", "archive", "rerun"} {
		if res, body := x.do(t, "POST", "/api/runs/"+id+"/"+verb, nil, nil); res.StatusCode != http.StatusConflict {
			t.Errorf("%s of an active run = %d %s", verb, res.StatusCode, body)
		}
	}
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/rebuild", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("rebuild of an active run = %d %s", res.StatusCode, body)
	}
}

func TestPhasesRoute(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id := x.createTicket(t, "T")
	x.e.Wait(id)
	res, body := x.do(t, "GET", "/api/runs/"+id+"/phases", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("phases = %d %s", res.StatusCode, body)
	}
	if phases := decode[[]engine.PhaseInfo](t, body); len(phases) == 0 {
		t.Errorf("no phases: %s", body)
	}
	// A body that is not JSON is the same as no subject.
	res, body2 := x.bare(t, "POST", "/api/runs/"+id+"/phases/main/start", "not json", map[string]string{"Authorization": "Bearer " + x.token})
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusBadRequest {
		t.Errorf("start with a junk body = %d %s", res.StatusCode, body2)
	}
}

// The list filters compose, newest first, with live overlays.
func TestRunListFilters(t *testing.T) {
	x := newEnv(t, engine.Config{})
	first := x.newTicket(t, "first", "")
	resp, data := x.startRun(t, first, "loop")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start = %d %s", resp.StatusCode, data)
	}
	a := decode[RunRef](t, data).RunID
	x.e.Wait(a)
	b := x.createTicket(t, "second")
	x.e.Wait(b)

	ids := func(q string) []string {
		t.Helper()
		_, body := x.do(t, "GET", "/api/runs"+q, nil, nil)
		var out []string
		for _, v := range decode[[]run.View](t, body) {
			out = append(out, v.RunID)
		}
		return out
	}
	if got := ids(""); len(got) != 2 || got[0] != b || got[1] != a {
		t.Errorf("all, newest first = %v (want %s, %s)", got, b, a)
	}
	if got := ids("?ticket=" + first); len(got) != 1 || got[0] != a {
		t.Errorf("by ticket = %v", got)
	}
	if got := ids("?project=" + testProject); len(got) != 2 {
		t.Errorf("by project = %v", got)
	}
	if got := ids("?project=nope"); len(got) != 0 {
		t.Errorf("by another project = %v", got)
	}
	if got := ids("?project=" + testProject + "&ticket=nope"); len(got) != 0 {
		t.Errorf("both filters = %v", got)
	}
}

// A long run's detail carries the tail of its log.
func TestRunDetailTailsLongLogs(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	if _, err := x.store.Append(ctx, "r1", &event.RunCreated{ProjectID: testProject, UserID: testUser, FlowName: "loop"}); err != nil {
		t.Fatal(err)
	}
	for i := range 120 {
		x.raisePing(t, "r1", "p"+itoa(int64(i)))
	}
	res, body := x.do(t, "GET", "/api/runs/r1", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("detail = %d", res.StatusCode)
	}
	d := decode[RunDetail](t, body)
	if len(d.Events) != 100 || d.Events[99].Seq != 121 {
		t.Errorf("tail = %d events ending at %d", len(d.Events), d.Events[len(d.Events)-1].Seq)
	}
}

// A store that keeps run rows answers the detail from the row: a long run's
// log is not read and reduced on every request (the page asks often).
func TestRunDetailReadsTheRowNotTheLog(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	row := run.View{RunID: "r1", UserID: testUser, TicketTitle: "from the row"}
	x.srv.Store = viewStore{
		Store: brokenStore{Store: x.store, events: errors.New("the log was read")},
		view:  row, found: true,
	}
	w := call(x.srv.getRun, "GET", "/", "", testUser, map[string]string{"id": "r1"})
	if w.Code != http.StatusOK {
		t.Fatalf("detail = %d %s", w.Code, w.Body)
	}
	d := decode[RunDetail](t, w.Body.Bytes())
	if d.Run.TicketTitle != "from the row" || len(d.Events) == 0 {
		t.Errorf("detail = %+v", d)
	}
}

// rebuildEnv is a provisioner that can recreate a workspace in place.
type rebuildEnv struct {
	*fake.EnvProvisioner
	err     error
	noCache []bool
}

func (r *rebuildEnv) Rebuild(_ context.Context, spec envport.EnvSpec) error {
	r.noCache = append(r.noCache, spec.NoCache)
	return r.err
}

// A rebuild passes its one option through; a body is optional, and a failed
// rebuild is the caller's to read.
func TestRebuildWorkspace(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id := x.createTicket(t, "T")
	x.e.Wait(id)
	r := &rebuildEnv{EnvProvisioner: x.envp}
	x.res.Envp = r
	path := "/api/runs/" + id + "/rebuild"

	if res, body := x.do(t, "POST", path, RebuildRequest{NoCache: true}, nil); res.StatusCode != http.StatusAccepted {
		t.Fatalf("rebuild = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", path, nil, nil); res.StatusCode != http.StatusAccepted {
		t.Fatalf("rebuild with no body = %d %s", res.StatusCode, body)
	}
	if len(r.noCache) != 2 || !r.noCache[0] || r.noCache[1] {
		t.Errorf("no_cache passed through = %v", r.noCache)
	}
	r.err = errors.New("devcontainer build failed")
	if res, body := x.do(t, "POST", path, nil, nil); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "devcontainer build failed") {
		t.Errorf("failed rebuild = %d %s", res.StatusCode, body)
	}
}

// A run with no workspace has nothing to rebuild.
func TestRebuildWithNoWorkspace(t *testing.T) {
	x := newEnv(t, engine.Config{})
	if _, err := x.store.Append(context.Background(), "r1", &event.RunCreated{ProjectID: testProject, UserID: testUser, FlowName: "loop"}); err != nil {
		t.Fatal(err)
	}
	if res, body := x.do(t, "POST", "/api/runs/r1/rebuild", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("rebuild = %d %s", res.StatusCode, body)
	}
}

func TestRerunRefusals(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id := x.createTicket(t, "T")
	x.e.Wait(id)
	res, body := x.do(t, "POST", "/api/runs/"+id+"/rerun", map[string]any{"flow": "no-such-flow"}, nil)
	if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
		t.Errorf("rerun on an unknown flow = %d %s", res.StatusCode, body)
	}
	if w := call(x.srv.postRebuild, "POST", "/", "", testUser, map[string]string{"id": "gone"}); w.Code != http.StatusNotFound {
		t.Errorf("rebuild of nothing = %d %s", w.Code, w.Body)
	}
}

// Taking over and giving back: both verbs map the engine's refusals.
func TestHoldRoutes(t *testing.T) {
	x := newEnv(t, engine.Config{})
	gone := map[string]string{"id": "gone"}
	for name, h := range map[string]http.HandlerFunc{"hold": x.srv.postHold, "release": x.srv.deleteHold} {
		w := call(h, "POST", "/", "", testUser, gone)
		if w.Code != http.StatusNotFound && w.Code != http.StatusConflict {
			t.Errorf("%s of nothing = %d %s", name, w.Code, w.Body)
		}
	}
	turn := make(chan struct{})
	x.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		select {
		case <-turn:
		case <-ctx.Done():
		}
		return agentport.AgentResult{TurnsUsed: 1, Session: "s"}, nil
	}
	defer close(turn)
	id := x.createTicket(t, "T")
	var status int
	waitFor(t, "the take-over", func() bool {
		res, _ := x.do(t, "POST", "/api/runs/"+id+"/hold", nil, nil)
		status = res.StatusCode
		return status != http.StatusConflict
	})
	if status != http.StatusAccepted {
		t.Fatalf("hold = %d", status)
	}
	if res, body := x.do(t, "POST", "/api/runs/"+id+"/hold", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("second hold = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "DELETE", "/api/runs/"+id+"/hold", nil, nil); res.StatusCode != http.StatusAccepted {
		t.Errorf("release = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "DELETE", "/api/runs/"+id+"/hold", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("second release = %d %s", res.StatusCode, body)
	}
	if res, _ := x.bare(t, "POST", "/api/runs/"+id+"/messages", "{", map[string]string{"Authorization": "Bearer " + x.token}); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed message = %d", res.StatusCode)
	}
}

// A take-over stops the agent's turn unless the body says force: false; no
// body at all is the forced default, a malformed one a 400 — trailing data
// after the JSON value included. A chunked body (length unknown) is read like
// any other, not taken for an absent one.
func TestTakeOverForce(t *testing.T) {
	x := newEnv(t, engine.Config{})
	stopped := make(chan bool, 1)
	var mu sync.Mutex
	seen := map[envport.EnvID]bool{}
	x.runner.RunFunc = func(ctx context.Context, call fake.RunCall) (agentport.AgentResult, error) {
		mu.Lock()
		first := !seen[call.Env]
		seen[call.Env] = true
		mu.Unlock()
		if !first { // the run's later stages: nothing to take over
			return agentport.AgentResult{TurnsUsed: 1, Session: "s"}, nil
		}
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: "t1"})
		call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: "s"}) // the turn is open
		for {
			select {
			case <-call.In.Inbox.Wake():
				if !call.In.Inbox.StopTurn("t1") {
					continue
				}
				stopped <- true
				call.In.Events(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: "t1", StopReason: "cancelled"})
			case <-ctx.Done():
				stopped <- false
			}
			return agentport.AgentResult{TurnsUsed: 1, Session: "s"}, nil
		}
	}
	auth := map[string]string{"Authorization": "Bearer " + x.token, "Content-Type": "application/json"}
	take := func(id, body string, chunked bool) int {
		t.Helper()
		waitFor(t, "the open turn", func() bool {
			evs, _ := x.store.Events(context.Background(), id, 0)
			for _, ev := range evs {
				if ev.Type == event.TypeAgentSession {
					return true
				}
			}
			return false
		})
		req, err := http.NewRequest("POST", x.ts.URL+"/api/runs/"+id+"/hold", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range auth {
			req.Header.Set(k, v)
		}
		if chunked {
			req.ContentLength = -1
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out := readBody(res)
		if res.StatusCode != http.StatusAccepted && res.StatusCode != http.StatusBadRequest {
			t.Fatalf("hold %q (chunked %v) = %d %s", body, chunked, res.StatusCode, out)
		}
		return res.StatusCode
	}
	type hold struct {
		body    string
		chunked bool
	}
	for _, h := range []hold{{"", false}, {`{}`, false}, {`{"force":true}`, false}, {`{"force":true}`, true}} {
		id := x.createTicket(t, "T")
		if take(id, h.body, h.chunked) != http.StatusAccepted {
			t.Fatalf("hold %+v refused", h)
		}
		if !<-stopped {
			t.Errorf("hold %+v did not stop the turn", h)
		}
		x.e.Wait(id)
	}

	for _, chunked := range []bool{false, true} {
		id := x.createTicket(t, "T")
		for _, bad := range []string{"{", `{"force":false}garbage`, `{"force":false}{"force":true}`, `{"force":false}{`} {
			if code := take(id, bad, chunked); code != http.StatusBadRequest {
				t.Errorf("malformed hold %q (chunked %v) = %d", bad, chunked, code)
			}
		}
		if take(id, "{\"force\":false}\n", chunked) != http.StatusAccepted { // trailing whitespace is no second value
			t.Fatalf("passive hold (chunked %v) refused", chunked)
		}
		evs, _ := x.store.Events(context.Background(), id, 0)
		if run.Reduce(evs).Hold == nil {
			t.Fatalf("passive hold (chunked %v) not taken", chunked)
		}
		select {
		case <-stopped:
			t.Fatalf("a passive take-over (chunked %v) stopped the turn", chunked)
		case <-time.After(50 * time.Millisecond):
		}
		if err := x.e.Abort(id); err != nil {
			t.Fatal(err)
		}
		<-stopped
		x.e.Wait(id)
	}
}

func TestExposePortBody(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	tok := map[string]string{"Authorization": "Bearer " + x.token}
	if res, _ := x.bare(t, "POST", "/api/runs/r1/expose-port", "{", tok); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
	for _, port := range []int{-1, 0, 65536} {
		res, body := x.do(t, "POST", "/api/runs/r1/expose-port", ExposePortRequest{Port: port}, nil)
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "1-65535") {
			t.Errorf("port %d = %d %s", port, res.StatusCode, body)
		}
	}
}

// The scanner's report is trimmed to what the page shows: oversized labels are
// cut, and an already exposed port is not "discovered".
func TestObservedPortsAreSanitised(t *testing.T) {
	x := newEnv(t, engine.Config{})
	release := make(chan struct{})
	defer close(release)
	x.runner.RunFunc = blockUntil(release)
	id := x.createTicket(t, "T")
	var tok string
	waitFor(t, "the run token", func() bool { tok = x.e.RunToken(id); return tok != "" })
	bearer := map[string]string{"Authorization": "Bearer " + tok}
	long := strings.Repeat("p", 100)
	res, body := x.do(t, "POST", "/api/runs/"+id+"/observed-ports", ObservedPortsRequest{Ports: []envport.ObservedPort{
		{Port: 8080, Process: long, Service: long}, {Port: 70000}, {Port: -1},
	}}, bearer)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("report = %d %s", res.StatusCode, body)
	}
	_, body = x.do(t, "GET", "/api/runs/"+id+"/ports", nil, nil)
	got := decode[RunPorts](t, body)
	if len(got.Observed) != 1 || len(got.Observed[0].Process) != 64 || len(got.Observed[0].Service) != 64 {
		t.Errorf("observed = %+v", got.Observed)
	}
	if got.Links {
		t.Error("no relay: no browser links, and the page must be told so")
	}
	if res, _ := x.bare(t, "POST", "/api/runs/"+id+"/observed-ports", "{", bearer); res.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d", res.StatusCode)
	}
}

func TestResourcesAreAListEvenWhenEmpty(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	res, body := x.do(t, "GET", "/api/runs/r1/resources", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"samples":[]`) {
		t.Errorf("resources = %d %s", res.StatusCode, body)
	}
}

func TestServiceErrorStatuses(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.seedLiveRun(t, "r1")
	// Unknown service on the log route and an out-of-range tail both land on
	// the same 404: the tail falls back to the default.
	if res, body := x.do(t, "GET", "/api/runs/r1/services/nope/logs?tail=99999", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("logs of nothing = %d %s", res.StatusCode, body)
	}
	if w := call(x.srv.getRunServices, "GET", "/", "", testUser, map[string]string{"id": "gone"}); w.Code != http.StatusNotFound {
		t.Errorf("services of nothing = %d %s", w.Code, w.Body)
	}
}

// Wake starts a workspace stopped when idle; one that will not come back is
// a 502 saying why, and an awake one is left alone.
func TestWakeWorkspace(t *testing.T) {
	x := newEnv(t, engine.Config{})
	id := x.createTicket(t, "T")
	x.e.Wait(id)
	_, d := x.do(t, "GET", "/api/runs/"+id, nil, nil)
	var detail struct {
		Run struct {
			EnvID string `json:"env_id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(d, &detail); err != nil || detail.Run.EnvID == "" {
		t.Fatalf("run detail: %v %s", err, d)
	}
	v := detail.Run
	path := "/api/runs/" + id + "/wake"
	if res, body := x.do(t, "POST", path, nil, nil); res.StatusCode != http.StatusNoContent || len(x.envp.Woken) != 0 {
		t.Fatalf("awake = %d %s, woken %v", res.StatusCode, body, x.envp.Woken)
	}
	if err := x.envp.Sleep(context.Background(), envport.EnvID(v.EnvID)); err != nil {
		t.Fatal(err)
	}
	x.envp.WakeErr = errors.New("no node has room")
	if res, body := x.do(t, "POST", path, nil, nil); res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "no node has room") {
		t.Errorf("failed wake = %d %s", res.StatusCode, body)
	}
	x.envp.WakeErr = nil
	if res, body := x.do(t, "POST", path, nil, nil); res.StatusCode != http.StatusNoContent || len(x.envp.Woken) != 1 {
		t.Errorf("wake = %d %s, woken %v", res.StatusCode, body, x.envp.Woken)
	}
	// Opening the editor wakes it too, before anything else.
	if err := x.envp.Sleep(context.Background(), envport.EnvID(v.EnvID)); err != nil {
		t.Fatal(err)
	}
	x.do(t, "POST", "/api/runs/"+id+"/open-editor", nil, nil)
	if len(x.envp.Woken) != 2 {
		t.Errorf("the editor did not wake it: %v", x.envp.Woken)
	}
}
