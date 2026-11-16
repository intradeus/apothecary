package engine

// The run journal (FLOWLANG §4 run.journal): what has happened in a run, as
// its agents read it, so none repeats a mistake or reopens a correction the
// task's owner discarded. It is a view, never a file: buildJournal reads the
// event log, and an entry's bodies — an attempt's outputs and log — are read
// from the snapshots captured as each attempt ended, when the entry is
// rendered. Two renderings share the one model, so they cannot disagree:
//
//   - the ledger (renderLedger), pushed into the prompt of a stage that binds
//     run.journal: every owner decision, the agents' notes as claims, a short
//     history — each line ending with its reference;
//   - the tool's files (journalAttemptFile, journalRecords) and inline entries
//     (journalDecisionText, noteText), behind the journal routes the shim's
//     journal_read calls.
//
// Every entry is named by its reference (apothecary/internal/run/ref), from
// the log, so it is stable across restarts: an attempt is review#5, a
// decision the ping's own reference (build#3/decision#1, recorded as it was
// raised), a note its place in its attempt's notes (build#3/note#2).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"apothecary/internal/flow"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/logs"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/run/ref"
	"apothecary/internal/run/runcore"
)

// JournalTool is the shim's tool that reads the journal
// (workspace/shim/journal.go): the name the ledger tells agents to call.
const JournalTool = "journal_read"

const (
	// journalHead bounds a long body in the ledger — a verdict, an answer, a
	// question: its head is shown, and its reference says where the rest is.
	journalHead = 1500
	// journalNotesListed bounds the agent notes the ledger lists one line
	// each; the rest are counted, with the call that reads them.
	journalNotesListed = 30
	// journalNoteLine bounds one listed note.
	journalNoteLine = 300
)

// journal is a run's record, built from its log (buildJournal).
type journal struct {
	// decisions are the run's answered pings, in the order they were
	// answered; the ledger shows those that decide something (decides).
	decisions []*jDecision
	attempts  []*jAttempt
	byAttempt map[string]*jAttempt
	byDec     map[string]*jDecision
	// notes are the agents' notes (journal_notes outputs), in attempt order,
	// filled from the snapshots by loadNotes.
	notes  []*jNote
	byNote map[string]*jNote
	// chainRun numbers the run's chain runs (phase_started) in log order;
	// the latest is the one in progress.
	chainRun int
}

// jAttempt is one attempt of a stage, from its stage_started.
type jAttempt struct {
	ref   string // review#5
	stage string
	n     int
	// chainRun is the chain run the attempt belongs to (journal.chainRun).
	chainRun int
	at       time.Time
	// end is how it ended: completed, skipped, failed, paused, or "" while
	// it is still running.
	end   string
	note  string // a skip's note
	by    string // who skipped it, when a person did
	why   string // a failure's reason
	err   string // a failure's error text
	turns int
	// verdict is the attempt's verdict, when it rendered one.
	verdict  *bool
	comments int
	// routed is where its edge sent the run: "on_reject → build (2/5)".
	routed string
	// outputs are what it left, one capture per output (port and path): a
	// capture taken again after a crash replaces the first.
	outputs []jSnap
	log     *jSnap
	// notesErr says why its notes could not be read, when they could not.
	notesErr string
}

// jNote is one note an agent left for the journal: what it did differently
// from its direction, on its own — its claim, never the owner's direction.
type jNote struct {
	ref     string // build#3/note#2: its place in its attempt's notes
	attempt *jAttempt
	did     string
	why     string
	files   []string
}

// jSnap is one snapshot an attempt left: an output (its port's name and
// type) or its log.
type jSnap struct {
	name, typ, path, id, mime, filename string
	size                                int64
	truncated                           bool
}

// jDecision is one answered ping.
type jDecision struct {
	ref  string // build#3/decision#1, as ping_raised recorded it
	ping run.PingView
	// attempt is the attempt it was raised in or about ("" for the run's
	// own: a phase confirmation, a selection).
	attempt string
	// decides: it says something about the work (journalDecides) — what
	// the ledger shows.
	decides bool
	// files are the paths the answer's attachments were delivered to.
	files []answerFile
	// edits, on a debug continue: which files the person changed there.
	edits *event.HandEdits
	// unwritten, on a skip at the impasse: the outputs the stage declares,
	// which the skip left unwritten — what a later stage will not find.
	unwritten []string
}

// owner is the decisions the ledger shows: those that decide something.
func (j *journal) owner() []*jDecision {
	var out []*jDecision
	for _, d := range j.decisions {
		if d.decides {
			out = append(out, d)
		}
	}
	return out
}

