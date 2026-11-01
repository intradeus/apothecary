package tickets

import (
	"testing"

	"apothecary/internal/ports/trackerport"
)

func TestSourceChangeType(t *testing.T) {
	for _, c := range []struct {
		st   trackerport.SourceTicket
		want trackerport.ChangeType
	}{
		{trackerport.SourceTicket{Type: "Bug"}, trackerport.ChangeFix},
		{trackerport.SourceTicket{Type: "Production Defect"}, trackerport.ChangeFix},
		{trackerport.SourceTicket{Type: "Tech Debt"}, trackerport.ChangeChore},
		{trackerport.SourceTicket{Type: "Incident"}, trackerport.ChangeHotfix},
		{trackerport.SourceTicket{Type: "Hotfix"}, trackerport.ChangeHotfix},
		{trackerport.SourceTicket{Type: "Performance"}, trackerport.ChangePerf},
		{trackerport.SourceTicket{Type: "Documentation"}, trackerport.ChangeDocs},
		{trackerport.SourceTicket{Type: "Docs bug"}, trackerport.ChangeFix},
		{trackerport.SourceTicket{Type: "Story"}, trackerport.ChangeFeat},
		{trackerport.SourceTicket{Type: ""}, trackerport.ChangeFeat},
	} {
		if got := SourceChangeType(c.st); got != c.want {
			t.Errorf("SourceChangeType(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
}

func TestBranchName(t *testing.T) {
	for _, c := range []struct {
		typ              trackerport.ChangeType
		key, title, want string
	}{
		{trackerport.ChangeFeat, "PROJ-123", "Add footnotes", "feat/proj-123-add-footnotes"},
		{trackerport.ChangeFix, "PROJ-9", "  Crash on save!! (iOS) ", "fix/proj-9-crash-on-save-ios"},
		{trackerport.ChangeFeat, "lu/greeter#5", "Say goodbye", "feat/5-say-goodbye"},
		{trackerport.ChangeChore, "", "Bump deps", "chore/bump-deps"},
		{trackerport.ChangeHotfix, "PROJ-4", "Login down", "hotfix/proj-4-login-down"},
		{trackerport.ChangeFeat, "", "—", "feat/ticket"},
	} {
		if got := BranchName(c.typ, c.key, c.title); got != c.want {
			t.Errorf("BranchName(%q, %q, %q) = %q, want %q", c.typ, c.key, c.title, got, c.want)
		}
	}
}
