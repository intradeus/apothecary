package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// peer is an agent in process, for what the fake agent process cannot be made
// to say: answer scripts each request, and frames keeps what reached it.
type peer struct {
	t   *testing.T
	out *io.PipeWriter
	wmu sync.Mutex

	mu     sync.Mutex
	frames []rpcFrame
}

type answerFunc func(method string, params json.RawMessage) (any, *rpcError)

// newPeerProc registers a process whose far end is a peer, as start would.
func newPeerProc(t *testing.T, h *agentHost, answer answerFunc) (*agentProc, *peer) {
	t.Helper()
	toAgentR, toAgentW := io.Pipe()
	fromAgentR, fromAgentW := io.Pipe()
	p := &agentProc{id: "p1", cmd: &exec.Cmd{}, conn: newRPCConn(toAgentW), exited: make(chan struct{}), stderr: newTailBuffer(1 << 10)}
	p.conn.onNotify, p.conn.onRequest = h.onNotify, h.onRequest
	go p.conn.readLoop(fromAgentR)
	pr := &peer{t: t, out: fromAgentW}
	go func() {
		sc := bufio.NewScanner(toAgentR)
		sc.Buffer(nil, 16<<20)
		for sc.Scan() {
			var f rpcFrame
			if json.Unmarshal(sc.Bytes(), &f) != nil {
				continue
			}
			pr.mu.Lock()
			pr.frames = append(pr.frames, f)
			pr.mu.Unlock()
			if len(f.ID) == 0 || f.Method == "" {
				continue
			}
			res, rerr := answer(f.Method, f.Params)
			if rerr != nil {
				pr.send(map[string]any{"id": f.ID, "error": rerr})
			} else {
				pr.send(map[string]any{"id": f.ID, "result": res})
			}
		}
	}()
	t.Cleanup(func() {
		toAgentW.Close()
		fromAgentW.Close()
	})
	h.mu.Lock()
	h.procs[p.id] = p
	h.mu.Unlock()
	return p, pr
}

func (pr *peer) send(v any) {
	data, _ := json.Marshal(v)
	pr.wmu.Lock()
	defer pr.wmu.Unlock()
	_, _ = pr.out.Write(append(data, '\n'))
}

func (pr *peer) update(sid, u string) {
	pr.send(map[string]any{"method": "session/update",
		"params": map[string]any{"sessionId": sid, "update": json.RawMessage(u)}})
}

// calls lists the methods that reached the agent, in order.
func (pr *peer) calls() []string {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	var out []string
	for _, f := range pr.frames {
		out = append(out, f.Method)
	}
	return out
}

func (pr *peer) params(method string) json.RawMessage {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	for _, f := range pr.frames {
		if f.Method == method {
			return f.Params
		}
	}
	pr.t.Fatalf("no %s reached the agent", method)
	return nil
}

func newSession(id string) map[string]any { return map[string]any{"sessionId": id} }

// accepts answers session/new and everything named; the rest is refused.
func accepts(methods ...string) answerFunc {
	return func(method string, _ json.RawMessage) (any, *rpcError) {
		if method == "session/new" {
			return newSession("s1"), nil
		}
		for _, m := range methods {
			if m == method {
				return map[string]any{}, nil
			}
		}
		return nil, &rpcError{Code: -32602, Message: "refused"}
	}
}

