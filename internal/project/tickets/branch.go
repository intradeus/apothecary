package tickets

import (
	"apothecary/internal/ports/trackerport"
)

// A ticket's base branch is named by the workshop a run is in (package
// branchname: {ticket_type}/{ticket_number}-{ticket_title} by default, so
// feat/proj-123-add-footnotes), from its change type, the tracker's issue key
// and its title; an internal ticket has no key.
//
// The name is rendered at each run's start (Factory.RunTicket), so two
// workshops sharing a source each name it their way, unless the user set
// the ticket's base branch, which every workshop takes as it is. Every run
// then appends its own tail.

// SourceChangeType is a GUESS at an issue's change type from its type's name
// — what a live row shows before the issue has a record. Once it has one,
// the change type is the integration's type list's, which a guess only
// pre-fills for a type seen the first time.
func SourceChangeType(st trackerport.SourceTicket) trackerport.ChangeType {
	return trackerport.GuessChangeType(st.Type)
}

// normalType spells a given change type canonically ("FIX" → fix) before a
// branch is named after it; one that is no change type is left for the store
// to refuse.
func normalType(ct trackerport.ChangeType) trackerport.ChangeType {
	if c, ok := trackerport.ParseChangeType(string(ct)); ok {
		return c
	}
	return ct
}
