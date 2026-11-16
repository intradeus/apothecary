package engine

// The run journal (FLOWLANG §4 run.journal): what has happened in a run, as
// its agents read it, so none repeats a mistake or reopens a correction the
// task's owner discarded. It is a view, never a file: buildJournal reads the
// event log, and an entry's bodies — a pass's outputs and log — are read
// from the snapshots captured as each attempt ended, when the entry is
// rendered. Two renderings share the one model, so they cannot disagree:
//
//   - the ledger (renderLedger), pushed into the prompt of a stage that binds
//     run.journal: every owner decision, the agents' notes as claims, a short
//     history — each line ending with its id;
//   - the tool's files (journalPassFile, journalRecords) and inline entries
//     (journalDecisionText), behind the journal routes the shim's
//     journal_read calls.
//
// Ids come from the log, so they are stable across restarts: a pass is its
// stage and pass number (review-5, as its log file is named), a decision is
// decision-<n> in the order the decisions were made.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
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
)

// JournalTool is the shim's tool that reads the journal
// (workspace/shim/journal.go): the name the ledger tells agents to call.
const JournalTool = "journal_read"

const (
	// journalHead bounds a long body in the ledger — a verdict, an answer, a
	// question: its head is shown, and its id says where the rest is.
	journalHead = 1500
	// journalNotesListed bounds the agent notes the ledger lists one line
	// each; the rest are counted, with the call that reads them.
	journalNotesListed = 30
	// journalNoteLine bounds one listed note.
	journalNoteLine = 300
)

// journal is a run's record, built from its log (buildJournal).
type journal struct {
	decisions []*jDecision
	passes    []*jPass
	byPass    map[string]*jPass
	byDec     map[string]*jDecision
	// notes are the agents' notes (journal_notes outputs), in pass order,
	// filled from the snapshots by loadNotes.
	notes  []*jNote
	byNote map[string]*jNote
	// chainRun numbers the run's chain runs (phase_started) in log order;
	// the latest is the one in progress.
	chainRun int
}

// jPass is one pass of a stage: an attempt, from its stage_started.
type jPass struct {
	id    string
	stage string
	n     int
	// chainRun is the chain run the pass belongs to (journal.chainRun).
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
	// verdict is the pass's verdict, when it rendered one.
	verdict  *bool
	comments int
	// routed is where its edge sent the run: "on_reject → build (2/5)".
	routed  string
	outputs []jSnap
	log     *jSnap
	// notesErr says why its notes could not be read, when they could not.
	notesErr string
}

// jNote is one note an agent left for the journal: what it did differently
// from its direction, on its own — its claim, never the owner's direction.
type jNote struct {
	id    string // note-<n>, in the order the passes left them
	pass  *jPass
	did   string
	why   string
	files []string
}

// jSnap is one snapshot a pass left: an output (its port's name and type)
// or its log.
type jSnap struct {
	name, typ, path, id, mime string
	size                      int64
	truncated                 bool
}

// jDecision is one owner decision: an answered ping that says something
// about the work (journalDecides).
type jDecision struct {
	id   string
	ping run.PingView
	// pass is the pass that asked — or, for a gate, a debug leaf, a manual
	// stage or the impasse, the stage's pass the decision is about.
	pass string
	// files are the paths the answer's attachments were delivered to.
	files []answerFile
	// edits, on a debug continue: which files the person changed there.
	edits *event.HandEdits
	// unwritten, on a skip at the impasse: the outputs the stage declares,
	// which the skip left unwritten — what a later stage will not find.
	unwritten []string
}

// passID names a pass: its stage and its number.
func passID(stage string, n int) string { return stage + "-" + strconv.Itoa(n) }

// splitPassID reads a pass id back: the stage is everything before the last
// dash, so a stage id with dashes (fix-ci-2 is pass 2 of fix-ci) reads right.
func splitPassID(id string) (string, int, bool) {
	i := strings.LastIndexByte(id, '-')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n < 1 {
		return "", 0, false
	}
	return id[:i], n, true
}

