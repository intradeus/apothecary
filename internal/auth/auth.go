// Package auth is who a request is: user profiles, the ways a user proves
// who they are (authentication methods: a password today; OAuth and
// passkeys are more rows of the same table), browser sessions and personal
// API tokens.
//
// A user owns everything they configure — integrations, projects, tickets,
// runs, channels, settings — and sees nothing of anyone else's. There is no
// organisation level and no role matrix: the one distinction is the
// instance admin, who manages accounts and nothing more.
package auth

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrInvalid marks validation failures (API maps to 400).
	ErrInvalid = errors.New("invalid")
	// ErrConflict is a taken handle or email (409).
	ErrConflict = errors.New("already in use")
	// ErrUnauthorized is a credential that does not check out (401). The
	// message never says which half was wrong.
	ErrUnauthorized = errors.New("invalid credentials")
	// ErrDisabled refuses a disabled account (403).
	ErrDisabled = errors.New("account disabled")
	// ErrSetupDone refuses a second instance setup (409).
	ErrSetupDone = errors.New("setup already done")
)

// User is an account: the profile, and whether it administers the instance.
type User struct {
	ID string `json:"id"`
	// Handle is the short unique name (lowercase slug): what URLs and
	// mentions use, and what a person can sign in with beside their email.
	Handle string `json:"handle"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	// Admin manages accounts. It grants no view into other users' work.
	Admin      bool       `json:"admin"`
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Disabled reports an account that may not sign in or act.
func (u User) Disabled() bool { return u.DisabledAt != nil }

// Method kinds. A kind is the shape of a proof: how a Verifier checks it and
// what the row stores. Every kind shares one table, so adding one is a
// Verifier and a login path, never a schema change.
const (
	KindPassword = "password"
)

// Method is one way a user can authenticate.
type Method struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Kind   string `json:"kind"`
	// Subject is the identity on the other side for kinds that have one
	// (an OAuth issuer+subject, a passkey credential id); empty for a
	// password. Unique per kind when set.
	Subject string `json:"subject,omitempty"`
	// Secret is what the verifier needs: the password hash, a passkey's
	// public key. Never serialised.
	Secret []byte `json:"-"`
	// Config is the kind's non-secret extras (an OAuth provider's name, a
	// passkey's display label).
	Config     map[string]any `json:"config,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	LastUsedAt *time.Time     `json:"last_used_at,omitempty"`
}

// Session is one signed-in browser. ID is the hash of the cookie value; the
// value itself exists only in the cookie.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
}

// Token is a personal API token (curl, scripts, automations outside the
// UI). Hash is the hash of the secret; Prefix its first characters, for
// telling tokens apart in a list.
type Token struct {
	ID         string     `json:"id"`
	UserID     string     `json:"-"`
	Name       string     `json:"name"`
	Hash       string     `json:"-"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// DB is the persistence boundary, implemented by pgstore and by Mem.
type DB interface {
	CountUsers(ctx context.Context) (int, error)
	CreateUser(ctx context.Context, u User) error
	UpdateUser(ctx context.Context, u User) error
	GetUser(ctx context.Context, id string) (User, error)
	// GetUserByLogin finds a user by handle or email (case-insensitive).
	GetUserByLogin(ctx context.Context, login string) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	TouchUser(ctx context.Context, id string, at time.Time) error

	PutMethod(ctx context.Context, m Method) error
	// GetMethod is the user's method of a kind that has at most one per
	// user (a password).
	GetMethod(ctx context.Context, userID, kind string) (Method, error)
	FindMethodBySubject(ctx context.Context, kind, subject string) (Method, error)
	ListMethods(ctx context.Context, userID string) ([]Method, error)
	DeleteMethod(ctx context.Context, id string) error

	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	TouchSession(ctx context.Context, id string, seen, expires time.Time) error
	DeleteSession(ctx context.Context, id string) error
	DeleteSessionsOf(ctx context.Context, userID string) error

	CreateToken(ctx context.Context, t Token) error
	GetTokenByHash(ctx context.Context, hash string) (Token, error)
	ListTokens(ctx context.Context, userID string) ([]Token, error)
	DeleteToken(ctx context.Context, userID, id string) error
	TouchToken(ctx context.Context, id string, at time.Time) error
	DeleteTokensOf(ctx context.Context, userID string) error
}

// Verifier checks a proof against a stored method of its kind.
type Verifier interface {
	Kind() string
	// Verify returns ErrUnauthorized when the proof does not match.
	Verify(m Method, proof string) error
}
