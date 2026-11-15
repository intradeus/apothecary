package trackerport

import (
	"strings"
	"testing"
)

func TestChangeTypesStartWithTheDefault(t *testing.T) {
	if got := ChangeTypes(); got[0] != ChangeFeat || len(got) != 6 {
		t.Errorf("change types = %v", got)
	}
}

func TestParseChangeType(t *testing.T) {
	for in, want := range map[string]ChangeType{"fix": ChangeFix, " HotFix ": ChangeHotfix, "docs": ChangeDocs} {
		if got, ok := ParseChangeType(in); !ok || got != want {
			t.Errorf("ParseChangeType(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "feature", "fix it"} {
		if got, ok := ParseChangeType(in); ok || got != "" {
			t.Errorf("ParseChangeType(%q) = %q, %v", in, got, ok)
		}
	}
}

func TestGuessChangeType(t *testing.T) {
	for in, want := range map[string]ChangeType{
		"Incident":          ChangeHotfix,
		"HOTFIX":            ChangeHotfix,
		" Bug ":             ChangeFix,
		"Production Defect": ChangeFix,
		"Problem":           ChangeFix,
		"Performance":       ChangePerf,
		"Documentation":     ChangeDocs,
		"Chore":             ChangeChore,
		"Maintenance":       ChangeChore,
		"Tech Debt":         ChangeChore,
		"Story":             ChangeFeat,
		"":                  ChangeFeat,
		// The first rule with a word in the type wins.
		"Bug in the docs": ChangeFix,
		"Incident bug":    ChangeHotfix,
	} {
		if got := GuessChangeType(in); got != want {
			t.Errorf("GuessChangeType(%q) = %s, want %s", in, got, want)
		}
	}
}

// A refusal's text is what a flow's failure message shows: it names the fix.
func TestErrorsNameTheFix(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		has  []string
		not  []string
	}{
		{"unreachable", &ErrStatusUnreachable{Key: "P-1", From: "To Do", To: "Done", Reachable: []string{"In Progress", "Blocked"}},
			[]string{`P-1: status "Done" is not reachable from "To Do"`, "reachable: In Progress, Blocked"}, []string{"no transition"}},
		{"nowhere to go", &ErrStatusUnreachable{Key: "P-1", From: "Done", To: "To Do"},
			[]string{"no transition is available"}, []string{"reachable:"}},
		{"unknown field", &ErrFieldUnknown{Key: "P-1", Field: "Team"}, []string{`no field named "Team"`}, []string{"candidates"}},
		{"ambiguous field", &ErrFieldUnknown{Key: "P-1", Field: "Team", Candidates: []string{"Team (cf_1)", "Team (cf_2)"}},
			[]string{"ambiguous or unknown", "candidates: Team (cf_1), Team (cf_2)"}, nil},
		{"field of a type", &ErrFieldUnsupported{Key: "P-1", Field: "Due", Type: "date"},
			[]string{`field "Due" cannot be set from text`, "its type is date"}, []string{"supported"}},
		{"field of a fixed set", &ErrFieldUnsupported{Key: "P-1", Field: "x", Supported: []string{"labels", "milestone"}},
			[]string{"supported: labels, milestone"}, []string{"its type"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			for _, w := range tc.has {
				if !strings.Contains(msg, w) {
					t.Errorf("%q does not say %q", msg, w)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(msg, w) {
					t.Errorf("%q says %q", msg, w)
				}
			}
		})
	}
}
