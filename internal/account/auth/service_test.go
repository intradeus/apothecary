package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const pw = "correct horse"

func newService(t *testing.T) (*Service, *Mem, *time.Time) {
	t.Helper()
	mem := NewMem()
	s := New(mem)
	s.HashCost = CheapHash
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, mem, &now
}

func user(t *testing.T, s *Service, handle string, admin bool) User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), NewUser{Handle: handle, Email: handle + "@example.test", Password: pw, Admin: admin})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSetupClaimsAFreshInstanceOnce(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	if need, err := s.SetupRequired(ctx); err != nil || !need {
		t.Fatalf("SetupRequired = %v, %v", need, err)
	}
	u, err := s.Setup(ctx, NewUser{Handle: "Lu", Email: "LU@Example.test", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	if !u.Admin || u.Handle != "lu" || u.Email != "lu@example.test" || u.Name != "lu" {
		t.Errorf("first user = %+v", u)
	}
	if need, _ := s.SetupRequired(ctx); need {
		t.Error("still needs setup")
	}
	if _, err := s.Setup(ctx, NewUser{Handle: "eve", Email: "eve@example.test", Password: pw}); !errors.Is(err, ErrSetupDone) {
		t.Errorf("second setup = %v", err)
	}
}

func TestCreateUserValidates(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	for name, in := range map[string]NewUser{
		"one-letter handle":  {Handle: "a", Email: "a@x", Password: pw},
		"leading dash":       {Handle: "-ab", Email: "a@x", Password: pw},
		"uppercase kept out": {Handle: "a b", Email: "a@x", Password: pw},
		"long handle":        {Handle: strings.Repeat("a", 33), Email: "a@x", Password: pw},
		"no email":           {Handle: "ab", Email: "", Password: pw},
		"email without at":   {Handle: "ab", Email: "ab.example", Password: pw},
		"email with a space": {Handle: "ab", Email: "a b@x", Password: pw},
		"short password":     {Handle: "ab", Email: "a@x", Password: "1234567"},
	} {
		if _, err := s.CreateUser(ctx, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s = %v, want invalid", name, err)
		}
	}
	// Exactly the floor is enough; the longest handle fits.
	if _, err := s.CreateUser(ctx, NewUser{Handle: strings.Repeat("a", 32), Email: "a@x", Password: "12345678"}); err != nil {
		t.Errorf("at the limits = %v", err)
	}
	user(t, s, "lu", false)
	if _, err := s.CreateUser(ctx, NewUser{Handle: "lu", Email: "other@x", Password: pw}); !errors.Is(err, ErrConflict) {
		t.Errorf("taken handle = %v", err)
	}
	if _, err := s.CreateUser(ctx, NewUser{Handle: "other", Email: "LU@example.test", Password: pw}); !errors.Is(err, ErrConflict) {
		t.Errorf("taken email, other case = %v", err)
	}
	if n, _ := s.db.CountUsers(ctx); n != 2 {
		t.Errorf("users after the refusals = %d", n)
	}
}

func TestUpdateProfile(t *testing.T) {
	s, _, now := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	user(t, s, "taken", false)
	*now = now.Add(time.Hour)

	got, err := s.UpdateProfile(ctx, u.ID, Profile{Name: "  Lucas  "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Lucas" || got.Handle != "lu" || got.Email != u.Email || !got.UpdatedAt.Equal(*now) || !got.CreatedAt.Equal(u.CreatedAt) {
		t.Errorf("name only = %+v", got)
	}
	got, err = s.UpdateProfile(ctx, u.ID, Profile{Handle: "LUC", Email: "NEW@example.test"})
	if err != nil || got.Handle != "luc" || got.Email != "new@example.test" || got.Name != "Lucas" {
		t.Errorf("handle and email = %+v, %v", got, err)
	}
	if stored, _ := s.GetUser(ctx, u.ID); stored.Handle != "luc" {
		t.Errorf("stored = %+v", stored)
	}
	if _, err := s.UpdateProfile(ctx, u.ID, Profile{Handle: "taken"}); !errors.Is(err, ErrConflict) {
		t.Errorf("taken = %v", err)
	}
	if _, err := s.UpdateProfile(ctx, u.ID, Profile{Email: "nope"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad email = %v", err)
	}
	if _, err := s.UpdateProfile(ctx, "nobody", Profile{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("nobody = %v", err)
	}
}

func TestLastAdminStays(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	a, b := user(t, s, "first", true), user(t, s, "second", true)
	plain := user(t, s, "plain", false)

	if _, err := s.SetAdmin(ctx, a.ID, false); err != nil {
		t.Fatalf("stepping down with another admin = %v", err)
	}
	if _, err := s.SetAdmin(ctx, b.ID, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("the last admin stepped down: %v", err)
	}
	if _, err := s.SetDisabled(ctx, b.ID, true); !errors.Is(err, ErrInvalid) {
		t.Errorf("the last admin was disabled: %v", err)
	}
	if got, _ := s.GetUser(ctx, b.ID); !got.Admin || got.Disabled() {
		t.Errorf("after the refusals = %+v", got)
	}
	// A plain user is no admin to lose.
	if _, err := s.SetAdmin(ctx, plain.ID, false); err != nil {
		t.Errorf("SetAdmin(false) on a plain user = %v", err)
	}
	if _, err := s.SetDisabled(ctx, plain.ID, true); err != nil {
		t.Errorf("disabling a plain user = %v", err)
	}
	// A disabled admin does not count: promote someone, disable them, and the
	// remaining one is the last again.
	if _, err := s.SetAdmin(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDisabled(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAdmin(ctx, b.ID, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("a disabled admin counted as another admin: %v", err)
	}
	if _, err := s.SetAdmin(ctx, "nobody", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetAdmin nobody = %v", err)
	}
	if _, err := s.SetDisabled(ctx, "nobody", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetDisabled nobody = %v", err)
	}
}

func TestDisableTakesCredentialsWithIt(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	other := user(t, s, "other", false)
	raw, _, err := s.NewSession(ctx, u.ID, "ua")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.NewToken(ctx, u.ID, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherRaw, _, _ := s.NewSession(ctx, other.ID, "ua")

	got, err := s.SetDisabled(ctx, u.ID, true)
	if err != nil || !got.Disabled() {
		t.Fatalf("disable = %+v, %v", got, err)
	}
	if _, err := s.AuthenticateSession(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("session = %v", err)
	}
	if _, err := s.AuthenticateToken(ctx, tok); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("token = %v", err)
	}
	if _, err := s.AuthenticateSession(ctx, otherRaw); err != nil {
		t.Errorf("someone else's session = %v", err)
	}
	if _, err := s.Login(ctx, KindPassword, "lu", pw); !errors.Is(err, ErrDisabled) {
		t.Errorf("login = %v", err)
	}
	// The wrong password reads the same as ever: a disabled account is not
	// confirmed to someone who cannot prove it.
	if _, err := s.Login(ctx, KindPassword, "lu", "wrong one"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong password on a disabled account = %v", err)
	}
	got, err = s.SetDisabled(ctx, u.ID, false)
	if err != nil || got.Disabled() {
		t.Fatalf("enable = %+v, %v", got, err)
	}
	if _, err := s.Login(ctx, KindPassword, "lu", pw); err != nil {
		t.Errorf("login after enable = %v", err)
	}
}

func TestLogin(t *testing.T) {
	s, mem, now := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)

	for name, c := range map[string]struct {
		kind, login, proof string
		want               error
	}{
		"unknown kind":    {"passkey", "lu", pw, ErrInvalid},
		"unknown account": {KindPassword, "nobody", pw, ErrUnauthorized},
		"wrong password":  {KindPassword, "lu", "wrong one", ErrUnauthorized},
		"empty password":  {KindPassword, "lu", "", ErrUnauthorized},
	} {
		if _, err := s.Login(ctx, c.kind, c.login, c.proof); !errors.Is(err, c.want) {
			t.Errorf("%s = %v, want %v", name, err, c.want)
		}
	}
	*now = now.Add(time.Minute)
	for _, login := range []string{"lu", "LU", "  lu  ", "lu@example.test", "LU@EXAMPLE.TEST"} {
		got, err := s.Login(ctx, KindPassword, login, pw)
		if err != nil || got.ID != u.ID {
			t.Errorf("login %q = %+v, %v", login, got, err)
		}
	}
	m, err := mem.GetMethod(ctx, u.ID, KindPassword)
	if err != nil || m.LastUsedAt == nil || !m.LastUsedAt.Equal(*now) {
		t.Errorf("method after login = %+v, %v", m, err)
	}

	// An account with no method of that kind fails like a wrong proof.
	if err := mem.DeleteMethod(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, KindPassword, "lu", pw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("no password method = %v", err)
	}
}

// failDB fails the named calls.
type failDB struct {
	DB
	login, method, session, user, token, list, put, count, create error
}

func (f failDB) GetUserByLogin(ctx context.Context, l string) (User, error) {
	if f.login != nil {
		return User{}, f.login
	}
	return f.DB.GetUserByLogin(ctx, l)
}
func (f failDB) GetMethod(ctx context.Context, u, k string) (Method, error) {
	if f.method != nil {
		return Method{}, f.method
	}
	return f.DB.GetMethod(ctx, u, k)
}
func (f failDB) GetSession(ctx context.Context, id string) (Session, error) {
	if f.session != nil {
		return Session{}, f.session
	}
	return f.DB.GetSession(ctx, id)
}
func (f failDB) GetUser(ctx context.Context, id string) (User, error) {
	if f.user != nil {
		return User{}, f.user
	}
	return f.DB.GetUser(ctx, id)
}
func (f failDB) GetTokenByHash(ctx context.Context, h string) (Token, error) {
	if f.token != nil {
		return Token{}, f.token
	}
	return f.DB.GetTokenByHash(ctx, h)
}
func (f failDB) ListMethods(ctx context.Context, u string) ([]Method, error) {
	if f.list != nil {
		return nil, f.list
	}
	return f.DB.ListMethods(ctx, u)
}
func (f failDB) ListUsers(ctx context.Context) ([]User, error) {
	if f.list != nil {
		return nil, f.list
	}
	return f.DB.ListUsers(ctx)
}
func (f failDB) PutMethod(ctx context.Context, m Method) error {
	if f.put != nil {
		return f.put
	}
	return f.DB.PutMethod(ctx, m)
}
func (f failDB) CountUsers(ctx context.Context) (int, error) {
	if f.count != nil {
		return 0, f.count
	}
	return f.DB.CountUsers(ctx)
}
func (f failDB) CreateUser(ctx context.Context, u User) error {
	if f.create != nil {
		return f.create
	}
	return f.DB.CreateUser(ctx, u)
}
func (f failDB) CreateSession(ctx context.Context, s Session) error {
	if f.create != nil {
		return f.create
	}
	return f.DB.CreateSession(ctx, s)
}
func (f failDB) CreateToken(ctx context.Context, t Token) error {
	if f.create != nil {
		return f.create
	}
	return f.DB.CreateToken(ctx, t)
}
func (f failDB) DeleteSessionsOf(ctx context.Context, u string) error {
	if f.put != nil {
		return f.put
	}
	return f.DB.DeleteSessionsOf(ctx, u)
}

// An outage is an outage: it never reads as a bad credential (which a client
// would answer by signing the person out) nor as success.
func TestStoreOutagesPropagate(t *testing.T) {
	s, mem, _ := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	raw, _, err := s.NewSession(ctx, u.ID, "ua")
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.NewToken(ctx, u.ID, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("db down")
	with := func(f failDB) *Service {
		f.DB = mem
		o := New(f)
		return o
	}
	check := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, boom) {
			t.Errorf("%s = %v, want the outage", name, err)
		}
	}
	_, err = with(failDB{login: boom}).Login(ctx, KindPassword, "lu", pw)
	check("login lookup", err)
	_, err = with(failDB{method: boom}).Login(ctx, KindPassword, "lu", pw)
	check("login method", err)
	_, err = with(failDB{session: boom}).AuthenticateSession(ctx, raw)
	check("session lookup", err)
	_, err = with(failDB{token: boom}).AuthenticateToken(ctx, tok)
	check("token lookup", err)
	_, err = with(failDB{list: boom}).ListMethods(ctx, u.ID)
	check("list methods", err)
	_, err = with(failDB{count: boom}).Setup(ctx, NewUser{})
	check("setup count", err)
	_, err = with(failDB{create: boom}).CreateUser(ctx, NewUser{Handle: "ab", Email: "a@x", Password: pw})
	check("create user", err)
	_, err = with(failDB{put: boom}).CreateUser(ctx, NewUser{Handle: "ac", Email: "c@x", Password: pw})
	check("create user's method", err)
	_, _, err = with(failDB{create: boom}).NewSession(ctx, u.ID, "ua")
	check("new session", err)
	_, _, err = with(failDB{create: boom}).NewToken(ctx, u.ID, "t", nil)
	check("new token", err)
	_, err = with(failDB{list: boom}).SetAdmin(ctx, mustAdmin(t, s), false)
	check("last-admin check", err)
	check("set password method", with(failDB{method: boom}).SetPassword(ctx, u.ID, pw))
	check("set password put", with(failDB{put: boom}).SetPassword(ctx, u.ID, pw))
	check("change password", with(failDB{method: boom}).ChangePassword(ctx, u.ID, pw, pw))

	// A row whose user is gone is a dead credential, not an outage.
	if _, err := with(failDB{user: ErrNotFound}).AuthenticateSession(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("session of nobody = %v", err)
	}
	if _, err := with(failDB{user: ErrNotFound}).AuthenticateToken(ctx, tok); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("token of nobody = %v", err)
	}
	if _, _, err := with(failDB{user: ErrNotFound}).NewToken(ctx, "x", "t", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("token for nobody = %v", err)
	}
}

func mustAdmin(t *testing.T, s *Service) string {
	t.Helper()
	u, err := s.CreateUser(context.Background(), NewUser{Handle: "boss", Email: "boss@x", Password: pw, Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestSetPassword(t *testing.T) {
	s, mem, now := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	raw, _, _ := s.NewSession(ctx, u.ID, "ua")

	if err := s.SetPassword(ctx, u.ID, "short"); !errors.Is(err, ErrInvalid) {
		t.Errorf("short = %v", err)
	}
	if err := s.SetPassword(ctx, "nobody", pw); !errors.Is(err, ErrNotFound) {
		t.Errorf("nobody = %v", err)
	}
	if _, err := s.AuthenticateSession(ctx, raw); err != nil {
		t.Errorf("a refused change signed them out: %v", err)
	}

	*now = now.Add(time.Hour)
	if err := s.SetPassword(ctx, u.ID, "a brand new one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("session after a reset = %v", err)
	}
	if _, err := s.Login(ctx, KindPassword, "lu", pw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("old password = %v", err)
	}
	if _, err := s.Login(ctx, KindPassword, "lu", "a brand new one"); err != nil {
		t.Errorf("new password = %v", err)
	}
	m, err := mem.GetMethod(ctx, u.ID, KindPassword)
	if err != nil || !m.UpdatedAt.Equal(*now) || m.CreatedAt.Equal(*now) {
		t.Errorf("method replaced rather than updated: %+v, %v", m, err)
	}

	// An account with no password gets one.
	if err := mem.DeleteMethod(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(ctx, u.ID, pw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, KindPassword, "lu", pw); err != nil {
		t.Errorf("first password = %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	if err := s.ChangePassword(ctx, u.ID, "wrong one", "a brand new one"); !errors.Is(err, ErrInvalid) {
		t.Errorf("wrong current = %v", err)
	}
	if _, err := s.Login(ctx, KindPassword, "lu", pw); err != nil {
		t.Errorf("a refused change took effect: %v", err)
	}
	if err := s.ChangePassword(ctx, u.ID, pw, "a brand new one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, KindPassword, "lu", "a brand new one"); err != nil {
		t.Errorf("after the change = %v", err)
	}
}

func TestListMethodsHidesSecrets(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	ms, err := s.ListMethods(ctx, u.ID)
	if err != nil || len(ms) != 1 || ms[0].Kind != KindPassword {
		t.Fatalf("methods = %+v, %v", ms, err)
	}
	if ms[0].Secret != nil {
		t.Error("the password hash left the service")
	}
	// The stored row still has it: the strip is on the copy.
	if _, err := s.Login(ctx, KindPassword, "lu", pw); err != nil {
		t.Errorf("login after a list = %v", err)
	}
}

func TestSessions(t *testing.T) {
	s, mem, now := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	raw, sess, err := s.NewSession(ctx, u.ID, "curl/8")
	if err != nil {
		t.Fatal(err)
	}
	// What the table holds is useless without the cookie.
	if raw == sess.ID || sess.ID != hashSecret(raw) || sess.UserAgent != "curl/8" || !sess.ExpiresAt.Equal(now.Add(s.SessionTTL)) {
		t.Errorf("session = %+v", sess)
	}

	p, err := s.AuthenticateSession(ctx, raw)
	if err != nil || p.User.ID != u.ID || !p.ViaCookie() || p.Token != nil {
		t.Fatalf("principal = %+v, %v", p, err)
	}
	for _, bad := range []string{"", "unknown", sess.ID} { // the stored hash is not a cookie
		if _, err := s.AuthenticateSession(ctx, bad); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("cookie %q = %v", bad, err)
		}
	}

	// Within a minute nothing is written.
	*now = now.Add(30 * time.Second)
	if _, err := s.AuthenticateSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if got, _ := mem.GetSession(ctx, sess.ID); !got.LastSeenAt.Equal(sess.LastSeenAt) {
		t.Errorf("touched within the minute: %+v", got)
	}
	if got, _ := mem.GetUser(ctx, u.ID); got.LastSeenAt != nil {
		t.Errorf("user touched within the minute: %+v", got)
	}
	// Past it, the expiry slides and the user is seen.
	*now = now.Add(time.Minute)
	p, err = s.AuthenticateSession(ctx, raw)
	if err != nil || p.User.LastSeenAt == nil || !p.User.LastSeenAt.Equal(*now) || !p.Session.ExpiresAt.Equal(now.Add(s.SessionTTL)) {
		t.Fatalf("slid = %+v, %v", p, err)
	}
	stored, _ := mem.GetSession(ctx, sess.ID)
	if !stored.ExpiresAt.Equal(now.Add(s.SessionTTL)) {
		t.Errorf("stored expiry = %v", stored.ExpiresAt)
	}
	if got, _ := mem.GetUser(ctx, u.ID); got.LastSeenAt == nil {
		t.Error("user not touched")
	}

	// Exactly at the expiry it is over, and the row goes.
	*now = stored.ExpiresAt
	if _, err := s.AuthenticateSession(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("at expiry = %v", err)
	}
	if _, err := mem.GetSession(ctx, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired row = %v", err)
	}
}

func TestLogout(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	raw, _, _ := s.NewSession(ctx, u.ID, "")
	if err := s.Logout(ctx, ""); err != nil {
		t.Errorf("no cookie = %v", err)
	}
	if err := s.Logout(ctx, "unknown"); err != nil {
		t.Errorf("unknown cookie = %v", err)
	}
	if err := s.Logout(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("after logout = %v", err)
	}
	if err := s.Logout(ctx, raw); err != nil {
		t.Errorf("double logout = %v", err)
	}
	boom := errors.New("db down")
	broken := New(deleteFails{NewMem(), boom})
	if err := broken.Logout(ctx, "x"); !errors.Is(err, boom) {
		t.Errorf("outage = %v", err)
	}
}

type deleteFails struct {
	*Mem
	err error
}

func (d deleteFails) DeleteSession(context.Context, string) error { return d.err }

func TestTokens(t *testing.T) {
	s, mem, now := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	other := user(t, s, "other", false)

	if _, _, err := s.NewToken(ctx, u.ID, "  ", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("nameless = %v", err)
	}
	secret, tok, err := s.NewToken(ctx, u.ID, " ci ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, TokenPrefix) || tok.Name != "ci" || tok.Hash != hashSecret(secret) ||
		tok.Hash == secret || tok.Prefix != secret[:len(TokenPrefix)+6] {
		t.Errorf("token = %+v", tok)
	}

	p, err := s.AuthenticateToken(ctx, secret)
	if err != nil || p.User.ID != u.ID || p.ViaCookie() || p.Token == nil {
		t.Fatalf("principal = %+v, %v", p, err)
	}
	stored, _ := mem.GetTokenByHash(ctx, tok.Hash)
	if stored.LastUsedAt == nil {
		t.Error("first use not recorded")
	}
	// Within a minute of its last use nothing is written; past it, it is.
	first := *stored.LastUsedAt
	*now = now.Add(30 * time.Second)
	if _, err := s.AuthenticateToken(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if got, _ := mem.GetTokenByHash(ctx, tok.Hash); !got.LastUsedAt.Equal(first) {
		t.Errorf("touched within the minute: %v", got.LastUsedAt)
	}
	*now = now.Add(time.Minute)
	if _, err := s.AuthenticateToken(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if got, _ := mem.GetTokenByHash(ctx, tok.Hash); !got.LastUsedAt.Equal(*now) {
		t.Errorf("not touched: %v", got.LastUsedAt)
	}

	for name, bad := range map[string]string{
		"no prefix":       strings.TrimPrefix(secret, TokenPrefix),
		"unknown":         TokenPrefix + "unknown",
		"the stored hash": tok.Hash,
		"empty":           "",
	} {
		if _, err := s.AuthenticateToken(ctx, bad); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%s = %v", name, err)
		}
	}

	// Expiry: the instant it passes.
	at := now.Add(time.Hour)
	expiring, _, err := s.NewToken(ctx, u.ID, "soon", &at)
	if err != nil {
		t.Fatal(err)
	}
	*now = at.Add(-time.Nanosecond)
	if _, err := s.AuthenticateToken(ctx, expiring); err != nil {
		t.Errorf("just before expiry = %v", err)
	}
	*now = at
	if _, err := s.AuthenticateToken(ctx, expiring); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("at expiry = %v", err)
	}

	list, err := s.ListTokens(ctx, u.ID)
	if err != nil || len(list) != 2 {
		t.Errorf("tokens = %+v, %v", list, err)
	}
	if list, _ := s.ListTokens(ctx, other.ID); len(list) != 0 {
		t.Errorf("other's tokens = %+v", list)
	}
	// Revoking is the owner's: another user's id reads as absent.
	if err := s.DeleteToken(ctx, other.ID, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else's revoke = %v", err)
	}
	if err := s.DeleteToken(ctx, u.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateToken(ctx, secret); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("revoked = %v", err)
	}
}

func TestDisabledCredentialIsRefusedByName(t *testing.T) {
	s, mem, _ := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	raw, _, _ := s.NewSession(ctx, u.ID, "")
	tok, _, _ := s.NewToken(ctx, u.ID, "t", nil)
	// Disabled at the store, as a race with the sign-out would find it.
	now := time.Now()
	u.DisabledAt = &now
	if err := mem.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateSession(ctx, raw); !errors.Is(err, ErrDisabled) {
		t.Errorf("session = %v", err)
	}
	if _, err := s.AuthenticateToken(ctx, tok); !errors.Is(err, ErrDisabled) {
		t.Errorf("token = %v", err)
	}
}

// A service prices a hash as asked and verifies it all the same, and one that
// asks nothing gets the production cost: the zero value must never be weak.
func TestHashCost(t *testing.T) {
	cheap, err := CheapHash.hash(pw)
	if err != nil || !strings.HasPrefix(cheap, "$argon2id$v=19$m=8,t=1,p=4$") {
		t.Fatalf("cheap = %q, %v", cheap, err)
	}
	if err := VerifyPassword(cheap, pw); err != nil {
		t.Error(err)
	}
	if err := VerifyPassword(cheap, "wrong one"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong password = %v", err)
	}
	mem := NewMem()
	prod := New(mem)
	u, err := prod.Setup(context.Background(), NewUser{Handle: "alice", Email: "a@x", Name: "A", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mem.GetMethod(context.Background(), u.ID, KindPassword)
	if err != nil || !strings.HasPrefix(string(m.Secret), "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("a plain service hashed at %q, %v", m.Secret, err)
	}
}

// resetOnRead sets the password right after a login has read the row, which is
// where a real reset lands: the login is about to spend a long time verifying.
type resetOnRead struct {
	DB
	reset func()
}

func (r *resetOnRead) GetMethod(ctx context.Context, userID, kind string) (Method, error) {
	m, err := r.DB.GetMethod(ctx, userID, kind)
	if r.reset != nil {
		reset := r.reset
		r.reset = nil
		reset()
	}
	return m, err
}

// A reset is how a stolen password stops working, so a login that started with
// the old one must not put it back on its way out.
func TestALoginCannotUndoAPasswordReset(t *testing.T) {
	admin, mem, now := newService(t)
	ctx := context.Background()
	u := user(t, admin, "lu", false)
	const fresh = "a brand new password"
	s := New(&resetOnRead{DB: mem, reset: func() {
		if err := admin.SetPassword(ctx, u.ID, fresh); err != nil {
			t.Error(err)
		}
	}})
	s.now = func() time.Time { return *now }
	if _, err := s.Login(ctx, KindPassword, "lu", pw); err != nil {
		t.Fatalf("the login that raced = %v", err)
	}
	if _, err := admin.Login(ctx, KindPassword, "lu", pw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("the old password after the reset = %v", err)
	}
	if _, err := admin.Login(ctx, KindPassword, "lu", fresh); err != nil {
		t.Errorf("the new password = %v", err)
	}
	if m, err := mem.GetMethod(ctx, u.ID, KindPassword); err != nil || m.LastUsedAt == nil {
		t.Errorf("the use went unrecorded: %+v, %v", m, err)
	}
}

func TestPasswordHashes(t *testing.T) {
	a, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two hashes of one password are equal: no salt")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash = %q", a)
	}
	if err := VerifyPassword(a, pw); err != nil {
		t.Errorf("verify = %v", err)
	}
	if err := VerifyPassword(a, "wrong one"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong = %v", err)
	}
	parts := strings.Split(a, "$")
	for name, bad := range map[string]string{
		"empty":        "",
		"plain":        pw,
		"another kind": strings.Replace(a, "argon2id", "bcrypt", 1),
		"few parts":    "$argon2id$v=19$m=1",
		"bad params":   strings.Replace(a, parts[3], "m=x,t=3,p=4", 1),
		"bad salt":     strings.Replace(a, parts[4], "!!!", 1),
		"bad key":      strings.Replace(a, parts[5], "!!!", 1),
	} {
		if err := VerifyPassword(bad, pw); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func TestPasswordVerifier(t *testing.T) {
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	var v Verifier = PasswordVerifier{}
	if v.Kind() != KindPassword {
		t.Errorf("kind = %q", v.Kind())
	}
	if err := v.Verify(Method{Secret: []byte(h)}, pw); err != nil {
		t.Errorf("verify = %v", err)
	}
	if err := v.Verify(Method{Secret: []byte(h)}, "wrong one"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong = %v", err)
	}
}

type stubVerifier struct{}

func (stubVerifier) Kind() string                { return "passkey" }
func (stubVerifier) Verify(Method, string) error { return nil }

func TestMethodsListWhatIsRegistered(t *testing.T) {
	s, mem, _ := newService(t)
	if got := s.Methods(); !slices.Equal(got, []string{KindPassword}) {
		t.Errorf("methods = %v", got)
	}
	s.Register(stubVerifier{})
	if got := s.Methods(); !slices.Equal(got, []string{"passkey", KindPassword}) {
		t.Errorf("sorted methods = %v", got)
	}
	if s.DB() != DB(mem) {
		t.Error("DB is not the store it was built on")
	}
}

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := PrincipalFrom(ctx); ok {
		t.Error("a bare context carries a principal")
	}
	if UserID(ctx) != "" {
		t.Error("a bare context has a user id")
	}
	p := Principal{User: User{ID: "u"}, Token: &Token{}}
	ctx = WithPrincipal(ctx, p)
	got, ok := PrincipalFrom(ctx)
	if !ok || got.User.ID != "u" || UserID(ctx) != "u" {
		t.Errorf("principal = %+v, %v", got, ok)
	}
	if got.ViaCookie() {
		t.Error("a token principal claims a cookie")
	}
	if !(Principal{Session: &Session{}}).ViaCookie() {
		t.Error("a session principal denies its cookie")
	}
}

func TestGetUserByLoginNormalises(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	u := user(t, s, "lu", false)
	got, err := s.GetUserByLogin(ctx, "  LU@example.test ")
	if err != nil || got.ID != u.ID {
		t.Errorf("by email = %+v, %v", got, err)
	}
	if _, err := s.GetUserByLogin(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("nobody = %v", err)
	}
	list, err := s.ListUsers(ctx)
	if err != nil || len(list) != 1 {
		t.Errorf("list = %+v, %v", list, err)
	}
}

// Mem's rules are the contract pgstore keeps: what tests above rely on.
func TestMemEdges(t *testing.T) {
	ctx := context.Background()
	m := NewMem()
	if err := m.UpdateUser(ctx, User{ID: "nobody"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update nobody = %v", err)
	}
	if err := m.CreateUser(ctx, User{ID: "a", Handle: "a", Email: "a@x"}); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateUser(ctx, User{ID: "b", Handle: "b", Email: "b@x"}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateUser(ctx, User{ID: "b", Handle: "a", Email: "b@x"}); !errors.Is(err, ErrConflict) {
		t.Errorf("handle clash = %v", err)
	}
	if err := m.TouchUser(ctx, "nobody", time.Now()); err != nil {
		t.Errorf("touch nobody = %v", err)
	}
	if err := m.TouchSession(ctx, "nobody", time.Now(), time.Now()); err != nil {
		t.Errorf("touch no session = %v", err)
	}
	if err := m.TouchToken(ctx, "nobody", time.Now()); err != nil {
		t.Errorf("touch no token = %v", err)
	}
	if err := m.DeleteSession(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete no session = %v", err)
	}
	if err := m.DeleteToken(ctx, "a", "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete no token = %v", err)
	}
	if err := m.DeleteTokensOf(ctx, "a"); err != nil {
		t.Errorf("delete tokens of a = %v", err)
	}

	if err := m.PutMethod(ctx, Method{ID: "m1", UserID: "a", Kind: "oauth", Subject: "sub"}); err != nil {
		t.Fatal(err)
	}
	if got, err := m.FindMethodBySubject(ctx, "oauth", "sub"); err != nil || got.ID != "m1" {
		t.Errorf("by subject = %+v, %v", got, err)
	}
	if _, err := m.FindMethodBySubject(ctx, "oauth", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty subject matched: %v", err)
	}
	if _, err := m.FindMethodBySubject(ctx, "passkey", "sub"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another kind matched: %v", err)
	}
}
