package engine

// Review rounds (PLAN6 M31): load the subject merge request's pending
// threads for the fixing agent, validate the replier's answers when its
// stage ends, and post them — each carrying a marker, so a crash between
// two posts is reconciled from the forge and nothing is posted twice.

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
)

// reviewReply is one entry of a review_replies output.
type reviewReply struct {
	Thread  string `yaml:"thread" json:"thread"`
	Reply   string `yaml:"reply" json:"reply"`
	Resolve bool   `yaml:"resolve" json:"resolve,omitempty"`
}

// parseReviewReplies enforces the review_replies contract against the
// threads the round loaded: a list of {thread, reply, resolve}, every thread
// one of them and named once, every reply non-empty. An empty list is valid.
func parseReviewReplies(data []byte, loaded []string) ([]reviewReply, error) {
	var out []reviewReply
	if strings.TrimSpace(string(data)) != "" {
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&out); err != nil {
			return nil, fmt.Errorf("replies: not a list of {thread, reply, resolve}: %v", err)
		}
	}
	seen := map[string]bool{}
	for i, r := range out {
		switch {
		case r.Thread == "":
			return nil, fmt.Errorf("replies[%d]: thread is required", i)
		case !slices.Contains(loaded, r.Thread):
			return nil, fmt.Errorf("replies[%d]: thread %q is not one of this round's threads (%s)", i, r.Thread, strings.Join(loaded, ", "))
		case seen[r.Thread]:
			return nil, fmt.Errorf("replies[%d]: thread %q is answered twice", i, r.Thread)
		case strings.TrimSpace(r.Reply) == "":
			return nil, fmt.Errorf("replies[%d]: reply to %q is empty", i, r.Thread)
		}
		seen[r.Thread] = true
	}
	return out, nil
}

// checkReviewReplies validates an agent stage's review_replies output when
// the stage ends; "" = fine (or none declared).
func (x *exec) checkReviewReplies(st *flow.Stage) string {
	p := flow.ReviewRepliesPath(st, x.rs.fl.Agent(st.Agent))
	if p == "" {
		return ""
	}
	data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, p)
	if err == nil {
		_, err = parseReviewReplies(data, x.rs.roundThreads)
	}
	if err != nil {
		return p + ": " + err.Error()
	}
	return ""
}

// errNoSubject: a subject action in a phase run that acts on no merge
// request — routable (on_fail), unlike a forge that cannot be reached.
var errNoSubject = errors.New("no merge request subject")

// noSubjectMsg explains a subject action's failure in a subject-less phase
// run: the phase was opened for no merge request (started by hand with none
// open, or opened by run_start), so there is nothing for it to act on.
func noSubjectMsg(st *flow.Stage) string {
	return fmt.Sprintf("%s: %s acts on the merge request a phase is opened for, and this phase run has none — guard the stage with `when: subject`, or start the phase on a merge request", errNoSubject, st.Action)
}

// subjectForge is the forge and merge request a subject action acts on.
func (x *exec) subjectForge(st *flow.Stage) (ports.Forge, *mrRecord, error) {
	mr := x.subjectMR()
	if mr == nil {
		return nil, nil, fmt.Errorf("%w: %s", errNoSubject, noSubjectMsg(st))
	}
	forge := x.res.forgeFor(mr.Repo)
	if forge == nil {
		return nil, nil, fmt.Errorf("stage %s: no integration reaches repository %s", st.ID, mr.Repo)
	}
	return forge, mr, nil
}

