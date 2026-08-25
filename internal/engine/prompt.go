package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"apothecary/internal/agentspec"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
)

// assemblePrompt builds the agent prompt per the §3.2 template. The ticket
// always forms the TASK section. Each of the stage's bound inputs renders as a
// named, described INPUT section (FLOWLANG §4); $CHANGED is a stat + file list,
// never the full diff.
func (x *exec) assemblePrompt(st *flow.Stage, iter int) string {
	rs := x.rs
	var b strings.Builder
	fmt.Fprintf(&b, "# TASK\n%s\n\n%s\n", rs.view.TicketTitle, rs.view.TicketGoal)

	// Per-attempt direction from the human (ticket goal stays the spec; this
	// is additive — "last try broke the router, use the middleware instead").
	if rs.view.RunGoal != "" {
		fmt.Fprintf(&b, "\n# ADDITIONAL DIRECTION FOR THIS ATTEMPT\n%s\n", rs.view.RunGoal)
	}

	spec := rs.fl.Agent(st.Agent)
	for _, in := range st.Inputs {
		if flow.IsFeedSentinel(in.Value) {
			// The content of the edge that re-entered this stage. Only one of
			// $DECISION / $FAILURE can be filled at a time (the edge kind
			// decides which), and neither on a first attempt.
			if in.Value != rs.feedSentinel || rs.feed == "" {
				continue
			}
			fmt.Fprintf(&b, "# INPUT: %s%s (%s)\n%s\n",
				in.Name, inputDesc(spec, in.Name), attemptOf(iter, rs.feedAttempts), tail(rs.feed, 8<<10))
			continue
		}
		if in.Value == flow.SentinelChanged {
			stat := "(no diff available)"
			// Diff against the branch this run actually forked from, not the
			// project default — a run started off a feature branch would
			// otherwise be diffed against the wrong base and report every
			// change the two branches differ by (PLAN5 M23).
			base := sourceBranchOr(rs.sourceBranch, x.res.RepoRef.DefaultBranch)
			res, err := x.res.Envp.Exec(x.ctx, rs.envID, ports.Command{
				// --name-status, not --stat: the sentinel is the changed-FILE
				// list (FLOWLANG §4) and the agent reads the files itself, so
				// churn counts are noise that crowds the prompt.
				Script:  fmt.Sprintf("git diff --name-status origin/%s...HEAD", base),
				Dir:     "repo",
				Timeout: rs.fl.Defaults.Timeout,
			})
			if err == nil && res.ExitCode == 0 {
				stat = res.Output
			}
			fmt.Fprintf(&b, "# INPUT: %s%s\n%s\nInspect changed files directly with your read tools; do not ask for the diff.\n", in.Name, inputDesc(spec, in.Name), stat)
			continue
		}
		content := "(artifact missing)"
		if data, err := x.res.Envp.ReadFile(x.ctx, rs.envID, in.Value); err == nil {
			content = tail(string(data), 16<<10)
		}
		fmt.Fprintf(&b, "# INPUT: %s%s (file: %s)\n%s\n", in.Name, inputDesc(spec, in.Name), in.Value, content)
	}

	if goals := x.goalAttachments(); len(goals) > 0 {
		b.WriteString("# ATTACHED FILES\nThe human attached these files to the task; read them with your tools:\n")
		for _, a := range goals {
			fmt.Fprintf(&b, "- %s (%s, %s)\n", attachmentPath(a), a.Filename, a.Mime)
		}
	}

	outs := flow.EffectiveOutputs(st, spec)
	if len(outs) > 0 {
		b.WriteString("# OUTPUT CONTRACT\n")
		b.WriteString("Before finishing you MUST write these files (relative to repo root):\n")
		for _, o := range outs {
			if o.Name == "" {
				fmt.Fprintf(&b, "- %s\n", o.Value)
			} else {
				fmt.Fprintf(&b, "- %s — %s%s\n", o.Value, o.Name, outputDesc(spec, o.Name))
			}
		}
		// A verdict output is a file like any other, plus a shape contract.
		if vpath := flow.VerdictPath(st, spec); vpath != "" {
			fmt.Fprintf(&b, "%s must contain exactly {\"verdict\":\"APPROVE\"|\"REJECT\",\"comments\":[\"...\"]} — comments required when REJECT.\n", vpath)
			b.WriteString("Printing the JSON in your reply does NOT satisfy this contract; the file itself must exist on disk.\n")
		}
	}
	return b.String()
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

// parseVerdict enforces the §3/§5.3 verdict contract:
// {"verdict":"APPROVE"|"REJECT","comments":["..."]}, comments required on REJECT.
func parseVerdict(data []byte) (*ports.Verdict, error) {
	var v struct {
		Verdict  string   `json:"verdict"`
		Comments []string `json:"comments"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("verdict.json unparsable: %w", err)
	}
	switch v.Verdict {
	case "APPROVE":
		return &ports.Verdict{Approve: true, Comments: v.Comments}, nil
	case "REJECT":
		if len(v.Comments) == 0 {
			return nil, fmt.Errorf("verdict.json: REJECT requires comments")
		}
		return &ports.Verdict{Approve: false, Comments: v.Comments}, nil
	default:
		return nil, fmt.Errorf("verdict.json: verdict must be APPROVE or REJECT, got %q", v.Verdict)
	}
}
