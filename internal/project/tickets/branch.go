package tickets

import (
	"strings"

	"apothecary/internal/ports/trackerport"
)

// A ticket's base branch is named {type}/{key}-{title}: its change type,
// the tracker's issue key, the title — feat/proj-123-add-footnotes. The key
// in the name is what links the branch to the issue on the tracker's side
// (Jira's Development panel picks up a branch whose name carries the key,
// once the repository host is connected to Jira); a ticket written by hand
// has none and is just {type}/{title}.
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

// BranchName is {type}/{key}-{title}, every part slugged; key may be empty.
// A key carrying its repository ("lu/greeter#5", GitHub and GitLab issues)
// keeps only its number.
func BranchName(changeType trackerport.ChangeType, key, title string) string {
	if i := strings.LastIndex(key, "#"); i >= 0 {
		key = key[i+1:]
	}
	stem := slug(key + " " + title)
	if stem == "" {
		stem = "ticket"
	}
	return string(changeType) + "/" + stem
}

// maxStem bounds the slug like the engine's own branch slugs: not a
// readability cap, only a backstop under a ref's 255-byte file name.
const maxStem = 200

// slug lowercases s to [a-z0-9] runs joined by single dashes.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
		if b.Len() >= maxStem {
			break
		}
	}
	return b.String()
}
