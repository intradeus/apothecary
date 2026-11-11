package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Mem is the in-memory DB for tests. It mirrors pgstore, held to it by
// authtest: the unique indexes, the foreign keys onto users, and rows that
// come back as columns hand them back, sharing nothing with the caller.
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

// --- the columns ----------------------------------------------------------------

// instant is a time as a timestamptz column hands it back: whole microseconds,
// truncated, in the process's zone.
func instant(t time.Time) time.Time { return t.Truncate(time.Microsecond).Local() }

func optional(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := instant(*t)
	return &v
}

// storable refuses what a text column cannot hold.
func storable(fields ...string) error {
	for _, f := range fields {
		if !utf8.ValidString(f) || strings.ContainsRune(f, 0) {
			return fmt.Errorf("invalid byte sequence in %q", f)
		}
	}
	return nil
}

// sameLower is lower(a)=lower(b), what the indexes on handle and email compare.
// Not EqualFold: that folds ſ onto s, which lower() leaves alone.
func sameLower(a, b string) bool { return strings.ToLower(a) == strings.ToLower(b) }

func userRow(u User) User {
	u.DisabledAt, u.LastSeenAt = optional(u.DisabledAt), optional(u.LastSeenAt)
	u.CreatedAt, u.UpdatedAt = instant(u.CreatedAt), instant(u.UpdatedAt)
	return u
}

// nulEscape is a NUL in JSON text, which jsonb refuses. A backslash before it
// may itself be escaped, and then it is only text.
var nulEscape = regexp.MustCompile(`(^|[^\\])(\\\\)*\\u0000`)

// methodRow is a method through its columns: a nil secret or config is the
// NOT NULL default, and config is whatever survives jsonb.
func methodRow(me Method) (Method, error) {
	me.Secret = append([]byte{}, me.Secret...)
	cfg, err := json.Marshal(me.Config)
	if err != nil {
		return Method{}, err
	}
	if nulEscape.Match(cfg) {
		return Method{}, fmt.Errorf("method config: unsupported Unicode escape sequence")
	}
	me.Config = map[string]any{}
	if err := json.Unmarshal(cfg, &me.Config); err != nil {
		return Method{}, err
	}
	if me.Config == nil {
		me.Config = map[string]any{}
	}
	me.LastUsedAt = optional(me.LastUsedAt)
	me.CreatedAt, me.UpdatedAt = instant(me.CreatedAt), instant(me.UpdatedAt)
	return me, nil
}

// methodCopy is a stored row for a reader: it cannot fail, the row came
// through methodRow.
func methodCopy(me Method) Method {
	out, _ := methodRow(me)
	return out
}

func sessionRow(s Session) Session {
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = instant(s.CreatedAt), instant(s.ExpiresAt), instant(s.LastSeenAt)
	return s
}

func tokenRow(t Token) Token {
	t.CreatedAt = instant(t.CreatedAt)
	t.LastUsedAt, t.ExpiresAt = optional(t.LastUsedAt), optional(t.ExpiresAt)
	t.IdleSeconds = max(t.IdleSeconds, 0) // Postgres keeps no window as NULL
	return t
}

// sshKeyRow reads Type off the public key: there is no such column.
func sshKeyRow(k SSHKey) SSHKey {
	k.Type = ""
	if f := strings.Fields(k.PublicKey); len(f) > 0 {
		k.Type = f[0]
	}
	k.CreatedAt, k.LastUsedAt = instant(k.CreatedAt), optional(k.LastUsedAt)
	return k
}

// sortRows is ORDER BY created_at, id.
func sortRows[T any](rows []T, created func(T) time.Time, id func(T) string) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := created(rows[i]), created(rows[j])
		if !a.Equal(b) {
			return a.Before(b)
		}
		return id(rows[i]) < id(rows[j])
	})
}

// owner is the foreign key every credential table has onto users. It is no
// sentinel in Postgres either.
func (m *Mem) owner(row, userID string) error {
	if _, ok := m.users[userID]; !ok {
		return fmt.Errorf("%s: no user %s", row, userID)
	}
	return nil
}

// --- users ------------------------------------------------------------------

func (m *Mem) CountUsers(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

// conflicts checks the unique indexes in the order Postgres does: the whole
// handle index before any of the email one.
func (m *Mem) conflicts(u User) error {
	for _, o := range m.users {
		if o.ID != u.ID && sameLower(o.Handle, u.Handle) {
			return fmt.Errorf("handle %s: %w", u.Handle, ErrConflict)
		}
	}
	for _, o := range m.users {
		if o.ID != u.ID && sameLower(o.Email, u.Email) {
			return fmt.Errorf("email %s: %w", u.Email, ErrConflict)
		}
	}
	return nil
}

func (m *Mem) CreateUser(_ context.Context, u User) error {
	if err := storable(u.ID, u.Handle, u.Email, u.Name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.users[u.ID]; dup {
		return fmt.Errorf("user %s: %w", u.ID, ErrConflict)
	}
	if err := m.conflicts(u); err != nil {
		return err
	}
	m.users[u.ID] = userRow(u)
	return nil
}

// UpdateUser writes the profile: created_at and last_seen_at are not an
// update's to change, so a row read before a touch cannot lose it.
func (m *Mem) UpdateUser(_ context.Context, u User) error {
	if err := storable(u.ID, u.Handle, u.Email, u.Name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.users[u.ID]
	if !ok {
		return fmt.Errorf("user %s: %w", u.ID, ErrNotFound)
	}
	if err := m.conflicts(u); err != nil {
		return err
	}
	u.CreatedAt, u.LastSeenAt = old.CreatedAt, old.LastSeenAt
	m.users[u.ID] = userRow(u)
	return nil
}

func (m *Mem) GetUser(_ context.Context, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return User{}, fmt.Errorf("user %s: %w", id, ErrNotFound)
	}
	return userRow(u), nil
}

func (m *Mem) GetUserByLogin(_ context.Context, login string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if sameLower(u.Handle, login) || sameLower(u.Email, login) {
			return userRow(u), nil
		}
	}
	return User{}, fmt.Errorf("user %s: %w", login, ErrNotFound)
}

func (m *Mem) ListUsers(context.Context) ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []User
	for _, u := range m.users {
		out = append(out, userRow(u))
	}
	sortRows(out, func(u User) time.Time { return u.CreatedAt }, func(u User) string { return u.ID })
	return out, nil
}

