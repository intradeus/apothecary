package tickets

import (
	"apothecary/internal/ports/trackerport"
)

// A ticket's base branch is named by its workshop's branch format (package
// branchname: {ticket_type}/{ticket_number}-{ticket_title} by default, so
// feat/proj-123-add-footnotes), from its change type, the tracker's issue key
// and its title; a ticket written by hand has no key.
//
// The name is decided once, when the ticket is created, and stored as its
// base branch — the form prefills it (still editable), an automation's
// import takes it as is. Every run then appends its own tail.

// SourceChangeType is a GUESS at an issue's change type from its type's name
// — what the import form prefills before the issue is imported. Once it is,
// the change type is the integration's type list's (Factory.mirror), which a
// guess only pre-fills for a type seen the first time.
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