// actionLoadReviewComments writes the round's threads for the fixing agent.
// With select: human a person first picks which pending threads to fix (an
// inbox question; the run parks until it is answered); the rest are skipped
// until someone comments on them again. The first entry picks the set; a
// re-entry rewrites the same set, so one round fixes what it started with.
func (x *exec) actionLoadReviewComments(st *flow.Stage) (bool, error) {
	rs := x.rs
	forge, mr, err := x.subjectForge(st)
	if errors.Is(err, errNoSubject) {
		return x.actionFail(st, event.ReasonInputMissing, noSubjectMsg(st))
	}
	if err != nil {
		return x.infraFail(st, err.Error())
	}
	threads, err := forge.MRThreads(x.ctx, mr.Ref)
	if err != nil {
		return x.infraFail(st, "listing review threads: "+err.Error())
	}
	var round []ports.ReviewThread
	if rs.roundLoaded {
		for _, t := range threads {
			if slices.Contains(rs.roundThreads, t.ID) {
				round = append(round, t)
			}
		}
	} else {
		pending := x.unskipped(pendingThreads(threads))
		chosen := pending
		if mode := inputOr(st, "select", actionspec.SelectHuman); mode == actionspec.SelectHuman && len(pending) > 0 {
			picked, waiting, err := x.selectThreads(st, mr, pending)
			if err != nil || waiting {
				return true, err
			}
			chosen = picked
		}
		ids := make([]string, 0, len(chosen))
		for _, t := range chosen {
			ids = append(ids, t.ID)
		}
		skipped := map[string]string{}
		for _, t := range pending {
			if !slices.Contains(ids, t.ID) {
				skipped[t.ID] = t.Comments[len(t.Comments)-1].ID
			}
		}
		newest := ""
		if c, ok := newestPending(chosen); ok {
			newest = c.ID
		}
		if err := x.append(&event.ReviewCommentsLoaded{StageID: st.ID, Subject: mr.Repo, ThreadIDs: ids, Newest: newest, Skipped: skipped}); err != nil {
			return true, err
		}
		rs.roundThreads, rs.roundLoaded = ids, true
		for id, last := range skipped {
			rs.skipped[id] = last
		}
		round = chosen
	}
	path := "artifacts/review.md"
	if act := actionspec.Get(st.Action); act != nil && len(act.Outputs) > 0 {
		path = act.Outputs[0].Path
	}
	if err := x.res.Envp.WriteFile(x.ctx, rs.envID, path, []byte(reviewMarkdown(mr, round)), 0o644); err != nil {
		return x.infraFail(st, "writing "+path+": "+err.Error())
	}
	if js, err := json.MarshalIndent(round, "", "  "); err == nil {
		_ = x.res.Envp.WriteFile(x.ctx, rs.envID, strings.TrimSuffix(path, ".md")+".json", js, 0o644)
	}
	return false, nil
}

// inputOr is a stage's literal input, or def when unbound.
func inputOr(st *flow.Stage, name, def string) string {
	if in := st.Input(name); in != nil && in.Value != "" {
		return in.Value
	}
	return def
}

// unskipped drops the threads a human skipped in an earlier round, unless
// someone has written there since.
func (x *exec) unskipped(threads []ports.ReviewThread) []ports.ReviewThread {
	var out []ports.ReviewThread
	for _, t := range threads {
		if last, ok := x.rs.skipped[t.ID]; ok && len(t.Comments) > 0 && t.Comments[len(t.Comments)-1].ID == last {
			continue
		}
		out = append(out, t)
	}
	return out
}

