package engine

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"apothecary/internal/agentspec"
	"apothecary/internal/bootstrap"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
	"apothecary/internal/shell"
)

// answerReply is what a paused stage resumes its agent with: the questions it
// stopped on, now answered. It reuses the prompt's own section grammar because
// that is what the agent was trained on in its first message — the task, the
// inputs and the output contract are all still in this session's context, so
// this message carries the new facts and nothing else.
func (x *exec) answerReply(asks []ask, written []string) string {
	var b strings.Builder
	b.WriteString("# ANSWER FROM THE HUMAN\n")
	for _, a := range asks {
		text := "(no answer recorded)"
		if a.answer != nil {
			// Attachments are (re-)delivered here rather than trusted to have
			// survived: the workspace may have been rebuilt since the question
			// was asked, and a path in the prompt that resolves to nothing is
			// worse than no path at all.
			kind := ""
			var form *event.Form
			var shown []string
			if r := x.rs.raisedPings[a.id]; r != nil {
				kind, form, shown = r.Kind(), r.Form, r.Files
			}
			ans := PingAnswer{Answer: a.answer.Answer, Feedback: a.answer.Feedback, Content: a.answer.Content, Review: a.answer.Review}
			text = composeAnswerText(kind, form, ans, shown, x.pingAnswerFiles(a.id))
		}
		fmt.Fprintf(&b, "\nYou asked: %s\nThey answered: %s\n", a.question, text)
	}
	b.WriteString("\nContinue the work from where you stopped. Everything else you were " +
		"given — the task, your inputs and the OUTPUT CONTRACT — still stands.\n")
	if len(written) > 0 {
		// It carried on and produced these without waiting, which means they
		// rest on its own guess at the answer above. Naming them is the whole
		// point: "revise if needed" is easy to skim past, a list of files it
		// has to re-read is not.
		b.WriteString("\nYou already wrote the following before this answer reached you, so " +
			"they were written without it:\n")
		for _, w := range written {
			fmt.Fprintf(&b, "- %s\n", w)
		}
		b.WriteString("Re-read each one and rewrite whatever the answer changes. Do not leave " +
			"a file standing on an assumption the human has now corrected.\n")
	}
	return b.String()
}