// buildJournal reads a run's log into its journal. An output's type is the
// one its snapshot_captured records; fl types an output whose event carries
// none, and names what a skipped stage did not write (nil: neither). atts
// finds what an answer attached (nil: none).
func buildJournal(evs []event.Event, fl *flow.Flow, atts func(pingID string) []answerFile) *journal {
	j := &journal{byPass: map[string]*jPass{}, byDec: map[string]*jDecision{}, byNote: map[string]*jNote{}}
	latest := map[string]*jPass{} // stage → its latest pass
	asked := map[string]string{}  // ping → the pass it was raised in
	edits := map[string]*event.HandEdits{}
	for _, e := range evs {
		switch p := e.Payload.(type) {
		case *event.PhaseStarted:
			j.chainRun++
		case *event.StageStarted:
			id := passID(p.StageID, p.Iteration)
			ps := j.byPass[id]
			if ps == nil {
				ps = &jPass{id: id, stage: p.StageID, n: p.Iteration, chainRun: j.chainRun, at: e.TS}
				j.byPass[id] = ps
				j.passes = append(j.passes, ps)
			}
			ps.end, ps.why, ps.err = "", "", ""
			latest[p.StageID] = ps
		case *event.StageCompleted:
			if ps := latest[p.StageID]; ps != nil {
				ps.end, ps.turns = "completed", p.TurnsUsed
				if p.Skipped {
					ps.end, ps.note, ps.by = "skipped", p.Note, p.By
				}
			}
		case *event.StageFailed:
			if ps := latest[p.StageID]; ps != nil {
				ps.end, ps.why, ps.err, ps.turns = "failed", p.Reason, p.Error, p.TurnsUsed
			}
		case *event.StagePaused:
			if ps := latest[p.StageID]; ps != nil {
				ps.end = "paused"
			}
		case *event.StageResumed:
			if ps := latest[p.StageID]; ps != nil {
				ps.end = ""
			}
		case *event.VerdictReceived:
			if ps := latest[p.StageID]; ps != nil {
				approve := p.Approve
				ps.verdict, ps.comments = &approve, p.NComments
			}
		case *event.EdgeTaken:
			if ps := latest[p.From]; ps != nil {
				ps.routed = edgeWords(p)
			}
		case *event.SnapshotCaptured:
			ps := j.byPass[passID(p.StageID, p.Iteration)]
			if ps == nil || (p.Kind != string(filestore.KindOutput) && p.Kind != string(filestore.KindLog)) {
				continue
			}
			s := jSnap{name: p.Name, path: p.Path, id: p.SnapshotID, mime: p.Mime, size: p.Size, truncated: p.Truncated}
			if p.Kind == string(filestore.KindLog) {
				ps.log = &s
				continue
			}
			// The type the attempt ran with, recorded on the event; the flow
			// only for an event that does not carry it.
			if s.typ = p.PortType; s.typ == "" {
				s.typ = outputType(fl, p.StageID, p.Name)
			}
			ps.outputs = append(ps.outputs, s)
		case *event.PingRaised:
			if ps := latest[p.StageID]; ps != nil {
				asked[p.PingID] = ps.id
			}
		case *event.HandEdits:
			edits[p.PingID] = p // a continue recorded again after a crash: the last stands
		}
	}
	for _, p := range answeredInOrder(evs) {
		if !journalDecides(p) {
			continue
		}
		d := &jDecision{id: "decision-" + strconv.Itoa(len(j.decisions)+1), ping: p, pass: asked[p.PingID], edits: edits[p.PingID]}
		if p.Type == event.PingImpasse && p.Answer == event.ChoiceSkip {
			d.unwritten = declaredOutputs(fl, p.StageID)
		}
		if atts != nil && len(p.AnswerAttachmentIDs) > 0 {
			d.files = atts(p.PingID)
		}
		j.decisions = append(j.decisions, d)
		j.byDec[d.id] = d
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
// decision in full (a long body by its head and its id), the agents' notes
// as claims to check, a short history, and how to read more. Never empty: a
// prompt that names the input never meets a silence it has to interpret.
func renderLedger(j *journal, name, desc string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# RUN JOURNAL (input: %s%s)\n", name, desc)
	b.WriteString("The engine's record of this run so far. Before you ask for a change or redo work, check it against this section: " +
		"what the task's owner decided stands, and a correction they discarded stays discarded. Each entry ends with its id [in brackets]; " +
		"the last part of this section says how to read more.\n")

	b.WriteString("\n## Owner decisions\n")
	if len(j.decisions) == 0 {
		b.WriteString("None yet: the task's owner has decided nothing during this run.\n")
	} else {
		fmt.Fprintf(&b, "The %d decision(s) the task's owner made during this run, oldest first. They are the owner's direction: "+
			"where one differs from the plan or another input, it stands, and a later one stands over an earlier one. "+
			"Work done to follow one is not a deviation from the plan.\n", len(j.decisions))
		for _, d := range j.decisions {
			b.WriteString(decisionBlock(d, journalHead))
		}
	}

	b.WriteString(notesBlock(j))
	b.WriteString(historyBlock(j))

	fmt.Fprintf(&b, "\n## Reading more\n"+
		"Call the %[1]s tool (your harness may list it with a prefix, such as ask_human_%[1]s). "+
		"%[1]s(id: \"<pass id>\") writes everything a pass left — its record, its verdict, its outputs and its full log — to one file and gives you its path; "+
		"%[1]s(id: \"decision-<n>\") gives a decision in full, %[1]s(id: \"note-<n>\") a note; %[1]s() with no id writes one file holding every record of the run "+
		"(decisions, notes, each pass and its verdict; no logs). Read the files with your own tools.\n", JournalTool)
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
		if n.pass.chainRun == j.chainRun {
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
		b.WriteString("- " + noteLine(n) + " [" + n.id + "]\n")
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

// noteLine is a note on one line, cut to journalNoteLine: its pass, what was
// done, why, the files.
func noteLine(n *jNote) string {
	line := n.pass.id + ": " + oneLine(n.did) + " — why: " + oneLine(n.why)
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
	fmt.Fprintf(&b, "### note from %s [%s]\nDid:\n%sWhy:\n%s", n.pass.id, n.id, quoteLines(n.did), quoteLines(n.why))
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
	if d.pass != "" {
		b.WriteString(" (" + d.pass + ")")
	}
	if p.AnsweredAt != nil {
		b.WriteString(" — " + p.AnsweredAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	b.WriteString(" [" + d.id + "]\n")
	more := fmt.Sprintf("%s(id: %q)", JournalTool, d.id)
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

// historyBlock is the passes in short: per stage, how many and how the
// latest ended — the ids of every pass follow from the count.
func historyBlock(j *journal) string {
	var b strings.Builder
	b.WriteString("\n## Passes\n")
	if len(j.passes) == 0 {
		b.WriteString("No stage has run yet.\n")
		return b.String()
	}
	b.WriteString("Each stage, how many passes it has had and how the latest went. A pass's id is its stage and number: " +
		"build with 3 passes has build-1 to build-3.\n")
	var order []string
	last := map[string]*jPass{}
	count := map[string]int{}
	for _, ps := range j.passes {
		if count[ps.stage] == 0 {
			order = append(order, ps.stage)
		}
		count[ps.stage]++
		last[ps.stage] = ps
	}
	for _, st := range order {
		ps := last[st]
		fmt.Fprintf(&b, "- %s: %d pass(es); the latest, %s: %s\n", st, count[st], ps.id, passOutcome(ps))
	}
	return b.String()
}

// passOutcome is how a pass went, in a few words.
func passOutcome(ps *jPass) string {
	var parts []string
	switch ps.end {
	case "completed":
		parts = append(parts, "completed")
	case "skipped":
		if ps.by != "" {
			parts = append(parts, "skipped by the owner")
		} else {
			parts = append(parts, "skipped")
		}
	case "failed":
		parts = append(parts, "failed ("+strings.ReplaceAll(ps.why, "_", " ")+")")
	case "paused":
		parts = append(parts, "paused")
	default:
		parts = append(parts, "running now — not in the journal until it ends")
	}
	if ps.verdict != nil {
		v := "REJECT"
		if *ps.verdict {
			v = "APPROVE"
		}
		parts = append(parts, fmt.Sprintf("verdict %s, %d comment(s)", v, ps.comments))
	}
	if ps.routed != "" {
		parts = append(parts, "then "+ps.routed)
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
	j := buildJournal(evs, fl, func(pingID string) []answerFile {
		if e.attachments == nil {
			return nil
		}
		atts, err := e.attachments.ForPing(ctx, pingID)
		if err != nil {
			return nil
		}
		return answerFilesOf(atts, attachmentPaths(atts))
	})
	e.loadNotes(ctx, j)
	return j, nil
}

// loadNotes reads every pass's journal_notes outputs from their snapshots,
// numbering the notes in pass order. A pass whose notes cannot be read keeps
// why, and its record says so.
func (e *Engine) loadNotes(ctx context.Context, j *journal) {
	for _, ps := range j.passes {
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
				note := &jNote{id: "note-" + strconv.Itoa(len(j.notes)+1), pass: ps, did: n.Did, why: n.Why, files: n.Files}
				j.notes = append(j.notes, note)
				j.byNote[note.id] = note
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

// JournalEntry is what journal_read gives for one id: an entry to return
// inline, or — File set — a file for the tool to write under
// .apothecary/journal/ and point the agent at.
type JournalEntry struct {
	// ID is the entry: a pass id, decision-<n>, note-<n>, or "records" for
	// every record.
	ID string `json:"id"`
	// File is the name to write Text under ("review-5.md"); empty for an
	// entry short enough to return inline.
	File string `json:"file,omitempty"`
	Text string `json:"text"`
}

// ErrJournalEntryNotFound: the id names no pass and no decision of the run.
var ErrJournalEntryNotFound = errors.New("no such journal entry")

// journalInline bounds a decision or a note returned inline; a longer one
// is written to a file like a pass, so it never lands whole in the agent's
// context (a note's did and why have no length limit).
const journalInline = 8 << 10

// inlineOrFile is an entry returned inline, or written to its file when it
// is longer than journalInline.
func inlineOrFile(id, text string) JournalEntry {
	if len(text) <= journalInline {
		return JournalEntry{ID: id, Text: text}
	}
	return JournalEntry{ID: id, File: id + ".md", Text: text}
}

// JournalRead renders one journal entry of a run for the journal_read tool:
// id "" is every record of the run, a pass id that pass whole (its record,
// its outputs, its log), decision-<n> that decision whole. The outputs are
// typed from the log, as the ledger's are; the run's flow is a fallback for
// what the log does not say (buildJournal), and one that cannot be loaded
// is logged and done without.
func (e *Engine) JournalRead(ctx context.Context, runID, id string) (JournalEntry, error) {
	fl, err := e.journalFlow(ctx, runID)
	if err != nil {
		e.log.WarnContext(ctx, "journal: loading the run's flow", logs.RunID, runID, "err", err)
	}
	j, err := e.journalOf(ctx, runID, fl)
	if err != nil {
		return JournalEntry{}, err
	}
	switch {
	case id == "":
		return JournalEntry{ID: "records", File: "records.md", Text: e.journalRecords(ctx, j)}, nil
	case j.byDec[id] != nil:
		return inlineOrFile(id, journalDecisionText(j.byDec[id])), nil
	case j.byNote[id] != nil:
		return inlineOrFile(id, noteText(j.byNote[id])), nil
	case j.byPass[id] != nil:
		return JournalEntry{ID: id, File: id + ".md", Text: e.journalPassFile(ctx, j, j.byPass[id])}, nil
	}
	return JournalEntry{}, fmt.Errorf("%w: %q (a pass is <stage>-<n>, a decision decision-<n>, a note note-<n>)", ErrJournalEntryNotFound, id)
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

// passRecord is a pass's record: when it ran, how it ended, its verdict and
// edge, the decisions made about it, what it left.
func passRecord(j *journal, ps *jPass) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s — pass %d of %s\n", ps.id, ps.n, ps.stage)
	if !ps.at.IsZero() {
		fmt.Fprintf(&b, "Started: %s\n", ps.at.UTC().Format("2006-01-02 15:04 UTC"))
	}
	b.WriteString("Ended: " + passOutcome(ps) + "\n")
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
		if d.pass == ps.id {
			decided = append(decided, d.id)
		}
	}
	if len(decided) > 0 {
		b.WriteString("Decisions in it: " + strings.Join(decided, ", ") + "\n")
	}
	var noted []string
	for _, n := range j.notes {
		if n.pass == ps {
			noted = append(noted, n.id)
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

// journalPassFile is everything a pass left, in one file: its record, each
// output (text embedded, anything else listed), then its full log.
func (e *Engine) journalPassFile(ctx context.Context, j *journal, ps *jPass) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# RUN JOURNAL — pass %s\n\n", ps.id)
	b.WriteString(passRecord(j, ps))
	if ps.end == "" {
		b.WriteString("\nThis pass is running now: what it leaves is in the journal once it ends.\n")
	}
	for _, o := range ps.outputs {
		fmt.Fprintf(&b, "\n## Output: %s\n\n", snapWords(o))
		b.WriteString(e.snapBody(ctx, o))
	}
	if ps.log != nil {
		fmt.Fprintf(&b, "\n## Log (%d bytes)\n\n", ps.log.size)
		b.WriteString(e.snapBody(ctx, *ps.log))
	}
	return b.String()
}

// journalRecords is every record of the run in one file: every decision
// whole, then every pass's record with its verdict — no logs, no other
// output.
func (e *Engine) journalRecords(ctx context.Context, j *journal) string {
	var b strings.Builder
	b.WriteString("# RUN JOURNAL — every record\n\n## Owner decisions\n")
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
	b.WriteString("\n## Passes\n")
	if len(j.passes) == 0 {
		b.WriteString("None.\n")
	}
	for _, ps := range j.passes {
		b.WriteString("\n" + passRecord(j, ps))
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

// verdictType is the port type whose snapshot a pass's verdict is.
const verdictType = agentspec.TypeVerdict