// buildJournal reads a run's log into its journal. An output's type is the
// one its snapshot_captured records; fl types an output whose event carries
// none, and names what a skipped stage did not write (nil: neither). atts
// finds what an answer attached, and where it was delivered — under the
// ping's reference (nil: none).
func buildJournal(evs []event.Event, fl *flow.Flow, atts func(pingID, ref string) []answerFile) *journal {
	j := &journal{byAttempt: map[string]*jAttempt{}, byDec: map[string]*jDecision{}, byNote: map[string]*jNote{}}
	latest := map[string]*jAttempt{} // stage → its latest attempt
	edits := map[string]*event.HandEdits{}
	// A ping is named by the reference it was recorded with; one recorded
	// without (a log seeded straight into the store) is numbered by the
	// engine's own rule, from the same events.
	refs := runcore.NewState(nil)
	refOf := map[string]string{} // ping → its reference
	for _, e := range evs {
		switch p := e.Payload.(type) {
		case *event.PhaseStarted:
			j.chainRun++
		case *event.StageStarted:
			refs.Apply(&e)
			r := ref.Attempt(p.StageID, p.Attempt)
			a := j.byAttempt[r]
			if a == nil {
				a = &jAttempt{ref: r, stage: p.StageID, n: p.Attempt, chainRun: j.chainRun, at: e.TS}
				j.byAttempt[r] = a
				j.attempts = append(j.attempts, a)
			}
			a.end, a.why, a.err = "", "", ""
			latest[p.StageID] = a
		case *event.StageCompleted:
			if a := latest[p.StageID]; a != nil {
				a.end, a.turns = "completed", p.TurnsUsed
				if p.Skipped {
					a.end, a.note, a.by = "skipped", p.Note, p.By
				}
			}
		case *event.StageFailed:
			if a := latest[p.StageID]; a != nil {
				a.end, a.why, a.err, a.turns = "failed", p.Reason, p.Error, p.TurnsUsed
			}
		case *event.StagePaused:
			if a := latest[p.StageID]; a != nil {
				a.end = "paused"
			}
		case *event.StageResumed:
			if a := latest[p.StageID]; a != nil {
				a.end = ""
			}
		case *event.VerdictReceived:
			if a := latest[p.StageID]; a != nil {
				approve := p.Approve
				a.verdict, a.comments = &approve, p.NComments
			}
		case *event.EdgeTaken:
			if a := latest[p.From]; a != nil {
				a.routed = edgeWords(p)
			}
		case *event.SnapshotCaptured:
			refs.Apply(&e)
			a := j.byAttempt[ref.Attempt(p.StageID, p.Attempt)]
			if a == nil || (p.Kind != string(filestore.KindOutput) && p.Kind != string(filestore.KindLog)) {
				continue
			}
			s := jSnap{name: p.Name, path: p.Path, id: p.SnapshotID, mime: p.Mime, filename: p.Filename, size: p.Size, truncated: p.Truncated}
			if p.Kind == string(filestore.KindLog) {
				a.log = &s
				continue
			}
			// The type the attempt ran with, recorded on the event; the flow
			// only for an event that does not carry it.
			if s.typ = p.PortType; s.typ == "" {
				s.typ = outputType(fl, p.StageID, p.Name)
			}
			if i := slices.IndexFunc(a.outputs, func(o jSnap) bool { return o.name == s.name && o.path == s.path }); i >= 0 {
				a.outputs[i] = s
				continue
			}
			a.outputs = append(a.outputs, s)
		case *event.PingRaised:
			if p.Ref == "" {
				cp := *p
				refs.PingRefs([]event.Payload{&cp})
				ev := e
				ev.Payload = &cp
				refs.Apply(&ev)
				refOf[p.PingID] = cp.Ref
				continue
			}
			refs.Apply(&e)
			refOf[p.PingID] = p.Ref
		case *event.HandEdits:
			edits[p.PingID] = p // a continue recorded again after a crash: the last stands
		}
	}
	for _, p := range answeredInOrder(evs) {
		// Listed whatever its reference: no decision is ever dropped (one
		// the log names oddly reads as the run's).
		r := refOf[p.PingID]
		parsed, _ := ref.Parse(r)
		d := &jDecision{ref: r, ping: p, attempt: parsed.AttemptRef(), decides: journalDecides(p), edits: edits[p.PingID]}
		if p.Type == event.PingImpasse && p.Answer == event.ChoiceSkip {
			d.unwritten = declaredOutputs(fl, p.StageID)
		}
		if atts != nil && len(p.AnswerAttachmentIDs) > 0 {
			d.files = atts(p.PingID, r)
		}
		j.decisions = append(j.decisions, d)
		j.byDec[d.ref] = d
	}
	return j
}

// answeredInOrder is the run's answered pings in the order they were
// answered: a decision's number is its place in that order.
func answeredInOrder(evs []event.Event) []run.PingView {
	views := map[string]run.PingView{}
	for _, p := range run.ReducePings(evs) {
		views[p.PingID] = p
	}
	var out []run.PingView
	for _, e := range evs {
		if a, ok := e.Payload.(*event.PingAnswered); ok {
			if p, ok := views[a.PingID]; ok && p.Status == "answered" {
				out = append(out, p)
				delete(views, a.PingID)
			}
		}
	}
	return out
}

// outputType is the port type of a stage's output, by its name ("" when the
// flow does not say).
func outputType(fl *flow.Flow, stage, name string) string {
	if fl == nil {
		return ""
	}
	st := fl.Stage(stage)
	if st == nil {
		return ""
	}
	for _, o := range st.Outputs {
		if o.Name == name {
			return o.Type
		}
	}
	return ""
}

// declaredOutputs is a stage's outputs as the flow declares them, each by
// name and where it goes (nil when the flow does not say).
func declaredOutputs(fl *flow.Flow, stage string) []string {
	if fl == nil {
		return nil
	}
	st := fl.Stage(stage)
	if st == nil {
		return nil
	}
	var out []string
	for _, o := range st.Outputs {
		out = append(out, o.Name+" ("+o.Value+")")
	}
	return out
}