// assemblePrompt builds the agent prompt per the §3.2 template. The ticket
// is assembled from the stage's ports (FLOWLANG §4): each input renders by
// its type — a ticket as the TASK section, a file as its content, a
// changeset as its file list (never the full diff), a failure or a verdict
// as what the entering edge carried — and the outputs as the OUTPUT CONTRACT.
// An agent sees the ticket only through a ticket port.
func (x *exec) assemblePrompt(st *flow.Stage, iter int) string {
	rs := x.rs
	var b strings.Builder
	spec := rs.fl.Agent(st.Agent)
	for _, in := range st.Inputs {
		if flow.IsEdgeSource(in.Value) {
			// The content of the edge that re-entered this stage. Only one of
			// $DECISION / $FAILURE can be filled at a time (the edge kind
			// decides which), and neither on a first attempt.
			if in.Value != rs.feedSentinel || rs.feed == "" {
				continue
			}
			// Both were capped where they were taken: a failure to its tail,
			// a verdict to its head.
			fmt.Fprintf(&b, "# INPUT: %s%s (%s)\n%s\n",
				in.Name, inputDesc(spec, in.Name), attemptOf(iter, rs.feedAttempts), strings.TrimRight(rs.feed, "\n"))
			continue
		}
		if name := flow.SourceService(in.Value); name != "" {
			fmt.Fprintf(&b, "# INPUT: %s%s (link)\n%s\n", in.Name, inputDesc(spec, in.Name), x.serviceInput(name))
			continue
		}
		switch in.Value {
		case flow.SourceTicket:
			x.ticketSection(&b)
			continue
		case flow.SourceCIFailure:
			fmt.Fprintf(&b, "# INPUT: %s%s (what failed in CI)\n%s", in.Name, inputDesc(spec, in.Name), ciFailureText(rs.ci))
			continue
		case flow.SourceMRURL, flow.SourceWorkspace:
			if link := x.sourceLink(in.Value); link != "" {
				fmt.Fprintf(&b, "# INPUT: %s%s (link)\n%s\n", in.Name, inputDesc(spec, in.Name), link)
			}
			continue
		case flow.SourceDiffTarget:
			fmt.Fprintf(&b, "# INPUT: %s%s (git diff --name-status <target>...HEAD: what the branch changes against its target)\n%s\nInspect changed files directly with your read tools; do not ask for the diff.\n",
				in.Name, inputDesc(spec, in.Name), x.diffTarget())
			continue
		case flow.SourceDiffPhase:
			fmt.Fprintf(&b, "# INPUT: %s%s (git diff --name-status <phase start>...HEAD: what this phase committed since it began)\n%s\nInspect changed files directly with your read tools; do not ask for the diff.\n",
				in.Name, inputDesc(spec, in.Name), x.diffPhase())
			continue
		case flow.SourceStatus:
			fmt.Fprintf(&b, "# INPUT: %s%s (git status --short)\n%s\nInspect changed files directly with your read tools; do not ask for the diff.\n",
				in.Name, inputDesc(spec, in.Name), x.statusFiles())
			continue
		}
		path := flow.FilePath(in.Value)
		if in.Type == agentspec.TypeFiles {
			// A directory: what it holds, by name — screenshots are read with
			// the agent's own tools, never pasted into a prompt.
			fmt.Fprintf(&b, "# INPUT: %s%s (files: %s)\n%s", in.Name, inputDesc(spec, in.Name), path, x.filesListing(path))
			continue
		}
		content := "(file missing)"
		if data, err := x.res.Envp.ReadFile(x.ctx, rs.envID, path); err == nil {
			content = shell.Tail(string(data), 16<<10)
		}
		fmt.Fprintf(&b, "# INPUT: %s%s (file: %s)\n%s\n", in.Name, inputDesc(spec, in.Name), path, content)
	}

	if sec := x.subjectSection(); sec != "" {
		b.WriteString(sec)
	}
	if len(rs.checkouts) > 1 {
		b.WriteString("# REPOSITORIES\nThis run works in several repositories, each checked out on its own run branch:\n")
		for _, co := range rs.checkouts {
			role := ""
			if co.Primary {
				role = " (primary — relative paths in this prompt are here)"
			}
			fmt.Fprintf(&b, "- %s: ~/%s, branch %s forked from %s%s\n", co.Name, co.Dir, co.Branch, co.Source, role)
		}
		b.WriteString("Change only the repositories the task needs; each one that changes gets its own merge request.\n")
	}

	outs := st.Outputs
	if len(outs) > 0 {
		b.WriteString("# OUTPUT CONTRACT\n")
		b.WriteString("Before finishing you MUST write these files (relative to repo root):\n")
		for _, o := range outs {
			switch {
			case o.Name == "":
				fmt.Fprintf(&b, "- %s\n", o.Value)
			case agentspec.IsStructuredType(o.Type):
				// A structured file names its type beside its role: what its
				// schema below is the shape of.
				fmt.Fprintf(&b, "- %s — %s (%s)%s\n", o.Value, o.Name, o.Type, outputDesc(spec, o.Name))
			default:
				fmt.Fprintf(&b, "- %s — %s%s\n", o.Value, o.Name, outputDesc(spec, o.Name))
			}
		}
		// A files output is a directory the agent fills.
		for _, o := range outs {
			if o.Type == agentspec.TypeFiles {
				fmt.Fprintf(&b, "%s is a directory: write one or more files into it (it starts empty). Leaving it empty fails the stage.\n", o.Value)
			}
		}
		// A verdict output is a file like any other, plus a shape contract
		// (its schema, below).
		if vpath := flow.TypedPath(st, agentspec.TypeVerdict); vpath != "" {
			fmt.Fprintf(&b, "%s is your verdict: APPROVE or REJECT; a REJECT says why — a summary, comments, or both. A comment is a remark on the whole change, or {path, line, end_line, quote, body} on a file (line and end_line optional: without them, the whole file).\n", vpath)
			b.WriteString("Printing it in your reply does NOT satisfy this contract; the file itself must exist on disk.\n")
		}
		// So is every output with a structure: the agent sees exactly what the
		// engine checks the file against when the stage ends.
		for _, o := range outs {
			for _, s := range rs.fl.PortSchemas(o) {
				format := "JSON"
				if !strings.HasSuffix(o.Value, ".json") {
					format = "YAML"
				}
				fmt.Fprintf(&b, "%s must be %s satisfying this JSON Schema — it is checked when you finish, and a file that does not satisfy it fails the stage:\n%s\n",
					o.Value, format, s.Source)
			}
		}
	}
	return b.String()
}

// promptFilesListed caps the files a files input lists in a prompt.
const promptFilesListed = 50

