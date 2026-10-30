package engine

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"apothecary/internal/event"
	"apothecary/internal/ports/agentport"
)

// --- inbox ----------------------------------------------------------------------

// inbox is one invocation's queue of human messages (agentport.Inbox). The run
// loop pushes; the runner takes from its own goroutine.
type inbox struct {
	mu   sync.Mutex
	q    []agentport.UserMessage
	held bool
	wake chan struct{}
}

func newInbox() *inbox { return &inbox{wake: make(chan struct{}, 1)} }

func (b *inbox) push(m agentport.UserMessage) {
	b.mu.Lock()
	b.q = append(b.q, m)
	b.mu.Unlock()
	b.signal()
}

func (b *inbox) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// setHeld flips the take-over flag; letting go wakes a runner waiting on
// the holder so it can return.
func (b *inbox) setHeld(held bool) {
	b.mu.Lock()
	b.held = held
	b.mu.Unlock()
	if !held {
		b.signal()
	}
}

func (b *inbox) Held() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held
}

// pending reports whether anything is queued and not yet taken.
func (b *inbox) pending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.q) > 0
}

func (b *inbox) Wake() <-chan struct{} { return b.wake }

func (b *inbox) Take() []agentport.UserMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.q
	b.q = nil
	return q
}

func (b *inbox) Interrupting() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range b.q {
		if m.Interrupt {
			return true
		}
	}
	return false
}

// msgReq is a human message on its way into the run loop.
type msgReq struct {
	msg agentport.UserMessage
	ack chan error
}

// formatMessages renders human messages for the agent. Kept identical to
// the ACP runner's rendering of messages it delivers mid-invocation. Text
// the engine composed (Verbatim) follows the human's, as it is.
func formatMessages(msgs []agentport.UserMessage) string {
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

// --- live deltas -------------------------------------------------------------------

// LiveDelta is ephemeral progress of a running agent — the text of the
// message segment being written, a tool call's latest state. It is never
// persisted: the coalesced agent_message / agent_tool event that follows
// supersedes it (same Segment / ToolCallID). Viewers that miss deltas lose
// nothing but liveness.
type LiveDelta struct {
	StageID   string           `json:"stage_id"`
	Iteration int              `json:"iteration,omitempty"`
	Turn      string           `json:"turn,omitempty"`
	Kind      string           `json:"kind"` // message|thought|tool|turn_start|turn_end
	Segment   string           `json:"segment,omitempty"`
	Text      string           `json:"text,omitempty"` // the segment's text SO FAR (replace, not append)
	Tool      *event.AgentTool `json:"tool,omitempty"`
}

type liveHub struct {
	mu   sync.Mutex
	subs map[string]map[chan LiveDelta]struct{}
}

func (h *liveHub) publish(runID string, d LiveDelta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[runID] {
		select {
		case ch <- d:
		default: // a slow viewer drops deltas, never stalls the run
		}
	}
}

// SubscribeLive streams a run's live deltas until cancel is called.
func (e *Engine) SubscribeLive(runID string) (<-chan LiveDelta, func()) {
	ch := make(chan LiveDelta, 256)
	h := &e.live
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[string]map[chan LiveDelta]struct{}{}
	}
	if h.subs[runID] == nil {
		h.subs[runID] = map[chan LiveDelta]struct{}{}
	}
	h.subs[runID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[runID], ch)
		if len(h.subs[runID]) == 0 {
			delete(h.subs, runID)
		}
		h.mu.Unlock()
	}
}

// --- the transcript ------------------------------------------------------------------

// Persisted size caps: a transcript is for reading, and the log must stay
// replay-cheap. The sidecar already truncates tool input/output.
const (
	maxMessageText = 64 << 10
	maxThoughtText = 16 << 10
	maxPromptText  = 32 << 10
	// deltaEvery throttles live message deltas: the full segment so far is
	// re-sent at most this often.
	deltaEvery = 120 * time.Millisecond
)

