// Package acpagent adapts the AgentRunner port to any coding agent that
// speaks the Agent Client Protocol, hosted by the workspace sidecar
// (internal/sidecar). One runner, every ACP harness: opencode (`opencode
// acp`), codex (`codex-acp`), and anything else that speaks the protocol. A
// harness kind contributes only a Profile — how to start it and how a flow's
// agent maps onto it.
//
// What this buys over one-shot CLI invocations:
//   - the transcript streams as it happens (message text, tool calls with
//     their input/output, plans), normalized by the protocol, not scraped;
//   - the agent process and its session outlive a turn, so a human message
//     is delivered into the SAME session (queued to the end of the turn, or
//     interrupting it), and an ask_human tool call can simply block;
//   - the process outlives the engine: after a restart, Reattach follows the
//     turn still in flight instead of restarting it.
package acpagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/agentport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/sidecar"
)

// Profile is what one harness contributes.
type Profile struct {
	// Harness names the harness in errors ("opencode").
	Harness string
	// Command is the shell command that starts the agent speaking ACP on
	// stdio ("opencode acp"). It runs in the repo after provider.env is
	// sourced, as the workspace user.
	Command string
	// Mode maps a stage's agent to the ACP session mode, "" for none
	// (opencode's modes ARE its agents; codex has no named agents).
	Mode func(spec agentport.AgentSpec) string
	// Preamble, when set, is prepended to a NEW session's first prompt —
	// the role prompt, for harnesses with nowhere else to put it.
	Preamble func(ctx context.Context, env envport.EnvID, spec agentport.AgentSpec) (string, error)
	// MCPServers ride EVERY session, ahead of the stage's own — for a
	// harness that reads no MCP config file in ACP mode (omp), this is the
	// only way ask_human reaches it.
	MCPServers []agentport.MCPServer
	// ReadOnlyBy says how the harness honours a read-only session
	// (agentport.AgentInput.ReadOnly, the Ask pane's): ReadOnlyByMode puts the
	// session in ReadOnlyMode; ReadOnlyByAgent relies on the agent
	// definition the engine prepared (its mutating tools denied);
	// ReadOnlyByProcess runs the session in a SEPARATE agent process started
	// with ReadOnlyCommand — a command line that loads no mutating tool
	// (omp's --tools). "" = the harness cannot promise it, and the engine
	// keeps Ask off for it. A read-only session never gets the profile's own
	// MCP servers either (ask_human): it answers, it does not ask.
	ReadOnlyBy      string
	ReadOnlyMode    string
	ReadOnlyCommand string
}

// How a Profile honours a read-only session.
const (
	ReadOnlyByMode    = "mode"
	ReadOnlyByAgent   = "agent"
	ReadOnlyByProcess = "process"
)

// CanReadOnly reports whether the harness can run a session that cannot
// change the workspace (agentport.ReadOnlyCapable).
func (r *Runner) CanReadOnly() bool { return r.p.ReadOnlyBy != "" }

type Runner struct {
	envp envport.EnvProvisioner
	p    Profile
	// cancelGrace bounds how long a cancelled turn may take to wind down
	// before the runner gives up on it. A var for tests.
	cancelGrace time.Duration
}

func New(envp envport.EnvProvisioner, p Profile) *Runner {
	return &Runner{envp: envp, p: p, cancelGrace: 30 * time.Second}
}

// envPrelude loads the engine-written credentials into the agent's
// environment, exactly as the one-shot invocations did.
const envPrelude = `set -a; [ -f /apothecary/provider.env ] && . /apothecary/provider.env; ` +
	`[ -s /apothecary/proxy.env ] && . /apothecary/proxy.env; set +a; `

func (r *Runner) client(ctx context.Context, env envport.EnvID) (*sidecar.Client, error) {
	sc, ok := r.envp.(envport.Sidecar)
	if !ok {
		return nil, fmt.Errorf("%s: this workspace provisioner cannot host the agent sidecar", r.p.Harness)
	}
	conn, err := sc.Sidecar(ctx, env)
	if err != nil {
		return nil, fmt.Errorf("%s: start workspace sidecar: %w", r.p.Harness, err)
	}
	return sidecar.New(conn), nil
}

