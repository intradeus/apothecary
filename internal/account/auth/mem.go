package auth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mem is the in-memory DB for tests.
type Mem struct {
	mu       sync.Mutex
	users    map[string]User
	methods  map[string]Method
	sessions map[string]Session
	tokens   map[string]Token
	sshKeys  map[string]SSHKey
}

func NewMem() *Mem {
	return &Mem{
		users:    map[string]User{},
		methods:  map[string]Method{},
		sessions: map[string]Session{},
		tokens:   map[string]Token{},
		sshKeys:  map[string]SSHKey{},
	}
}

func (m *Mem) CountUsers(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *Mem) conflicts(u User) error {
	for _, o := range m.users {
		if o.ID == u.ID {
			continue
		}
		if o.Handle == u.Handle {
			return fmt.Errorf("handle %s: %w", u.Handle, ErrConflict)
		}
		if o.Email == u.Email {
			return fmt.Errorf("email %s: %w", u.Email, ErrConflict)
		}
	}
	return nil
}

func (m *Mem) CreateUser(_ context.Context, u User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.conflicts(u); err != nil {
		return err
	}
	m.users[u.ID] = u
	return nil
}

func (m *Mem) UpdateUser(_ context.Context, u User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[u.ID]; !ok {
		return fmt.Errorf("user %s: %w", u.ID, ErrNotFound)
	}
	if err := m.conflicts(u); err != nil {
		return err
	}
	m.users[u.ID] = u
	return nil
}

func (m *Mem) GetUser(_ context.Context, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return User{}, fmt.Errorf("user %s: %w", id, ErrNotFound)
	}
	return u, nil
}

func (m *Mem) GetUserByLogin(_ context.Context, login string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if strings.EqualFold(u.Handle, login) || strings.EqualFold(u.Email, login) {
			return u, nil
		}
	}
	return User{}, fmt.Errorf("user %s: %w", login, ErrNotFound)
}

func (m *Mem) ListUsers(context.Context) ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]User, 0, len(m.users))
	for _, u := range m.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) TouchUser(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[id]; ok {
		u.LastSeenAt = &at
		m.users[id] = u
	}
	return nil
}

func (m *Mem) PutMethod(_ context.Context, me Method) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.methods[me.ID] = me
	return nil
}

func (m *Mem) GetMethod(_ context.Context, userID, kind string) (Method, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, me := range m.methods {
		if me.UserID == userID && me.Kind == kind {
			return me, nil
		}
	}
	return Method{}, fmt.Errorf("%s method: %w", kind, ErrNotFound)
}

func (m *Mem) FindMethodBySubject(_ context.Context, kind, subject string) (Method, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, me := range m.methods {
		if me.Kind == kind && me.Subject == subject && subject != "" {
			return me, nil
		}
	}
	return Method{}, fmt.Errorf("%s method: %w", kind, ErrNotFound)
}

func (m *Mem) ListMethods(_ context.Context, userID string) ([]Method, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Method
	for _, me := range m.methods {
		if me.UserID == userID {
			out = append(out, me)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteMethod(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.methods, id)
	return nil
}

func (m *Mem) CreateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.ID] = s
	return nil
}

func (m *Mem) GetSession(_ context.Context, id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Session{}, fmt.Errorf("session: %w", ErrNotFound)
	}
	return s, nil
}

func (m *Mem) TouchSession(_ context.Context, id string, seen, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.LastSeenAt, s.ExpiresAt = seen, expires
		m.sessions[id] = s
	}
	return nil
}

func (m *Mem) DeleteSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return fmt.Errorf("session: %w", ErrNotFound)
	}
	delete(m.sessions, id)
	return nil
}

func (m *Mem) DeleteSessionsOf(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		if s.UserID == userID {
			delete(m.sessions, id)
		}
	}
	return nil
}

func (m *Mem) CreateToken(_ context.Context, t Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[t.ID] = t
	return nil
}

func (m *Mem) GetTokenByHash(_ context.Context, hash string) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.Hash == hash {
			return t, nil
		}
	}
	return Token{}, fmt.Errorf("token: %w", ErrNotFound)
}

func (m *Mem) ListTokens(_ context.Context, userID string) ([]Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Token
	for _, t := range m.tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteToken(_ context.Context, userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok || t.UserID != userID {
		return fmt.Errorf("token %s: %w", id, ErrNotFound)
	}
	delete(m.tokens, id)
	return nil
}

func (m *Mem) TouchToken(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[id]; ok {
		t.LastUsedAt = &at
		m.tokens[id] = t
	}
	return nil
}

func (m *Mem) DeleteTokensOf(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.tokens {
		if t.UserID == userID {
			delete(m.tokens, id)
		}
	}
	return nil
}

var _ DB = (*Mem)(nil)

// --- ssh keys -----------------------------------------------------------------

func (m *Mem) CreateSSHKey(_ context.Context, k SSHKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, have := range m.sshKeys {
		if have.Fingerprint == k.Fingerprint {
			return fmt.Errorf("ssh key %s: %w", k.Fingerprint, ErrConflict)
		}
	}
	m.sshKeys[k.ID] = k
	return nil
}

func (m *Mem) GetSSHKeyByFingerprint(_ context.Context, fingerprint string) (SSHKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.sshKeys {
		if k.Fingerprint == fingerprint {
			return k, nil
		}
	}
	return SSHKey{}, fmt.Errorf("ssh key: %w", ErrNotFound)
}

func (m *Mem) ListSSHKeys(_ context.Context, userID string) ([]SSHKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SSHKey
	for _, k := range m.sshKeys {
		if k.UserID == userID {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Mem) DeleteSSHKey(_ context.Context, userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.sshKeys[id]
	if !ok || k.UserID != userID {
		return fmt.Errorf("ssh key %s: %w", id, ErrNotFound)
	}
	delete(m.sshKeys, id)
	return nil
}

func (m *Mem) TouchSSHKey(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.sshKeys[id]; ok {
		k.LastUsedAt = &at
		m.sshKeys[id] = k
	}
	return nil
}