// edgeWords is where an edge sent the run, as the history reads it.
func edgeWords(p *event.EdgeTaken) string {
	switch {
	case p.Max < 0 && (p.To == flow.ReservedDebug || p.To == flow.ReservedImpasse):
		return fmt.Sprintf("%s → %s", p.Edge, p.To)
	case p.Max < 0:
		return fmt.Sprintf("%s → %s (%d)", p.Edge, p.To, p.Count)
	}
	return fmt.Sprintf("%s → %s (%d/%d)", p.Edge, p.To, p.Count, p.Max)
}

// journalDecides reports whether an answered ping is an owner decision: what
// said something about the work. Every question an agent asked; the
// impasse, always (keeping going, skipping or giving up is a decision on a
// verdict); a gate, a debug leaf or a manual stage when the person wrote
// feedback or review comments there — a bare approve says nothing a plan does
// not. A phase confirmation and a selection decide what runs, not what is
// built: never.
func journalDecides(p run.PingView) bool {
	if p.Status != "answered" {
		return false
	}
	switch {
	case run.AgentPingType(p.Type), p.Type == event.PingImpasse:
		return true
	case p.Type == event.PingGate, p.Type == event.PingDebug, p.Type == event.PingManual:
		return strings.TrimSpace(p.Feedback) != "" || len(p.Review) > 0
	}
	return false
}

// --- the ledger ---------------------------------------------------------------

// renderLedger is the # RUN JOURNAL section of a prompt: every owner
// decision in full (a long body by its head and its reference), the agents'
// notes as claims to check, a short history, and how to read more. Never
// empty: a prompt that names the input never meets a silence it has to
// interpret.
func renderLedger(j *journal, name, desc string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# RUN JOURNAL (input: %s%s)\n", name, desc)
	b.WriteString("The engine's record of this run so far. Before you ask for a change or redo work, check it against this section: " +
		"what the task's owner decided stands, and a correction they discarded stays discarded. Each entry ends with its reference [in brackets]: " +
		"review#5 is attempt 5 of the review stage, review#5/decision#1 the first decision in it; " +
		"the last part of this section says how to read more.\n")

	b.WriteString("\n## Owner decisions\n")
	if owner := j.owner(); len(owner) == 0 {
		b.WriteString("None yet: the task's owner has decided nothing during this run.\n")
	} else {
		fmt.Fprintf(&b, "The %d decision(s) the task's owner made during this run, oldest first. They are the owner's direction: "+
			"where one differs from the plan or another input, it stands, and a later one stands over an earlier one. "+
			"Work done to follow one is not a deviation from the plan.\n", len(owner))
		for _, d := range owner {
			b.WriteString(decisionBlock(d, journalHead))
		}
	}

	b.WriteString(notesBlock(j))
	b.WriteString(historyBlock(j))

	fmt.Fprintf(&b, "\n## Reading more\n"+
		"Call the %[1]s tool (your harness may list it with a prefix, such as ask_human_%[1]s) with a reference from this section. "+
		"%[1]s(ref: \"<stage>#<n>\") writes everything that attempt left — its record, its verdict, its outputs and its full log — to one file and gives you its path; "+
		"%[1]s(ref: \"<stage>#<n>/decision#<k>\") gives a decision in full, %[1]s(ref: \"<stage>#<n>/note#<k>\") a note; "+
		"%[1]s() with no reference writes one file holding every record of the run "+
		"(decisions, notes, each attempt and its verdict; no logs). Read the files with your own tools.\n", JournalTool)
	return b.String()
}

// notesBlock is the agents' notes in the ledger, one line each: this chain
// run's latest, up to journalNotesListed, oldest of them first — the latest
// are what the next agent works on — and the rest counted with the call
// that reads them all. They are claims, and the section says so: an agent cannot
// excuse its work to the next one by writing a note.
func notesBlock(j *journal) string {
	var b strings.Builder
	b.WriteString("\n## Agent notes\n")
	var now []*jNote
	for _, n := range j.notes {
		if n.attempt.chainRun == j.chainRun {
			now = append(now, n)
		}
	}
	earlier := len(j.notes) - len(now)
	if len(j.notes) == 0 {
		b.WriteString("None: no agent has noted doing anything differently from its direction.\n")
		return b.String()
	}
	b.WriteString("What agents said they did differently from their direction, on their own. These are their claims, not the owner's direction: " +
		"check each against the work — a note explains a change, it does not excuse it.\n")
	listed := now[len(now)-min(len(now), journalNotesListed):]
	if len(listed) == 0 {
		b.WriteString("None this round.\n")
	}
	for _, n := range listed {
		b.WriteString("- " + noteLine(n) + " [" + n.ref + "]\n")
	}
	if more := len(now) - len(listed); more > 0 || earlier > 0 {
		var parts []string
		if more > 0 {
			parts = append(parts, fmt.Sprintf("%d earlier from this round", more))
		}
		if earlier > 0 {
			parts = append(parts, fmt.Sprintf("%d from earlier rounds", earlier))
		}
		fmt.Fprintf(&b, "(%s: %s() writes them all.)\n", strings.Join(parts, " and "), JournalTool)
	}
	return b.String()
}

// noteLine is a note on one line, cut to journalNoteLine: what was done,
// why, the files (its reference, which follows, names its attempt).
func noteLine(n *jNote) string {
	line := oneLine(n.did) + " — why: " + oneLine(n.why)
	if len(n.files) > 0 {
		line += " (files: " + strings.Join(n.files, ", ") + ")"
	}
	if len(line) > journalNoteLine {
		cut := journalNoteLine
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + "…"
	}
	return line
}

