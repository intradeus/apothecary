package main

// The sidecar's agent host: it owns ONE long-lived coding agent process per
// workspace and speaks the Agent Client Protocol (ACP, JSON-RPC 2.0 over the
// agent's stdio, one message per line) to it. Everything the engine knows
// about a running agent comes through here, normalized into one small event
// vocabulary — the same for every harness, because ACP already is.
//
// The engine drives it (start, session new/load, prompt, cancel) and follows
// its event stream; the host never decides what to do next. What it DOES own
// is the process: a turn keeps running when the engine restarts, and the
// buffered events let the engine pick the stream up where it left off.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// --- JSON-RPC over stdio ------------------------------------------------------

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.Code, tailStr(string(e.Data), 300))
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type rpcFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcConn is a bidirectional JSON-RPC peer: we call the agent (initialize,
// session/*), and the agent calls us (session/update notifications,
// session/request_permission requests).
type rpcConn struct {
	w      io.Writer
	wmu    sync.Mutex
	nextID atomic.Int64

	pmu     sync.Mutex
	pending map[int64]chan rpcResult
	closed  bool

	onNotify  func(method string, params json.RawMessage)
	onRequest func(method string, params json.RawMessage) (any, *rpcError)
}

func newRPCConn(w io.Writer) *rpcConn {
	return &rpcConn{w: w, pending: map[int64]chan rpcResult{}}
}

func (c *rpcConn) write(f rpcFrame) error {
	f.JSONRPC = "2.0"
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// call sends a request and waits for its response (or the connection dying).
// There is deliberately no timeout here: session/prompt legitimately runs for
// as long as the agent works. Callers that need one cancel via done.
func (c *rpcConn) call(method string, params any, done <-chan struct{}) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcResult, 1)
	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		return nil, errAgentGone
	}
	c.pending[id] = ch
	c.pmu.Unlock()
	if err := c.write(rpcFrame{ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: raw}); err != nil {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-done:
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return nil, errors.New(method + ": abandoned")
	}
}

func (c *rpcConn) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(rpcFrame{Method: method, Params: raw})
}

var errAgentGone = errors.New("agent process is not running")

// readLoop dispatches frames until the agent's stdout closes. Lines can be
// large (a whole file read back as a tool result), hence a Reader, not a
// Scanner with a line cap.
func (c *rpcConn) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			break
		}
	}
	c.pmu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		ch <- rpcResult{err: errAgentGone}
		delete(c.pending, id)
	}
	c.pmu.Unlock()
}

func (c *rpcConn) dispatch(line []byte) {
	var f rpcFrame
	if err := json.Unmarshal(line, &f); err != nil {
		return // agents print the odd non-JSON log line on stdout; skip it
	}
	hasID := len(f.ID) > 0 && string(f.ID) != "null"
	switch {
	case f.Method != "" && hasID: // a request from the agent
		go func() {
			var result any
			var rerr *rpcError
			if c.onRequest != nil {
				result, rerr = c.onRequest(f.Method, f.Params)
			} else {
				rerr = &rpcError{Code: -32601, Message: "method not found"}
			}
			if rerr != nil {
				_ = c.write(rpcFrame{ID: f.ID, Error: rerr})
				return
			}
			raw, _ := json.Marshal(result)
			_ = c.write(rpcFrame{ID: f.ID, Result: raw})
		}()
	case f.Method != "": // a notification
		if c.onNotify != nil {
			c.onNotify(f.Method, f.Params)
		}
	case hasID: // a response to one of our calls
		var id int64
		if err := json.Unmarshal(f.ID, &id); err != nil {
			return
		}
		c.pmu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.pmu.Unlock()
		if !ok {
			return
		}
		if f.Error != nil {
			ch <- rpcResult{err: f.Error}
		} else {
			ch <- rpcResult{result: f.Result}
		}
	}
}

// --- normalized events ----------------------------------------------------------

