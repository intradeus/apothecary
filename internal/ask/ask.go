// Package ask is the Ask pane's conversation: a run's read-only session
// beside its stages, answering questions about the code as it stands
// (FLOWLANG §1 ask). It is not part of the run's story — no run events, its
// own rows — and its spend is the conversation's, never a run budget's.
package ask

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Thread is a run's Ask conversation: the harness session it continues
// (empty until the first answer) and the model it runs on.
type Thread struct {
	RunID     string    `json:"run_id"`
	SessionID string    `json:"session_id,omitempty"`
	Model     string    `json:"model,omitempty"`
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
	// Seq orders a run's messages, from 1.
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
	// AskThread is the run's thread, found or not.
	AskThread(ctx context.Context, runID string) (Thread, bool, error)
	SaveAskThread(ctx context.Context, t Thread) error
	// AppendAskMessage stores a message, assigning its Seq (the run's next)
	// and, when zero, its At.
	AppendAskMessage(ctx context.Context, m Message) (Message, error)
	// AskMessages lists a run's messages after seq, oldest first.
	AskMessages(ctx context.Context, runID string, afterSeq int64) ([]Message, error)
}

// MemStore is an in-memory Store (tests, a daemon with no database).
type MemStore struct {
	mu       sync.Mutex
	threads  map[string]Thread
	messages map[string][]Message
}

func NewMemStore() *MemStore {
	return &MemStore{threads: map[string]Thread{}, messages: map[string][]Message{}}
}

func (s *MemStore) AskThread(_ context.Context, runID string) (Thread, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[runID]
	return t, ok, nil
}

func (s *MemStore) SaveAskThread(_ context.Context, t Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = time.Now().UTC()
	}
	s.threads[t.RunID] = t
	return nil
}

func (s *MemStore) AppendAskMessage(_ context.Context, m Message) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.Seq = int64(len(s.messages[m.RunID])) + 1
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	s.messages[m.RunID] = append(s.messages[m.RunID], m)
	return m, nil
}

func (s *MemStore) AskMessages(_ context.Context, runID string, afterSeq int64) ([]Message, error) {
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

var _ Store = (*MemStore)(nil)