// oneLine folds a text's lines into one.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// noteText is one note whole.
func noteText(n *jNote) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### note from %s [%s]\nDid:\n%sWhy:\n%s", n.attempt.ref, n.ref, quoteLines(n.did), quoteLines(n.why))
	if len(n.files) > 0 {
		b.WriteString("Files: " + strings.Join(n.files, ", ") + "\n")
	}
	return b.String()
}

// decisionBlock renders one decision: a heading with what was asked where,
// then the question (or, at the impasse, the verdict it answers) and the
// answer, every body line quoted. room bounds each body; a body longer than
// that is cut on a line and says where the whole of it is.
func decisionBlock(d *jDecision, room int) string {
	p := d.ping
	var b strings.Builder
	b.WriteString("\n### " + p.Type)
	if p.StageID != "" {
		b.WriteString(" at " + p.StageID)
	}
	if p.AnsweredAt != nil {
		b.WriteString(" — " + p.AnsweredAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	b.WriteString(" [" + d.ref + "]\n")
	more := fmt.Sprintf("%s(ref: %q)", JournalTool, d.ref)
	if p.Type == event.PingImpasse {
		b.WriteString(impasseAsked(p))
		if v := strings.TrimSpace(p.Context); v != "" {
			b.WriteString("The verdict it answers:\n")
			b.WriteString(quoteHead(v, room, more))
		}
	} else {
		b.WriteString("Asked:\n" + quoteHead(p.Question, room, more))
	}
	switch p.Type {
	case event.PingGate:
		// What the person was shown is what their decision is about.
		if shown := append(slices.Clone(p.Files), nonEmpty(p.Link)...); len(shown) > 0 {
			b.WriteString("It showed: " + strings.Join(shown, ", ") + "\n")
		}
	case event.PingDebug:
		if f := strings.TrimSpace(p.Context); f != "" {
			b.WriteString("What failed (the end of its output):\n" + quoteHead(f, room, more))
		}
	}
	b.WriteString("Decided:\n" + quoteHead(decisionAnswer(d), room, more))
	return b.String()
}

// nonEmpty is s as a list: none when it is blank.
func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// impasseAsked says what the impasse put to the person.
func impasseAsked(p run.PingView) string {
	return fmt.Sprintf("%s was rejected past its budget (%s), and the owner chose whether to keep going, skip it or give up.\n",
		p.StageID, p.Question)
}

// decisionAnswer is a decision's answer in words: the choice made, the
// person's note, their review comments and what they attached.
func decisionAnswer(d *jDecision) string {
	p := d.ping
	ans := PingAnswer{Answer: p.Answer, Feedback: p.Feedback, Content: p.Content, Review: p.Review}
	switch p.Type {
	case event.PingImpasse:
		ans.Answer = impasseChoiceWords(p.Answer, p.StageID, p.EndsPhase)
		ans.Feedback = ""
		text := composeAnswerText(event.AnswerYesNo, nil, ans, p.Files, d.files)
		if len(d.unwritten) > 0 {
			text += "\nIt would have written, and did not: " + strings.Join(d.unwritten, ", ")
		}
		if n := strings.TrimSpace(p.Feedback); n != "" {
			text += "\nTheir note: " + n
		}
		return text
	case event.PingGate:
		ans.Answer = map[string]string{"approve": "approved", "reject": "sent it back"}[p.Answer]
	case event.PingDebug:
		if p.Answer == "approve" {
			// A continue: what the person said they did, and the files they
			// changed — what the next attempt starts from.
			ans.Answer, ans.Feedback = "continued: the failing stage ran again", ""
			text := composeAnswerText(event.AnswerYesNo, nil, ans, p.Files, d.files)
			if n := strings.TrimSpace(p.Feedback); n != "" {
				text += "\nWhat they did: " + n
			}
			return text + handEditsWords(d.edits)
		}
		ans.Answer = map[string]string{"reject": "gave up"}[p.Answer]
	case event.PingManual:
		ans.Answer = map[string]string{"approve": "done by hand", "reject": "gave up"}[p.Answer]
	}
	if ans.Answer == "" {
		ans.Answer = p.Answer
	}
	// A form with no field leaves only the note: it starts the answer.
	return strings.TrimLeft(composeAnswerText(p.AnswerKind, p.Form, ans, p.Files, d.files), "\n")
}

// handEditsShown bounds the paths one checkout's hand edits list.
const handEditsShown = 50

// handEditsWords is which files a person changed at the debug leaf: per
// checkout on a run with several, the paths, none, or why it is not known.
// Nothing for a continue recorded without them (an older log).
func handEditsWords(e *event.HandEdits) string {
	if e == nil || len(e.Checkouts) == 0 {
		return ""
	}
	one := func(c event.CheckoutEdits) string {
		switch {
		case c.Unknown != "":
			return "could not be determined — " + c.Unknown
		case len(c.Paths) == 0:
			return "none"
		case len(c.Paths) > handEditsShown:
			return strings.Join(c.Paths[:handEditsShown], ", ") + fmt.Sprintf(" and %d more", len(c.Paths)-handEditsShown)
		}
		return strings.Join(c.Paths, ", ")
	}
	if len(e.Checkouts) == 1 {
		return "\nFiles they changed: " + one(e.Checkouts[0])
	}
	var b strings.Builder
	b.WriteString("\nFiles they changed:")
	for _, c := range e.Checkouts {
		b.WriteString("\n- " + c.Checkout + ": " + one(c))
	}
	return b.String()
}

// impasseChoiceWords is what a choice at the impasse did.
func impasseChoiceWords(choice, stage string, endsPhase bool) string {
	switch choice {
	case event.ChoiceKeepGoing:
		return "kept going: the rejection went back along its route, " + stage + "'s rejection budget restored"
	case event.ChoiceSkip:
		return "skipped " + stage + ": it completed with no output and the flow went on past it — its verdict was overruled"
	case event.ChoiceGiveUp:
		if endsPhase {
			return "gave up: that round ended"
		}
		return "gave up: the run was abandoned"
	}
	return choice
}

// historyBlock is the attempts in short: per stage, how many and how the
// latest ended — the references of every attempt follow from the count.
func historyBlock(j *journal) string {
	var b strings.Builder
	b.WriteString("\n## Attempts\n")
	if len(j.attempts) == 0 {
		b.WriteString("No stage has run yet.\n")
		return b.String()
	}
	b.WriteString("Each stage, how many attempts it has had and how the latest went, ending with the latest's reference.\n")
	var order []string
	last := map[string]*jAttempt{}
	count := map[string]int{}
	for _, a := range j.attempts {
		if count[a.stage] == 0 {
			order = append(order, a.stage)
		}
		count[a.stage]++
		last[a.stage] = a
	}
	for _, st := range order {
		a := last[st]
		if count[st] == 1 {
			fmt.Fprintf(&b, "- %s: 1 attempt; %s [%s]\n", st, attemptOutcome(a), a.ref)
			continue
		}
		first := j.attempts[slices.IndexFunc(j.attempts, func(x *jAttempt) bool { return x.stage == st })]
		fmt.Fprintf(&b, "- %s: %d attempts (%s to %s); the latest: %s [%s]\n", st, count[st], first.ref, a.ref, attemptOutcome(a), a.ref)
	}
	return b.String()
}

// attemptOutcome is how an attempt went, in a few words.
func attemptOutcome(a *jAttempt) string {
	var parts []string
	switch a.end {
	case "completed":
		parts = append(parts, "completed")
	case "skipped":
		if a.by != "" {
			parts = append(parts, "skipped by the owner")
		} else {
			parts = append(parts, "skipped")
		}
	case "failed":
		parts = append(parts, "failed ("+strings.ReplaceAll(a.why, "_", " ")+")")
	case "paused":
		parts = append(parts, "paused")
	default:
		parts = append(parts, "running now — not in the journal until it ends")
	}
	if a.verdict != nil {
		v := "REJECT"
		if *a.verdict {
			v = "APPROVE"
		}
		parts = append(parts, fmt.Sprintf("verdict %s, %d comment(s)", v, a.comments))
	}
	if a.routed != "" {
		parts = append(parts, "then "+a.routed)
	}
	return strings.Join(parts, "; ")
}

// quoteHead quotes body's lines, at most room bytes of it cut on a line; a
// cut body ends saying where the whole of it is.
func quoteHead(body string, room int, more string) string {
	body = strings.TrimRight(body, "\n")
	if len(body) <= room {
		return quoteLines(body)
	}
	return quoteLines(strings.TrimRight(cutAtLine(body, room), "\n")) + "> … (cut — " + more + " gives all of it)\n"
}

// cutAtLine keeps at most n bytes of s, ending on a whole line when there is
// one to end on.
func cutAtLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = strings.ToValidUTF8(s[:n], "")
	if i := strings.LastIndexByte(s, '\n'); i > 0 {
		return s[:i+1]
	}
	return s + "\n"
}

// --- reading it ---------------------------------------------------------------

// journalOf builds a run's journal from its log, its flow typing the
// outputs.
func (e *Engine) journalOf(ctx context.Context, runID string, fl *flow.Flow) (*journal, error) {
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return nil, err
	}
	j := buildJournal(evs, fl, func(pingID, r string) []answerFile {
		if e.attachments == nil {
			return nil
		}
		atts, err := e.attachments.ForPing(ctx, pingID)
		if err != nil {
			return nil
		}
		return answerFilesOf(atts, attachmentPaths(atts, func(string) string { return r }))
	})
	e.loadNotes(ctx, j)
	return j, nil
}

