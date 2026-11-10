package engine

import (
	"fmt"
	"strings"

	"apothecary/internal/flow"
	"apothecary/internal/platform/logs"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// pingAnswersSection renders a run.ping_answers input: what the task's owner
// answered while the run went on, read from the run's log as the prompt is
// built — oldest first, each under the stage that asked and when, at most
// flow.PingAnswersBudget bytes of them (the oldest dropped first, a lone
// answer longer than the budget cut).
//
// What counts as an answer is what said something about the work: every
// question an agent asked (ask_human), and a gate, a debug leaf or a manual
// stage only when the person wrote feedback or review comments there — a
// bare approve says nothing a plan does not. A phase confirmation and a
// selection decide what runs, not what is built: never.
//
// The person answering owns the task, so unlike a ticket's comments these are
// direction, and the section says so: they amend the plan and the other
// inputs where they differ, a later answer over an earlier one.
func (x *exec) pingAnswersSection(name, desc string) string {
	evs, err := x.e.store.Events(x.ctx, x.rs.RunID, 0)
	if err != nil {
		x.e.log.WarnContext(x.bg, "prompt: reading the run's answers", logs.RunID, x.rs.RunID, "err", err)
		return fmt.Sprintf("# PING ANSWERS (input: %s%s)\n(the answers could not be read just now)\n", name, desc)
	}
	var answers []pingAnswer
	for _, p := range run.ReducePings(evs) {
		if !answerSaysSomething(p) {
			continue
		}
		ans := PingAnswer{Answer: p.Answer, Feedback: p.Feedback, Content: p.Content, Review: p.Review}
		answers = append(answers, pingAnswer{view: p, text: composeAnswerText(p.AnswerKind, p.Form, ans, p.Files, x.answerFileRefs(p.PingID))})
	}
	return renderPingAnswers(name, desc, answers)
}

// pingAnswer is one answered ping and its answer as the asking agent read it.
type pingAnswer struct {
	view run.PingView
	text string
}

// answerSaysSomething reports whether an answered ping belongs in a
// run.ping_answers input (pingAnswersSection).
func answerSaysSomething(p run.PingView) bool {
	if p.Status != "answered" {
		return false
	}
	switch {
	case run.AgentPingType(p.Type):
		return true
	case p.Type == event.PingGate, p.Type == event.PingDebug, p.Type == event.PingManual:
		return strings.TrimSpace(p.Feedback) != "" || len(p.Review) > 0
	}
	return false
}

// renderPingAnswers is the section proper; "(none yet)" when nothing was
// answered, so a prompt that names the input never meets a silence it has to
// interpret.
func renderPingAnswers(name, desc string, answers []pingAnswer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# PING ANSWERS (input: %s%s)\n", name, desc)
	if len(answers) == 0 {
		b.WriteString("No question has been answered by the task's owner in this run yet.\n")
		return b.String()
	}
	blocks := make([]string, 0, len(answers))
	used := 0
	for i := len(answers) - 1; i >= 0; i-- {
		block := answerBlock(answers[i], flow.PingAnswersBudget-used)
		if block == "" {
			break
		}
		used += len(block)
		blocks = append(blocks, block)
	}
	if len(blocks) == len(answers) {
		fmt.Fprintf(&b, "The %d answer(s) the task's owner gave during this run, oldest first.", len(answers))
	} else {
		fmt.Fprintf(&b, "The latest %d of the %d answer(s) the task's owner gave during this run, oldest first; the older ones are left out.", len(blocks), len(answers))
	}
	b.WriteString(" They are the owner's direction: where one differs from the plan or another input, the answer stands, " +
		"and a later answer stands over an earlier one. Work done to follow an answer is not a deviation from the plan.\n")
	for i := len(blocks) - 1; i >= 0; i-- {
		b.WriteString(blocks[i])
	}
	return b.String()
}

// answerBlock renders one answer in at most room bytes: a heading with the
// stage that asked, the kind of question and when it was answered, then the
// question and the answer, every line quoted. An answer too long for the room
// is cut at a line's worth of margin; "" when not even the heading, the
// question's first line and a line of the answer fit.
func answerBlock(a pingAnswer, room int) string {
	p := a.view
	head := "\n## " + p.Type
	if p.StageID != "" {
		head += " at " + p.StageID
	}
	if p.AnsweredAt != nil {
		head += " — " + p.AnsweredAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	head += "\n"
	asked := "Asked:\n" + quoteLines(p.Question)
	answered := "Answered:\n" + quoteLines(a.text)
	if len(head)+len(asked)+len(answered) <= room {
		return head + asked + answered
	}
	const cut = "> … (cut: longer)\n"
	// The answer is what the reader needs; a long question gives way first.
	if q := firstLine(asked); len(head)+len(q)+len(answered) <= room {
		return head + q + "> … (question cut)\n" + answered
	}
	asked = firstLine(asked)
	fit := room - len(head) - len(asked) - len("> … (question cut)\n") - len(cut)
	if fit < 80 {
		return ""
	}
	answered = cutAtLine(answered, fit)
	return head + asked + "> … (question cut)\n" + answered + cut
}

// firstLine is "Asked:\n" and the question's first quoted line.
func firstLine(asked string) string {
	lines := strings.SplitN(asked, "\n", 3)
	if len(lines) < 2 {
		return asked
	}
	return lines[0] + "\n" + lines[1] + "\n"
}

// cutAtLine keeps at most n bytes of s, ending on a whole line.
func cutAtLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = strings.ToValidUTF8(s[:n], "")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[:i+1]
	}
	return s + "\n"
}
