package gitlab

// A search over several scopes (projects and groups) merges their issue
// lists into one, most recently updated first. A page holds at most the
// limit, whatever the number of scopes, and the cursor carries each scope's
// position in its own list, so the next page resumes every list where this
// one stopped and skips nothing. A project inside a configured group shows
// its issues through both scopes: a copy already listed is dropped, on this
// page and on the next.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"apothecary/internal/ports/trackerport"
)

// searchCursor is Search's cursor, base64url JSON. At is each scope's
// position: the rows of its list consumed, -1 once it has no more. Keys are
// the issues listed at Last, the last listed row's update time: the only
// ones whose copy in another scope can still be ahead (every copy of an
// issue is updated when it is, and each list is in that order).
type searchCursor struct {
	At   []int     `json:"at"`
	Last time.Time `json:"last,omitzero"`
	Keys []string  `json:"keys,omitempty"`
}

// scopeList reads one scope's list from a position, a page of the search's
// size at a time.
type scopeList struct {
	path   string
	offset int // rows consumed
	buf    []trackerport.SourceTicket
	end    bool // nothing past buf
}

// issueMerge is one Search call over the scopes.
type issueMerge struct {
	s      *Issues
	params url.Values
	per    int
	lists  []*scopeList
	from   searchCursor
}

func newIssueMerge(s *Issues, scopes []string, params url.Values, per int, cursor string) (*issueMerge, error) {
	m := &issueMerge{s: s, params: params, per: per}
	at := make([]int, len(scopes))
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err == nil {
			err = json.Unmarshal(raw, &m.from)
		}
		if err != nil || len(m.from.At) != len(scopes) {
			return nil, fmt.Errorf("gitlab: bad search cursor %q", cursor)
		}
		at = m.from.At
	}
	for i, path := range scopes {
		l := &scopeList{path: path, offset: at[i]}
		if at[i] < 0 {
			l.offset, l.end = 0, true
		}
		m.lists = append(m.lists, l)
	}
	return m, nil
}

// head is the list's next row, nil when it has none: a page is read when
// what was read is used up. A position inside a page rereads that page and
// skips what was consumed.
func (m *issueMerge) head(ctx context.Context, l *scopeList) (*trackerport.SourceTicket, error) {
	for len(l.buf) == 0 && !l.end {
		params := maps.Clone(m.params)
		params.Set("page", strconv.Itoa(l.offset/m.per+1))
		var rows []issueJSON
		if _, err := m.s.a.api(ctx, http.MethodGet, l.path+"?"+params.Encode(), nil, &rows); err != nil {
			return nil, err
		}
		l.end = len(rows) < m.per
		for _, it := range rows[min(l.offset%m.per, len(rows)):] {
			l.buf = append(l.buf, m.s.toTicket(it, ""))
		}
	}
	if len(l.buf) == 0 {
		return nil, nil
	}
	return &l.buf[0], nil
}

func (l *scopeList) pop() {
	l.buf = l.buf[1:]
	l.offset++
}

func (l *scopeList) done() bool { return l.end && len(l.buf) == 0 }

// page takes the newest head of every list until the page is full or the
// lists end, dropping a head already listed (here, or at the previous
// page's last update time).
func (m *issueMerge) page(ctx context.Context) (trackerport.SearchPage, error) {
	seen := map[string]bool{}
	for _, k := range m.from.Keys {
		seen[k] = true
	}
	var out trackerport.SearchPage
	for len(out.Tickets) < m.per {
		var best *scopeList
		var next *trackerport.SourceTicket
		for _, l := range m.lists {
			h, err := m.head(ctx, l)
			for err == nil && h != nil && seen[h.Key] {
				l.pop()
				h, err = m.head(ctx, l)
			}
			if err != nil {
				return trackerport.SearchPage{}, err
			}
			if h != nil && (next == nil || h.Updated.After(next.Updated)) {
				best, next = l, h
			}
		}
		if next == nil {
			break
		}
		t := *next
		best.pop()
		seen[t.Key] = true
		out.Tickets = append(out.Tickets, t)
	}
	c := searchCursor{At: make([]int, len(m.lists))}
	more := false
	for i, l := range m.lists {
		c.At[i] = l.offset
		if l.done() {
			c.At[i] = -1
		} else {
			more = true
		}
	}
	if !more {
		return out, nil
	}
	c.Last = out.Tickets[len(out.Tickets)-1].Updated
	if c.Last.Equal(m.from.Last) {
		c.Keys = append(c.Keys, m.from.Keys...)
	}
	for _, t := range out.Tickets {
		if t.Updated.Equal(c.Last) {
			c.Keys = append(c.Keys, t.Key)
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return trackerport.SearchPage{}, err
	}
	out.Next = base64.RawURLEncoding.EncodeToString(raw)
	return out, nil
}
