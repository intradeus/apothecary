package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Service is the one path to accounts: it validates, hashes, mints ids and
// session/token secrets, and knows which authentication methods this build
// can verify.
type Service struct {
	db        DB
	verifiers map[string]Verifier

	// SessionTTL is how long a browser stays signed in without activity;
	// each request within it slides the expiry. Default 30 days.
	SessionTTL time.Duration

	now   func() time.Time
	newID func() string
}

// New builds a service over a DB with the password method registered.
func New(db DB) *Service {
	s := &Service{
		db:         db,
		verifiers:  map[string]Verifier{},
		SessionTTL: 30 * 24 * time.Hour,
		now:        time.Now,
		newID:      func() string { return ulid.Make().String() },
	}
	s.Register(PasswordVerifier{})
	return s
}

// Register adds a method kind this instance can verify.
func (s *Service) Register(v Verifier) { s.verifiers[v.Kind()] = v }

// Methods lists the kinds a person may sign in with, sorted.
func (s *Service) Methods() []string {
	out := make([]string, 0, len(s.verifiers))
	for k := range s.verifiers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DB exposes the store (the CLI's account commands).
func (s *Service) DB() DB { return s.db }

type invalidError struct{ msg string }

func (e invalidError) Error() string         { return e.msg }
func (invalidError) Is(target error) bool    { return target == ErrInvalid }
func invalidf(format string, a ...any) error { return invalidError{fmt.Sprintf(format, a...)} }

// --- setup ------------------------------------------------------------------

// SetupRequired reports a database with no account yet: the UI shows the
// setup page and nothing else answers.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	n, err := s.db.CountUsers(ctx)
	return n == 0, err
}

// NewUser is what creating an account takes.
type NewUser struct {
	Handle   string
	Email    string
	Name     string
	Password string
	Admin    bool
}

// Setup claims the instance: creates the first user as admin, with a
// password.
func (s *Service) Setup(ctx context.Context, in NewUser) (User, error) {
	n, err := s.db.CountUsers(ctx)
	if err != nil {
		return User{}, err
	}
	if n > 0 {
		return User{}, ErrSetupDone
	}
	in.Admin = true
	return s.CreateUser(ctx, in)
}

// --- accounts ---------------------------------------------------------------

var handleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,31}$`)

func normalizeUser(u *User) error {
	u.Handle = strings.ToLower(strings.TrimSpace(u.Handle))
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	u.Name = strings.TrimSpace(u.Name)
	if !handleRe.MatchString(u.Handle) {
		return invalidf("handle must be 2-32 lowercase letters, digits or dashes, starting with a letter or digit")
	}
	if u.Email == "" || !strings.Contains(u.Email, "@") || strings.ContainsAny(u.Email, " \t\n") {
		return invalidf("email must be an address")
	}
	if u.Name == "" {
		u.Name = u.Handle
	}
	return nil
}

// CreateUser makes an account with a password (admin: inviting a colleague).
func (s *Service) CreateUser(ctx context.Context, in NewUser) (User, error) {
	u := User{Handle: in.Handle, Email: in.Email, Name: in.Name, Admin: in.Admin}
	if err := normalizeUser(&u); err != nil {
		return User{}, err
	}
	if err := checkPassword(in.Password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	u.ID = s.newID()
	u.CreatedAt = s.now().UTC()
	u.UpdatedAt = u.CreatedAt
	if err := s.db.CreateUser(ctx, u); err != nil {
		return User{}, err
	}
	if err := s.db.PutMethod(ctx, Method{
		ID: s.newID(), UserID: u.ID, Kind: KindPassword, Secret: []byte(hash),
		CreatedAt: u.CreatedAt, UpdatedAt: u.CreatedAt,
	}); err != nil {
		return User{}, err
	}
	return u, nil
}

// Profile is what a user may change about themselves.
type Profile struct {
	Handle string
	Email  string
	Name   string
}

// UpdateProfile changes a user's handle, email or name.
func (s *Service) UpdateProfile(ctx context.Context, userID string, p Profile) (User, error) {
	u, err := s.db.GetUser(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if p.Handle != "" {
		u.Handle = p.Handle
	}
	if p.Email != "" {
		u.Email = p.Email
	}
	if p.Name != "" {
		u.Name = p.Name
	}
	if err := normalizeUser(&u); err != nil {
		return User{}, err
	}
	u.UpdatedAt = s.now().UTC()
	if err := s.db.UpdateUser(ctx, u); err != nil {
		return User{}, err
	}
	return u, nil
}

// SetAdmin grants or removes instance administration. The last admin
// cannot step down: someone must be able to manage accounts.
func (s *Service) SetAdmin(ctx context.Context, userID string, admin bool) (User, error) {
	u, err := s.db.GetUser(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if !admin && u.Admin {
		if err := s.checkNotLastAdmin(ctx, userID); err != nil {
			return User{}, err
		}
	}
	u.Admin = admin
	u.UpdatedAt = s.now().UTC()
	return u, s.db.UpdateUser(ctx, u)
}

// SetDisabled turns an account off (its sessions and tokens go with it) or
// back on. The last admin cannot be disabled.
func (s *Service) SetDisabled(ctx context.Context, userID string, disabled bool) (User, error) {
	u, err := s.db.GetUser(ctx, userID)
	if err != nil {
		return User{}, err
	}
	if disabled && u.Admin {
		if err := s.checkNotLastAdmin(ctx, userID); err != nil {
			return User{}, err
		}
	}
	now := s.now().UTC()
	if disabled {
		u.DisabledAt = &now
		if err := s.db.DeleteSessionsOf(ctx, userID); err != nil {
			return User{}, err
		}
		if err := s.db.DeleteTokensOf(ctx, userID); err != nil {
			return User{}, err
		}
	} else {
		u.DisabledAt = nil
	}
	u.UpdatedAt = now
	return u, s.db.UpdateUser(ctx, u)
}

func (s *Service) checkNotLastAdmin(ctx context.Context, userID string) error {
	all, err := s.db.ListUsers(ctx)
	if err != nil {
		return err
	}
	for _, o := range all {
		if o.ID != userID && o.Admin && !o.Disabled() {
			return nil
		}
	}
	return invalidf("this is the last admin: make someone else an admin first")
}

func (s *Service) GetUser(ctx context.Context, id string) (User, error) { return s.db.GetUser(ctx, id) }
func (s *Service) ListUsers(ctx context.Context) ([]User, error)        { return s.db.ListUsers(ctx) }

// GetUserByLogin finds a user by handle or email.
func (s *Service) GetUserByLogin(ctx context.Context, login string) (User, error) {
	return s.db.GetUserByLogin(ctx, strings.ToLower(strings.TrimSpace(login)))
}

// --- passwords ----------------------------------------------------------------

// SetPassword replaces a user's password (an admin reset, or the CLI). Every
// session is signed out: whoever held the old password holds none of them.
func (s *Service) SetPassword(ctx context.Context, userID, password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	if _, err := s.db.GetUser(ctx, userID); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	m, err := s.db.GetMethod(ctx, userID, KindPassword)
	if errors.Is(err, ErrNotFound) {
		m = Method{ID: s.newID(), UserID: userID, Kind: KindPassword, CreatedAt: now}
	} else if err != nil {
		return err
	}
	m.Secret = []byte(hash)
	m.UpdatedAt = now
	if err := s.db.PutMethod(ctx, m); err != nil {
		return err
	}
	return s.db.DeleteSessionsOf(ctx, userID)
}

// ChangePassword is a user changing their own: the current one must check
// out first.
func (s *Service) ChangePassword(ctx context.Context, userID, current, next string) error {
	m, err := s.db.GetMethod(ctx, userID, KindPassword)
	if errors.Is(err, ErrNotFound) {
		return invalidf("this account has no password; ask an admin to set one")
	}
	if err != nil {
		return err
	}
	if err := VerifyPassword(string(m.Secret), current); err != nil {
		return invalidf("the current password is wrong")
	}
	return s.SetPassword(ctx, userID, next)
}

// ListMethods is a user's authentication methods, secrets stripped.
func (s *Service) ListMethods(ctx context.Context, userID string) ([]Method, error) {
	ms, err := s.db.ListMethods(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range ms {
		ms[i].Secret = nil
	}
	return ms, nil
}

// --- login & sessions ---------------------------------------------------------

// Login verifies a proof of the given kind for the account named by login
// (handle or email). A wrong login and a wrong proof fail the same way.
func (s *Service) Login(ctx context.Context, kind, login, proof string) (User, error) {
	v, ok := s.verifiers[kind]
	if !ok {
		return User{}, invalidf("unknown authentication method %q", kind)
	}
	u, err := s.db.GetUserByLogin(ctx, strings.ToLower(strings.TrimSpace(login)))
	if errors.Is(err, ErrNotFound) {
		// Burn the same time a real verification would, so a missing
		// account is not distinguishable by latency.
		_ = VerifyPassword(dummyHash, proof)
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, err
	}
	m, err := s.db.GetMethod(ctx, u.ID, kind)
	if errors.Is(err, ErrNotFound) {
		_ = VerifyPassword(dummyHash, proof)
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, err
	}
	if err := v.Verify(m, proof); err != nil {
		return User{}, ErrUnauthorized
	}
	if u.Disabled() {
		return User{}, ErrDisabled
	}
	now := s.now().UTC()
	m.LastUsedAt = &now
	_ = s.db.PutMethod(ctx, m)
	return u, nil
}

// dummyHash is verified against when there is no account, to equalise
// timing. A hash of nothing in particular.
var dummyHash = func() string {
	h, _ := HashPassword("nobody")
	return h
}()

// NewSession signs a user in: the returned value goes into the cookie, the
// session row keeps only its hash.
func (s *Service) NewSession(ctx context.Context, userID, userAgent string) (string, Session, error) {
	raw, err := randomSecret(32)
	if err != nil {
		return "", Session{}, err
	}
	now := s.now().UTC()
	sess := Session{
		ID: hashSecret(raw), UserID: userID,
		CreatedAt: now, ExpiresAt: now.Add(s.SessionTTL), LastSeenAt: now,
		UserAgent: userAgent,
	}
	if err := s.db.CreateSession(ctx, sess); err != nil {
		return "", Session{}, err
	}
	return raw, sess, nil
}

// AuthenticateSession resolves a cookie value to its principal, sliding the
// expiry. An expired or unknown session is ErrUnauthorized.
func (s *Service) AuthenticateSession(ctx context.Context, cookie string) (Principal, error) {
	if cookie == "" {
		return Principal{}, ErrUnauthorized
	}
	sess, err := s.db.GetSession(ctx, hashSecret(cookie))
	if errors.Is(err, ErrNotFound) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	now := s.now().UTC()
	if !now.Before(sess.ExpiresAt) {
		_ = s.db.DeleteSession(ctx, sess.ID)
		return Principal{}, ErrUnauthorized
	}
	u, err := s.db.GetUser(ctx, sess.UserID)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	if u.Disabled() {
		return Principal{}, ErrDisabled
	}
	// Slide the expiry at most once a minute: every poll touching the row
	// would be a write per request for nothing.
	if now.Sub(sess.LastSeenAt) > time.Minute {
		sess.LastSeenAt, sess.ExpiresAt = now, now.Add(s.SessionTTL)
		_ = s.db.TouchSession(ctx, sess.ID, sess.LastSeenAt, sess.ExpiresAt)
		_ = s.db.TouchUser(ctx, u.ID, now)
		u.LastSeenAt = &now
	}
	return Principal{User: u, Session: &sess}, nil
}

// Logout ends the session behind a cookie value.
func (s *Service) Logout(ctx context.Context, cookie string) error {
	if cookie == "" {
		return nil
	}
	err := s.db.DeleteSession(ctx, hashSecret(cookie))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// --- personal tokens ----------------------------------------------------------

// TokenPrefix marks a personal token, so one is recognisable in a config
// file or a log, and never mistaken for a run token.
const TokenPrefix = "apo_"

// NewToken mints a personal token. The secret is returned once; the row
// keeps its hash.
func (s *Service) NewToken(ctx context.Context, userID, name string, expires *time.Time) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Token{}, invalidf("token name is required")
	}
	if _, err := s.db.GetUser(ctx, userID); err != nil {
		return "", Token{}, err
	}
	raw, err := randomSecret(32)
	if err != nil {
		return "", Token{}, err
	}
	secret := TokenPrefix + raw
	t := Token{
		ID: s.newID(), UserID: userID, Name: name,
		Hash: hashSecret(secret), Prefix: secret[:len(TokenPrefix)+6],
		CreatedAt: s.now().UTC(), ExpiresAt: expires,
	}
	if err := s.db.CreateToken(ctx, t); err != nil {
		return "", Token{}, err
	}
	return secret, t, nil
}

// AuthenticateToken resolves a bearer secret to its principal.
func (s *Service) AuthenticateToken(ctx context.Context, secret string) (Principal, error) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return Principal{}, ErrUnauthorized
	}
	t, err := s.db.GetTokenByHash(ctx, hashSecret(secret))
	if errors.Is(err, ErrNotFound) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, err
	}
	now := s.now().UTC()
	if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return Principal{}, ErrUnauthorized
	}
	u, err := s.db.GetUser(ctx, t.UserID)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	if u.Disabled() {
		return Principal{}, ErrDisabled
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute {
		_ = s.db.TouchToken(ctx, t.ID, now)
		_ = s.db.TouchUser(ctx, u.ID, now)
		t.LastUsedAt = &now
	}
	return Principal{User: u, Token: &t}, nil
}

func (s *Service) ListTokens(ctx context.Context, userID string) ([]Token, error) {
	return s.db.ListTokens(ctx, userID)
}

func (s *Service) DeleteToken(ctx context.Context, userID, id string) error {
	return s.db.DeleteToken(ctx, userID, id)
}

// --- secrets ------------------------------------------------------------------

func randomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSecret is how sessions and tokens are stored: a leaked table yields
// nothing usable.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