// loadNotes reads every attempt's journal_notes outputs from their
// snapshots, numbering each attempt's notes from 1: a note's reference
// depends on its own attempt alone, so another attempt's notes being dropped
// or captured again never moves it. An attempt whose notes cannot be read
// keeps why, and its record says so.
func (e *Engine) loadNotes(ctx context.Context, j *journal) {
	for _, ps := range j.attempts {
		k := 0
		for _, o := range ps.outputs {
			if o.typ != agentspec.TypeJournalNotes {
				continue
			}
			if e.snapshots == nil {
				ps.notesErr = "no longer kept"
				continue
			}
			data, _, err := e.snapshots.Bytes(ctx, o.id)
			if err != nil {
				ps.notesErr = e.snapLost(ctx, o, err)
				continue
			}
			var file struct {
				Notes []struct {
					Did   string   `yaml:"did"`
					Why   string   `yaml:"why"`
					Files []string `yaml:"files"`
				} `yaml:"notes"`
			}
			if err := yaml.Unmarshal(data, &file); err != nil {
				ps.notesErr = "not a notes file: " + err.Error()
				continue
			}
			for _, n := range file.Notes {
				k++
				note := &jNote{ref: ref.In(ps.ref, ref.Note, k), attempt: ps, did: n.Did, why: n.Why, files: n.Files}
				j.notes = append(j.notes, note)
				j.byNote[note.ref] = note
			}
		}
	}
}