func (m *Mem) TouchUser(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[id]; ok {
		u.LastSeenAt = optional(&at)
		m.users[id] = u
	}
	return nil
}

// --- methods ------------------------------------------------------------------

// PutMethod is the upsert on id: an existing row keeps its owner, kind and
// created_at. Either path can trip the two unique indexes.
func (m *Mem) PutMethod(_ context.Context, me Method) error {
	if err := storable(me.ID, me.UserID, me.Kind, me.Subject); err != nil {
		return err
	}
	me, err := methodRow(me)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.methods[me.ID]; ok {
		me.UserID, me.Kind, me.CreatedAt = old.UserID, old.Kind, old.CreatedAt
	} else if err := m.owner("method", me.UserID); err != nil {
		return err
	}
	for _, o := range m.methods {
		if o.ID == me.ID {
			continue
		}
		subject := me.Subject != "" && o.Kind == me.Kind && o.Subject == me.Subject
		password := me.Kind == KindPassword && o.Kind == KindPassword && o.UserID == me.UserID
		if subject || password {
			return fmt.Errorf("%s method: %w", me.Kind, ErrConflict)
		}
	}
	m.methods[me.ID] = me
	return nil
}

func (m *Mem) TouchMethod(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	me, ok := m.methods[id]
	if !ok {
		return fmt.Errorf("method %s: %w", id, ErrNotFound)
	}
	at = instant(at)
	me.LastUsedAt = &at
	m.methods[id] = me
	return nil
}

func (m *Mem) GetMethod(_ context.Context, userID, kind string) (Method, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []Method
	for _, me := range m.methods {
		if me.UserID == userID && me.Kind == kind {
			rows = append(rows, me)
		}
	}
	if len(rows) == 0 {
		return Method{}, fmt.Errorf("%s method: %w", kind, ErrNotFound)
	}
	return methodCopy(slices.MinFunc(rows, func(a, b Method) int { return a.CreatedAt.Compare(b.CreatedAt) })), nil
}

func (m *Mem) FindMethodBySubject(_ context.Context, kind, subject string) (Method, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, me := range m.methods {
		if me.Kind == kind && me.Subject == subject && subject != "" {
			return methodCopy(me), nil
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
			out = append(out, methodCopy(me))
		}
	}
	sortRows(out, func(me Method) time.Time { return me.CreatedAt }, func(me Method) string { return me.ID })
	return out, nil
}

func (m *Mem) DeleteMethod(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.methods, id)
	return nil
}

// --- sessions -----------------------------------------------------------------

func (m *Mem) CreateSession(_ context.Context, s Session) error {
	if err := storable(s.ID, s.UserID, s.UserAgent); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.sessions[s.ID]; dup {
		return fmt.Errorf("session: duplicate key")
	}
	if err := m.owner("session", s.UserID); err != nil {
		return err
	}
	m.sessions[s.ID] = sessionRow(s)
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
		s.LastSeenAt, s.ExpiresAt = instant(seen), instant(expires)
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

// --- tokens -------------------------------------------------------------------

func (m *Mem) CreateToken(_ context.Context, t Token) error {
	if err := storable(t.ID, t.UserID, t.Name, t.Hash, t.Prefix); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.tokens {
		if o.ID == t.ID || o.Hash == t.Hash {
			return fmt.Errorf("token %s: duplicate key", t.ID)
		}
	}
	if err := m.owner("token", t.UserID); err != nil {
		return err
	}
	m.tokens[t.ID] = tokenRow(t)
	return nil
}

func (m *Mem) GetTokenByHash(_ context.Context, hash string) (Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.Hash == hash {
			return tokenRow(t), nil
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
			out = append(out, tokenRow(t))
		}
	}
	sortRows(out, func(t Token) time.Time { return t.CreatedAt }, func(t Token) string { return t.ID })
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
		t.LastUsedAt = optional(&at)
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
	if err := storable(k.ID, k.UserID, k.Name, k.PublicKey, k.Fingerprint); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, have := range m.sshKeys {
		if have.ID == k.ID || have.Fingerprint == k.Fingerprint {
			return fmt.Errorf("ssh key %s: %w", k.Fingerprint, ErrConflict)
		}
	}
	if err := m.owner("ssh key", k.UserID); err != nil {
		return err
	}
	m.sshKeys[k.ID] = sshKeyRow(k)
	return nil
}

func (m *Mem) GetSSHKeyByFingerprint(_ context.Context, fingerprint string) (SSHKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.sshKeys {
		if k.Fingerprint == fingerprint {
			return sshKeyRow(k), nil
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
			out = append(out, sshKeyRow(k))
		}
	}
	sortRows(out, func(k SSHKey) time.Time { return k.CreatedAt }, func(k SSHKey) string { return k.ID })
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
		k.LastUsedAt = optional(&at)
		m.sshKeys[id] = k
	}
	return nil
}
