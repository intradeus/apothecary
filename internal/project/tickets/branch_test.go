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
