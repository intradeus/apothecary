// Package ask is the Ask pane's conversation: a run's read-only session
// beside its stages, answering questions about the code as it stands
// (FLOWLANG §1 ask). It is not part of the run's story — no run events, its
// own rows — and its spend is the conversation's, never a run budget's.
package ask

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Thread is one of a run's Ask conversations (the pane calls it a session):
// the harness session it continues (empty until its first answer) and the
// model it runs on. A run holds any number of them.
type Thread struct {
	RunID     string    `json:"run_id"`
	ID        string    `json:"id"`
	SessionID string    `json:"session_id,omitempty"`
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Message roles.
const (
	RoleQuestion = "question"
	RoleAnswer   = "answer"
)

// Message is one turn of the conversation: a question someone asked, or
// the answer it got (with what that answer cost, or why there is none).
type Message struct {
	RunID string `json:"-"`
	// ThreadID is the thread the message belongs to.
	ThreadID string `json:"thread_id"`
	// Seq orders a run's messages, from 1, across all of its threads.
	Seq     int64     `json:"seq"`
	ID      string    `json:"id"`
	Role    string    `json:"role"`
	Text    string    `json:"text"`
	UserID  string    `json:"user_id,omitempty"` // a question's asker
	Model   string    `json:"model,omitempty"`   // an answer's model
	CostUSD float64   `json:"cost_usd,omitempty"`
	Turns   int       `json:"turns,omitempty"`
	Error   string    `json:"error,omitempty"` // an answer that failed
	At      time.Time `json:"at"`
}

// Store keeps threads and messages.
type Store interface {
	// AskThread is one of the run's threads, found or not.
	AskThread(ctx context.Context, runID, threadID string) (Thread, bool, error)
	// AskThreads lists the run's threads, oldest first (created_at, then id).
	AskThreads(ctx context.Context, runID string) ([]Thread, error)
	// SaveAskThread stores a thread whole, keyed by its run and id: its
	// created_at is stamped on the first save and kept, its updated_at on
	// every save.
	SaveAskThread(ctx context.Context, t Thread) error
	// AppendAskMessage stores a message of a thread, assigning its Seq (the
	// run's next) and, when zero, its At.
	AppendAskMessage(ctx context.Context, m Message) (Message, error)
	// AskMessages lists a run's messages after seq, oldest first.
	AskMessages(ctx context.Context, runID string, afterSeq int64) ([]Message, error)
}

// MemStore is an in-memory Store (tests, a daemon with no database). It keeps
// and refuses what Postgres does (asktest holds the two together).
type MemStore struct {
	mu       sync.Mutex
	threads  map[string]map[string]Thread // run → thread id → thread
	messages map[string][]Message
}

func NewMemStore() *MemStore {
	return &MemStore{threads: map[string]map[string]Thread{}, messages: map[string][]Message{}}
}

func (s *MemStore) AskThread(ctx context.Context, runID, threadID string) (Thread, bool, error) {
	if err := usable(ctx, runID, threadID); err != nil {
		return Thread{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[runID][threadID]
	return t, ok, nil
}

func (s *MemStore) AskThreads(ctx context.Context, runID string) ([]Thread, error) {
	if err := usable(ctx, runID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Thread
	for _, t := range s.threads[runID] {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *MemStore) SaveAskThread(ctx context.Context, t Thread) error {
	if err := usable(ctx, t.RunID, t.ID, t.SessionID, t.Model); err != nil {
		return err
	}
	if t.ID == "" {
		return fmt.Errorf("ask thread: no id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := stored(time.Now())
	t.CreatedAt, t.UpdatedAt = now, now
	if prev, ok := s.threads[t.RunID][t.ID]; ok {
		t.CreatedAt = prev.CreatedAt
	}
	if s.threads[t.RunID] == nil {
		s.threads[t.RunID] = map[string]Thread{}
	}
	s.threads[t.RunID][t.ID] = t
	return nil
}

func (s *MemStore) AppendAskMessage(ctx context.Context, m Message) (Message, error) {
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	if err := usable(ctx, m.RunID, m.ThreadID, m.ID, m.Role, m.Text, m.UserID, m.Model, m.Error); err != nil {
		return m, err
	}
	if m.ThreadID == "" {
		return m, fmt.Errorf("ask message: no thread")
	}
	if m.Role != RoleQuestion && m.Role != RoleAnswer {
		return m, fmt.Errorf("ask message: role %q", m.Role)
	}
	if m.Turns < math.MinInt32 || m.Turns > math.MaxInt32 {
		return m, fmt.Errorf("ask message: %d turns", m.Turns)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m.Seq = int64(len(s.messages[m.RunID])) + 1
	row := m
	row.At = stored(m.At)
	s.messages[m.RunID] = append(s.messages[m.RunID], row)
	return m, nil
}

func (s *MemStore) AskMessages(ctx context.Context, runID string, afterSeq int64) ([]Message, error) {
	if err := usable(ctx, runID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.messages[runID] {
		if m.Seq > afterSeq {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// usable refuses what never reaches a row: a dead context, and strings that
// are not text to Postgres.
func usable(ctx context.Context, values ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, v := range values {
		if strings.ContainsRune(v, 0) || !utf8.ValidString(v) {
			return fmt.Errorf("ask: invalid text %q", v)
		}
	}
	return nil
}

// stored is a time as a timestamptz column gives it back.
func stored(t time.Time) time.Time { return t.Truncate(time.Microsecond).Local() }

var _ Store = (*MemStore)(nil)
