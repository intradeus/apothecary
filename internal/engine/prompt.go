package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"factory/internal/flow"
	"factory/internal/ports"
)

// assemblePrompt builds the agent prompt exactly per the §3.2 template. The
// ticket always forms the TASK section; a literal `ticket` input entry adds
// nothing extra. $CHANGED is a stat + file list, never the full diff.
func (x *exec) assemblePrompt(st *flow.Stage, iter int) string {
	rs := x.rs
	var b strings.Builder
	fmt.Fprintf(&b, "# TASK\n%s\n\n%s\n", rs.view.TicketTitle, rs.view.TicketGoal)

	for _, in := range st.Input {
		switch in {
		case flow.InputTicket:
			// already the TASK section
		case flow.InputChanged:
			stat := "(no diff available)"
			res, err := x.e.envp.Exec(x.ctx, rs.envID, ports.Command{
				Script:  fmt.Sprintf("git diff --stat origin/%s...HEAD", x.e.cfg.DefaultBranch),
				Dir:     "repo",
				Timeout: rs.fl.Defaults.Timeout,
			})
			if err == nil && res.ExitCode == 0 {
				stat = res.Output
			}
			fmt.Fprintf(&b, "# CHANGED FILES\n%s\nInspect changed files directly with your read tools; do not ask for the diff.\n", stat)
		default:
			content := "(artifact missing)"
			if data, err := x.e.envp.ReadFile(x.ctx, rs.envID, in); err == nil {
				content = tail(string(data), 16<<10)
			}
			fmt.Fprintf(&b, "# ARTIFACT: %s\n%s\n", in, content)
		}
	}

	if iter > 1 {
		maxAttempts := max(rs.feedAttempts, iter)
		feed := rs.feed
		if feed == "" {
			feed = "(feedback unavailable)"
		}
		fmt.Fprintf(&b, "# PREVIOUS ATTEMPT FEEDBACK (iteration %d of %d)\n%s\n", iter, maxAttempts, tail(feed, 8<<10))
	}

	if st.Output != "" || st.Verdict {
		b.WriteString("# OUTPUT CONTRACT\n")
		if st.Output != "" {
			fmt.Fprintf(&b, "Before finishing you MUST write %s (relative to repo root).\n", st.Output)
		}
		if st.Verdict {
			b.WriteString(`Before finishing you MUST write artifacts/verdict.json exactly as` + "\n" +
				`{"verdict":"APPROVE"|"REJECT","comments":["..."]} — comments required when REJECT.` + "\n")
		}
	}
	return b.String()
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