// start launches (or reuses) the harness process a session runs in: the
// read-only command line's own process for a read-only session, when that
// is how the profile promises read-only.
func (r *Runner) start(ctx context.Context, c *sidecar.Client, readOnly bool) (sidecar.StartResponse, error) {
	cmd := r.p.Command
	if readOnly && r.p.ReadOnlyBy == ReadOnlyByProcess {
		cmd = r.p.ReadOnlyCommand
	}
	st, err := c.Start(ctx, sidecar.StartRequest{Argv: []string{"bash", "-c", envPrelude + "exec " + cmd}})
	if err != nil {
		return st, fmt.Errorf("%s: start agent: %w", r.p.Harness, err)
	}
	return st, nil
}

// mcpServers maps a stage's MCP servers onto the session: ACP carries them
// on session/new and session/load, so they belong to the session — the
// agent's — rather than to a config file every agent in the workspace
// reads. A remote server needs the agent to say it takes one (initialize:
// agentCapabilities.mcpCapabilities.http); one that does not is refused
// here, before the session, rather than silently dropped by the agent.
func (r *Runner) mcpServers(init json.RawMessage, stage []agentport.MCPServer, readOnly bool) ([]sidecar.MCPServer, error) {
	servers := stage
	if !readOnly {
		servers = append(slices.Clone(r.p.MCPServers), stage...)
	}
	if len(servers) == 0 {
		return nil, nil
	}
	for _, m := range stage {
		for _, own := range r.p.MCPServers {
			if m.Name == own.Name {
				return nil, fmt.Errorf("%s: mcp server %q is the harness's own; name the flow's server something else", r.p.Harness, m.Name)
			}
		}
	}
	var caps struct {
		AgentCapabilities struct {
			MCPCapabilities struct {
				HTTP bool `json:"http"`
			} `json:"mcpCapabilities"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(init, &caps)
	out := make([]sidecar.MCPServer, 0, len(servers))
	for _, m := range servers {
		if m.URL != "" {
			if !caps.AgentCapabilities.MCPCapabilities.HTTP {
				return nil, fmt.Errorf("%s: mcp server %q is remote (url), and this harness takes only local (command) MCP servers", r.p.Harness, m.Name)
			}
			out = append(out, sidecar.MCPServer{Name: m.Name, URL: m.URL, Headers: pairs(m.Headers)})
			continue
		}
		if len(m.Command) == 0 {
			return nil, fmt.Errorf("%s: mcp server %q has neither a command nor a url", r.p.Harness, m.Name)
		}
		out = append(out, sidecar.MCPServer{Name: m.Name, Command: m.Command[0], Args: m.Command[1:], Env: pairs(m.Env)})
	}
	return out, nil
}

// pairs lists a map as ACP's name/value pairs, in name order.
func pairs(m map[string]string) []sidecar.EnvVar {
	if len(m) == 0 {
		return nil
	}
	out := make([]sidecar.EnvVar, 0, len(m))
	for k, v := range m {
		out = append(out, sidecar.EnvVar{Name: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Runner) Run(ctx context.Context, env envport.EnvID, spec agentport.AgentSpec, in agentport.AgentInput) (agentport.AgentResult, error) {
	c, err := r.client(ctx, env)
	if err != nil {
		return agentport.AgentResult{}, err
	}
	st, err := r.start(ctx, c, in.ReadOnly)
	if err != nil {
		return agentport.AgentResult{}, err
	}
	mcp, err := r.mcpServers(st.Init, in.MCPServers, in.ReadOnly)
	if err != nil {
		return agentport.AgentResult{}, err
	}
	mode := ""
	if r.p.Mode != nil {
		mode = r.p.Mode(spec)
	}
	if in.ReadOnly && r.p.ReadOnlyBy == ReadOnlyByMode {
		mode = r.p.ReadOnlyMode
	}
	sess, err := c.Session(ctx, sidecar.SessionRequest{Proc: st.Proc, Mode: mode, Model: spec.Model, MCPServers: mcp, ReadOnly: in.ReadOnly})
	if err != nil {
		return agentport.AgentResult{}, fmt.Errorf("%s: %w", r.p.Harness, err)
	}
	prompt := in.Prompt
	if r.p.Preamble != nil {
		pre, err := r.p.Preamble(ctx, env, spec)
		if err != nil {
			return agentport.AgentResult{Session: agentport.SessionID(sess.SessionID)}, err
		}
		prompt = pre + prompt
	}
	return r.drive(ctx, c, st.Proc, sess.SessionID, sess.CostUSD, prompt, "", "", in)
}

func (r *Runner) Resume(ctx context.Context, env envport.EnvID, s agentport.SessionID, in agentport.AgentInput) (agentport.AgentResult, error) {
	if s == "" {
		return agentport.AgentResult{}, fmt.Errorf("%s resume: no session recorded for this stage", r.p.Harness)
	}
	c, err := r.client(ctx, env)
	if err != nil {
		return agentport.AgentResult{}, err
	}
	st, err := r.start(ctx, c, in.ReadOnly)
	if err != nil {
		return agentport.AgentResult{}, err
	}
	mcp, err := r.mcpServers(st.Init, in.MCPServers, in.ReadOnly)
	if err != nil {
		return agentport.AgentResult{Session: s}, err
	}
	// Live in the process: a no-op (but for a model switch asked for).
	// Otherwise (the container restarted, the harness was swapped) the agent
	// reloads it from its own store — with the stage's MCP servers again,
	// which a reloaded session does not keep, and read-only again if it was.
	req := sidecar.SessionRequest{SessionID: string(s), Proc: st.Proc, MCPServers: mcp, ReadOnly: in.ReadOnly, Model: in.Model}
	if in.ReadOnly && r.p.ReadOnlyBy == ReadOnlyByMode {
		req.Mode = r.p.ReadOnlyMode
	}
	sess, err := c.Session(ctx, req)
	if err != nil {
		return agentport.AgentResult{Session: s}, fmt.Errorf("%s: resume session: %w", r.p.Harness, err)
	}
	return r.drive(ctx, c, st.Proc, sess.SessionID, sess.CostUSD, in.Prompt, "", "", in)
}

// attachable finds what Reattach would follow: the turn of s in flight, or
// the last turn of s that ran after cursor (it may have finished while the
// engine was down — its events are still in the sidecar's buffer).
func (r *Runner) attachable(ctx context.Context, c *sidecar.Client, s, cursor string) (turn string, cost float64, ok bool) {
	st, err := c.Status(ctx)
	if err != nil {
		return "", 0, false
	}
	if t := st.Turn(s); t != nil {
		return t.ID, st.Sessions[s], true
	}
	boot, seq, okc := sidecar.ParseCursor(cursor)
	if !okc {
		boot, seq = st.Boot, 0
	}
	if boot != st.Boot {
		return "", 0, false // the sidecar restarted: nothing of that turn survives
	}
	evs, err := c.Buffered(ctx, boot, seq)
	if err != nil {
		return "", 0, false
	}
	for _, ev := range evs {
		if ev.Session == s && ev.Turn != "" && (ev.Type == "turn_start" || ev.Type == "turn_end") {
			turn = ev.Turn
		}
	}
	return turn, st.Sessions[s], turn != ""
}

func (r *Runner) Reattachable(ctx context.Context, env envport.EnvID, s agentport.SessionID, cursor string) bool {
	if s == "" {
		return false
	}
	c, err := r.client(ctx, env)
	if err != nil {
		return false
	}
	_, _, ok := r.attachable(ctx, c, string(s), cursor)
	return ok
}

func (r *Runner) Reattach(ctx context.Context, env envport.EnvID, s agentport.SessionID, cursor string, in agentport.AgentInput) (agentport.AgentResult, bool, error) {
	if s == "" {
		return agentport.AgentResult{}, false, nil
	}
	c, err := r.client(ctx, env)
	if err != nil {
		return agentport.AgentResult{}, false, nil
	}
	turn, cost, ok := r.attachable(ctx, c, string(s), cursor)
	if !ok {
		return agentport.AgentResult{Session: s}, false, nil
	}
	// The process is the one the turn runs in (a reuse); its id keeps
	// another harness's exit out of this session's transcript.
	st, err := r.start(ctx, c, in.ReadOnly)
	if err != nil {
		return agentport.AgentResult{Session: s}, false, nil
	}
	// Spend before the cursor was already banked by the engine; what the
	// session cost meanwhile is not recoverable, so this undercounts by at
	// most the turn's own spend up to the restart.
	res, err := r.drive(ctx, c, st.Proc, string(s), cost, "", turn, cursor, in)
	return res, true, err
}

// AgentModels opens a throwaway session and returns the models the agent
// advertises for it. An agent that advertises none returns an empty list
// with no error — callers treat that as "cannot check".
func (r *Runner) AgentModels(ctx context.Context, env envport.EnvID) ([]string, error) {
	c, err := r.client(ctx, env)
	if err != nil {
		return nil, err
	}
	st, err := r.start(ctx, c, false)
	if err != nil {
		return nil, err
	}
	sess, err := c.Session(ctx, sidecar.SessionRequest{Proc: st.Proc})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.p.Harness, err)
	}
	return sess.Models, nil
}

func (r *Runner) StopAgent(ctx context.Context, env envport.EnvID) error {
	c, err := r.client(ctx, env)
	if err != nil {
		return err
	}
	return c.Stop(ctx)
}

func newTurnID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "t" + hex.EncodeToString(b)
}

// stream follows the sidecar's event stream from a position, reconnecting
// across dropped connections (an engine-side hiccup, not the agent's).
type stream struct {
	c    *sidecar.Client
	ctx  context.Context
	boot string
	seq  int64
}

// errSidecarRestarted: the sidecar itself restarted, so the turn in flight
// (and its agent process) is gone.
var errSidecarRestarted = errors.New("the workspace sidecar restarted mid-turn")

// pump delivers events to out until ctx ends; a fatal error goes to errc.
func (s *stream) pump(out chan<- sidecar.Event, errc chan<- error) {
	failures := 0
	for {
		evs, errs, err := s.c.Events(s.ctx, s.boot, s.seq)
		if err == nil {
			for ev := range evs {
				failures = 0
				if ev.Type == "gap" && s.boot != "" && ev.Boot != s.boot {
					errc <- errSidecarRestarted
					return
				}
				if ev.Boot != "" {
					s.boot = ev.Boot
				}
				if ev.Seq > 0 {
					s.seq = ev.Seq
				}
				select {
				case out <- ev:
				case <-s.ctx.Done():
					return
				}
			}
			<-errs
		}
		if s.ctx.Err() != nil {
			return
		}
		failures++
		if failures > 30 {
			errc <- errors.New("lost the agent's event stream")
			return
		}
		select {
		case <-time.After(time.Second):
		case <-s.ctx.Done():
			return
		}
	}
}

// drive runs turns in one session until the agent has nothing left to do:
// the first turn is text (or, when attaching, the turn already in flight),
// and each human message queued meanwhile becomes the next turn. proc is
// the agent process the session runs in: another process's exit (a second
// harness in the same workspace) is not this session's news.
func (r *Runner) drive(ctx context.Context, c *sidecar.Client, proc, sid string, costBase float64,
	text, attachTurn, cursor string, in agentport.AgentInput) (agentport.AgentResult, error) {
	res := agentport.AgentResult{Session: agentport.SessionID(sid)}
	cost := costBase
	done := func(err error) (agentport.AgentResult, error) {
		res.Usage.CostUSD = cost - costBase
		return res, err
	}

	// The stream outlives ctx by the cancel grace, so a cancelled turn can
	// still be followed to its end (its usage is real spend).
	sctx, cancelStream := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelStream()
	st := &stream{c: c, ctx: sctx}
	if b, seq, ok := sidecar.ParseCursor(cursor); ok && attachTurn != "" {
		st.boot, st.seq = b, seq
	} else {
		status, err := c.Status(ctx)
		if err != nil {
			return done(fmt.Errorf("%s: sidecar status: %w", r.p.Harness, err))
		}
		st.boot, st.seq = status.Boot, status.LastSeq
		if attachTurn != "" {
			st.seq = 0 // no recorded position: replay the whole buffer for this session
		}
	}
	evc := make(chan sidecar.Event, 64)
	perr := make(chan error, 1)
	go st.pump(evc, perr)

	var wake <-chan struct{}
	if in.Inbox != nil {
		wake = in.Inbox.Wake()
	}
	emit := func(ev agentport.AgentEvent) {
		if in.Events != nil {
			in.Events(ev)
		}
	}
	emit(agentport.AgentEvent{Kind: agentport.AgentEvSession, Text: sid})
	// Output is what the agent SAID — its message text, turn by turn: the
	// stage log and the on_fail feed, as the one-shot CLIs' text was.
	var said []string
	var saying strings.Builder
	flushSaid := func() {
		if t := strings.TrimSpace(saying.String()); t != "" {
			said = append(said, t)
		}
		saying.Reset()
	}
	done = func(err error) (agentport.AgentResult, error) {
		flushSaid()
		res.Usage.CostUSD = cost - costBase
		res.Output = strings.Join(said, "\n\n")
		if err != nil && res.Output == "" {
			res.Output = err.Error()
		}
		return res, err
	}
	ctxDone := ctx.Done()
	cancelled := false
	var deadline <-chan time.Time
	lastWasTool := false
	for {
		turn := attachTurn
		attachTurn = ""
		if turn == "" {
			if cancelled {
				return done(ctx.Err())
			}
			turn = newTurnID()
			if err := c.Prompt(ctx, sidecar.PromptRequest{SessionID: sid, Turn: turn, Text: text}); err != nil {
				return done(fmt.Errorf("%s: prompt: %w", r.p.Harness, err))
			}
		}
		res.TurnsUsed++
		turnCost := cost
		interrupted := false
		var end *sidecar.Event
		for end == nil {
			select {
			case err := <-perr:
				if errors.Is(err, errSidecarRestarted) {
					emit(agentport.AgentEvent{Kind: agentport.AgentEvNotice, Text: "The workspace sidecar restarted; the turn in flight was lost."})
				}
				return done(fmt.Errorf("%s: %w", r.p.Harness, err))
			case <-wake:
				if in.Inbox.Interrupting() && !interrupted {
					interrupted = true
					_ = c.Cancel(sctx, sid)
				}
			case <-ctxDone:
				// Cancelled by the engine (timeout, park, abort): ask the
				// agent to stop, then follow the turn to its end for a while.
				ctxDone, cancelled = nil, true
				_ = c.Cancel(sctx, sid)
				deadline = time.After(r.cancelGrace)
			case <-deadline:
				return done(fmt.Errorf("%s: turn did not end within %s of being cancelled", r.p.Harness, r.cancelGrace))
			case ev := <-evc:
				if ev.Session != "" && ev.Session != sid {
					continue // another session's traffic (a previous stage's leftovers)
				}
				cur := sidecar.Cursor(ev.Boot, ev.Seq)
				switch ev.Type {
				case "message", "thought":
					if lastWasTool {
						res.TurnsUsed++ // a new model step after tool results
						lastWasTool = false
					}
					kind := agentport.AgentEvMessage
					if ev.Type == "thought" {
						kind = agentport.AgentEvThought
					} else {
						saying.WriteString(ev.Text)
					}
					emit(agentport.AgentEvent{Kind: kind, Turn: ev.Turn, Text: ev.Text, Cursor: cur})
				case "tool_call", "tool_update":
					if !lastWasTool {
						flushSaid()
					}
					lastWasTool = true
					emit(agentport.AgentEvent{Kind: agentport.AgentEvTool, Turn: ev.Turn, Tool: toTool(ev.Tool), Cursor: cur})
				case "permission":
					emit(agentport.AgentEvent{Kind: agentport.AgentEvPermission, Turn: ev.Turn, Tool: toTool(ev.Tool), Text: ev.Text, Cursor: cur})
				case "plan":
					var plan []agentport.AgentPlanEntry
					for _, p := range ev.Plan {
						plan = append(plan, agentport.AgentPlanEntry{Content: p.Content, Status: p.Status, Priority: p.Priority})
					}
					emit(agentport.AgentEvent{Kind: agentport.AgentEvPlan, Turn: ev.Turn, Plan: plan, Cursor: cur})
				case "usage":
					if ev.CostUSD != nil {
						cost = *ev.CostUSD
					}
				case "turn_start":
					if ev.Turn == turn {
						emit(agentport.AgentEvent{Kind: agentport.AgentEvTurnStart, Turn: turn, Cursor: cur})
					}
				case "agent_exit":
					if ev.Proc != "" && proc != "" && ev.Proc != proc {
						continue
					}
					msg := strings.TrimSpace(ev.Error)
					if t := strings.TrimSpace(ev.Text); t != "" {
						msg += "\n" + shell.Tail(t, 2000)
					}
					emit(agentport.AgentEvent{Kind: agentport.AgentEvNotice, Text: msg, Cursor: cur})
				case "turn_end":
					if ev.Turn == turn {
						e := ev
						end = &e
						// What this one turn spent, for the engine's account
						// of the turns a take-over prompted.
						spent := &agentport.Usage{CostUSD: cost - turnCost}
						if u := ev.Usage; u != nil {
							spent.InputTokens = int(u.InputTokens + u.CachedWriteTokens)
							spent.OutputTokens = int(u.OutputTokens + u.ThoughtTokens)
						}
						emit(agentport.AgentEvent{Kind: agentport.AgentEvTurnEnd, Turn: turn, StopReason: ev.StopReason,
							Error: ev.Error, Cursor: cur, Usage: spent})
					}
				}
			}
		}
		if u := end.Usage; u != nil {
			res.Usage.InputTokens += int(u.InputTokens + u.CachedWriteTokens)
			res.Usage.OutputTokens += int(u.OutputTokens + u.ThoughtTokens)
		}
		if end.StopReason == "error" {
			return done(fmt.Errorf("%s: %s", r.p.Harness, end.Error))
		}
		if cancelled {
			return done(ctx.Err())
		}
		if in.Inbox == nil {
			return done(nil)
		}
		msgs := in.Inbox.Take()
		for len(msgs) == 0 {
			if !in.Inbox.Held() {
				// One last look: a release queues the handback and lets go
				// in the same breath, and the runner must not miss it.
				if msgs = in.Inbox.Take(); len(msgs) == 0 {
					return done(nil)
				}
				break
			}
			// A human holds the session: the turn's end is not the task's
			// end. Wait for their next word, or the release.
			select {
			case <-wake:
			case <-ctxDone:
				return done(ctx.Err())
			case err := <-perr:
				return done(fmt.Errorf("%s: %w", r.p.Harness, err))
			}
			msgs = in.Inbox.Take()
		}
		text = FormatMessages(msgs)
		emit(agentport.AgentEvent{Kind: agentport.AgentEvUserMessage, Messages: msgs, Text: text})
	}
}

// FormatMessages renders human messages as the agent's next prompt. Text
// the engine composed (Verbatim — the handback that ends a take-over)
// follows the human's, as it is.
func FormatMessages(msgs []agentport.UserMessage) string {
	var human []agentport.UserMessage
	var verbatim []string
	for _, m := range msgs {
		if m.Verbatim {
			verbatim = append(verbatim, m.Text)
		} else {
			human = append(human, m)
		}
	}
	var b strings.Builder
	switch len(human) {
	case 0:
	case 1:
		b.WriteString("Message from the human operator:\n\n" + human[0].Text)
	default:
		b.WriteString("Messages from the human operator:\n")
		for i, m := range human {
			fmt.Fprintf(&b, "\n%d. %s\n", i+1, m.Text)
		}
	}
	for _, v := range verbatim {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(v)
	}
	return b.String()
}

func toTool(t *sidecar.Tool) *agentport.AgentTool {
	if t == nil {
		return nil
	}
	out := &agentport.AgentTool{ID: t.ID, Title: t.Title, Kind: t.Kind, Status: t.Status,
		Input: t.Input, Output: t.Output, Locations: t.Locations}
	for _, d := range t.Diffs {
		out.Diffs = append(out.Diffs, agentport.AgentDiff{Path: d.Path, OldText: d.OldText, NewText: d.NewText})
	}
	return out
}

var (
	_ agentport.AgentRunner      = (*Runner)(nil)
	_ agentport.AgentReattacher  = (*Runner)(nil)
	_ agentport.AgentStopper     = (*Runner)(nil)
	_ agentport.AgentModelLister = (*Runner)(nil)
)
