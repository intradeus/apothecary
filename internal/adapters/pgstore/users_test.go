package pgstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
)

var testNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func mkUser(id, handle, email string) auth.User {
	return auth.User{ID: id, Handle: handle, Email: email, Name: "N " + id, CreatedAt: testNow, UpdatedAt: testNow}
}

func seedUsers(t *testing.T, s *Store, us ...auth.User) {
	t.Helper()
	for _, u := range us {
		if err := s.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsersRoundtripAndLookups(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if n, err := s.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("empty count = %d, %v", n, err)
	}
	a := mkUser("u1", "Alice", "Alice@Example.com")
	a.Admin = true
	b := mkUser("u2", "bob", "bob@example.com")
	b.CreatedAt = testNow.Add(time.Minute)
	seedUsers(t, s, b, a)

	if n, _ := s.CountUsers(ctx); n != 2 {
		t.Errorf("count = %d", n)
	}
	got, err := s.GetUser(ctx, "u1")
	if err != nil || got.Handle != "Alice" || !got.Admin || got.DisabledAt != nil || got.LastSeenAt != nil || !got.CreatedAt.Equal(testNow) {
		t.Fatalf("GetUser = %+v, %v", got, err)
	}
	if _, err := s.GetUser(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("missing user = %v", err)
	}
	// Sign-in matches the handle or the email, in any case.
	for _, login := range []string{"alice", "ALICE", "alice@example.COM"} {
		if u, err := s.GetUserByLogin(ctx, login); err != nil || u.ID != "u1" {
			t.Errorf("login %q = %+v, %v", login, u, err)
		}
	}
	if _, err := s.GetUserByLogin(ctx, "ghost"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown login = %v", err)
	}
	list, err := s.ListUsers(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "u1" || list[1].ID != "u2" {
		t.Errorf("ListUsers = %+v, %v (want created_at order)", list, err)
	}
}

func TestUserConflictsNameTheField(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seedUsers(t, s, mkUser("u1", "alice", "a@x"), mkUser("u2", "bob", "b@x"))

	// Uniqueness ignores case on both, and the error says which one collided.
	err := s.CreateUser(ctx, mkUser("u3", "ALICE", "c@x"))
	if !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "handle") {
		t.Errorf("duplicate handle = %v", err)
	}
	err = s.CreateUser(ctx, mkUser("u3", "carol", "A@X"))
	if !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "email") {
		t.Errorf("duplicate email = %v", err)
	}
	// The same refusals on the update path.
	bob, _ := s.GetUser(ctx, "u2")
	bob.Handle = "alice"
	if err := s.UpdateUser(ctx, bob); !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "handle") {
		t.Errorf("update onto a taken handle = %v", err)
	}
	bob.Handle, bob.Email = "bob", "a@x"
	if err := s.UpdateUser(ctx, bob); !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "email") {
		t.Errorf("update onto a taken email = %v", err)
	}
	// A refused write changed nothing.
	if got, _ := s.GetUser(ctx, "u2"); got.Handle != "bob" || got.Email != "b@x" {
		t.Errorf("refused update wrote: %+v", got)
	}
}

func TestUpdateUserAndTouch(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u := mkUser("u1", "alice", "a@x")
	seedUsers(t, s, u)

	disabled := testNow.Add(time.Hour)
	u.Handle, u.Email, u.Name, u.Admin, u.DisabledAt, u.UpdatedAt = "al", "al@x", "Al", true, &disabled, testNow.Add(2*time.Hour)
	if err := s.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUser(ctx, "u1")
	if err != nil || got.Handle != "al" || got.Email != "al@x" || got.Name != "Al" || !got.Admin ||
		got.DisabledAt == nil || !got.DisabledAt.Equal(disabled) || !got.UpdatedAt.Equal(testNow.Add(2*time.Hour)) {
		t.Fatalf("after update = %+v, %v", got, err)
	}
	if !got.CreatedAt.Equal(testNow) {
		t.Errorf("update moved created_at: %v", got.CreatedAt)
	}
	ghost := mkUser("nobody", "x", "y")
	if err := s.UpdateUser(ctx, ghost); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("update of nobody = %v", err)
	}

	seen := testNow.Add(3 * time.Hour)
	if err := s.TouchUser(ctx, "u1", seen); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUser(ctx, "u1"); got.LastSeenAt == nil || !got.LastSeenAt.Equal(seen) {
		t.Errorf("last_seen = %v", got.LastSeenAt)
	}
}

