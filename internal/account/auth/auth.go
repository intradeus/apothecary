// Package auth is who a request is: user profiles, the ways a user proves
// who they are (authentication methods: a password today; OAuth and
// passkeys are more rows of the same table), browser sessions and personal
// API tokens.
//
// A user owns everything they configure — integrations, projects, tickets,
// runs, channels, settings. What else they may see and do — a workshop
// someone shared with them, the instance's administration, what a scoped
// token leaves of their reach — is package authz's to decide: the instance
// role and a token's grant live here, the policies there. There is no
// organisation level.
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
	// ErrLastAdmin refuses taking admin away from the last enabled admin
	// (400): someone must be able to manage accounts. It is an ErrInvalid.
	ErrLastAdmin error = invalidError{"this is the last admin: make someone else an admin first"}
)

// Role is an account's instance role.
type Role string

const (
	// RoleAdmin is a member who also administers the instance: accounts,
	// limits, the provisioner. It grants no view into other users' work.
	RoleAdmin Role = "admin"
	// RoleMember keeps workshops and configuration of its own.
	RoleMember Role = "member"
	// RoleGuest has only what is shared with it: it creates no workshop,
	// integration, credential, skill or channel, and an account demoted to
	// it loses the ones it owns (hidden, or frozen for its configuration).
	RoleGuest Role = "guest"
)

// Roles are the instance roles, highest first.
var Roles = []Role{RoleAdmin, RoleMember, RoleGuest}

// Valid reports one of the three roles.
func (r Role) Valid() bool { return r == RoleAdmin || r == RoleMember || r == RoleGuest }

// User is an account: the profile, and its instance role.
type User struct {
	ID string `json:"id"`
	// Handle is the short unique name (lowercase slug): what URLs and
	// mentions use, and what a person can sign in with beside their email.
	Handle     string     `json:"handle"`
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	Role       Role       `json:"role"`
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
	// IdleSeconds, when positive, is an inactivity window: the token is
	// refused once its last use (or its creation, never used) is that old,
	// and every use slides it — what the CLI's browser-approved sign-in
	// mints. Zero: no window, only ExpiresAt.
	IdleSeconds int `json:"idle_seconds,omitempty"`
	// Actions, when set, scope the token: it may do only these (authz's
	// vocabulary), and never more than its account may at the time. Nil is
	// a full token: the account itself.
	Actions []string `json:"actions,omitempty"`
	// ProjectIDs, when set, limit a scoped token to these workshops. Nil:
	// every workshop the account reaches.
	ProjectIDs []string `json:"project_ids,omitempty"`
}

// Scoped reports a token limited to some actions.
func (t Token) Scoped() bool { return t.Actions != nil }

// SSHKey is a public key a person registered to reach their runs'
// workspaces through the SSH door. PublicKey is the normalised
// authorized_keys form (type and base64, no comment); Fingerprint its
// SHA256 fingerprint, unique across the instance — what the door looks an
// offered key up by; Type the key's algorithm, for the list.
type SSHKey struct {
	ID          string     `json:"id"`
	UserID      string     `json:"-"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	PublicKey   string     `json:"public_key"`
	Fingerprint string     `json:"fingerprint"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// TokenCall is one call a personal token made: a write through the API,
// or a tool of the MCP face. The act itself names the token's person, as
// one made in a browser does; this names the token.
type TokenCall struct {
	TokenID string    `json:"-"`
	At      time.Time `json:"at"`
	Method  string    `json:"method"`
	// Route is the route's id (createWorkshopTicket); Path what was asked.
	Route string `json:"route"`
	Path  string `json:"path"`
	// Tool is the MCP tool called, when the call came through the face.
	Tool   string `json:"tool,omitempty"`
	Status int    `json:"status"`
}

// KeepTokenCalls is how many calls a token's record keeps.
const KeepTokenCalls = 200

// DB is the persistence boundary, implemented by pgstore and by Mem.
type DB interface {
	CountUsers(ctx context.Context) (int, error)
	CreateUser(ctx context.Context, u User) error
	// UpdateUserProfile writes handle, email and name (and updated_at) and
	// nothing else, so a profile read before a role change or a disable
	// cannot write the old role or state back. It returns the stored row.
	UpdateUserProfile(ctx context.Context, id string, p Profile, at time.Time) (User, error)
	// SetUserRole writes the role alone. Taking admin from the last enabled
	// admin is ErrLastAdmin, decided in the same transaction as the write
	// and serialised with every other such change: of two demotions at
	// once, one is refused.
	SetUserRole(ctx context.Context, id string, role Role, at time.Time) (User, error)
	// SetUserDisabled sets (non-nil) or clears disabled_at alone, refusing
	// to disable the last enabled admin as SetUserRole does.
	SetUserDisabled(ctx context.Context, id string, disabledAt *time.Time, at time.Time) (User, error)
	GetUser(ctx context.Context, id string) (User, error)
	// GetUserByLogin finds a user by handle or email (case-insensitive).
	GetUserByLogin(ctx context.Context, login string) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	TouchUser(ctx context.Context, id string, at time.Time) error

	PutMethod(ctx context.Context, m Method) error
	// TouchMethod records a use of the method and changes nothing else: a
	// login holds a row read before its slow verify, and writing that back
	// would undo a password set in the meantime.
	TouchMethod(ctx context.Context, id string, at time.Time) error
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
	// RecordTokenCall keeps one call a token made, and only its token's
	// last KeepTokenCalls; deleting the token deletes them.
	RecordTokenCall(ctx context.Context, c TokenCall) error
	// TokenCalls are a token's calls, newest first.
	TokenCalls(ctx context.Context, tokenID string) ([]TokenCall, error)

	// SSH keys: a duplicate fingerprint is ErrConflict; DeleteSSHKey is
	// owner-scoped (another user's key is ErrNotFound).
	CreateSSHKey(ctx context.Context, k SSHKey) error
	GetSSHKeyByFingerprint(ctx context.Context, fingerprint string) (SSHKey, error)
	ListSSHKeys(ctx context.Context, userID string) ([]SSHKey, error)
	DeleteSSHKey(ctx context.Context, userID, id string) error
	TouchSSHKey(ctx context.Context, id string, at time.Time) error
}

// Verifier checks a proof against a stored method of its kind.
type Verifier interface {
	Kind() string
	// Verify returns ErrUnauthorized when the proof does not match.
	Verify(m Method, proof string) error
}
