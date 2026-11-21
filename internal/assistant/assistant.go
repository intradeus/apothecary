// Package assistant is a person's assistant (docs/assistant-plan.md): a
// person asks, an agent in their own assistant pod answers. A session on
// a page (a flow, a ticket, a skill, a run, a dashboard) fills the page's
// inputs: the engine checks a fill and the page writes it into its form or
// editor; nothing is saved until the person saves. The general session,
// the assistant button's on every page, fills nothing and acts instead,
// by requests the person confirms.
//
// A session belongs to a kind and a subject (that flow, ticket or skill,
// or a draft id before the first save). It reaches the pod through
// assistantport, the person's harness, their workshops and their skills
// through the interfaces below; archtest holds it off the engine, the API
// and the adapters.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"apothecary/internal/run/event"
	"apothecary/internal/skills"
)

// Kind is what a session fills.
type Kind string

const (
	KindFlow      Kind = "flow"
	KindTicket    Kind = "ticket"
	KindSkill     Kind = "skill"
	KindRun       Kind = "run"
	KindDashboard Kind = "dashboard"
	// KindGeneral is no page: the assistant button's, which acts and
	// fills nothing.
	KindGeneral Kind = "general"
)

// Kinds are the pages the assistant serves.
var Kinds = []Kind{KindFlow, KindTicket, KindSkill, KindRun, KindDashboard, KindGeneral}

// Valid reports a kind the assistant serves.
func (k Kind) Valid() bool { return slices.Contains(Kinds, k) }

// Session is one conversation, about one subject.
type Session struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Kind   Kind   `json:"kind"`
	// Workshop is the workshop the session's subject lives in (a flow's, a
	// ticket's) or that a skill's session reads; "" for none.
	Workshop string `json:"workshop,omitempty"`
	// Subject is the flow's name, the ticket's key, the skill's id, or a
	// draft id before its first save.
	Subject string `json:"subject"`
	// HarnessSession is the harness's own session id, loaded again in a new
	// process; Integration the harness integration it runs on.
	HarnessSession string `json:"-"`
	Integration    string `json:"integration,omitempty"`
	Model          string `json:"model,omitempty"`
	// TokenID is the session's token on the engine's MCP face: the one
	// principal that may fill this session.
	TokenID string `json:"-"`
	// Skillsets are the skillset versions the session's turns install,
	// fixed when it opened from the person's choice.
	Skillsets []event.SkillsetPin `json:"skillsets,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// Roles of a message.
const (
	RolePerson    = "person"
	RoleAssistant = "assistant"
)

// Message is one turn's words: the person's question or the assistant's
// answer, with the fill it made, if any.
type Message struct {
	SessionID string          `json:"session_id"`
	Seq       int64           `json:"seq"`
	Role      string          `json:"role"`
	Text      string          `json:"text"`
	Fill      json.RawMessage `json:"fill,omitempty"`
	// Request is what the answer asked the person to confirm, and what came
	// of it.
	Request *Request  `json:"request,omitempty"`
	Error   string    `json:"error,omitempty"`
	CostUSD float64   `json:"cost_usd,omitempty"`
	At      time.Time `json:"at"`
}

// Request states.
const (
	RequestPending = "pending"
	// RequestRunning: confirmed, its acts running. One left so (the engine
	// stopped mid-way) is never run again: what it did is unknown.
	RequestRunning  = "running"
	RequestDone     = "done"
	RequestDeclined = "declined"
)

// ErrStale is a request no longer in the state a change expected: decided,
// or being decided, meanwhile.
var ErrStale = errors.New("assistant: the request changed meanwhile")

// Request is acts an answer asks the person to confirm (docs/assistant-plan.md,
// phase 5): the assistant's token changes nothing, so each act is one of the
// MCP face's write tools that the person's click runs, as the person.
type Request struct {
	// Why is the assistant's one line for the person.
	Why   string `json:"why"`
	Acts  []Act  `json:"acts"`
	State string `json:"state"`
	// Reported: the outcome was put in a later prompt.
	Reported bool `json:"reported,omitempty"`
}

// Act is one tool call a request holds, and its outcome once confirmed.
type Act struct {
	Tool string `json:"tool"`
	// Title is what the tool does, for the person.
	Title     string          `json:"title,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
	// Status is the route's answer (0: not run, an earlier act failed);
	// Result its text, cut.
	Status int    `json:"status,omitempty"`
	Result string `json:"result,omitempty"`
}

