package tickets

import (
	"testing"

	"apothecary/internal/ports"
)

func TestSourceChangeType(t *testing.T) {
	for _, c := range []struct {
		st   ports.SourceTicket
		want ports.ChangeType
	}{
		{ports.SourceTicket{Type: "Bug"}, ports.ChangeFix},
		{ports.SourceTicket{Type: "Production Defect"}, ports.ChangeFix},
		{ports.SourceTicket{Type: "Tech Debt"}, ports.ChangeChore},
		{ports.SourceTicket{Type: "Incident"}, ports.ChangeHotfix},
		{ports.SourceTicket{Type: "Hotfix"}, ports.ChangeHotfix},
		{ports.SourceTicket{Type: "Performance"}, ports.ChangePerf},
		{ports.SourceTicket{Type: "Documentation"}, ports.ChangeDocs},
		{ports.SourceTicket{Type: "Docs bug"}, ports.ChangeFix},
		{ports.SourceTicket{Type: "Story"}, ports.ChangeFeat},
		{ports.SourceTicket{Type: ""}, ports.ChangeFeat},
		// The integration's mapping wins over the guess.
		{ports.SourceTicket{Type: "Task", ChangeType: ports.ChangeChore}, ports.ChangeChore},
		{ports.SourceTicket{Type: "Bug", ChangeType: ports.ChangeFeat}, ports.ChangeFeat},
	} {
		if got := SourceChangeType(c.st); got != c.want {
			t.Errorf("SourceChangeType(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
}

func TestBranchName(t *testing.T) {
	for _, c := range []struct {
		typ              ports.ChangeType
		key, title, want string
	}{
		{ports.ChangeFeat, "PROJ-123", "Add footnotes", "feat/proj-123-add-footnotes"},
		{ports.ChangeFix, "PROJ-9", "  Crash on save!! (iOS) ", "fix/proj-9-crash-on-save-ios"},
		{ports.ChangeFeat, "lu/greeter#5", "Say goodbye", "feat/5-say-goodbye"},
		{ports.ChangeChore, "", "Bump deps", "chore/bump-deps"},
		{ports.ChangeHotfix, "PROJ-4", "Login down", "hotfix/proj-4-login-down"},
		{ports.ChangeFeat, "", "—", "feat/ticket"},
	} {
		if got := BranchName(c.typ, c.key, c.title); got != c.want {
			t.Errorf("BranchName(%q, %q, %q) = %q, want %q", c.typ, c.key, c.title, got, c.want)
		}
	}
}