// threadOption is how a thread is offered for selection: its id to answer
// with, where it is and who started it as the label, its first words as
// the detail.
func threadOption(t ports.ReviewThread) event.Option {
	where := "conversation"
	if t.Path != "" {
		where = t.Path
		if t.Line > 0 {
			where += fmt.Sprintf(":%d", t.Line)
		}
	}
	first := t.Comments[0]
	text := strings.Join(strings.Fields(stripMarker(first.Body)), " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return event.Option{ID: t.ID, Label: where + " — " + first.Author, Detail: text}
}

// selectThreads asks a human which pending threads this round fixes. It
// raises the question once per round and parks the run until it is
// answered (waiting=true); the answer is the chosen threads, possibly none.
func (x *exec) selectThreads(st *flow.Stage, mr *mrRecord, pending []ports.ReviewThread) (picked []ports.ReviewThread, waiting bool, err error) {
	rs := x.rs
	if rs.selectPing == nil {
		opts := make([]event.Option, len(pending))
		for i, t := range pending {
			opts[i] = threadOption(t)
		}
		q := fmt.Sprintf("Which review comments on %s should be fixed? Uncheck the ones that are wrong, already handled, or not worth changing — they are left alone until someone comments on them again. Selecting none ends this round.", mr.Ref.URL)
		p := &Ping{ID: x.e.newID(), RunID: rs.runID, Type: event.PingSelect, Question: q, Link: mr.Ref.URL,
			StageID: st.ID, Options: opts, AnswerKind: event.AnswerMulti, RaisedAt: x.e.clock(), done: make(chan struct{})}
		x.e.mu.Lock()
		x.e.pings[p.ID] = p
		x.e.mu.Unlock()
		raised := &event.PingRaised{PingID: p.ID, Type: p.Type, Question: q, Link: p.Link, StageID: st.ID, Options: opts, AnswerKind: event.AnswerMulti}
		if err := x.append(raised); err != nil {
			return nil, true, err
		}
		rs.selectPing = raised
		x.e.notify(x.bg, ports.Notification{RunID: rs.runID, PingID: p.ID, Type: p.Type, Title: q, Body: p.Link})
		return nil, true, errWaitingOnHuman
	}
	ans := rs.answeredPings[rs.selectPing.PingID]
	if ans == nil {
		return nil, true, errWaitingOnHuman
	}
	for _, id := range selectedOptions(ans.Answer) {
		for _, t := range pending {
			if t.ID == id {
				picked = append(picked, t)
			}
		}
	}
	return picked, false, nil
}

// emptyRound reports a review round that loaded nothing to fix (a human
// selected none, or nothing was pending): the phase ends right after the
// load instead of spending agent turns on nothing.
func (x *exec) emptyRound(st *flow.Stage) bool {
	return st.Type == flow.StageAction && st.Action == actionspec.LoadReviewComments &&
		x.rs.roundLoaded && len(x.rs.roundThreads) == 0
}

// reviewMarkdown renders a round for the agent: one section per thread.
func reviewMarkdown(mr *mrRecord, round []ports.ReviewThread) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review comments on %s\n\n", mr.Ref.URL)
	if len(round) == 0 {
		b.WriteString("No review thread is waiting on a reply.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%d thread(s) waiting. Address every one; each reply names its thread id.\n", len(round))
	for _, t := range round {
		fmt.Fprintf(&b, "\n## Thread `%s`", t.ID)
		if t.Path != "" {
			fmt.Fprintf(&b, " — %s", t.Path)
			if t.Line > 0 {
				fmt.Fprintf(&b, ":%d", t.Line)
			}
		}
		b.WriteString("\n")
		if t.DiffHunk != "" {
			fmt.Fprintf(&b, "\n```diff\n%s\n```\n", strings.TrimRight(t.DiffHunk, "\n"))
		}
		for _, c := range t.Comments {
			who := c.Author
			if ports.IsFactoryReply(c.Body) {
				who += " (the factory, earlier)"
			}
			fmt.Fprintf(&b, "\n**%s** (%s):\n\n%s\n", who, c.CreatedAt.UTC().Format("2006-01-02 15:04"), quoteBlock(stripMarker(c.Body)))
		}
	}
	return b.String()
}

func stripMarker(body string) string {
	if i := strings.Index(body, "<!-- apothecary run="); i >= 0 {
		return strings.TrimSpace(body[:i])
	}
	return body
}

func quoteBlock(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// actionReplyReviewComments posts the round's replies. Intent first; on a
// re-entry after a crash, threads that already carry this run's reply are
// skipped — a duplicate reply on a reviewer's thread is worse than a retry.
func (x *exec) actionReplyReviewComments(st *flow.Stage) (bool, error) {
	rs := x.rs
	forge, mr, err := x.subjectForge(st)
	if errors.Is(err, errNoSubject) {
		return x.actionFail(st, event.ReasonInputMissing, noSubjectMsg(st))
	}
	if err != nil {
		return x.infraFail(st, err.Error())
	}
	path := ""
	if in := st.Input("replies"); in != nil {
		path = in.Value
	}
	data, err := x.res.Envp.ReadFile(x.ctx, rs.envID, path)
	if err != nil {
		return x.infraFail(st, fmt.Sprintf("replies %s not readable: %v", path, err))
	}
	replies, err := parseReviewReplies(data, rs.roundThreads)
	if err != nil {
		return x.infraFail(st, err.Error())
	}
	done := map[string]bool{}
	if rs.actionPending[st.ID] {
		threads, err := forge.MRThreads(x.ctx, mr.Ref)
		if err != nil {
			return x.infraFail(st, "cannot tell which replies were already posted: "+err.Error())
		}
		for _, t := range threads {
			for _, c := range t.Comments {
				if strings.Contains(c.Body, ports.ReplyMarker(x.roundID(), t.ID)) {
					done[t.ID] = true
				}
			}
		}
	} else {
		if err := x.append(&event.ActionStarted{StageID: st.ID, Action: st.Action}); err != nil {
			return true, err
		}
		rs.actionPending[st.ID] = true
	}
	var posted, resolved []string
	for _, r := range replies {
		if !done[r.Thread] {
			body := strings.TrimSpace(r.Reply) + "\n\n" + ports.ReplyMarker(x.roundID(), r.Thread)
			if err := forge.ReplyThread(x.ctx, mr.Ref, r.Thread, body); err != nil {
				return x.infraFail(st, fmt.Sprintf("replying to thread %s: %v", r.Thread, err))
			}
		}
		posted = append(posted, r.Thread)
		if r.Resolve {
			if err := forge.ResolveThread(x.ctx, mr.Ref, r.Thread); err != nil {
				return x.infraFail(st, fmt.Sprintf("resolving thread %s: %v", r.Thread, err))
			}
			resolved = append(resolved, r.Thread)
		}
	}
	if err := x.append(&event.ReviewRepliesPosted{StageID: st.ID, Subject: mr.Repo, ThreadIDs: posted, Resolved: resolved}); err != nil {
		return true, err
	}
	delete(rs.actionPending, st.ID)
	return false, nil
}

// roundID names this phase run in reply markers — "<run>/<phase>-<n>" —
// so reconciling after a crash recognises this round's replies only, never
// an earlier round's answer on the same thread.
func (x *exec) roundID() string {
	rs := x.rs
	return fmt.Sprintf("%s/%s-%d", rs.runID, rs.phase, rs.phaseRuns[phaseRunKey(rs.phase, rs.subject)])
}