// Choice is a person's standing choice: the harness integration their
// sessions run on, the model a new session starts with, and the skillsets
// (theirs; no seq is the latest) a new session installs.
type Choice struct {
	UserID      string       `json:"-"`
	Integration string       `json:"integration"`
	Model       string       `json:"model"`
	Skillsets   []skills.Ref `json:"skillsets,omitempty"`
}

// Skills is the person's skill library, as a session installs it.
type Skills interface {
	// ResolveSkillsets pins refs to versions: each skillset must be
	// person's own. A ref that does not resolve (the set deleted or
	// another's, the version gone) is an error wrapping ErrSkillsetGone;
	// any other error is the library failing to answer.
	ResolveSkillsets(ctx context.Context, person string, refs []skills.Ref) ([]event.SkillsetPin, error)
	// SkillTrees are the pins' skills, each once, with their files.
	SkillTrees(ctx context.Context, pins []event.SkillsetPin) ([]skills.Tree, error)
}

// ErrSkillsetGone is a skillset ref the library no longer resolves.
var ErrSkillsetGone = errors.New("assistant: no such skillset")

// Store keeps sessions, their messages and persons' choices.
type Store interface {
	AssistantSession(ctx context.Context, id string) (Session, bool, error)
	// FindAssistantSession is a person's latest session about a subject.
	FindAssistantSession(ctx context.Context, userID string, kind Kind, workshop, subject string) (Session, bool, error)
	AssistantSessions(ctx context.Context, userID string) ([]Session, error)
	SaveAssistantSession(ctx context.Context, s Session) error
	// AppendAssistantMessage numbers m after the session's last.
	AppendAssistantMessage(ctx context.Context, m Message) (Message, error)
	AssistantMessages(ctx context.Context, sessionID string, afterSeq int64) ([]Message, error)
	// SetAssistantRequest replaces a message's request (a decision, an
	// outcome reported) when it is in state from, else ErrStale: the state
	// moves once, whoever else tries. A message that has none is ErrInvalid.
	SetAssistantRequest(ctx context.Context, sessionID string, seq int64, from string, r Request) error
	AssistantChoice(ctx context.Context, userID string) (Choice, error)
	SaveAssistantChoice(ctx context.Context, c Choice) error
}

// ErrInvalid is a refused value.
var ErrInvalid = errors.New("assistant: invalid")

// MemStore is Store in memory: the tests', and a server's with no database.
type MemStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	messages map[string][]Message
	choices  map[string]Choice
}

func NewMemStore() *MemStore {
	return &MemStore{sessions: map[string]Session{}, messages: map[string][]Message{}, choices: map[string]Choice{}}
}

func (s *MemStore) AssistantSession(_ context.Context, id string) (Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[id]
	return v, ok, nil
}

func (s *MemStore) FindAssistantSession(_ context.Context, userID string, kind Kind, workshop, subject string) (Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best Session
	found := false
	for _, v := range s.sessions {
		if v.UserID == userID && v.Kind == kind && v.Workshop == workshop && v.Subject == subject &&
			(!found || v.UpdatedAt.After(best.UpdatedAt) || (v.UpdatedAt.Equal(best.UpdatedAt) && v.ID > best.ID)) {
			best, found = v, true
		}
	}
	return best, found, nil
}

func (s *MemStore) AssistantSessions(_ context.Context, userID string) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, v := range s.sessions {
		if v.UserID == userID {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b Session) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out, nil
}

