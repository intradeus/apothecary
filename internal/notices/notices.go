// Package notices is the feed of problems with apothecary itself that a
// person must act on: a login that needs signing in again, a tracker that
// cannot be read or grew statuses to map, a channel that drops
// notifications, an image that does not build. They are not run pings (those
// belong to one run and live in its story); a notice belongs to a user, stays
// until that user dismisses it, and is persisted so a restart does not lose it.
package notices

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Notice is one problem raised to one user.
type Notice struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Kind   string `json:"kind"`
	// Key deduplicates: while a notice with the same (UserID, Key) is not
	// cleared, a repeat updates it instead of adding a row. Empty never
	// deduplicates.
	Key   string `json:"-"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Link  string `json:"link,omitempty"` // CPUI path, e.g. /system/providers
	// Count is how many times the problem was raised into this notice.
	Count      int       `json:"count"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	// ClearedAt is when the problem went away, re-arming its key; a cleared
	// notice stays listed until dismissed.
	ClearedAt   time.Time `json:"-"`
	DismissedAt time.Time `json:"-"`
}

var ErrNotFound = errors.New("notice not found")

// Store keeps notices.
type Store interface {
	// RaiseNotice inserts n (Count 1, CreatedAt = LastSeenAt = n.LastSeenAt)
	// unless n.Key != "" and n.UserID holds an uncleared notice with that key:
	// that row then takes n's Title, Body, Link and LastSeenAt, Count+1,
	// keeping its DismissedAt. inserted says which.
	RaiseNotice(ctx context.Context, n Notice) (stored Notice, inserted bool, err error)
	// ClearNotices clears every user's uncleared notices with key ("" is a no-op).
	ClearNotices(ctx context.Context, key string, at time.Time) error
	// ListNotices is userID's undismissed notices, LastSeenAt desc, then ID desc.
	ListNotices(ctx context.Context, userID string) ([]Notice, error)
	GetNotice(ctx context.Context, id string) (Notice, error) // ErrNotFound
	// DismissNotice hides a notice; already dismissed is a no-op. ErrNotFound.
	DismissNotice(ctx context.Context, id string, at time.Time) error
	DismissAllNotices(ctx context.Context, userID string, at time.Time) error
}

// MemStore is an in-memory Store (tests, a daemon with no database). It keeps
// and refuses what Postgres does (noticestest holds the two together), save
// that it knows no users: any user id is one.
type MemStore struct {
	mu   sync.Mutex
	rows []Notice
}

func NewMemStore() *MemStore { return &MemStore{} }

func (s *MemStore) RaiseNotice(ctx context.Context, n Notice) (Notice, bool, error) {
	if err := usable(ctx, n.ID, n.UserID, n.Kind, n.Key, n.Title, n.Body, n.Link); err != nil {
		return Notice{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.Key != "" {
		for i, r := range s.rows {
			if r.UserID == n.UserID && r.Key == n.Key && r.ClearedAt.IsZero() {
				r.Title, r.Body, r.Link = n.Title, n.Body, n.Link
				r.LastSeenAt = stored(n.LastSeenAt)
				r.Count++
				s.rows[i] = r
				return r, false, nil
			}
		}
	}
	for _, r := range s.rows {
		if r.ID == n.ID {
			return Notice{}, false, fmt.Errorf("notice %s: duplicate id", n.ID)
		}
	}
	n.Count = 1
	n.LastSeenAt = stored(n.LastSeenAt)
	n.CreatedAt = n.LastSeenAt
	n.ClearedAt, n.DismissedAt = time.Time{}, time.Time{}
	s.rows = append(s.rows, n)
	return n, true, nil
}

func (s *MemStore) ClearNotices(ctx context.Context, key string, at time.Time) error {
	if err := usable(ctx, key); err != nil || key == "" {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rows {
		if r.Key == key && r.ClearedAt.IsZero() {
			s.rows[i].ClearedAt = stored(at)
		}
	}
	return nil
}

func (s *MemStore) ListNotices(ctx context.Context, userID string) ([]Notice, error) {
	if err := usable(ctx, userID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Notice
	for _, r := range s.rows {
		if r.UserID == userID && r.DismissedAt.IsZero() {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeenAt.Equal(out[j].LastSeenAt) {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (s *MemStore) GetNotice(ctx context.Context, id string) (Notice, error) {
	if err := usable(ctx, id); err != nil {
		return Notice{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return Notice{}, fmt.Errorf("notice %s: %w", id, ErrNotFound)
}

func (s *MemStore) DismissNotice(ctx context.Context, id string, at time.Time) error {
	if err := usable(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rows {
		if r.ID == id {
			if r.DismissedAt.IsZero() {
				s.rows[i].DismissedAt = stored(at)
			}
			return nil
		}
	}
	return fmt.Errorf("notice %s: %w", id, ErrNotFound)
}

func (s *MemStore) DismissAllNotices(ctx context.Context, userID string, at time.Time) error {
	if err := usable(ctx, userID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rows {
		if r.UserID == userID && r.DismissedAt.IsZero() {
			s.rows[i].DismissedAt = stored(at)
		}
	}
	return nil
}

// usable refuses what never reaches a row: a dead context, and strings that
// are not text to Postgres.
func usable(ctx context.Context, values ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, v := range values {
		if strings.ContainsRune(v, 0) || !utf8.ValidString(v) {
			return fmt.Errorf("notices: invalid text %q", v)
		}
	}
	return nil
}

// stored is a time as a timestamptz column gives it back.
func stored(t time.Time) time.Time { return t.Truncate(time.Microsecond).Local() }

var _ Store = (*MemStore)(nil)