// sideEvent is the one vocabulary the engine consumes, whatever the agent.
// The engine mirrors this type (internal/sidecar); keep the two in step.
type sideEvent struct {
	Seq     int64     `json:"seq"`
	Boot    string    `json:"boot"`
	TS      time.Time `json:"ts"`
	Session string    `json:"session,omitempty"`
	Turn    string    `json:"turn,omitempty"`
	// Type: message | thought | tool_call | tool_update | plan | usage |
	// turn_start | turn_end | permission | agent_exit | gap
	Type    string      `json:"type"`
	Text    string      `json:"text,omitempty"`
	Tool    *toolInfo   `json:"tool,omitempty"`
	Plan    []planEntry `json:"plan,omitempty"`
	CostUSD *float64    `json:"cost_usd,omitempty"` // usage: cumulative for the session
	// ContextUsed/ContextSize: usage — how full the model's context window is.
	ContextUsed int64      `json:"context_used,omitempty"`
	ContextSize int64      `json:"context_size,omitempty"`
	StopReason  string     `json:"stop_reason,omitempty"`
	Error       string     `json:"error,omitempty"`
	Usage       *turnUsage `json:"usage,omitempty"` // turn_end
}

type toolInfo struct {
	ID        string     `json:"id"`
	Title     string     `json:"title,omitempty"`
	Kind      string     `json:"kind,omitempty"`   // read|edit|delete|move|search|execute|think|fetch|other
	Status    string     `json:"status,omitempty"` // pending|in_progress|completed|failed
	Input     string     `json:"input,omitempty"`  // JSON of rawInput, truncated
	Output    string     `json:"output,omitempty"` // text content, truncated
	Locations []string   `json:"locations,omitempty"`
	Diffs     []toolDiff `json:"diffs,omitempty"`
}

type toolDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
}

type planEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