// journalSection renders a run.journal input for the prompt being built.
func (x *exec) journalSection(name, desc string) string {
	j, err := x.e.journalOf(x.ctx, x.rs.RunID, x.rs.Flow)
	if err != nil {
		x.e.log.WarnContext(x.bg, "prompt: reading the run's journal", "run", x.rs.RunID, "err", err)
		return fmt.Sprintf("# RUN JOURNAL (input: %s%s)\n(the journal could not be read just now)\n", name, desc)
	}
	return renderLedger(j, name, desc)
}

// journalFilesDir is where the journal_read tool writes its files, in the
// primary checkout (workspace/shim/journal.go, journalDir).
const journalFilesDir = ".apothecary/journal"

// clearJournalFiles removes the files journal_read wrote for an earlier
// attempt, before a fresh one: each was the tool's answer at the time, and
// an agent that searches the folder instead of calling the tool must find
// nothing rather than another stage's stale copy. Like clearOutputs, never
// on re-entering an attempt — a resumed session may still be reading one —
// and best effort.
func (x *exec) clearJournalFiles() {
	if _, err := x.res.Envp.Exec(x.ctx, x.rs.EnvID, envport.Command{
		Script:  "rm -rf -- " + journalFilesDir,
		Dir:     event.PrimaryCheckoutDir,
		Timeout: time.Minute,
	}); err != nil {
		x.e.log.WarnContext(x.bg, "clearing the journal's files failed", logs.RunID, x.rs.RunID, "err", err)
	}
}

// --- the tool's entries -------------------------------------------------------

// JournalEntry is what journal_read gives for one reference: an entry to
// return inline, or — File set — a file for the tool to write under
// .apothecary/journal/ and point the agent at, and — for an attempt — the
// outputs that are not text, for the tool to fetch (JournalBeside) and write
// as themselves beside it.
type JournalEntry struct {
	// Ref is the entry's reference: an attempt (review#5), a decision or a
	// note in one (build#3/decision#1, build#3/note#2), run/decision#<k>, or
	// run for every record.
	Ref string `json:"ref"`
	// File is where to write Text, relative to the journal's folder: the
	// reference and .md (review#5.md, build#3/decision#1.md, run.md); empty
	// for an entry short enough to return inline.
	File string `json:"file,omitempty"`
	Text string `json:"text"`
	// Beside are an attempt's outputs that are not text and are still
	// kept, to write in the folder named after the attempt (review#5/).
	Beside []JournalFile `json:"beside,omitempty"`
}

// JournalFile is an output written beside its attempt's file.
type JournalFile struct {
	// Name is its file name in the attempt's folder: the stored name, a
	// second with the same name name-2.ext (besideFiles).
	Name string `json:"name"`
	Size int64  `json:"size"`
	Mime string `json:"mime,omitempty"`
}

// jBeside is an attempt's output that is not text, by the name it is
// written under.
type jBeside struct {
	name string
	snap jSnap
}

// besideFiles names an attempt's outputs that are not text, as the tool
// writes them beside the attempt's file: in output order, each by its stored
// file name (no #: filestore.SafeName), a second with the same name as
// name-2.ext. Every one is named whether or not its bytes are still kept,
// and the names depend on the log alone, so the attempt's file and the
// fetches that follow it always agree.
func besideFiles(a *jAttempt) []jBeside {
	taken := map[string]bool{}
	var out []jBeside
	for _, o := range a.outputs {
		if textual(o) {
			continue
		}
		name := o.filename
		if name == "" {
			name = path.Base(o.path)
		}
		out = append(out, jBeside{name: filestore.UniqueName(filestore.SafeName(name), taken), snap: o})
	}
	return out
}

// ErrJournalEntryNotFound: a well-formed reference the run has no entry for.
var ErrJournalEntryNotFound = errors.New("no such journal entry")

// journalInline bounds a decision or a note returned inline; a longer one
// is written to a file like an attempt, so it never lands whole in the
// agent's context (a note's did and why have no length limit).
const journalInline = 8 << 10

// journalFile is where an entry is written: its reference, as a path.
func journalFile(r string) string { return r + ".md" }

// inlineOrFile is an entry returned inline, or written to its file when it
// is longer than journalInline.
func inlineOrFile(r, text string) JournalEntry {
	if len(text) <= journalInline {
		return JournalEntry{Ref: r, Text: text}
	}
	return JournalEntry{Ref: r, File: journalFile(r), Text: text}
}