// filesListing renders a files input: each file it holds and its size, one
// per line, at most promptFilesListed of them.
func (x *exec) filesListing(dir string) string {
	files, err := x.dirFiles(dir)
	if err != nil || len(files) == 0 {
		return "(no files)\n"
	}
	var b strings.Builder
	for i, f := range files {
		if i == promptFilesListed {
			fmt.Fprintf(&b, "… and %d more\n", len(files)-i)
			break
		}
		fmt.Fprintf(&b, "- %s (%s)\n", f.Path, humanSize(f.Size))
	}
	return b.String()
}

// humanSize renders a byte count the way a person reads one.
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ticketSection renders a ticket port: the TASK — its title and goal — with
// the human's direction for this attempt and the files attached to it.
func (x *exec) ticketSection(b *strings.Builder) {
	rs := x.rs
	fmt.Fprintf(b, "# TASK\n%s\n", rs.view.TicketTitle)
	if rs.view.TicketChangeType != "" {
		fmt.Fprintf(b, "Change type: %s\n", rs.view.TicketChangeType)
	}
	fmt.Fprintf(b, "\n%s\n", rs.view.TicketGoal)
	// Per-attempt direction from the human (ticket goal stays the spec; this
	// is additive — "last try broke the router, use the middleware instead").
	if rs.view.RunGoal != "" {
		fmt.Fprintf(b, "\n# ADDITIONAL DIRECTION FOR THIS ATTEMPT\n%s\n", rs.view.RunGoal)
	}
	if goals := x.goalAttachments(); len(goals) > 0 {
		b.WriteString("# ATTACHED FILES\nThe human attached these files to the task; read them with your tools:\n")
		for _, a := range goals {
			fmt.Fprintf(b, "- %s (%s, %s)\n", attachmentPath(a), a.Filename, a.Mime)
		}
	}
}

// attemptOf renders the retry counter for a feed section. An unlimited link
// (max -1) records no total, and claiming "attempt 2 of 2" there would tell an
// agent this is its last chance when the loop is bounded only by human patience.
func attemptOf(iter, total int) string {
	if total <= 0 || total < iter {
		return fmt.Sprintf("attempt %d", iter)
	}
	return fmt.Sprintf("attempt %d of %d", iter, total)
}

// inputDesc renders " — <description>" for a declared input, or "" when the
// agent has no spec or no matching/empty description. Keeps INPUT headers
// self-describing without hard-failing on drift.
func inputDesc(spec *agentspec.Spec, name string) string {
	if spec == nil {
		return ""
	}
	for _, d := range spec.Inputs {
		if d.Name == name {
			return declDesc(d.Description)
		}
	}
	return ""
}

// outputDesc renders ": <description>" for a declared output, or "".
func outputDesc(spec *agentspec.Spec, name string) string {
	if spec == nil {
		return ""
	}
	for _, d := range spec.Outputs {
		if d.Name == name {
			if d.Description == "" {
				return ""
			}
			return ": " + d.Description
		}
	}
	return ""
}

func declDesc(desc string) string {
	if desc == "" {
		return ""
	}
	return " — " + desc
}