type turnUsage struct {
	InputTokens       int64 `json:"input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	ThoughtTokens     int64 `json:"thought_tokens,omitempty"`
	CachedReadTokens  int64 `json:"cached_read_tokens,omitempty"`
	CachedWriteTokens int64 `json:"cached_write_tokens,omitempty"`
}

// Size caps: events travel to the engine and (coalesced) into its durable log;
// a tool that cats a megabyte file must not become a megabyte event.
const (
	maxToolText  = 16 << 10
	maxDiffText  = 32 << 10
	maxEventsBuf = 20000
)

// eventLog is the host's replayable buffer: the engine follows it live and,
// after its own restart, re-reads from the last seq it persisted.
type eventLog struct {
	mu    sync.Mutex
	boot  string
	seq   int64
	buf   []sideEvent
	subs  map[chan struct{}]struct{}
	first int64 // seq of buf[0] (events before it were dropped)
}

func newEventLog() *eventLog {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return &eventLog{boot: hex.EncodeToString(b), subs: map[chan struct{}]struct{}{}, first: 1}
}

func (l *eventLog) emit(ev sideEvent) sideEvent {
	l.mu.Lock()
	l.seq++
	ev.Seq, ev.Boot, ev.TS = l.seq, l.boot, time.Now().UTC()
	l.buf = append(l.buf, ev)
	if len(l.buf) > maxEventsBuf {
		drop := len(l.buf) - maxEventsBuf
		l.buf = append([]sideEvent(nil), l.buf[drop:]...)
		l.first = l.buf[0].Seq
	}
	for ch := range l.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	l.mu.Unlock()
	return ev
}

// since returns the buffered events after seq, and whether some were lost
// (dropped from the buffer before the caller read them).
func (l *eventLog) since(after int64) ([]sideEvent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	gap := after+1 < l.first && l.seq >= l.first
	var out []sideEvent
	for _, ev := range l.buf {
		if ev.Seq > after {
			out = append(out, ev)
		}
	}
	return out, gap
}

func (l *eventLog) subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
}

func (l *eventLog) last() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// --- the agent host -----------------------------------------------------------------

type agentHost struct {
	log *eventLog

	mu       sync.Mutex
	proc     *agentProc
	sessions map[string]*sessState
	turn     *turnState
}

type agentProc struct {
	key    string // identity of the command line: same key = reuse
	cmd    *exec.Cmd
	conn   *rpcConn
	init   json.RawMessage
	exited chan struct{}
	stderr *tailBuffer
}

type sessState struct {
	cwd     string
	costUSD float64
	// replaying is set while session/load replays history: those updates are
	// the past, which the engine already has, so they are not re-emitted.
	replaying bool
}

type turnState struct {
	id      string
	session string
	since   time.Time
	// produced: the agent said or did something this turn. A turn that ends
	// "normally" having produced nothing usually means the model call failed
	// and the agent only logged it (opencode does exactly this).
	produced   bool
	stderrMark int64
}

func newAgentHost(log *eventLog) *agentHost {
	return &agentHost{log: log, sessions: map[string]*sessState{}}
}

type startReq struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
}

// start launches (or reuses) the agent process and runs ACP initialize.
func (h *agentHost) start(req startReq) (json.RawMessage, error) {
	if len(req.Argv) == 0 {
		return nil, errors.New("argv is required")
	}
	key := strings.Join(req.Argv, "\x00")
	h.mu.Lock()
	if p := h.proc; p != nil && p.key == key && p.alive() {
		h.mu.Unlock()
		return p.init, nil
	}
	old := h.proc
	h.proc = nil
	h.sessions = map[string]*sessState{}
	h.mu.Unlock()
	if old != nil {
		old.kill()
	}

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Cwd
	cmd.Env = os.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Own process group: a stop kills the agent AND whatever it spawned
	// (MCP servers, shells), not just the top process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &agentProc{key: key, cmd: cmd, exited: make(chan struct{}), stderr: newTailBuffer(16 << 10)}
	cmd.Stderr = io.MultiWriter(p.stderr, os.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agent %q: %w", req.Argv[0], err)
	}
	p.conn = newRPCConn(stdin)
	p.conn.onNotify = h.onNotify
	p.conn.onRequest = h.onRequest
	go func() {
		p.conn.readLoop(stdout)
		err := cmd.Wait()
		close(p.exited)
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		h.mu.Lock()
		current := h.proc == p
		if current {
			h.proc = nil
			h.sessions = map[string]*sessState{}
		}
		h.mu.Unlock()
		if current {
			msg := fmt.Sprintf("agent exited (code %d)", code)
			if err != nil && code == -1 {
				msg = "agent exited: " + err.Error()
			}
			h.log.emit(sideEvent{Type: "agent_exit", Error: msg, Text: p.stderr.String()})
		}
	}()

	res, err := p.conn.call("initialize", map[string]any{
		"protocolVersion": 1,
		// No client-side fs or terminal: the agent works on the workspace
		// directly, as it does on a developer's machine.
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "apothecary", "version": "1"},
	}, p.exited)
	if err != nil {
		p.kill()
		return nil, fmt.Errorf("initialize: %w%s", err, stderrHint(p.stderr))
	}
	p.init = res
	h.mu.Lock()
	h.proc = p
	h.mu.Unlock()
	return res, nil
}

func stderrHint(t *tailBuffer) string {
	if s := strings.TrimSpace(t.String()); s != "" {
		return "\n" + tailStr(s, 2000)
	}
	return ""
}

func (p *agentProc) alive() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

func (p *agentProc) kill() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.exited
		}
	}
}

func (h *agentHost) stop() {
	h.mu.Lock()
	p := h.proc
	h.proc = nil
	h.sessions = map[string]*sessState{}
	h.turn = nil
	h.mu.Unlock()
	if p != nil {
		p.kill()
	}
}

func (h *agentHost) current() (*agentProc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.proc == nil || !h.proc.alive() {
		return nil, errAgentGone
	}
	return h.proc, nil
}

type mcpServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []envVar `json:"env"`
}

type envVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type sessionReq struct {
	// SessionID set = load that session (resume); empty = a new one.
	SessionID  string      `json:"session_id,omitempty"`
	Cwd        string      `json:"cwd"`
	Mode       string      `json:"mode,omitempty"`  // ACP session mode (opencode: the agent)
	Model      string      `json:"model,omitempty"` // provider/model as the flow says it
	MCPServers []mcpServer `json:"mcp_servers,omitempty"`
}

type sessionResp struct {
	SessionID string  `json:"session_id"`
	Loaded    bool    `json:"loaded"` // false: it was already live in this process
	CostUSD   float64 `json:"cost_usd"`
	Model     string  `json:"model,omitempty"`
	// Models / Modes are what the agent advertised for the session: the
	// authoritative list of what it will accept (empty when not reported).
	Models []string `json:"models,omitempty"`
	Modes  []string `json:"modes,omitempty"`
}

// session creates or loads an ACP session and applies the mode and model.
func (h *agentHost) session(req sessionReq) (sessionResp, error) {
	p, err := h.current()
	if err != nil {
		return sessionResp{}, err
	}
	if req.MCPServers == nil {
		req.MCPServers = []mcpServer{}
	}
	var raw json.RawMessage
	sid := req.SessionID
	loaded := false
	if sid != "" {
		h.mu.Lock()
		st, live := h.sessions[sid]
		h.mu.Unlock()
		if live {
			return sessionResp{SessionID: sid, CostUSD: st.costUSD}, nil
		}
		h.mu.Lock()
		h.sessions[sid] = &sessState{cwd: req.Cwd, replaying: true}
		h.mu.Unlock()
		raw, err = p.conn.call("session/load", map[string]any{
			"sessionId": sid, "cwd": req.Cwd, "mcpServers": req.MCPServers,
		}, p.exited)
		h.mu.Lock()
		if st := h.sessions[sid]; st != nil {
			st.replaying = false
		}
		h.mu.Unlock()
		if err != nil {
			h.mu.Lock()
			delete(h.sessions, sid)
			h.mu.Unlock()
			return sessionResp{}, fmt.Errorf("session/load %s: %w", sid, err)
		}
		loaded = true
	} else {
		raw, err = p.conn.call("session/new", map[string]any{
			"cwd": req.Cwd, "mcpServers": req.MCPServers,
		}, p.exited)
		if err != nil {
			return sessionResp{}, fmt.Errorf("session/new: %w%s", err, stderrHint(p.stderr))
		}
		var nr struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(raw, &nr); err != nil || nr.SessionID == "" {
			return sessionResp{}, fmt.Errorf("session/new: no sessionId in %s", tailStr(string(raw), 300))
		}
		sid = nr.SessionID
		h.mu.Lock()
		h.sessions[sid] = &sessState{cwd: req.Cwd}
		h.mu.Unlock()
	}
	caps := parseSessionCaps(raw)
	if req.Mode != "" {
		if err := h.setMode(p, sid, req.Mode, caps); err != nil {
			return sessionResp{}, err
		}
	}
	model := ""
	if req.Model != "" {
		model, err = h.setModel(p, sid, req.Model, caps)
		if err != nil {
			return sessionResp{}, err
		}
	}
	h.mu.Lock()
	cost := 0.0
	if st := h.sessions[sid]; st != nil {
		cost = st.costUSD
	}
	h.mu.Unlock()
	return sessionResp{SessionID: sid, Loaded: loaded, CostUSD: cost, Model: model,
		Models: caps.models, Modes: caps.modes}, nil
}

// sessionCaps is what session/new|load said the session can be set to.
type sessionCaps struct {
	modes   []string
	models  []string
	configs []configOption
}

type configOption struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Options  []struct {
		Value string `json:"value"`
	} `json:"options"`
}

func parseSessionCaps(raw json.RawMessage) sessionCaps {
	var r struct {
		Modes *struct {
			AvailableModes []struct {
				ID string `json:"id"`
			} `json:"availableModes"`
		} `json:"modes"`
		Models *struct {
			AvailableModels []struct {
				ModelID string `json:"modelId"`
			} `json:"availableModels"`
		} `json:"models"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	_ = json.Unmarshal(raw, &r)
	var c sessionCaps
	if r.Modes != nil {
		for _, m := range r.Modes.AvailableModes {
			c.modes = append(c.modes, m.ID)
		}
	}
	if r.Models != nil {
		for _, m := range r.Models.AvailableModels {
			c.models = append(c.models, m.ModelID)
		}
	}
	c.configs = r.ConfigOptions
	for _, o := range r.ConfigOptions {
		switch {
		case o.ID == "mode" || o.Category == "mode":
			for _, v := range o.Options {
				c.modes = append(c.modes, v.Value)
			}
		case o.ID == "model" || o.Category == "model":
			for _, v := range o.Options {
				c.models = append(c.models, v.Value)
			}
		}
	}
	return c
}