// JournalRead renders one journal entry of a run for the journal_read tool,
// by its reference: "" or run is every record of the run, an attempt that
// attempt whole (its record, its outputs, its log), a decision or a note
// that entry whole. A reference that is not one wraps ref.ErrMalformed; one
// the run has no entry for, ErrJournalEntryNotFound. The outputs are typed
// from the log, as the ledger's are; the run's flow is a fallback for what
// the log does not say (buildJournal), and one that cannot be loaded is
// logged and done without.
func (e *Engine) JournalRead(ctx context.Context, runID, reference string) (JournalEntry, error) {
	var r ref.Ref
	if reference != "" {
		var err error
		if r, err = ref.Parse(reference); err != nil {
			return JournalEntry{}, err
		}
	}
	fl, err := e.journalFlow(ctx, runID)
	if err != nil {
		e.log.WarnContext(ctx, "journal: loading the run's flow", logs.RunID, runID, "err", err)
	}
	j, err := e.journalOf(ctx, runID, fl)
	if err != nil {
		return JournalEntry{}, err
	}
	key := r.String()
	switch {
	case key == ref.Run:
		return JournalEntry{Ref: ref.Run, File: journalFile(ref.Run), Text: e.journalRecords(ctx, j)}, nil
	case j.byDec[key] != nil:
		return inlineOrFile(key, journalDecisionText(j.byDec[key])), nil
	case j.byNote[key] != nil:
		return inlineOrFile(key, noteText(j.byNote[key])), nil
	case r.Kind == "" && j.byAttempt[key] != nil:
		text, beside := e.journalAttemptFile(ctx, j, j.byAttempt[key])
		return JournalEntry{Ref: key, File: journalFile(key), Text: text, Beside: beside}, nil
	}
	what := map[string]string{"": "attempt", ref.Decision: "answered decision", ref.Note: "note"}[r.Kind]
	return JournalEntry{}, fmt.Errorf("%w: the run has no %s %s (the references in your RUN JOURNAL section are the ones it has)",
		ErrJournalEntryNotFound, what, key)
}

