package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"apothecary/internal/flow"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/logs"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/notifyport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

// manualBriefMax caps the brief a manual stage's ping carries; the whole of
// it is in the workspace (briefPath).
const manualBriefMax = 64 << 10

// stageManual is a manual stage (FLOWLANG §2): the person has the workspace.
// The debug leaf's hand-over, declared: the stage starts, its brief — the
// inputs rendered as an agent's prompt renders them — goes on the ping and
// into the workspace, and the run waits with no slot and no clock. Done
// follows on_success; giving up is a dead end, as at the debug leaf.
func (x *exec) stageManual(st *flow.Stage) error {
	rs := x.rs
	// The answer, replayed into GateAnswer (DecidesHere): route it.
	if rs.Ph == runcore.PhIn && rs.GateAnswer != nil {
		ans := rs.GateAnswer
		rs.GateAnswer = nil
		return x.routeManual(st, ans)
	}
	var p *Ping
	if rs.Ph == runcore.PhIn {
		p = x.openYesNo(st, PingManual)
	}
	if p == nil {
		iter := rs.Iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
		brief := x.manualBrief(st, iter)
		x.writeBrief(st, brief)
		p = &Ping{Type: PingManual, Question: manualQuestion(st, rs.View.TicketTitle),
			Context: capBrief(brief, st.ID), Link: x.manualLink(st), StageID: st.ID,
			AnswerKind: event.AnswerYesNo, Phase: rs.Phase, EndsPhase: !x.endsRunAtDeadEnd()}
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

// routeManual acts on the person's word at a manual stage: Done completes
// it — the stage's time is the hand-over's, and nothing is carried on — and
// giving up is a dead end.
func (x *exec) routeManual(st *flow.Stage, rec *event.PingAnswered) error {
	if rec.Answer == "approve" {
		now := x.e.clock()
		started := x.rs.ParkSince[st.ID]
		if started.IsZero() {
			started = now
		}
		if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: now.Sub(started).Milliseconds()}); err != nil {
			return err
		}
		x.rs.RouteSuccess(st, "")
		return nil
	}
	reason := fmt.Sprintf("gave up at manual stage %s", st.ID)
	if fb := strings.TrimSpace(rec.Feedback); fb != "" {
		reason += ": " + shell.Tail(fb, 2<<10)
	}
	return x.deadEnd(st, "", reason)
}

// manualQuestion is what the card asks: the stage's instruction, or a plain
// fallback naming the stage, with the ticket title so a notification
// identifies the run (as gateQuestion).
func manualQuestion(st *flow.Stage, ticketTitle string) string {
	q := st.Question
	if q == "" {
		q = fmt.Sprintf("Your turn: %s", st.ID)
	}
	if ticketTitle != "" {
		q += " — " + ticketTitle
	}
	return q
}

// manualLink is the first link input that resolves — the card's one button
// out; every link is in the brief too.
func (x *exec) manualLink(st *flow.Stage) string {
	for _, in := range st.Inputs {
		if flow.SourceType(in.Value) != agentspec.TypeLink {
			continue
		}
		if link := x.sourceLink(in.Value); link != "" {
			return link
		}
	}
	return ""
}

// manualBrief is what the person is handed: the instruction, then the
// stage's inputs rendered as an agent's prompt renders them — review threads
// as readable markdown.
func (x *exec) manualBrief(st *flow.Stage, iter int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: your turn\n\n", st.ID)
	if st.Question != "" {
		b.WriteString(st.Question + "\n\n")
	}
	x.writeInputs(&b, st, iter, func(in flow.Port) string { return declDesc(in.Description) }, true)
	return b.String()
}

// briefPath is where a manual stage's brief lands in the workspace: under
// .apothecary/, which bootstrap excludes from git, so it is never committed.
func briefPath(stageID string) string { return ".apothecary/manual/" + stageID + ".md" }

// writeBrief puts the brief in the workspace, overwriting an earlier
// attempt's. A failure costs the file, not the stage: the card carries the
// brief.
func (x *exec) writeBrief(st *flow.Stage, brief string) {
	if err := x.res.Envp.WriteFile(x.ctx, x.rs.EnvID, briefPath(st.ID), []byte(brief), 0o644); err != nil {
		x.e.log.WarnContext(x.bg, "writing a manual stage's brief",
			logs.RunID, x.rs.RunID, logs.StageID, st.ID, "err", err)
	}
}

// capBrief keeps a brief's head within manualBriefMax, pointing at the whole
// of it in the workspace.
func capBrief(brief, stageID string) string {
	if len(brief) <= manualBriefMax {
		return brief
	}
	cut := manualBriefMax
	for cut > 0 && !utf8.RuneStart(brief[cut]) {
		cut--
	}
	return brief[:cut] + "\n… (cut: the whole brief is in " + briefPath(stageID) + " in the workspace)\n"
}