func (h *agentHost) setMode(p *agentProc, sid, mode string, caps sessionCaps) error {
	_, err := p.conn.call("session/set_mode", map[string]any{"sessionId": sid, "modeId": mode}, p.exited)
	if err == nil {
		return nil
	}
	if id := configID(caps, "mode"); id != "" {
		if _, cerr := p.conn.call("session/set_config_option",
			map[string]any{"sessionId": sid, "configId": id, "value": mode}, p.exited); cerr == nil {
			return nil
		}
	}
	if len(caps.modes) > 0 {
		return fmt.Errorf("agent %q is not available in this harness (available: %s): %w",
			mode, strings.Join(caps.modes, ", "), err)
	}
	return fmt.Errorf("set agent %q: %w", mode, err)
}

// setModel applies the flow's provider/model. Agents spell model ids their
// own way (opencode: provider/model, codex: bare model[/effort]), so the
// exact string is tried first, then the closest advertised id.
func (h *agentHost) setModel(p *agentProc, sid, model string, caps sessionCaps) (string, error) {
	candidates := modelCandidates(model, caps.models)
	var lastErr error
	for _, m := range candidates {
		_, err := p.conn.call("session/set_model", map[string]any{"sessionId": sid, "modelId": m}, p.exited)
		if err == nil {
			return m, nil
		}
		lastErr = err
		if id := configID(caps, "model"); id != "" {
			if _, cerr := p.conn.call("session/set_config_option",
				map[string]any{"sessionId": sid, "configId": id, "value": m}, p.exited); cerr == nil {
				return m, nil
			}
		}
	}
	if len(caps.models) > 0 {
		return "", fmt.Errorf("model %q is not available in this harness (available: %s): %v",
			model, strings.Join(truncList(caps.models, 30), ", "), lastErr)
	}
	return "", fmt.Errorf("set model %q: %v", model, lastErr)
}