// parseVerdict reads a verdict file — JSON or YAML, as its path says, like
// every structured file (flow.DecodeStructured). Its shape is the type's
// JSON Schema (flow.TypeSchema, checked with the outputs); here what a
// schema cannot say — a rejection says why, a path stays in the repository,
// a range runs forward.
func parseVerdict(path string, data []byte) (*Verdict, error) {
	decoded, err := flow.DecodeStructured(path, data)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var v struct {
		Verdict  string            `json:"verdict"`
		Summary  string            `json:"summary"`
		Comments []json.RawMessage `json:"comments"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%s is not a verdict: %w", path, err)
	}
	out := &Verdict{Summary: strings.TrimSpace(v.Summary)}
	switch v.Verdict {
	case "APPROVE":
		out.Approve = true
	case "REJECT":
	default:
		return nil, fmt.Errorf("%s: verdict must be APPROVE or REJECT, got %q", path, v.Verdict)
	}
	for i, rawC := range v.Comments {
		var remark string
		if json.Unmarshal(rawC, &remark) == nil {
			if strings.TrimSpace(remark) == "" {
				return nil, fmt.Errorf("%s: comment %d is empty", path, i+1)
			}
			out.Comments = append(out.Comments, VerdictComment{Body: remark})
			continue
		}
		var c struct {
			Path    string `json:"path"`
			Line    int    `json:"line"`
			EndLine int    `json:"end_line"`
			Quote   string `json:"quote"`
			Body    string `json:"body"`
		}
		if err := json.Unmarshal(rawC, &c); err != nil {
			return nil, fmt.Errorf("%s: comment %d is neither a remark nor {path, line, body}: %w", path, i+1, err)
		}
		switch {
		case strings.TrimSpace(c.Body) == "":
			return nil, fmt.Errorf("%s: comment %d is empty", path, i+1)
		case !filepath.IsLocal(c.Path):
			return nil, fmt.Errorf("%s: comment %d: path %q must be a file of the repository, relative to its root", path, i+1, c.Path)
		case c.Line < 0 || (c.EndLine != 0 && (c.Line == 0 || c.EndLine < c.Line)):
			return nil, fmt.Errorf("%s: comment %d: lines %d–%d are not a range", path, i+1, c.Line, c.EndLine)
		}
		out.Comments = append(out.Comments, VerdictComment{Path: c.Path, Line: c.Line, EndLine: c.EndLine, Quote: c.Quote, Body: c.Body})
	}
	if !out.Approve && len(out.Comments) == 0 && out.Summary == "" {
		return nil, fmt.Errorf("%s: REJECT requires comments, or a summary saying why", path)
	}
	return out, nil
}

// gitDiff is `git diff --name-status <base>...HEAD` in a checkout: the
// changed-file list, never the diff itself. --name-status, not --stat: the
// agent reads the files itself, so churn counts are noise in the prompt.
func (x *exec) gitDiff(dir, base string) string {
	rs := x.rs
	res, err := x.res.Envp.Exec(x.ctx, rs.envID, ports.Command{
		Script:  fmt.Sprintf("git diff --name-status %s...HEAD", base),
		Dir:     dir,
		Timeout: rs.fl.Defaults.Timeout,
	})
	if err != nil || res.ExitCode != 0 {
		return "(no diff available)"
	}
	return res.Output
}

// diffTarget renders $DIFF_TARGET: what each branch changes against its
// target — the branch a checkout forked from, the one its merge request
// merges into (a run started off a feature branch is diffed against that
// branch, not the project default: PLAN5 M23). The same range in every
// phase; inside a subject phase only the subject's checkout.
func (x *exec) diffTarget() string {
	rs := x.rs
	target := func(co event.Checkout) string {
		return "origin/" + bootstrap.SourceBranchOr(co.Source, x.res.RepoRef.DefaultBranch)
	}
	if co := x.subjectCheckout(); co != nil {
		return x.gitDiff(co.Dir, target(*co))
	}
	if len(rs.checkouts) <= 1 {
		return x.gitDiff(event.PrimaryCheckoutDir, "origin/"+bootstrap.SourceBranchOr(rs.sourceBranch, x.res.RepoRef.DefaultBranch))
	}
	return x.checkoutSections(func(co event.Checkout) string { return x.gitDiff(co.Dir, target(co)) })
}

// diffPhase renders $DIFF_PHASE: what this phase run committed since the
// commit it began on — a review round's fixes, not the whole branch. A
// phase run that began on no recorded commit (the delivery, which forked
// the branch) counts from the target, which is the same thing there.
func (x *exec) diffPhase() string {
	if co := x.subjectCheckout(); co != nil && x.rs.phaseTip != "" {
		return x.gitDiff(co.Dir, x.rs.phaseTip)
	}
	return x.diffTarget()
}

// statusFiles renders $STATUS: the working tree's pending changes (staged,
// unstaged and untracked) — what a commit stage would record right now,
// which the diffs cannot show since they compare commits. Scoped like them:
// the subject alone inside a subject phase, else every checkout.
func (x *exec) statusFiles() string {
	rs := x.rs
	status := func(dir string) string {
		res, err := x.res.Envp.Exec(x.ctx, rs.envID, ports.Command{
			Script:  "git status --short --untracked-files=all",
			Dir:     dir,
			Timeout: rs.fl.Defaults.Timeout,
		})
		if err != nil || res.ExitCode != 0 {
			return "(no status available)"
		}
		if strings.TrimSpace(res.Output) == "" {
			return "(nothing uncommitted)\n"
		}
		return res.Output
	}
	if co := x.subjectCheckout(); co != nil {
		return status(co.Dir)
	}
	if len(rs.checkouts) <= 1 {
		return status(event.PrimaryCheckoutDir)
	}
	return x.checkoutSections(func(co event.Checkout) string { return status(co.Dir) })
}

// checkoutSections renders a per-checkout sentinel on a multi-repo run: one
// section per repository, headed by its name and path.
func (x *exec) checkoutSections(render func(co event.Checkout) string) string {
	var b strings.Builder
	for _, co := range x.rs.checkouts {
		fmt.Fprintf(&b, "## %s (~/%s)\n%s", co.Name, co.Dir, strings.TrimRight(render(co), "\n"))
		b.WriteString("\n")
	}
	return b.String()
}