func TestAuthMethods(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seedUsers(t, s, mkUser("u1", "alice", "a@x"), mkUser("u2", "bob", "b@x"))

	pw := auth.Method{ID: "m1", UserID: "u1", Kind: "password", Secret: []byte("hash"), CreatedAt: testNow, UpdatedAt: testNow}
	if err := s.PutMethod(ctx, pw); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMethod(ctx, "u1", "password")
	if err != nil || string(got.Secret) != "hash" || got.Config != nil && len(got.Config) != 0 {
		t.Fatalf("GetMethod = %+v, %v", got, err)
	}
	// Another user's method is not reachable under your id.
	if _, err := s.GetMethod(ctx, "u2", "password"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("other user's method = %v", err)
	}
	// One password per user.
	dup := pw
	dup.ID = "m2"
	if err := s.PutMethod(ctx, dup); !errors.Is(err, auth.ErrConflict) {
		t.Errorf("second password = %v", err)
	}
	// Re-putting the same id rotates the secret, keeps the owner.
	pw.Secret, pw.UserID, pw.UpdatedAt = []byte("hash2"), "u2", testNow.Add(time.Minute)
	if err := s.PutMethod(ctx, pw); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetMethod(ctx, "u1", "password")
	if string(got.Secret) != "hash2" || got.UserID != "u1" || !got.UpdatedAt.Equal(testNow.Add(time.Minute)) {
		t.Errorf("rotated = %+v", got)
	}

	// A nil secret stores empty, Config round-trips, subjects are unique per kind.
	oauth := auth.Method{ID: "m3", UserID: "u1", Kind: "oauth", Subject: "iss|sub", Config: map[string]any{"provider": "idp"}, CreatedAt: testNow, UpdatedAt: testNow}
	if err := s.PutMethod(ctx, oauth); err != nil {
		t.Fatal(err)
	}
	found, err := s.FindMethodBySubject(ctx, "oauth", "iss|sub")
	if err != nil || found.ID != "m3" || found.Config["provider"] != "idp" || len(found.Secret) != 0 {
		t.Fatalf("FindMethodBySubject = %+v, %v", found, err)
	}
	stolen := oauth
	stolen.ID, stolen.UserID = "m4", "u2"
	if err := s.PutMethod(ctx, stolen); !errors.Is(err, auth.ErrConflict) {
		t.Errorf("subject claimed twice = %v", err)
	}
	if _, err := s.FindMethodBySubject(ctx, "oauth", "other"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown subject = %v", err)
	}
	if _, err := s.FindMethodBySubject(ctx, "password", "iss|sub"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("subject of another kind = %v", err)
	}
	// An empty subject never matches: every password row has one.
	if _, err := s.FindMethodBySubject(ctx, "password", ""); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("empty subject = %v", err)
	}
	// A bad config never reaches the row.
	bad := oauth
	bad.ID, bad.Subject, bad.Config = "m5", "s5", map[string]any{"x": make(chan int)}
	if err := s.PutMethod(ctx, bad); err == nil {
		t.Error("unmarshalable config stored")
	}

	mine, err := s.ListMethods(ctx, "u1")
	if err != nil || len(mine) != 2 || mine[0].ID != "m1" || mine[1].ID != "m3" {
		t.Errorf("ListMethods = %+v, %v", mine, err)
	}
	if theirs, _ := s.ListMethods(ctx, "u2"); len(theirs) != 0 {
		t.Errorf("u2 lists u1's methods: %+v", theirs)
	}
	if err := s.DeleteMethod(ctx, "m3"); err != nil {
		t.Fatal(err)
	}
	if mine, _ := s.ListMethods(ctx, "u1"); len(mine) != 1 {
		t.Errorf("after delete = %+v", mine)
	}
}

func TestSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seedUsers(t, s, mkUser("u1", "alice", "a@x"), mkUser("u2", "bob", "b@x"))
	mk := func(id, user string) auth.Session {
		return auth.Session{ID: id, UserID: user, CreatedAt: testNow, ExpiresAt: testNow.Add(time.Hour), LastSeenAt: testNow, UserAgent: "ua"}
	}
	for _, se := range []auth.Session{mk("s1", "u1"), mk("s2", "u1"), mk("s3", "u2")} {
		if err := s.CreateSession(ctx, se); err != nil {
			t.Fatal(err)
		}
	}
	// A session cannot outlive or precede its user.
	if err := s.CreateSession(ctx, mk("s4", "ghost")); err == nil {
		t.Error("session for a missing user stored")
	}
	got, err := s.GetSession(ctx, "s1")
	if err != nil || got.UserID != "u1" || got.UserAgent != "ua" || !got.ExpiresAt.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("GetSession = %+v, %v", got, err)
	}
	if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("missing session = %v", err)
	}
	seen, exp := testNow.Add(time.Minute), testNow.Add(2*time.Hour)
	if err := s.TouchSession(ctx, "s1", seen, exp); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(ctx, "s1"); !got.LastSeenAt.Equal(seen) || !got.ExpiresAt.Equal(exp) {
		t.Errorf("touched = %+v", got)
	}
	if err := s.DeleteSession(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "s1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
	if err := s.DeleteSessionsOf(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "s2"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("u1's session survived: %v", err)
	}
	if _, err := s.GetSession(ctx, "s3"); err != nil {
		t.Errorf("sign-out-everywhere reached another user: %v", err)
	}
}

func TestTokensAreScopedToTheirOwner(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seedUsers(t, s, mkUser("u1", "alice", "a@x"), mkUser("u2", "bob", "b@x"))
	exp := testNow.Add(time.Hour)
	t1 := auth.Token{ID: "t1", UserID: "u1", Name: "ci", Hash: "h1", Prefix: "ap_1", CreatedAt: testNow, ExpiresAt: &exp}
	t2 := auth.Token{ID: "t2", UserID: "u1", Name: "cli", Hash: "h2", Prefix: "ap_2", CreatedAt: testNow.Add(time.Second)}
	t3 := auth.Token{ID: "t3", UserID: "u2", Name: "cli", Hash: "h3", Prefix: "ap_3", CreatedAt: testNow}
	for _, tk := range []auth.Token{t1, t2, t3} {
		if err := s.CreateToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	dup := t1
	dup.ID = "t9"
	if err := s.CreateToken(ctx, dup); err == nil {
		t.Error("a second token with the same hash stored")
	}
	got, err := s.GetTokenByHash(ctx, "h1")
	if err != nil || got.ID != "t1" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.LastUsedAt != nil {
		t.Fatalf("GetTokenByHash = %+v, %v", got, err)
	}
	if _, err := s.GetTokenByHash(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("unknown hash = %v", err)
	}
	mine, err := s.ListTokens(ctx, "u1")
	if err != nil || len(mine) != 2 || mine[0].ID != "t1" || mine[1].ID != "t2" {
		t.Errorf("ListTokens = %+v, %v", mine, err)
	}
	used := testNow.Add(time.Minute)
	if err := s.TouchToken(ctx, "t1", used); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTokenByHash(ctx, "h1"); got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
		t.Errorf("last_used = %v", got.LastUsedAt)
	}
	// One user cannot revoke another's token.
	if err := s.DeleteToken(ctx, "u2", "t1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("foreign delete = %v", err)
	}
	if _, err := s.GetTokenByHash(ctx, "h1"); err != nil {
		t.Errorf("foreign delete removed the token: %v", err)
	}
	if err := s.DeleteToken(ctx, "u1", "t1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTokensOf(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if mine, _ := s.ListTokens(ctx, "u1"); len(mine) != 0 {
		t.Errorf("after DeleteTokensOf = %+v", mine)
	}
	if theirs, _ := s.ListTokens(ctx, "u2"); len(theirs) != 1 {
		t.Errorf("DeleteTokensOf reached another user: %+v", theirs)
	}
}

// Deleting a user takes everything that proves identity with them: there is
// no way back in through a stale session or token.
func TestUserRowsDieWithTheUser(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seedUsers(t, s, mkUser("u1", "alice", "a@x"))
	if err := s.CreateSession(ctx, auth.Session{ID: "s1", UserID: "u1", CreatedAt: testNow, ExpiresAt: testNow, LastSeenAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateToken(ctx, auth.Token{ID: "t1", UserID: "u1", Name: "n", Hash: "h", Prefix: "p", CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMethod(ctx, auth.Method{ID: "m1", UserID: "u1", Kind: "password", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, "s1"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("session = %v", err)
	}
	if _, err := s.GetTokenByHash(ctx, "h"); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("token = %v", err)
	}
	if ms, _ := s.ListMethods(ctx, "u1"); len(ms) != 0 {
		t.Errorf("methods = %+v", ms)
	}
}