func configID(caps sessionCaps, kind string) string {
	for _, o := range caps.configs {
		if o.ID == kind || o.Category == kind {
			return o.ID
		}
	}
	return ""
}

// modelCandidates orders the ids worth trying for a flow model string: the
// exact string, then advertised ids that match it with or without the
// provider prefix, then the bare id.
func modelCandidates(model string, advertised []string) []string {
	bare := model
	if _, rest, ok := strings.Cut(model, "/"); ok {
		bare = rest
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(model)
	for _, a := range advertised {
		if a == model || a == bare {
			add(a)
		}
	}
	for _, a := range advertised {
		if strings.HasPrefix(a, model+"/") || strings.HasPrefix(a, bare+"/") || strings.HasSuffix(a, "/"+bare) {
			add(a)
		}
	}
	add(bare)
	return out
}

type promptReq struct {
	SessionID string `json:"session_id"`
	Turn      string `json:"turn"`
	Text      string `json:"text"`
}

var errBusy = errors.New("a turn is already in progress")

// prompt starts a turn and returns at once; the turn's end arrives as a
// turn_end event. One turn at a time per host: the engine queues the rest.
func (h *agentHost) prompt(req promptReq) error {
	p, err := h.current()
	if err != nil {
		return err
	}
	h.mu.Lock()
	if h.turn != nil {
		h.mu.Unlock()
		return errBusy
	}
	if _, ok := h.sessions[req.SessionID]; !ok {
		h.mu.Unlock()
		return fmt.Errorf("session %s is not live in this agent process", req.SessionID)
	}
	h.turn = &turnState{id: req.Turn, session: req.SessionID, since: time.Now(), stderrMark: p.stderr.mark()}
	turn := h.turn
	h.mu.Unlock()
	h.log.emit(sideEvent{Type: "turn_start", Session: req.SessionID, Turn: req.Turn})
	go func() {
		raw, err := p.conn.call("session/prompt", map[string]any{
			"sessionId": req.SessionID,
			"prompt":    []any{map[string]any{"type": "text", "text": req.Text}},
		}, p.exited)
		ev := sideEvent{Type: "turn_end", Session: req.SessionID, Turn: req.Turn}
		if err != nil {
			ev.StopReason = "error"
			ev.Error = err.Error()
			if errors.Is(err, errAgentGone) {
				ev.Error += stderrHint(p.stderr)
			}
		} else {
			var r struct {
				StopReason string `json:"stopReason"`
				Usage      *struct {
					InputTokens       int64 `json:"inputTokens"`
					OutputTokens      int64 `json:"outputTokens"`
					ThoughtTokens     int64 `json:"thoughtTokens"`
					CachedReadTokens  int64 `json:"cachedReadTokens"`
					CachedWriteTokens int64 `json:"cachedWriteTokens"`
				} `json:"usage"`
			}
			_ = json.Unmarshal(raw, &r)
			ev.StopReason = r.StopReason
			h.mu.Lock()
			produced := turn.produced
			h.mu.Unlock()
			if !produced && r.StopReason != "cancelled" {
				if errs := errorLines(p.stderr.since(turn.stderrMark)); errs != "" {
					ev.StopReason = "error"
					ev.Error = "the agent ended its turn without any output; its log reports:\n" + errs
				}
			}
			if r.Usage != nil {
				ev.Usage = &turnUsage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens,
					ThoughtTokens: r.Usage.ThoughtTokens, CachedReadTokens: r.Usage.CachedReadTokens,
					CachedWriteTokens: r.Usage.CachedWriteTokens}
			}
		}
		h.mu.Lock()
		if h.turn != nil && h.turn.id == req.Turn {
			h.turn = nil
		}
		h.mu.Unlock()
		h.log.emit(ev)
	}()
	return nil
}