// JournalBeside finds an output written beside an attempt's file
// (JournalEntry.Beside) by its name: the snapshot that holds its bytes.
// ErrJournalEntryNotFound for a name the attempt has not; a reference that
// is not an attempt wraps ref.ErrMalformed.
func (e *Engine) JournalBeside(ctx context.Context, runID, reference, name string) (string, error) {
	r, err := ref.Parse(reference)
	if err == nil && (r.Kind != "" || r.IsRun()) {
		err = fmt.Errorf("%w: %q is not an attempt (<stage>#<n>): only an attempt has outputs", ref.ErrMalformed, reference)
	}
	if err != nil {
		return "", err
	}
	evs, err := e.store.Events(ctx, runID, 0)
	if err != nil {
		return "", err
	}
	if a := buildJournal(evs, nil, nil).byAttempt[r.String()]; a != nil {
		for _, b := range besideFiles(a) {
			if b.name == name {
				return b.snap.id, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %s has no output named %q", ErrJournalEntryNotFound, r, name)
}

// journalFlow is the run's flow, resolved with its parameters.
func (e *Engine) journalFlow(ctx context.Context, runID string) (*flow.Flow, error) {
	v, err := e.runs.View(ctx, runID)
	if err != nil {
		return nil, err
	}
	return e.RunFlow(ctx, v)
}

// journalDecisionText is one decision whole: nothing cut.
func journalDecisionText(d *jDecision) string {
	return strings.TrimPrefix(decisionBlock(d, math.MaxInt), "\n")
}

// attemptRecord is an attempt's record: when it ran, how it ended, its
// verdict and edge, the decisions made in or about it, what it left.
func attemptRecord(j *journal, ps *jAttempt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s — attempt %d of %s\n", ps.ref, ps.n, ps.stage)
	if !ps.at.IsZero() {
		fmt.Fprintf(&b, "Started: %s\n", ps.at.UTC().Format("2006-01-02 15:04 UTC"))
	}
	b.WriteString("Ended: " + attemptOutcome(ps) + "\n")
	if ps.end == "failed" && ps.err != "" {
		b.WriteString("Error:\n" + quoteLines(ps.err))
	}
	if ps.end == "skipped" && strings.TrimSpace(ps.note) != "" {
		b.WriteString("Note:\n" + quoteLines(ps.note))
	}
	if ps.turns > 0 {
		fmt.Fprintf(&b, "Turns: %d\n", ps.turns)
	}
	var decided []string
	for _, d := range j.decisions {
		if d.attempt == ps.ref {
			decided = append(decided, d.ref)
		}
	}
	if len(decided) > 0 {
		b.WriteString("Decisions in it: " + strings.Join(decided, ", ") + "\n")
	}
	var noted []string
	for _, n := range j.notes {
		if n.attempt == ps {
			noted = append(noted, n.ref)
		}
	}
	if len(noted) > 0 {
		b.WriteString("Its notes: " + strings.Join(noted, ", ") + "\n")
	}
	if ps.notesErr != "" {
		b.WriteString("Its notes could not be read: " + ps.notesErr + "\n")
	}
	for _, o := range ps.outputs {
		b.WriteString("Output: " + snapWords(o) + "\n")
	}
	if ps.log != nil {
		fmt.Fprintf(&b, "Log: %d bytes\n", ps.log.size)
	}
	return b.String()
}

// snapWords names an output: its port, its path, its type, its size.
func snapWords(s jSnap) string {
	w := s.name
	if w == "" {
		w = "(unnamed)"
	}
	if s.path != "" {
		w += " — " + s.path
	}
	if s.typ != "" {
		w += " (" + s.typ + ")"
	}
	w += fmt.Sprintf(", %d bytes", s.size)
	if s.truncated {
		w += ", captured truncated"
	}
	return w
}

// journalAttemptFile is everything an attempt left, in one file: its
// record, each output, then its full log. A text output is embedded; one
// that is not text is written as itself beside the file (besideFiles) — the
// file names where, and the list is what the tool fetches — or said no
// longer kept.
func (e *Engine) journalAttemptFile(ctx context.Context, j *journal, ps *jAttempt) (string, []JournalFile) {
	beside := map[string]string{} // snapshot → its name beside the file
	for _, f := range besideFiles(ps) {
		beside[f.snap.id] = f.name
	}
	var files []JournalFile
	var b strings.Builder
	fmt.Fprintf(&b, "# RUN JOURNAL — %s\n\n", ps.ref)
	b.WriteString(attemptRecord(j, ps))
	if ps.end == "" {
		b.WriteString("\nThis attempt is running now: what it leaves is in the journal once it ends.\n")
	}
	for _, o := range ps.outputs {
		fmt.Fprintf(&b, "\n## Output: %s\n\n", snapWords(o))
		name, ok := beside[o.id]
		if !ok {
			b.WriteString(e.snapBody(ctx, o))
			continue
		}
		if lost := e.snapKept(ctx, o); lost != "" {
			fmt.Fprintf(&b, "(not text — %s: %s)\n", o.mime, lost)
			continue
		}
		fmt.Fprintf(&b, "Not text (%s): written beside this file, as %s/%s/%s — open it with your own tools.\n", o.mime, journalFilesDir, ps.ref, name)
		files = append(files, JournalFile{Name: name, Size: o.size, Mime: o.mime})
	}
	if ps.log != nil {
		fmt.Fprintf(&b, "\n## Log (%d bytes)\n\n", ps.log.size)
		b.WriteString(e.snapBody(ctx, *ps.log))
	}
	return b.String(), files
}

// snapKept says why a snapshot's bytes cannot be had ("" when they can).
func (e *Engine) snapKept(ctx context.Context, s jSnap) string {
	if e.snapshots == nil {
		return "no longer kept"
	}
	f, _, err := e.snapshots.Open(ctx, s.id)
	if err != nil {
		return e.snapLost(ctx, s, err)
	}
	_ = f.Close()
	return ""
}

// journalRecords is every record of the run in one file: every answered
// ping whole — the ones the ledger leaves out too, so every reference an
// attempt's record names is here — every note, then every attempt's record
// with its verdict: no logs, no other output.
func (e *Engine) journalRecords(ctx context.Context, j *journal) string {
	var b strings.Builder
	b.WriteString("# RUN JOURNAL — every record\n\n## Decisions\n")
	if len(j.decisions) == 0 {
		b.WriteString("None.\n")
	}
	for _, d := range j.decisions {
		b.WriteString("\n" + journalDecisionText(d))
	}
	b.WriteString("\n## Agent notes\nWhat agents said they did differently from their direction — their claims, to check against the work.\n")
	if len(j.notes) == 0 {
		b.WriteString("None.\n")
	}
	for _, n := range j.notes {
		b.WriteString("\n" + noteText(n))
	}
	b.WriteString("\n## Attempts\n")
	if len(j.attempts) == 0 {
		b.WriteString("None.\n")
	}
	for _, ps := range j.attempts {
		b.WriteString("\n" + attemptRecord(j, ps))
		for _, o := range ps.outputs {
			if o.typ == verdictType {
				b.WriteString("Verdict:\n" + quoteLines(strings.TrimRight(e.snapBody(ctx, o), "\n")))
			}
		}
	}
	return b.String()
}

// snapBody is a snapshot's content as the journal embeds it: text whole, a
// binary named, a dropped one said so.
func (e *Engine) snapBody(ctx context.Context, s jSnap) string {
	if !textual(s) {
		return fmt.Sprintf("(not text — %s: listed, not embedded)\n", s.mime)
	}
	if e.snapshots == nil {
		return "(no longer kept)\n"
	}
	data, _, err := e.snapshots.Bytes(ctx, s.id)
	switch {
	case err != nil:
		return "(" + e.snapLost(ctx, s, err) + ")\n"
	case !isText(data):
		return "(not text: listed, not embedded)\n"
	}
	out := string(data)
	// A truncated capture says so itself, where its kept tail starts.
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

// snapLost says why a snapshot could not be read: swept by retention, or a
// store that failed (logged), never taken for one another.
func (e *Engine) snapLost(ctx context.Context, s jSnap, err error) string {
	if errors.Is(err, filestore.ErrNotFound) || errors.Is(err, filestore.ErrContentGone) {
		return "no longer kept"
	}
	e.log.WarnContext(ctx, "journal: reading a snapshot", "snapshot", s.id, "err", err)
	return "could not be read just now"
}

// isText reports whether a snapshot's bytes read as text: valid UTF-8 with
// no NUL byte. An image, an archive or a binary is listed, never embedded.
func isText(data []byte) bool {
	return utf8.Valid(data) && !slices.Contains(data, 0)
}

// textual reports whether a snapshot is worth fetching to embed: not an
// image, audio, video or other declared binary.
func textual(s jSnap) bool {
	for _, p := range []string{"image/", "audio/", "video/", "font/"} {
		if strings.HasPrefix(s.mime, p) {
			return false
		}
	}
	return s.mime != "application/octet-stream" && s.mime != "application/zip" && s.mime != "application/pdf"
}

// verdictType is the port type whose snapshot an attempt's verdict is.
const verdictType = agentspec.TypeVerdict
