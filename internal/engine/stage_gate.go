package engine

import (
	"fmt"
	"strings"
	"time"

	"apothecary/internal/agentspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/runcore"
)

func (x *exec) stageGate(st *flow.Stage) error {
	rs := x.rs

	// Recovery: answered pre-crash but routing unrecorded.
	if rs.Ph == runcore.PhIn && rs.GateAnswer != nil {
		return x.routeGate(st, rs.GateAnswer, x.e.clock())
	}

	var p *Ping
	if rs.Ph == runcore.PhIn {
		p = x.openYesNo(st, PingGate)
	}
	if p == nil {
		iter := rs.Iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
		// The gate's inputs: files to review and at most one link source
		// the engine resolves. TYPED HERE, at source resolution — this is
		// the one place that knows which is which, so nothing downstream ever
		// sniffs a string for "http" (DESIGN-FILES §4).
		files, types, link := x.gateFiles(st)
		p = &Ping{
			Type:     PingGate,
			Question: gateQuestion(st, rs.View.TicketTitle),
			// A gate over several merge requests lists them all; the link
			// stays the first (the primary repository's).
			Context: x.mrListMarkdown(),
			Files:   files, FileTypes: types, Link: link, StageID: st.ID, AnswerKind: event.AnswerYesNo,
		}
		if err := x.raiseYesNo(p); err != nil {
			return err
		}
		// The plan a human approves is the single most important document in
		// the run — snapshot it as reviewed, not as the workspace has it later.
		x.publishPingFiles(p, st.ID)
		x.e.notify(x.bg, notifyport.Notification{
			RunID: rs.RunID, PingID: p.ID, Type: p.Type,
			Title: p.Question, Body: p.Primary(),
		})
	}

	// A gate is a wait point (FLOWLANG §1): the chain parks on the decision
	// and keeps the cursor, so nothing else opens while a person thinks.
	// The idle loop takes the answer and routes it
	// (runcore.State.PlanResume). Recovery re-registers the open ping from
	// the log and lands here again.
	return x.parkAt(st, p.ID)
}

// gateFiles is what a gate shows: its files in port order — a folder's
// (type files) each, as attachments are bounded: the first
// attachmentMaxFiles in all, none past attachmentMaxBytes (a snapshot of it
// would be cut) — each structured file's port type by path (what offers
// curation), and the link its link port resolves to.
func (x *exec) gateFiles(st *flow.Stage) (files []string, types map[string]string, link string) {
	for _, in := range st.Inputs {
		switch {
		case flow.IsEngineSource(in.Value):
			link = x.resolveGateInput(in.Value)
			continue
		case in.Type != agentspec.TypeFiles:
			path := flow.FilePath(in.Value)
			files = append(files, path)
			if agentspec.IsStructuredType(in.Type) {
				if types == nil {
					types = map[string]string{}
				}
				types[path] = in.Type
			}
			continue
		}
		dir := flow.FilePath(in.Value)
		held, err := x.dirFiles(dir)
		if err != nil {
			x.e.log.Warn("listing a gate's folder", "run", x.rs.RunID, "stage", st.ID, "path", dir, "err", err)
		}
		for _, f := range held {
			if f.Size > attachmentMaxBytes {
				x.e.log.Warn("file too large to review", "run", x.rs.RunID, "stage", st.ID, "path", f.Path, "size", f.Size)
				continue
			}
			files = append(files, f.Path)
		}
	}
	if len(files) > attachmentMaxFiles {
		x.e.log.Warn("a gate shows more files than are reviewed", "run", x.rs.RunID, "stage", st.ID,
			"files", len(files), "shown", attachmentMaxFiles)
		files = files[:attachmentMaxFiles]
	}
	return files, types, link
}

// resolveGateInput turns a gate's input into what the human is handed: the
// link for a link source, the workspace path of a file to review.
func (x *exec) resolveGateInput(v string) string {
	if link := x.sourceLink(v); link != "" || flow.IsEngineSource(v) {
		return link
	}
	return flow.FilePath(v)
}

// sourceLink resolves a link source (run.mr.url, run.workspace,
// run.service.<name> — the human's preview link), or "".
func (x *exec) sourceLink(v string) string {
	switch v {
	case flow.SourceMRURL:
		// On a subject, the merge request the phase acts on.
		if mr := x.rs.SubjectMR(); mr != nil {
			return mr.Ref.URL
		}
		return x.rs.View.MRURL
	case flow.SourceWorkspace:
		// Workspace hand-off: the environment's app link, when it has one.
		return workspaceLink(x.rs.Links)
	}
	if name := flow.SourceService(v); name != "" {
		return x.serviceLink(name)
	}
	return ""
}

// openYesNo returns the live Ping for a still-open yes/no question of this stage
// and type — one runLoop re-registered from the log after a restart — or nil
// when the stage has to raise one.
func (x *exec) openYesNo(st *flow.Stage, ptype string) *Ping {
	op := x.rs.OpenPing(st.ID, ptype)
	if op == nil {
		return nil
	}
	x.e.mu.Lock()
	defer x.e.mu.Unlock()
	return x.e.pings[op.PingID]
}