func (h *agentHost) cancel(sessionID string) error {
	p, err := h.current()
	if err != nil {
		return err
	}
	return p.conn.notify("session/cancel", map[string]any{"sessionId": sessionID})
}

type hostStatus struct {
	Boot     string             `json:"boot"`
	Running  bool               `json:"running"`
	LastSeq  int64              `json:"last_seq"`
	Sessions map[string]float64 `json:"sessions"` // live session id -> cumulative cost
	Turn     *turnStatus        `json:"turn,omitempty"`
}

type turnStatus struct {
	ID      string    `json:"id"`
	Session string    `json:"session"`
	Since   time.Time `json:"since"`
}

func (h *agentHost) status() hostStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := hostStatus{Boot: h.log.boot, LastSeq: h.log.last(), Sessions: map[string]float64{}}
	st.Running = h.proc != nil && h.proc.alive()
	for id, s := range h.sessions {
		st.Sessions[id] = s.costUSD
	}
	if h.turn != nil {
		st.Turn = &turnStatus{ID: h.turn.id, Session: h.turn.session, Since: h.turn.since}
	}
	return st
}

// onRequest answers what the agent asks of the client. Permissions are
// granted: the workspace is the sandbox (container + egress gateway), and the
// harnesses are configured not to ask in the first place — this is the
// backstop that keeps a stray prompt from hanging a turn forever.
func (h *agentHost) onRequest(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "session/request_permission":
		var r struct {
			SessionID string          `json:"sessionId"`
			ToolCall  json.RawMessage `json:"toolCall"`
			Options   []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
				Name     string `json:"name"`
			} `json:"options"`
		}
		_ = json.Unmarshal(params, &r)
		choice := ""
		for _, want := range []string{"allow_always", "allow_once"} {
			for _, o := range r.Options {
				if o.Kind == want && choice == "" {
					choice = o.OptionID
				}
			}
		}
		tool := normalizeTool(r.ToolCall)
		ev := sideEvent{Type: "permission", Session: r.SessionID, Tool: tool, Turn: h.turnID()}
		if choice == "" {
			ev.Text = "denied (no allow option offered)"
			h.log.emit(ev)
			return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
		}
		ev.Text = "auto-approved"
		h.log.emit(ev)
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": choice}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not supported by this client: " + method}
	}
}

