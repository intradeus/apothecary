package reviewfile

// Curation: what a person at a gate made of a review document — which
// items get posted and how they read. It is a list of DECISIONS over the
// document as it was shown (by item key), never a rewritten document: the
// engine rebuilds the file from the raise-time snapshot and these, so what
// is posted is provably a subset, rewritten, of what the person saw — even
// when the workspace file changed under a parked gate.

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxRewriteBytes bounds a rewritten text: a longer one is a different
// review, which the agent writes.
const MaxRewriteBytes = 8 << 10

// Item lists: a review's notes and replies; a replies document is one list
// of replies.
const (
	ListNotes   = "notes"
	ListReplies = "replies"
)

// Curation is one document's decisions. Summary replaces a review's summary
// (nil: as written). Items decide the listed items; an item absent is kept
// as written — all selected is the default, unpicking is the deliberate act.
type Curation struct {
	Summary *string       `json:"summary,omitempty"`
	Items   []CuratedItem `json:"items,omitempty"`
}

// CuratedItem is one decision: Item is "notes/<i>" or "replies/<i>" in the
// document as shown; Keep says whether it is posted; Body the text as
// rewritten ("" = as written); Resolve overrides a reply's resolve flag.
type CuratedItem struct {
	Item    string `json:"item"`
	Keep    bool   `json:"keep"`
	Body    string `json:"body,omitempty"`
	Resolve *bool  `json:"resolve,omitempty"`
}

// ItemKey names the i-th item of a list.
func ItemKey(list string, i int) string { return list + "/" + strconv.Itoa(i) }

// parseKey splits an item key into its list and index.
func parseKey(key string) (list string, i int, err error) {
	list, idx, ok := strings.Cut(key, "/")
	if !ok || (list != ListNotes && list != ListReplies) {
		return "", 0, fmt.Errorf("item %q: not notes/<i> or replies/<i>", key)
	}
	i, err = strconv.Atoi(idx)
	if err != nil || i < 0 {
		return "", 0, fmt.Errorf("item %q: not notes/<i> or replies/<i>", key)
	}
	return list, i, nil
}

// Applied says what a curation did to a document.
type Applied struct {
	// Dropped are the notes not posted, as they were written.
	Dropped []Note `json:"dropped,omitempty"`
	// DroppedReplies are the threads whose reply was not posted.
	DroppedReplies []string `json:"dropped_replies,omitempty"`
	// Rewritten are the item keys whose text or resolve flag changed, and
	// "summary" when the summary did.
	Rewritten []string `json:"rewritten,omitempty"`
}

// check holds a curation against the lists it curates — each key a real
// item, named once; a kept text non-empty and bounded; resolve only on a
// reply — and tidies it: text trimmed, and a text or flag equal to the
// original cleared, so "rewritten" means rewritten. origText and origResolve
// return the item as written.
func (c *Curation) check(sizes map[string]int, origText func(list string, i int) string, origResolve func(list string, i int) (bool, bool)) error {
	seen := map[string]bool{}
	for k := range c.Items {
		it := &c.Items[k]
		list, i, err := parseKey(it.Item)
		if err != nil {
			return err
		}
		n, ok := sizes[list]
		if !ok || i >= n {
			return fmt.Errorf("item %q: the document shown has no such item", it.Item)
		}
		if seen[it.Item] {
			return fmt.Errorf("item %q: decided twice", it.Item)
		}
		seen[it.Item] = true
		it.Body = strings.TrimSpace(it.Body)
		if len(it.Body) > MaxRewriteBytes {
			return fmt.Errorf("item %q: the rewritten text is longer than %d bytes — a different note is the agent's to write", it.Item, MaxRewriteBytes)
		}
		if it.Body != "" && it.Body == strings.TrimSpace(origText(list, i)) {
			it.Body = ""
		}
		if it.Resolve != nil {
			orig, can := origResolve(list, i)
			if !can {
				return fmt.Errorf("item %q: only a reply resolves a thread", it.Item)
			}
			if *it.Resolve == orig {
				it.Resolve = nil
			}
		}
	}
	if c.Summary != nil {
		s := strings.TrimSpace(*c.Summary)
		c.Summary = &s
	}
	return nil
}