func (s *MemStore) SaveAssistantSession(_ context.Context, v Session) error {
	if err := CheckSession(v); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := stored(time.Now())
	if old, ok := s.sessions[v.ID]; ok {
		if old.UserID != v.UserID {
			return errors.Join(ErrInvalid, errors.New("a session changes owner"))
		}
		v.CreatedAt = old.CreatedAt
	} else {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	v.Skillsets = slices.Clone(v.Skillsets)
	s.sessions[v.ID] = v
	return nil
}

func (s *MemStore) AppendAssistantMessage(_ context.Context, m Message) (Message, error) {
	if err := CheckMessage(m); err != nil {
		return Message{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[m.SessionID]; !ok {
		return Message{}, errors.Join(ErrInvalid, errors.New("no session "+m.SessionID))
	}
	list := s.messages[m.SessionID]
	m.Seq = int64(len(list)) + 1
	if m.At.IsZero() {
		m.At = time.Now()
	}
	m.At = stored(m.At)
	if m.Request != nil {
		cp := cloneRequest(*m.Request)
		m.Request = &cp
	}
	s.messages[m.SessionID] = append(list, m)
	return m, nil
}

func (s *MemStore) AssistantMessages(_ context.Context, sessionID string, afterSeq int64) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.messages[sessionID] {
		if m.Seq > afterSeq {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *MemStore) SetAssistantRequest(_ context.Context, sessionID string, seq int64, from string, r Request) error {
	if err := CheckRequest(r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.messages[sessionID]
	for i := range list {
		if list[i].Seq == seq {
			if list[i].Request == nil {
				return errors.Join(ErrInvalid, errors.New("the message holds no request"))
			}
			if list[i].Request.State != from {
				return errors.Join(ErrStale, fmt.Errorf("the request is %s, not %s", list[i].Request.State, from))
			}
			cp := cloneRequest(r)
			list[i].Request = &cp
			return nil
		}
	}
	return errors.Join(ErrInvalid, fmt.Errorf("no message %d in session %s", seq, sessionID))
}

func cloneRequest(r Request) Request {
	r.Acts = slices.Clone(r.Acts)
	return r
}

func (s *MemStore) AssistantChoice(_ context.Context, userID string) (Choice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.choices[userID]
	c.UserID = userID
	return c, nil
}

func (s *MemStore) SaveAssistantChoice(_ context.Context, c Choice) error {
	if c.UserID == "" {
		return errors.Join(ErrInvalid, errors.New("a choice needs its person"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Skillsets = slices.Clone(c.Skillsets)
	s.choices[c.UserID] = c
	return nil
}

// CheckSession refuses a session the stores could not keep faithfully.
func CheckSession(v Session) error {
	if v.ID == "" || v.UserID == "" || !v.Kind.Valid() || v.Subject == "" {
		return errors.Join(ErrInvalid, errors.New("a session needs an id, a person, a kind and a subject"))
	}
	for _, s := range []string{v.ID, v.UserID, v.Workshop, v.Subject, v.HarnessSession, v.Integration, v.Model, v.TokenID} {
		if strings.ContainsRune(s, 0) {
			return errors.Join(ErrInvalid, errors.New("a NUL byte"))
		}
	}
	return nil
}

// CheckMessage refuses a message the stores could not keep faithfully.
func CheckMessage(m Message) error {
	if m.SessionID == "" || (m.Role != RolePerson && m.Role != RoleAssistant) {
		return errors.Join(ErrInvalid, errors.New("a message needs its session and a role"))
	}
	if strings.ContainsRune(m.Text, 0) || strings.ContainsRune(m.Error, 0) {
		return errors.Join(ErrInvalid, errors.New("a NUL byte"))
	}
	if len(m.Fill) > 0 && !json.Valid(m.Fill) {
		return errors.Join(ErrInvalid, errors.New("a fill that is not JSON"))
	}
	if m.Request != nil {
		return CheckRequest(*m.Request)
	}
	return nil
}

// CheckRequest refuses a request the stores could not keep faithfully.
func CheckRequest(r Request) error {
	if len(r.Acts) == 0 || (r.State != RequestPending && r.State != RequestRunning && r.State != RequestDone && r.State != RequestDeclined) {
		return errors.Join(ErrInvalid, errors.New("a request needs its acts and a state"))
	}
	if strings.ContainsRune(r.Why, 0) {
		return errors.Join(ErrInvalid, errors.New("a NUL byte"))
	}
	for _, a := range r.Acts {
		if a.Tool == "" || !json.Valid(a.Arguments) || strings.ContainsRune(a.Result, 0) || strings.ContainsRune(a.Title, 0) {
			return errors.Join(ErrInvalid, errors.New("an act needs its tool and JSON arguments"))
		}
	}
	return nil
}

// stored is a time as the database keeps it.
func stored(t time.Time) time.Time { return t.Truncate(time.Microsecond).UTC() }