func (h *agentHost) turnID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turn != nil {
		return h.turn.id
	}
	return ""
}

func (h *agentHost) onNotify(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var n struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &n); err != nil {
		return
	}
	h.mu.Lock()
	st := h.sessions[n.SessionID]
	replaying := st != nil && st.replaying
	turn := ""
	if h.turn != nil && h.turn.session == n.SessionID {
		turn = h.turn.id
	}
	h.mu.Unlock()
	ev, ok := normalizeUpdate(n.Update)
	if !ok {
		return
	}
	if ev.Type == "usage" && ev.CostUSD != nil {
		h.mu.Lock()
		if st != nil {
			st.costUSD = *ev.CostUSD
		}
		h.mu.Unlock()
	}
	if replaying {
		return
	}
	if ev.Type != "usage" {
		h.mu.Lock()
		if h.turn != nil && h.turn.session == n.SessionID {
			h.turn.produced = true
		}
		h.mu.Unlock()
	}
	ev.Session, ev.Turn = n.SessionID, turn
	h.log.emit(ev)
}

// normalizeUpdate maps one ACP session/update to the engine's vocabulary.
// Unknown update kinds are dropped: they are UI sugar for editors
// (available commands, mode changes) the engine has no use for.
func normalizeUpdate(raw json.RawMessage) (sideEvent, bool) {
	var u struct {
		Kind string `json:"sessionUpdate"`
		// Content is one block for message chunks but an ARRAY for tool
		// calls, so it is decoded per kind below.
		Content json.RawMessage `json:"content"`
		Entries []struct {
			Content  string `json:"content"`
			Status   string `json:"status"`
			Priority string `json:"priority"`
		} `json:"entries"`
		Used int64 `json:"used"`
		Size int64 `json:"size"`
		Cost *struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return sideEvent{}, false
	}
	var block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	switch u.Kind {
	case "agent_message_chunk":
		if json.Unmarshal(u.Content, &block) != nil || block.Type != "text" || block.Text == "" {
			return sideEvent{}, false
		}
		return sideEvent{Type: "message", Text: block.Text}, true
	case "agent_thought_chunk":
		if json.Unmarshal(u.Content, &block) != nil || block.Text == "" {
			return sideEvent{}, false
		}
		return sideEvent{Type: "thought", Text: block.Text}, true
	case "tool_call":
		return sideEvent{Type: "tool_call", Tool: normalizeTool(raw)}, true
	case "tool_call_update":
		return sideEvent{Type: "tool_update", Tool: normalizeTool(raw)}, true
	case "plan":
		ev := sideEvent{Type: "plan"}
		for _, e := range u.Entries {
			ev.Plan = append(ev.Plan, planEntry{Content: e.Content, Status: e.Status, Priority: e.Priority})
		}
		return ev, true
	case "usage_update":
		ev := sideEvent{Type: "usage", ContextUsed: u.Used, ContextSize: u.Size}
		if u.Cost != nil && (u.Cost.Currency == "" || strings.EqualFold(u.Cost.Currency, "USD")) {
			c := u.Cost.Amount
			ev.CostUSD = &c
		}
		return ev, true
	}
	return sideEvent{}, false
}