// ValidateReview checks a review's curation against the review shown and
// tidies it in place.
func ValidateReview(doc *Review, c *Curation) error {
	err := c.check(map[string]int{ListNotes: len(doc.Notes), ListReplies: len(doc.Replies)},
		func(list string, i int) string {
			if list == ListNotes {
				return doc.Notes[i].Body
			}
			return doc.Replies[i].Body
		},
		func(list string, i int) (bool, bool) {
			if list == ListNotes {
				return false, false
			}
			return doc.Replies[i].Resolve, true
		})
	if err != nil {
		return err
	}
	if c.Summary != nil && *c.Summary == doc.Summary {
		c.Summary = nil
	}
	return nil
}

// ValidateReplies checks a replies document's curation against the list
// shown and tidies it in place. A replies document has no summary.
func ValidateReplies(list []Reply, c *Curation) error {
	if c.Summary != nil {
		return fmt.Errorf("a replies document has no summary to rewrite")
	}
	return c.check(map[string]int{ListReplies: len(list)},
		func(_ string, i int) string { return list[i].Reply },
		func(_ string, i int) (bool, bool) { return list[i].Resolve, true })
}

// decision is the curation's say on one item: kept unless unpicked, text
// and flag as rewritten.
func (c Curation) decision(key string) (keep bool, body string, resolve *bool) {
	for _, it := range c.Items {
		if it.Item == key {
			return it.Keep, it.Body, it.Resolve
		}
	}
	return true, "", nil
}

// ApplyReview rebuilds a review from the one shown and a person's
// decisions: unpicked notes and replies left out (and reported), rewritten
// text and flags in. The owner-only fields travel with a kept item.
func ApplyReview(doc Review, c Curation) (Review, Applied) {
	out := Review{Summary: doc.Summary}
	var ap Applied
	if c.Summary != nil && *c.Summary != doc.Summary {
		out.Summary = *c.Summary
		ap.Rewritten = append(ap.Rewritten, "summary")
	}
	for i, n := range doc.Notes {
		key := ItemKey(ListNotes, i)
		keep, body, _ := c.decision(key)
		if !keep {
			ap.Dropped = append(ap.Dropped, n)
			continue
		}
		if body != "" && body != strings.TrimSpace(n.Body) {
			n.Body = body
			ap.Rewritten = append(ap.Rewritten, key)
		}
		out.Notes = append(out.Notes, n)
	}
	for i, r := range doc.Replies {
		key := ItemKey(ListReplies, i)
		keep, body, resolve := c.decision(key)
		if !keep {
			ap.DroppedReplies = append(ap.DroppedReplies, r.Thread)
			continue
		}
		changed := false
		if body != "" && body != strings.TrimSpace(r.Body) {
			r.Body, changed = body, true
		}
		if resolve != nil && *resolve != r.Resolve {
			r.Resolve, changed = *resolve, true
		}
		if changed {
			ap.Rewritten = append(ap.Rewritten, key)
		}
		out.Replies = append(out.Replies, r)
	}
	return out, ap
}

// ApplyReplies rebuilds a replies list from the one shown and a person's
// decisions: a reply unpicked is not sent (its thread reported), rewritten
// text and flags in.
func ApplyReplies(list []Reply, c Curation) ([]Reply, Applied) {
	var out []Reply
	var ap Applied
	for i, r := range list {
		key := ItemKey(ListReplies, i)
		keep, body, resolve := c.decision(key)
		if !keep {
			ap.DroppedReplies = append(ap.DroppedReplies, r.Thread)
			continue
		}
		changed := false
		if body != "" && body != strings.TrimSpace(r.Reply) {
			r.Reply, changed = body, true
		}
		if resolve != nil && *resolve != r.Resolve {
			r.Resolve, changed = *resolve, true
		}
		if changed {
			ap.Rewritten = append(ap.Rewritten, key)
		}
		out = append(out, r)
	}
	return out, ap
}