// ACP's McpServer is an untagged union: stdio has every field present and no
// type, http is tagged. An agent decoding it variant by variant reads exactly
// this.
func TestMCPServersOnTheWire(t *testing.T) {
	h := newAgentHost(newEventLog())
	_, pr := newPeerProc(t, h, accepts())
	_, err := h.session(sessionReq{Cwd: "/repo", MCPServers: []mcpServer{
		{Name: "ask", Command: "shim"},
		{Name: "tools", Command: "t", Args: []string{"-v"}, Env: []envVar{{"K", "V"}}},
		{Name: "remote", URL: "https://mcp.example/x"},
		{Name: "signed", URL: "https://mcp.example/y", Headers: []envVar{{"Authorization", "Bearer t"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Servers json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(pr.params("session/new"), &p); err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"ask","command":"shim","args":[],"env":[]},` +
		`{"name":"tools","command":"t","args":["-v"],"env":[{"name":"K","value":"V"}]},` +
		`{"type":"http","name":"remote","url":"https://mcp.example/x","headers":[]},` +
		`{"type":"http","name":"signed","url":"https://mcp.example/y","headers":[{"name":"Authorization","value":"Bearer t"}]}]`
	if string(p.Servers) != want {
		t.Errorf("mcpServers = %s\nwant          %s", p.Servers, want)
	}
}

// No server named is an empty list, never a null an agent would choke on.
func TestMCPServersDefaultToAnEmptyList(t *testing.T) {
	h := newAgentHost(newEventLog())
	_, pr := newPeerProc(t, h, accepts())
	if _, err := h.session(sessionReq{Cwd: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if got := string(pr.params("session/new")); !strings.Contains(got, `"mcpServers":[]`) {
		t.Errorf("params = %s", got)
	}
}

// configCaps is a session/new answer offering modes and models as config
// options, as codex-acp's later releases do.
func configCaps(answer map[string]any) map[string]any {
	answer["sessionId"] = "s1"
	return answer
}

func TestSetMode(t *testing.T) {
	spelled := map[string]any{"modes": map[string]any{"availableModes": []any{map[string]any{"id": "agent-read-only"}}}}
	refusedByName := func(method string, params json.RawMessage) (any, *rpcError) {
		switch method {
		case "session/new":
			return configCaps(spelled), nil
		case "session/set_mode":
			if strings.Contains(string(params), `"modeId":"agent-read-only"`) {
				return map[string]any{}, nil
			}
		}
		return nil, &rpcError{Code: -32602, Message: "unknown mode"}
	}
	h := newAgentHost(newEventLog())
	_, pr := newPeerProc(t, h, refusedByName)
	if _, err := h.session(sessionReq{Mode: "read-only"}); err != nil {
		t.Errorf("another spelling of the mode: %v", err)
	}
	if got := strings.Join(pr.calls(), " "); got != "session/new session/set_mode session/set_mode" {
		t.Errorf("calls = %s", got)
	}

	// A config option named after the mode, or filed under it.
	viaConfig := func(id, category string) answerFunc {
		return func(method string, _ json.RawMessage) (any, *rpcError) {
			switch method {
			case "session/new":
				return configCaps(map[string]any{"configOptions": []any{
					map[string]any{"id": "unrelated", "category": "other"},
					map[string]any{"id": id, "category": category, "options": []any{map[string]any{"value": "plan"}}},
				}}), nil
			case "session/set_config_option":
				return map[string]any{}, nil
			}
			return nil, &rpcError{Code: -32601, Message: "method not found"}
		}
	}
	for _, c := range []struct{ id, category string }{{"mode", ""}, {"profile", "mode"}} {
		h := newAgentHost(newEventLog())
		_, pr := newPeerProc(t, h, viaConfig(c.id, c.category))
		res, err := h.session(sessionReq{Mode: "plan"})
		if err != nil || strings.Join(res.Modes, ",") != "plan" {
			t.Errorf("config %+v: %+v, %v", c, res, err)
		}
		var p map[string]string
		_ = json.Unmarshal(pr.params("session/set_config_option"), &p)
		if p["configId"] != c.id || p["value"] != "plan" || p["sessionId"] != "s1" {
			t.Errorf("config call = %v", p)
		}
	}

	// Nothing works: the error names what would have.
	h = newAgentHost(newEventLog())
	newPeerProc(t, h, func(method string, _ json.RawMessage) (any, *rpcError) {
		if method == "session/new" {
			return configCaps(map[string]any{"modes": map[string]any{"availableModes": []any{map[string]any{"id": "build"}}}}), nil
		}
		return nil, &rpcError{Code: -32602, Message: "unknown mode"}
	})
	if _, err := h.session(sessionReq{Mode: "plan"}); err == nil || !strings.Contains(err.Error(), "available: build") {
		t.Errorf("no such mode: %v", err)
	}
	h = newAgentHost(newEventLog())
	newPeerProc(t, h, accepts())
	if _, err := h.session(sessionReq{Mode: "plan"}); err == nil || !strings.Contains(err.Error(), `set agent "plan"`) {
		t.Errorf("no modes advertised: %v", err)
	}
}

func TestSetModel(t *testing.T) {
	// The exact id refused, the advertised spelling taken.
	h := newAgentHost(newEventLog())
	_, pr := newPeerProc(t, h, func(method string, params json.RawMessage) (any, *rpcError) {
		switch method {
		case "session/new":
			return configCaps(map[string]any{"models": map[string]any{"availableModels": []any{map[string]any{"modelId": "alpha-5/high"}}}}), nil
		case "session/set_model":
			if strings.Contains(string(params), `"alpha-5/high"`) {
				return map[string]any{}, nil
			}
		}
		return nil, &rpcError{Code: -32602, Message: "unknown model"}
	})
	res, err := h.session(sessionReq{Model: "fake/alpha-5"})
	if err != nil || res.Model != "alpha-5/high" {
		t.Errorf("advertised spelling: %+v, %v", res, err)
	}
	if got := strings.Join(pr.calls(), " "); got != "session/new session/set_model session/set_model" {
		t.Errorf("calls = %s", got)
	}

	// No session/set_model at all: the config option carries it.
	h = newAgentHost(newEventLog())
	_, pr = newPeerProc(t, h, func(method string, _ json.RawMessage) (any, *rpcError) {
		switch method {
		case "session/new":
			return configCaps(map[string]any{"configOptions": []any{map[string]any{"id": "model", "options": []any{map[string]any{"value": "fake/alpha-5"}}}}}), nil
		case "session/set_config_option":
			return map[string]any{}, nil
		}
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	})
	res, err = h.session(sessionReq{Model: "fake/alpha-5"})
	if err != nil || res.Model != "fake/alpha-5" || strings.Join(res.Models, ",") != "fake/alpha-5" {
		t.Errorf("config option: %+v, %v", res, err)
	}
	var p map[string]string
	_ = json.Unmarshal(pr.params("session/set_config_option"), &p)
	if p["configId"] != "model" || p["value"] != "fake/alpha-5" {
		t.Errorf("config call = %v", p)
	}

	// Nothing takes: the error lists what would, cut to a readable length.
	var models []any
	for i := range 35 {
		models = append(models, map[string]any{"modelId": fmt.Sprintf("m%d", i)})
	}
	h = newAgentHost(newEventLog())
	newPeerProc(t, h, func(method string, _ json.RawMessage) (any, *rpcError) {
		if method == "session/new" {
			return configCaps(map[string]any{"models": map[string]any{"availableModels": models}}), nil
		}
		return nil, &rpcError{Code: -32602, Message: "unknown model"}
	})
	_, err = h.session(sessionReq{Model: "fake/nope"})
	if err == nil || !strings.Contains(err.Error(), "available: m0, m1") || !strings.Contains(err.Error(), "m29, … 5 more") {
		t.Errorf("no such model: %v", err)
	}
	h = newAgentHost(newEventLog())
	newPeerProc(t, h, accepts())
	if _, err := h.session(sessionReq{Model: "fake/nope"}); err == nil || !strings.Contains(err.Error(), `set model "fake/nope"`) {
		t.Errorf("no models advertised: %v", err)
	}
}

// A session already live is left as it is, except what a request changes;
// nothing is loaded twice.
func TestLiveSessionSwitches(t *testing.T) {
	h := newAgentHost(newEventLog())
	p, pr := newPeerProc(t, h, accepts("session/load", "session/set_mode", "session/set_model"))
	if _, err := h.session(sessionReq{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.sessions["s1"].costUSD = 2.5
	h.mu.Unlock()
	res, err := h.session(sessionReq{SessionID: "s1", Mode: "plan", Model: "fake/alpha", ReadOnly: true})
	if err != nil || res.Loaded || res.CostUSD != 2.5 || res.Model != "fake/alpha" {
		t.Errorf("live session = %+v, %v", res, err)
	}
	if got := strings.Join(pr.calls(), " "); got != "session/load session/set_mode session/set_model" {
		t.Errorf("calls = %s", got)
	}
	h.mu.Lock()
	readOnly := h.sessions["s1"].readOnly
	h.mu.Unlock()
	if !readOnly {
		t.Error("a live session did not turn read-only")
	}

	h2 := newAgentHost(newEventLog())
	newPeerProc(t, h2, accepts("session/load"))
	if _, err := h2.session(sessionReq{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.session(sessionReq{SessionID: "s1", Mode: "plan"}); err == nil {
		t.Error("a refused mode on a live session went unreported")
	}
	if _, err := h2.session(sessionReq{SessionID: "s1", Model: "fake/alpha"}); err == nil {
		t.Error("a refused model on a live session went unreported")
	}

	close(p.exited)
	if _, err := h.session(sessionReq{SessionID: "s1"}); !errors.Is(err, errAgentGone) {
		t.Errorf("a live session whose process is gone: %v", err)
	}
	if err := h.prompt(promptReq{SessionID: "s1"}); !errors.Is(err, errAgentGone) {
		t.Errorf("prompt: %v", err)
	}
	if _, err := h.procFor("p1"); !errors.Is(err, errAgentGone) {
		t.Errorf("procFor a dead process: %v", err)
	}
	if _, err := h.procFor(""); !errors.Is(err, errAgentGone) {
		t.Errorf("the only process, dead: %v", err)
	}
}

// Asking after a live session's cost while its turn reports usage is safe.
func TestLiveSessionCostWhileUsageArrives(t *testing.T) {
	h := newAgentHost(newEventLog())
	newPeerProc(t, h, accepts("session/load"))
	if _, err := h.session(sessionReq{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	const reports = 2000
	go func() {
		usage, _ := json.Marshal(map[string]any{"sessionId": "s1", "update": map[string]any{
			"sessionUpdate": "usage_update", "cost": map[string]any{"amount": 1.0}}})
		for range reports {
			h.onNotify("session/update", usage)
		}
	}()
	for h.log.last() < reports {
		if _, err := h.session(sessionReq{SessionID: "s1"}); err != nil {
			t.Fatal(err)
		}
	}
}

// A session the agent will not give leaves no trace of itself.
func TestSessionFailures(t *testing.T) {
	h := newAgentHost(newEventLog())
	p, _ := newPeerProc(t, h, accepts())
	p.stderr.Write([]byte("error: bad config\n"))
	if _, err := h.session(sessionReq{SessionID: "old"}); err == nil || !strings.Contains(err.Error(), "session/load old") {
		t.Errorf("load refused: %v", err)
	}
	if st := h.status(); len(st.Sessions) != 0 {
		t.Errorf("a failed load left %v", st.Sessions)
	}

	h = newAgentHost(newEventLog())
	p, _ = newPeerProc(t, h, func(string, json.RawMessage) (any, *rpcError) {
		return nil, &rpcError{Code: 1, Message: "no"}
	})
	if _, err := h.session(sessionReq{}); err == nil || err.Error() != "session/new: no (1)" {
		t.Errorf("new refused, nothing in the log: %v", err)
	}
	p.stderr.Write([]byte("error: bad config\n"))
	if _, err := h.session(sessionReq{}); err == nil || !strings.Contains(err.Error(), "session/new") || !strings.Contains(err.Error(), "bad config") {
		t.Errorf("new refused: %v", err)
	}

	h = newAgentHost(newEventLog())
	newPeerProc(t, h, func(string, json.RawMessage) (any, *rpcError) { return map[string]any{"no": "id"}, nil })
	if _, err := h.session(sessionReq{}); err == nil || !strings.Contains(err.Error(), "no sessionId") {
		t.Errorf("no id: %v", err)
	}

	// Load's history is the past: not re-emitted, but its cost still counts.
	h = newAgentHost(newEventLog())
	var pr *peer
	_, pr = newPeerProc(t, h, func(method string, _ json.RawMessage) (any, *rpcError) {
		pr.update("old", `{"sessionUpdate":"usage_update","cost":{"amount":1.5,"currency":"usd"}}`)
		return map[string]any{}, nil
	})
	res, err := h.session(sessionReq{SessionID: "old"})
	if err != nil || res.CostUSD != 1.5 || !res.Loaded {
		t.Errorf("load = %+v, %v", res, err)
	}
	if n := h.log.last(); n != 0 {
		t.Errorf("replay emitted %d events", n)
	}
}

func turnEnd(t *testing.T, log *eventLog) sideEvent {
	t.Helper()
	var end sideEvent
	waitFor(t, func() bool {
		evs, _ := log.since(0)
		for _, ev := range evs {
			if ev.Type == "turn_end" {
				end = ev
				return true
			}
		}
		return false
	})
	return end
}

// A refused prompt ends the turn as an error, and a cancelled turn with
// nothing to show is not second-guessed. A session frees up either way.
func TestPromptOutcomes(t *testing.T) {
	h := newAgentHost(newEventLog())
	newPeerProc(t, h, func(method string, _ json.RawMessage) (any, *rpcError) {
		if method == "session/new" {
			return newSession("s1"), nil
		}
		return nil, &rpcError{Code: 1, Message: "over quota"}
	})
	if _, err := h.session(sessionReq{}); err != nil {
		t.Fatal(err)
	}
	if err := h.prompt(promptReq{SessionID: "s1", Turn: "T1", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if end := turnEnd(t, h.log); end.StopReason != "error" || !strings.Contains(end.Error, "over quota") {
		t.Errorf("turn_end = %+v", end)
	}
	waitFor(t, func() bool { return len(h.status().Turns) == 0 })

	h = newAgentHost(newEventLog())
	p, _ := newPeerProc(t, h, func(method string, _ json.RawMessage) (any, *rpcError) {
		if method == "session/new" {
			return newSession("s1"), nil
		}
		return map[string]any{"stopReason": "cancelled"}, nil
	})
	if _, err := h.session(sessionReq{}); err != nil {
		t.Fatal(err)
	}
	if err := h.prompt(promptReq{SessionID: "s1", Turn: "T1", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	p.stderr.Write([]byte("error: cancelled by the client\n"))
	if end := turnEnd(t, h.log); end.StopReason != "cancelled" || end.Error != "" {
		t.Errorf("turn_end = %+v", end)
	}
}

// Usage of every kind reaches the turn's end.
func TestPromptUsage(t *testing.T) {
	h := newAgentHost(newEventLog())
	newPeerProc(t, h, func(method string, _ json.RawMessage) (any, *rpcError) {
		if method == "session/new" {
			return newSession("s1"), nil
		}
		return map[string]any{"stopReason": "end_turn", "usage": map[string]any{
			"inputTokens": 1, "outputTokens": 2, "thoughtTokens": 3, "cachedReadTokens": 4, "cachedWriteTokens": 5}}, nil
	})
	if _, err := h.session(sessionReq{}); err != nil {
		t.Fatal(err)
	}
	if err := h.prompt(promptReq{SessionID: "s1", Turn: "T1"}); err != nil {
		t.Fatal(err)
	}
	if u := turnEnd(t, h.log).Usage; u == nil || *u != (turnUsage{1, 2, 3, 4, 5}) {
		t.Errorf("usage = %+v", u)
	}
}

func outcome(t *testing.T, res any, rerr *rpcError) map[string]any {
	t.Helper()
	if rerr != nil {
		t.Fatal(rerr)
	}
	return res.(map[string]any)["outcome"].(map[string]any)
}

func permissionParams(kind string, options ...string) json.RawMessage {
	var opts []any
	for _, o := range options {
		opts = append(opts, map[string]any{"optionId": "opt-" + o, "kind": o})
	}
	data, _ := json.Marshal(map[string]any{"sessionId": "s1",
		"toolCall": map[string]any{"toolCallId": "t", "title": "x", "kind": kind}, "options": opts})
	return data
}

// The stray permission prompt: granted, strongest allow first; refused in a
// read-only session unless the call plainly reads; cancelled when no option
// fits, so a turn never hangs on one. Each leaves a trace in the log.
func TestPermissionRequests(t *testing.T) {
	newHost := func(readOnly bool) *agentHost {
		h := newAgentHost(newEventLog())
		h.sessions["s1"] = &sessState{readOnly: readOnly}
		h.turns["s1"] = &turnState{id: "T1", session: "s1"}
		return h
	}
	for _, c := range []struct {
		name     string
		readOnly bool
		params   json.RawMessage
		want     string // optionId, or "" for cancelled
		logged   string
	}{
		{"allow", false, permissionParams("edit", "allow_once", "allow_always"), "opt-allow_always", "auto-approved"},
		{"allow once", false, permissionParams("edit", "reject_once", "allow_once"), "opt-allow_once", "auto-approved"},
		{"no allow", false, permissionParams("edit", "reject_once"), "", "denied (no allow option offered)"},
		{"read-only edit", true, permissionParams("edit", "allow_once", "reject_always"), "opt-reject_always", "denied (read-only session)"},
		{"read-only, nothing to refuse with", true, permissionParams("execute", "allow_once"), "", "denied (read-only session)"},
		{"read-only read", true, permissionParams("read", "allow_once"), "opt-allow_once", "auto-approved"},
		{"read-only unknown kind", true, permissionParams("", "allow_once"), "", "denied (read-only session)"},
		{"read-only search", true, permissionParams("search", "allow_once"), "opt-allow_once", "auto-approved"},
	} {
		h := newHost(c.readOnly)
		res, rerr := h.onRequest("session/request_permission", c.params, nil)
		o := outcome(t, res, rerr)
		if c.want == "" && o["outcome"] != "cancelled" || c.want != "" && o["optionId"] != c.want {
			t.Errorf("%s: outcome = %v", c.name, o)
		}
		evs, _ := h.log.since(0)
		if len(evs) != 1 || evs[0].Text != c.logged || evs[0].Turn != "T1" || evs[0].Session != "s1" {
			t.Errorf("%s: logged %+v", c.name, evs)
		}
	}
}

// A permission asked outside a turn is answered all the same, attributed to
// no turn.
func TestPermissionOutsideATurn(t *testing.T) {
	h := newAgentHost(newEventLog())
	res, rerr := h.onRequest("session/request_permission", permissionParams("edit", "allow_once"), nil)
	if o := outcome(t, res, rerr); o["optionId"] != "opt-allow_once" {
		t.Errorf("outcome = %v", o)
	}
	if evs, _ := h.log.since(0); len(evs) != 1 || evs[0].Turn != "" {
		t.Errorf("logged %+v", evs)
	}
}

// A harness's question comes in by the same door as its permissions.
func TestOnRequestRoutesElicitation(t *testing.T) {
	h := newAgentHost(newEventLog())
	res, rerr := h.onRequest("elicitation/create", json.RawMessage(`{"mode":"url"}`), nil)
	if rerr != nil || res.(map[string]any)["action"] != "decline" {
		t.Errorf("res = %v, %v", res, rerr)
	}
}

// Turns in flight read oldest first, whichever session they are in.
func TestStatusOrdersTurns(t *testing.T) {
	h := newAgentHost(newEventLog())
	now := time.Now()
	h.turns["a"] = &turnState{id: "new", session: "a", since: now}
	h.turns["b"] = &turnState{id: "old", session: "b", since: now.Add(-time.Minute)}
	if st := h.status(); len(st.Turns) != 2 || st.Turns[0].ID != "old" || st.Turns[1].ID != "new" {
		t.Errorf("turns = %+v", st.Turns)
	}
}

func TestOnRequestUnknownMethod(t *testing.T) {
	h := newAgentHost(newEventLog())
	if _, rerr := h.onRequest("fs/read_text_file", nil, nil); rerr == nil || rerr.Code != -32601 || !strings.Contains(rerr.Message, "fs/read_text_file") {
		t.Errorf("rerr = %v", rerr)
	}
}

func TestReadOnlyAllows(t *testing.T) {
	if readOnlyAllows(nil) {
		t.Error("no tool call is not a read")
	}
	for _, k := range []string{"read", "search", "fetch", "think"} {
		if !readOnlyAllows(&toolInfo{Kind: k}) {
			t.Errorf("%s refused", k)
		}
	}
	for _, k := range []string{"edit", "delete", "move", "execute", "other", ""} {
		if readOnlyAllows(&toolInfo{Kind: k}) {
			t.Errorf("%s allowed", k)
		}
	}
}

// The agent's notifications: what the engine has a word for, and only that.
func TestOnNotify(t *testing.T) {
	h := newAgentHost(newEventLog())
	h.sessions["s1"] = &sessState{}
	notify := func(u string) {
		data, _ := json.Marshal(map[string]any{"sessionId": "s1", "update": json.RawMessage(u)})
		h.onNotify("session/update", data)
	}
	h.onNotify("something/else", json.RawMessage(`{}`))
	h.onNotify("session/update", json.RawMessage(`[1]`))
	notify(`{"sessionUpdate":"available_commands_update"}`)
	notify(`{"sessionUpdate":"agent_message_chunk","content":{"type":"image"}}`)
	notify(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}}`)
	notify(`{"sessionUpdate":"agent_thought_chunk","content":"not a block"}`)
	notify(`[1]`)
	if n := h.log.last(); n != 0 {
		t.Fatalf("%d events from what the engine has no word for", n)
	}

	// Usage is no output: only something said or done counts for the turn.
	turn := &turnState{id: "T1", session: "s1"}
	h.turns["s1"] = turn
	notify(`{"sessionUpdate":"usage_update","used":5,"size":10,"cost":{"amount":3,"currency":"EUR"}}`)
	if turn.produced || h.sessions["s1"].costUSD != 0 {
		t.Errorf("after usage: produced %v, cost %v", turn.produced, h.sessions["s1"].costUSD)
	}
	notify(`{"sessionUpdate":"usage_update","cost":{"amount":0.5}}`)
	notify(`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hm"}}`)
	if !turn.produced || h.sessions["s1"].costUSD != 0.5 {
		t.Errorf("after a thought: produced %v, cost %v", turn.produced, h.sessions["s1"].costUSD)
	}
	evs, _ := h.log.since(0)
	if len(evs) != 3 || evs[0].ContextUsed != 5 || evs[0].ContextSize != 10 || evs[0].CostUSD != nil || *evs[1].CostUSD != 0.5 ||
		evs[2].Type != "thought" || evs[2].Turn != "T1" {
		t.Errorf("events = %+v", evs)
	}

	// An update for a session this host does not know still reaches the log.
	data, _ := json.Marshal(map[string]any{"sessionId": "ghost", "update": json.RawMessage(`{"sessionUpdate":"usage_update","cost":{"amount":1}}`)})
	h.onNotify("session/update", data)
	if h.log.last() != 4 {
		t.Errorf("seq = %d", h.log.last())
	}
}

func TestNormalizeTool(t *testing.T) {
	if got := normalizeTool(json.RawMessage(`[1]`)); got == nil || got.ID != "" {
		t.Errorf("unreadable call = %+v", got)
	}
	ti := normalizeTool(json.RawMessage(`{"toolCallId":"t","rawInput":{"a":1},"rawOutput":"plain",
		"locations":[{"path":"a.go"},{"path":"b.go","line":3}],
		"content":[{"type":"diff","path":"a.go","newText":"n"},{"type":"content","content":{"type":"image"}}]}`))
	if ti.Input != `{"a":1}` || ti.Output != "plain" || strings.Join(ti.Locations, ",") != "a.go,b.go:3" ||
		len(ti.Diffs) != 1 || ti.Diffs[0].OldText != "" || ti.Diffs[0].NewText != "n" {
		t.Errorf("tool = %+v", ti)
	}
	if ti := normalizeTool(json.RawMessage(`{"rawOutput":{"output":"from object"},"rawInput":{}}`)); ti.Output != "from object" || ti.Input != "" {
		t.Errorf("object output = %+v", ti)
	}
	for _, raw := range []string{`{"rawOutput":null}`, `{"rawOutput":{"other":1}}`, `{"rawOutput":5}`} {
		if ti := normalizeTool(json.RawMessage(raw)); ti.Output != "" {
			t.Errorf("%s output = %q", raw, ti.Output)
		}
	}
	// Text content wins over the raw output, and both are bounded.
	big := strings.Repeat("x", maxToolText+100)
	data, _ := json.Marshal(map[string]any{"rawOutput": "raw", "content": []any{
		map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": big}}}})
	if ti := normalizeTool(data); len(ti.Output) > maxToolText+64 || !strings.Contains(ti.Output, "bytes elided") {
		t.Errorf("bounded output = %d bytes", len(ti.Output))
	}
}

func TestParseSessionCaps(t *testing.T) {
	c := parseSessionCaps(json.RawMessage(`{"modes":{"availableModes":[{"id":"a"}]},
		"models":{"availableModels":[{"modelId":"m"}]},
		"configOptions":[{"id":"x","category":"mode","options":[{"value":"b"}]},
		                 {"id":"model","options":[{"value":"n"}]},{"id":"other"}]}`))
	if strings.Join(c.modes, ",") != "a,b" || strings.Join(c.models, ",") != "m,n" || len(c.configs) != 3 {
		t.Errorf("caps = %+v", c)
	}
	if c := parseSessionCaps(json.RawMessage(`not json`)); c.modes != nil || c.models != nil {
		t.Errorf("garbage = %+v", c)
	}
	if id := configID(sessionCaps{}, "mode"); id != "" {
		t.Errorf("no configs = %q", id)
	}
}

func TestRPCErrorText(t *testing.T) {
	if got := (&rpcError{Code: 1, Message: "no"}).Error(); got != "no (1)" {
		t.Errorf("plain = %q", got)
	}
	if got := (&rpcError{Code: 1, Message: "no", Data: json.RawMessage("null")}).Error(); got != "no (1)" {
		t.Errorf("null data = %q", got)
	}
	long := `"` + strings.Repeat("x", 400) + "\nend\""
	if got := (&rpcError{Code: 1, Message: "no", Data: json.RawMessage(long)}).Error(); got != `no (1): end"` {
		t.Errorf("data = %q", got)
	}
	// Cut with no line start kept: a fragment could be a token's tail.
	if got := (&rpcError{Message: "m", Code: 1, Data: json.RawMessage(strings.Repeat("x", 400))}).Error(); got != "m (1)" {
		t.Errorf("data cut mid-line = %q", got)
	}
}

func TestRPCCallFailures(t *testing.T) {
	c := newRPCConn(io.Discard)
	if _, err := c.call("m", make(chan int), nil); err == nil {
		t.Error("params that do not marshal were sent")
	}
	if err := c.notify("m", make(chan int)); err == nil {
		t.Error("a notification that does not marshal was sent")
	}
	if err := c.write(rpcFrame{Params: json.RawMessage("{bad")}); err == nil {
		t.Error("a frame that does not marshal was sent")
	}
	// Nobody answers, and the caller stops waiting.
	done := make(chan struct{})
	close(done)
	if _, err := c.call("session/prompt", nil, done); err == nil || !strings.Contains(err.Error(), "abandoned") {
		t.Errorf("abandoned: %v", err)
	}
	if len(c.pending) != 0 {
		t.Errorf("an abandoned call is still pending: %v", c.pending)
	}

	w := newRPCConn(brokenWriter{})
	if _, err := w.call("m", nil, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("failed write: %v", err)
	}
	if len(w.pending) != 0 {
		t.Errorf("an unsent call is still pending: %v", w.pending)
	}

	gone := newRPCConn(io.Discard)
	gone.readLoop(strings.NewReader(""))
	if _, err := gone.call("m", nil, nil); !errors.Is(err, errAgentGone) {
		t.Errorf("after the agent left: %v", err)
	}
}

// The agent's stream is forgiving: log lines on stdout are skipped, a response
// nobody waits for dropped, and the last line counts without a newline.
func TestRPCReadLoop(t *testing.T) {
	notified := make(chan string, 4)
	var out strings.Builder
	var wmu sync.Mutex
	c := newRPCConn(lockedWriter{&wmu, &out})
	c.onNotify = func(method string, _ json.RawMessage) { notified <- method }
	first := make(chan rpcResult, 1)
	c.pending[1] = first
	second := make(chan rpcResult, 1)
	c.pending[2] = second
	c.readLoop(strings.NewReader(strings.Join([]string{
		`INFO starting up`,
		``,
		`{"jsonrpc":"2.0","id":"a","result":{}}`,
		`{"jsonrpc":"2.0","id":99,"result":{}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":5,"message":"no"}}`,
		`{"jsonrpc":"2.0","method":"$/cancel_request","params":"bad"}`,
		`{"jsonrpc":"2.0","method":"session/update"}`,
		`{"jsonrpc":"2.0","id":7,"method":"unasked"}`,
		`{"jsonrpc":"2.0","method":"last"}`,
	}, "\n")))
	var re *rpcError
	if r := <-first; !errors.As(r.err, &re) || re.Code != 5 {
		t.Errorf("first = %+v", r)
	}
	if r := <-second; !errors.Is(r.err, errAgentGone) {
		t.Errorf("a call outstanding at EOF = %+v", r)
	}
	if a, b := <-notified, <-notified; a != "session/update" || b != "last" {
		t.Errorf("notified %s, %s", a, b)
	}
	// A request with no handler is answered as not found.
	waitFor(t, func() bool {
		wmu.Lock()
		defer wmu.Unlock()
		return strings.Contains(out.String(), `"id":7`) && strings.Contains(out.String(), "-32601")
	})
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// A notification with no handler, or an agent leaving mid-request, is fine.
func TestRPCHandlersOptional(t *testing.T) {
	c := newRPCConn(io.Discard)
	stopped := make(chan struct{})
	c.onRequest = func(_ string, _ json.RawMessage, done <-chan struct{}) (any, *rpcError) {
		<-done
		close(stopped)
		return map[string]any{}, nil
	}
	c.readLoop(strings.NewReader(`{"method":"note"}` + "\n" + `{"id":1,"method":"elicitation/create"}` + "\n"))
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent left, and its request's handler kept waiting")
	}
}

func TestEventLogDropsTheOldest(t *testing.T) {
	l := newEventLog()
	for range maxEventsBuf + 3 {
		l.emit(sideEvent{Type: "message"})
	}
	if evs, gap := l.since(0); !gap || len(evs) != maxEventsBuf || evs[0].Seq != 4 {
		t.Errorf("%d events from %d, gap %v", len(evs), evs[0].Seq, gap)
	}
	if evs, gap := l.since(3); gap || len(evs) != maxEventsBuf {
		t.Errorf("right at the edge: %d events, gap %v", len(evs), gap)
	}
	// A subscriber's wake never blocks the emitter, however far behind.
	wake, unsub := l.subscribe()
	defer unsub()
	l.emit(sideEvent{})
	l.emit(sideEvent{})
	<-wake
}

func TestTailBuffer(t *testing.T) {
	whole := newTailBuffer(16)
	whole.Write([]byte("ab\ncd"))
	if got := whole.String(); got != "ab\ncd" { // nothing dropped: all of it
		t.Errorf("nothing dropped = %q", got)
	}
	lines := newTailBuffer(8)
	lines.Write([]byte("abc\ndef\ngh"))
	if got := lines.String(); got != "def\ngh" { // from the first kept line start
		t.Errorf("dropped = %q", got)
	}
	b := newTailBuffer(4)
	if n, err := b.Write([]byte("abcdef")); n != 6 || err != nil || b.String() != "" { // no line start kept
		t.Errorf("%d, %v, %q", n, err, b.String())
	}

	s := newTailBuffer(8)
	s.Write([]byte("abc"))
	mark := s.mark()
	if got := s.since(mark); got != "" {
		t.Errorf("nothing since = %q", got)
	}
	s.Write([]byte("d"))
	if got := s.since(mark); got != "d" {
		t.Errorf("since = %q", got)
	}
	s.Write([]byte("ef\nghijk"))
	if got := s.since(mark); got != "ghijk" { // as much as is kept, from a line start
		t.Errorf("since, past the tail = %q", got)
	}
}

func TestTextBounds(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate(strings.Repeat("a", 10)+strings.Repeat("b", 10), 10); !strings.HasPrefix(got, "aaaaa\n") || !strings.HasSuffix(got, "\nbbbbb") ||
		!strings.Contains(got, "[10 bytes elided]") {
		t.Errorf("truncate = %q", got)
	}
	if got := tailStr("line1\nline2", 8); got != "line2" {
		t.Errorf("tailStr = %q", got)
	}
	if got := tailStr("abcdef", 2); got != "" {
		t.Errorf("tailStr, no line start = %q", got)
	}
	if got := tailStr("ab", 5); got != "ab" {
		t.Errorf("tailStr short = %q", got)
	}
	if got := truncList([]string{"a", "b"}, 5); len(got) != 2 {
		t.Errorf("truncList short = %v", got)
	}
	xs := []string{"a", "b", "c"}
	if got := truncList(xs, 2); strings.Join(got, ",") != "a,b,… 1 more" || len(xs) != 3 || xs[2] != "c" {
		t.Errorf("truncList = %v, input %v", got, xs)
	}
}

func TestErrorLines(t *testing.T) {
	var log strings.Builder
	log.WriteString("level=INFO all fine\n")
	for i := range 10 {
		fmt.Fprintf(&log, "  Error: %d\n", i)
	}
	got := strings.Split(errorLines(log.String()), "\n")
	if len(got) != 8 || got[0] != "Error: 2" || got[7] != "Error: 9" {
		t.Errorf("lines = %q", got)
	}
	if got := errorLines("all fine\nlevel=info\n"); got != "" {
		t.Errorf("no errors = %q", got)
	}
}

const initOK = `echo '{"jsonrpc":"2.0","id":1,"result":{}}'`

// An agent gets its environment, its directory, and the marker that lets it
// ask the human.
func TestStartGivesTheAgentItsEnvironment(t *testing.T) {
	h := newAgentHost(newEventLog())
	t.Cleanup(h.stop)
	dir := t.TempDir()
	script := `read line; echo "$APOTHECARY_AGENT $CUSTOM $PWD" > seen; ` + initOK + `; exec cat >/dev/null`
	if _, err := h.start(startReq{Argv: []string{"sh", "-c", script}, Env: map[string]string{"CUSTOM": "v"}, Cwd: dir}); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	data, err := os.ReadFile(filepath.Join(dir, "seen"))
	if err != nil || strings.TrimSpace(string(data)) != "1 v "+real {
		t.Errorf("agent saw %q, %v", data, err)
	}
}

// An agent that refuses initialize is not kept, and its log says why.
func TestStartInitializeRefused(t *testing.T) {
	h := newAgentHost(newEventLog())
	script := `read line; echo 'error: no credentials' >&2; ` +
		`echo '{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"auth required"}}'; exec cat >/dev/null`
	_, err := h.start(startReq{Argv: []string{"sh", "-c", script}})
	if err == nil || !strings.Contains(err.Error(), "auth required") {
		t.Fatalf("err = %v", err)
	}
	if st := h.status(); st.Running {
		t.Errorf("a refused agent stayed: %+v", st)
	}
}

// Two starts of one command line at once end with one process: the second
// to finish initializing goes away quietly and gets the first.
func TestStartRace(t *testing.T) {
	h := newAgentHost(newEventLog())
	t.Cleanup(h.stop)
	dir := t.TempDir()
	script := `read line; : > "$0/$$"; while [ "$(ls "$0" | wc -l)" -lt 2 ]; do sleep 0.01; done; ` + initOK + `; exec cat >/dev/null`
	argv := []string{"sh", "-c", script, dir}
	got := make([]*agentProc, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := h.start(startReq{Argv: argv})
			if err != nil {
				t.Error(err)
			}
			got[i] = p
		}()
	}
	wg.Wait()
	if t.Failed() || got[0] != got[1] {
		t.Fatalf("procs = %p, %p", got[0], got[1])
	}
	waitFor(t, func() bool {
		ents, _ := os.ReadDir(dir)
		alive := 0
		for _, e := range ents {
			if pid, err := strconv.Atoi(e.Name()); err == nil && syscall.Kill(pid, 0) == nil {
				alive++
			}
		}
		return alive == 1
	})
	if st := h.status(); len(st.Procs) != 1 || st.LastSeq != 0 {
		t.Errorf("status = %+v, want one process and no exit to report", st)
	}
}

// An agent killed from outside is reported as that, not as a code it never
// exited with.
func TestAgentKilledBySignal(t *testing.T) {
	h := newAgentHost(newEventLog())
	p, err := h.start(startReq{Argv: []string{"sh", "-c", `read line; ` + initOK + `; exec cat >/dev/null`}})
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(p.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	var exit sideEvent
	waitFor(t, func() bool {
		evs, _ := h.log.since(0)
		if len(evs) == 0 {
			return false
		}
		exit = evs[0]
		return true
	})
	if exit.Type != "agent_exit" || exit.Error != "agent exited: signal: killed" {
		t.Errorf("exit = %+v", exit)
	}
}

// An agent that shrugs off the TERM is killed once its grace is up, with
// whatever it spawned.
func TestStopKillsWhatIgnoresTheTerm(t *testing.T) {
	old := agentKillGrace
	agentKillGrace = 20 * time.Millisecond
	t.Cleanup(func() { agentKillGrace = old })
	h := newAgentHost(newEventLog())
	script := `trap '' TERM; read line; ` + initOK + `; exec cat >/dev/null`
	p, err := h.start(startReq{Argv: []string{"sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.cmd.Process.Pid
	h.stop()
	if p.alive() || groupAlive(pid) {
		t.Errorf("the agent outlived its stop: alive %v, group %v", p.alive(), groupAlive(pid))
	}
}

// Failing to even start an agent's process leaves the host as it was.
func TestKillOfAProcessThatNeverStarted(t *testing.T) {
	(&agentProc{cmd: &exec.Cmd{}, exited: make(chan struct{})}).kill() // no process: nothing to wait for
}