// raiseYesNo registers a yes/no question and records it: the caller fills
// what is asked and of which stage, this mints the id and the clock. What
// happens next (a snapshot of the reviewed file, the channels) is the
// caller's.
func (x *exec) raiseYesNo(p *Ping) error {
	p.ID, p.RunID, p.RaisedAt, p.done = x.e.newID(), x.rs.RunID, x.e.clock(), make(chan struct{})
	x.e.mu.Lock()
	x.e.pings[p.ID] = p
	x.e.mu.Unlock()
	raised := &event.PingRaised{PingID: p.ID, Type: p.Type, Question: p.Question, Context: p.Context,
		Files: p.Files, FileTypes: p.FileTypes, Link: p.Link, StageID: p.StageID, AnswerKind: p.AnswerKind,
		Phase: p.Phase, EndsPhase: p.EndsPhase}
	if err := x.append(raised); err != nil {
		return err
	}
	// As replay has it: what the ping showed, for the answer's review.
	return nil
}

// stageDebug is the debug leaf (FLOWLANG §3): st took `then: debug`, and the
// run parks here for a human who has the workspace. No stage of its own —
// the ping belongs to st — and nothing is spent while they look. The engine
// composes no prose: the ping carries which stage, what it printed and where
// the workspace is, and the card is the UI's. Continue re-enters st as a
// fresh attempt (the cursor never left it); giving up is a dead end — it
// abandons the run in the main chain or an outcome, and ends a watcher's
// round.
// Like a gate it never provisions: ensureEnv runs lazily when st re-enters,
// and re-seeds what a human may have changed.
func (x *exec) stageDebug(st *flow.Stage) error {
	rs := x.rs
	// Recovery: answered pre-crash, routing unrecorded.
	if rs.GateAnswer != nil {
		ans := rs.GateAnswer
		rs.GateAnswer = nil
		return x.routeDebug(st, PingAnswer{Answer: ans.Answer, Feedback: ans.Feedback})
	}
	if x.openYesNo(st, PingDebug) == nil {
		p := &Ping{Type: PingDebug, Question: st.ID + " failed", Context: shell.Tail(rs.Feed, 4<<10),
			Link: workspaceLink(rs.Links), StageID: st.ID, AnswerKind: event.AnswerYesNo,
			Phase: rs.Phase, EndsPhase: !x.endsRunAtDeadEnd()}
		if err := x.raiseYesNo(p); err != nil {
			return err
		}
		x.e.notify(x.bg, notifyport.Notification{
			RunID: rs.RunID, PingID: p.ID, Type: p.Type,
			Title: p.Question, Body: p.Primary(),
		})
	}
	// Pure waiting: give the slot back. The answer wakes the run, and replay
	// lands on GateAnswer above.
	return errWaitingOnHuman
}

// routeDebug acts on the human's decision at the debug leaf.
func (x *exec) routeDebug(st *flow.Stage, ans PingAnswer) error {
	rs := x.rs
	if ans.Answer == "approve" {
		// Continue: back into st, fresh — pos never moved. What advance does
		// after a gate's approval, by hand: no feed, no "attempt n of m".
		rs.Ph = runcore.PhStart
		rs.Feed, rs.FeedSentinel, rs.FeedAttempts = "", "", 0
		return nil
	}
	reason := fmt.Sprintf("debug rejected at stage %s", st.ID)
	if fb := strings.TrimSpace(ans.Feedback); fb != "" {
		reason += ": " + shell.Tail(fb, 2<<10)
	}
	return x.deadEnd(st, "", reason)
}

// routeGate follows a gate's answer, carrying the verdict — the person's
// summary, their comments file by file, what they attached — to on_reject,
// or to on_success when the approval said something. What they made of a
// review document the gate showed goes on the record first (recordCuration):
// approved, it is written back before the next reader starts; rejected, the
// redo reads what they dropped.
func (x *exec) routeGate(st *flow.Stage, rec *event.PingAnswered, started time.Time) error {
	var shown []string
	if r := x.rs.RaisedPings[rec.PingID]; r != nil {
		shown = r.Files
	}
	curated, err := x.recordCuration(st, rec)
	if err != nil {
		return err
	}
	verdict := gateVerdict(st.ID, rec, shown, x.answerFileRefs(rec.PingID))
	if note := renderCuration(curated); note != "" && rec.Answer != "approve" {
		verdict = capVerdict(strings.TrimRight(verdict, "\n") + "\n" + note)
	}
	if rec.Answer == "approve" {
		verdict = x.redact.Scrub(verdict)
		if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds(), Feed: verdict}); err != nil {
			return err
		}
		x.rs.RouteSuccess(st, verdict)
		return nil
	}
	return x.takeEdge(st, flow.EdgeReject, verdict)
}

// gateQuestion is what the human is asked: the stage's question when it has
// one, else a plain fallback naming the stage. The ticket title rides along so
// a notification identifies the run.
func gateQuestion(st *flow.Stage, ticketTitle string) string {
	q := st.Question
	if q == "" {
		q = fmt.Sprintf("Gate: %s", st.ID)
	}
	if ticketTitle == "" {
		return q
	}
	return fmt.Sprintf("%s — %s", q, ticketTitle)
}