// transcript coalesces one attempt's AgentEvents into durable events: text
// chunks into segments, tool-call updates into one record per call. Runs on
// the loop goroutine (it appends).
type transcript struct {
	x    *exec
	st   string
	iter int

	segN      int
	seg       string
	segThink  bool
	segTurn   string
	buf       strings.Builder
	cursor    string
	lastDelta time.Time

	tools   map[string]*event.AgentTool
	order   []string
	lastPln string
}

func (x *exec) newTranscript(stageID string, iter int) *transcript {
	return &transcript{x: x, st: stageID, iter: iter, tools: map[string]*event.AgentTool{}}
}

func (t *transcript) publish(d LiveDelta) {
	d.StageID, d.Iteration = t.st, t.iter
	t.x.e.live.publish(t.x.rs.runID, d)
}

// handle folds one event in, appending whatever it completes.
func (t *transcript) handle(ev agentport.AgentEvent) error {
	if ev.Cursor != "" {
		t.cursor = ev.Cursor
	}
	switch ev.Kind {
	case agentport.AgentEvMessage, agentport.AgentEvThought:
		thought := ev.Kind == agentport.AgentEvThought
		if t.seg != "" && (t.segThink != thought || t.segTurn != ev.Turn) {
			if err := t.flushText(); err != nil {
				return err
			}
		}
		if t.seg == "" {
			t.segN++
			t.seg = fmt.Sprintf("%s.%d.%d", t.st, t.iter, t.segN)
			t.segThink, t.segTurn = thought, ev.Turn
		}
		t.buf.WriteString(ev.Text)
		if time.Since(t.lastDelta) >= deltaEvery {
			t.lastDelta = time.Now()
			kind := "message"
			if thought {
				kind = "thought"
			}
			t.publish(LiveDelta{Kind: kind, Turn: ev.Turn, Segment: t.seg, Text: t.x.redact.Scrub(t.buf.String())})
		}
		return nil
	case agentport.AgentEvTool:
		if err := t.flushText(); err != nil {
			return err
		}
		if ev.Tool == nil || ev.Tool.ID == "" {
			return nil
		}
		cur := t.tools[ev.Tool.ID]
		if cur == nil {
			cur = &event.AgentTool{StageID: t.st, Iteration: t.iter, ToolCallID: ev.Tool.ID}
			t.tools[ev.Tool.ID] = cur
			t.order = append(t.order, ev.Tool.ID)
		}
		mergeTool(cur, ev.Tool, ev.Turn)
		cur.Cursor = t.cursor
		snap := *cur
		snap.Input, snap.Output = t.x.redact.Scrub(snap.Input), t.x.redact.Scrub(snap.Output)
		t.publish(LiveDelta{Kind: "tool", Turn: ev.Turn, Tool: &snap})
		if cur.Status == "completed" || cur.Status == "failed" {
			return t.flushTool(ev.Tool.ID)
		}
		return nil
	case agentport.AgentEvPlan:
		if err := t.flushText(); err != nil {
			return err
		}
		p := &event.AgentPlan{StageID: t.st, Iteration: t.iter, Turn: ev.Turn, Cursor: t.cursor}
		var sig strings.Builder
		for _, e := range ev.Plan {
			p.Entries = append(p.Entries, event.PlanEntry{Content: e.Content, Status: e.Status, Priority: e.Priority})
			sig.WriteString(e.Status + "\x00" + e.Content + "\x00")
		}
		if sig.String() == t.lastPln {
			return nil // agents re-send unchanged plans; one record per change
		}
		t.lastPln = sig.String()
		return t.x.append(p)
	case agentport.AgentEvPermission:
		if err := t.flushText(); err != nil {
			return err
		}
		title := "a tool call"
		if ev.Tool != nil && ev.Tool.Title != "" {
			title = ev.Tool.Title
		}
		return t.x.append(&event.AgentNotice{StageID: t.st, Iteration: t.iter, Cursor: t.cursor,
			Text: fmt.Sprintf("Permission requested for %s: %s", title, ev.Text)})
	case agentport.AgentEvTurnStart:
		t.publish(LiveDelta{Kind: "turn_start", Turn: ev.Turn})
		return nil
	case agentport.AgentEvTurnEnd:
		if err := t.flushAll(); err != nil {
			return err
		}
		t.publish(LiveDelta{Kind: "turn_end", Turn: ev.Turn})
		if ev.Error != "" {
			return t.x.append(&event.AgentNotice{StageID: t.st, Iteration: t.iter, Cursor: t.cursor,
				Text: "The turn ended with an error: " + ev.Error})
		}
		return nil
	case agentport.AgentEvUserMessage:
		if err := t.flushAll(); err != nil {
			return err
		}
		ids := make([]string, 0, len(ev.Messages))
		for _, m := range ev.Messages {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		if len(ids) == 0 {
			return nil // engine-composed text (the handback) is recorded as agent_prompted
		}
		if err := t.x.append(&event.MessageDelivered{MessageIDs: ids, StageID: t.st, Iteration: t.iter}); err != nil {
			return err
		}
		return nil
	case agentport.AgentEvNotice:
		if err := t.flushAll(); err != nil {
			return err
		}
		return t.x.append(&event.AgentNotice{StageID: t.st, Iteration: t.iter, Cursor: t.cursor, Text: ev.Text})
	}
	return nil
}

func mergeTool(cur *event.AgentTool, u *agentport.AgentTool, turn string) {
	if turn != "" {
		cur.Turn = turn
	}
	if u.Title != "" {
		cur.Title = u.Title
	}
	if u.Kind != "" {
		cur.Kind = u.Kind
	}
	if u.Status != "" {
		cur.Status = u.Status
	}
	if u.Input != "" {
		cur.Input = u.Input
	}
	if u.Output != "" {
		cur.Output = u.Output
	}
	if len(u.Locations) > 0 {
		cur.Locations = u.Locations
	}
	if len(u.Diffs) > 0 {
		cur.Diffs = cur.Diffs[:0]
		for _, d := range u.Diffs {
			cur.Diffs = append(cur.Diffs, event.AgentDiff{Path: d.Path, OldText: d.OldText, NewText: d.NewText})
		}
	}
}

func (t *transcript) flushText() error {
	if t.seg == "" {
		return nil
	}
	text := t.buf.String()
	limit := maxMessageText
	if t.segThink {
		limit = maxThoughtText
	}
	if len(text) > limit {
		text = text[:limit] + "\n… [truncated]"
	}
	m := &event.AgentMessage{StageID: t.st, Iteration: t.iter, Turn: t.segTurn, Segment: t.seg,
		Text: text, Thought: t.segThink, Cursor: t.cursor}
	t.seg = ""
	t.buf.Reset()
	if strings.TrimSpace(m.Text) == "" {
		return nil
	}
	return t.x.append(m)
}

func (t *transcript) flushTool(id string) error {
	cur := t.tools[id]
	if cur == nil {
		return nil
	}
	delete(t.tools, id)
	for i, o := range t.order {
		if o == id {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	return t.x.append(cur)
}

// flushAll persists everything still open: the text segment and any tool
// call that never reported a final state (its last state is what is known).
func (t *transcript) flushAll() error {
	if err := t.flushText(); err != nil {
		return err
	}
	for len(t.order) > 0 {
		if err := t.flushTool(t.order[0]); err != nil {
			return err
		}
	}
	return nil
}

// prompted records what the engine sent the agent.
func (x *exec) prompted(stageID string, iter int, kind, text string) error {
	p := &event.AgentPrompted{StageID: stageID, Iteration: iter, Kind: kind, Text: text}
	if len(p.Text) > maxPromptText {
		p.Text, p.Truncated = p.Text[:maxPromptText], true
	}
	return x.append(p)
}