// normalizeTool flattens an ACP ToolCall / ToolCallUpdate. Updates carry only
// the fields that changed, so empty fields mean "unchanged", not "cleared".
func normalizeTool(raw json.RawMessage) *toolInfo {
	var t struct {
		ID        string          `json:"toolCallId"`
		Title     string          `json:"title"`
		Kind      string          `json:"kind"`
		Status    string          `json:"status"`
		RawInput  json.RawMessage `json:"rawInput"`
		RawOutput json.RawMessage `json:"rawOutput"`
		Content   []struct {
			Type    string `json:"type"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Path    string  `json:"path"`
			OldText *string `json:"oldText"`
			NewText string  `json:"newText"`
		} `json:"content"`
		Locations []struct {
			Path string `json:"path"`
			Line *int   `json:"line"`
		} `json:"locations"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return &toolInfo{}
	}
	ti := &toolInfo{ID: t.ID, Title: t.Title, Kind: t.Kind, Status: t.Status}
	if len(t.RawInput) > 0 && string(t.RawInput) != "null" && string(t.RawInput) != "{}" {
		ti.Input = truncate(string(t.RawInput), maxToolText)
	}
	var texts []string
	for _, c := range t.Content {
		switch c.Type {
		case "content":
			if c.Content.Type == "text" && c.Content.Text != "" {
				texts = append(texts, c.Content.Text)
			}
		case "diff":
			d := toolDiff{Path: c.Path, NewText: truncate(c.NewText, maxDiffText)}
			if c.OldText != nil {
				d.OldText = truncate(*c.OldText, maxDiffText)
			}
			ti.Diffs = append(ti.Diffs, d)
		}
	}
	if len(texts) == 0 && len(t.RawOutput) > 0 && string(t.RawOutput) != "null" {
		var s string
		if json.Unmarshal(t.RawOutput, &s) == nil {
			texts = append(texts, s)
		} else {
			var o struct {
				Output string `json:"output"`
			}
			if json.Unmarshal(t.RawOutput, &o) == nil && o.Output != "" {
				texts = append(texts, o.Output)
			}
		}
	}
	if len(texts) > 0 {
		ti.Output = truncate(strings.Join(texts, "\n"), maxToolText)
	}
	for _, l := range t.Locations {
		loc := l.Path
		if l.Line != nil {
			loc = fmt.Sprintf("%s:%d", l.Path, *l.Line)
		}
		ti.Locations = append(ti.Locations, loc)
	}
	return ti
}

// truncate keeps the head and tail of s within n bytes: tool output's useful
// part is usually its end (the error), a file's its start.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	half := n / 2
	return s[:half] + fmt.Sprintf("\n… [%d bytes elided] …\n", len(s)-n) + s[len(s)-half:]
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func truncList(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n]...), fmt.Sprintf("… %d more", len(xs)-n))
}

// tailBuffer keeps the last n bytes written to it (the agent's stderr, shown
// when it dies so the failure explains itself).
type tailBuffer struct {
	mu    sync.Mutex
	n     int
	buf   []byte
	total int64 // bytes ever written: a mark for "what came after"
}

func newTailBuffer(n int) *tailBuffer { return &tailBuffer{n: n} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	t.total += int64(len(p))
	if len(t.buf) > t.n {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.n:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func (t *tailBuffer) mark() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.total
}

// since returns what was written after mark (as much of it as is kept).
func (t *tailBuffer) since(mark int64) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.total - mark
	if n <= 0 {
		return ""
	}
	if n > int64(len(t.buf)) {
		n = int64(len(t.buf))
	}
	return string(t.buf[int64(len(t.buf))-n:])
}

// errorLines picks the lines of an agent's log that report errors.
func errorLines(log string) string {
	var out []string
	for line := range strings.SplitSeq(log, "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "level=error") || strings.Contains(l, "error:") || strings.HasPrefix(l, "error") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	if len(out) > 8 {
		out = out[len(out)-8:]
	}
	return strings.Join(out, "\n")
}
