package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"apothecary/internal/agentspec"
	"apothecary/internal/flow"
	"apothecary/internal/ports"
)

// assemblePrompt builds the agent prompt exactly per the §3.2 template. The
// ticket always forms the TASK section; a literal `ticket` input entry adds
// nothing extra. $CHANGED is a stat + file list, never the full diff.
func (x *exec) assemblePrompt(st *flow.Stage, iter int) string {
	rs := x.rs
	var b strings.Builder
	fmt.Fprintf(&b, "# TASK\n%s\n\n%s\n", rs.view.TicketTitle, rs.view.TicketGoal)

	// Per-attempt direction from the human (ticket goal stays the spec; this
	// is additive — "last try broke the router, use the middleware instead").
	if rs.view.RunGoal != "" {
		fmt.Fprintf(&b, "\n# ADDITIONAL DIRECTION FOR THIS ATTEMPT\n%s\n", rs.view.RunGoal)
	}

	spec := x.res.agentSpec(st.Agent)
	for _, in := range st.Inputs {
		switch {
		case in.Name == "" && in.Value == flow.InputTicket:
			// already the TASK section
		case in.Value == flow.InputChanged:
			stat := "(no diff available)"
			res, err := x.res.Envp.Exec(x.ctx, rs.envID, ports.Command{
				Script:  fmt.Sprintf("git diff --stat origin/%s...HEAD", x.res.RepoRef.DefaultBranch),
				Dir:     "repo",
				Timeout: rs.fl.Defaults.Timeout,
			})
			if err == nil && res.ExitCode == 0 {
				stat = res.Output
			}
			if in.Name == "" {
				fmt.Fprintf(&b, "# CHANGED FILES\n%s\nInspect changed files directly with your read tools; do not ask for the diff.\n", stat)
			} else {
				fmt.Fprintf(&b, "# INPUT: %s%s\n%s\nInspect changed files directly with your read tools; do not ask for the diff.\n", in.Name, inputDesc(spec, in.Name), stat)
			}
		default:
			content := "(artifact missing)"
			if data, err := x.res.Envp.ReadFile(x.ctx, rs.envID, in.Value); err == nil {
				content = tail(string(data), 16<<10)
			}
			if in.Name == "" {
				fmt.Fprintf(&b, "# ARTIFACT: %s\n%s\n", in.Value, content)
			} else {
				fmt.Fprintf(&b, "# INPUT: %s%s (file: %s)\n%s\n", in.Name, inputDesc(spec, in.Name), in.Value, content)
			}
		}
	}

	if iter > 1 {
		maxAttempts := max(rs.feedAttempts, iter)
		feed := rs.feed
		if feed == "" {
			feed = "(feedback unavailable)"
		}
		// When the agent declares the reserved feedback input, the retry
		// content flows into a named INPUT section (described); otherwise the
		// generic header, unchanged for legacy agents.
		if fb := feedbackDecl(spec); fb != nil {
			fmt.Fprintf(&b, "# INPUT: %s%s (iteration %d of %d)\n%s\n", fb.Name, declDesc(fb.Description), iter, maxAttempts, tail(feed, 8<<10))
		} else {
			fmt.Fprintf(&b, "# PREVIOUS ATTEMPT FEEDBACK (iteration %d of %d)\n%s\n", iter, maxAttempts, tail(feed, 8<<10))
		}
	}

	if goals := x.goalArtifacts(); len(goals) > 0 {
		b.WriteString("# ATTACHED FILES\nThe human attached these files to the task; read them with your tools:\n")
		for _, m := range goals {
			fmt.Fprintf(&b, "- %s (%s, %s)\n", x.artifactPath(m), m.Filename, m.Mime)
		}
	}

	outs := flow.EffectiveOutputs(st, spec)
	if len(outs) > 0 || st.Verdict {
		b.WriteString("# OUTPUT CONTRACT\n")
		if len(outs) > 0 {
			b.WriteString("Before finishing you MUST write these files (relative to repo root):\n")
			for _, o := range outs {
				if o.Name == "" {
					fmt.Fprintf(&b, "- %s\n", o.Value)
				} else {
					fmt.Fprintf(&b, "- %s — %s%s\n", o.Value, o.Name, outputDesc(spec, o.Name))
				}
			}
		}
		if st.Verdict {
			b.WriteString(`Before finishing you MUST use your file-writing tool to create the file artifacts/verdict.json containing exactly` + "\n" +
				`{"verdict":"APPROVE"|"REJECT","comments":["..."]} — comments required when REJECT.` + "\n" +
				`Printing the JSON in your reply does NOT satisfy this contract; the file itself must exist on disk.` + "\n")
		}
	}
	return b.String()
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

// feedbackDecl returns the agent's reserved feedback input decl, or nil.
func feedbackDecl(spec *agentspec.Spec) *agentspec.InputDecl {
	if spec == nil {
		return nil
	}
	for i := range spec.Inputs {
		if spec.Inputs[i].Name == agentspec.InputFeedback {
			return &spec.Inputs[i]
		}
	}
	return nil
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
