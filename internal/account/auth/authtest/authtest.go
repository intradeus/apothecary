// Package authtest is the auth.DB contract, run against every implementation:
// Mem in auth's own tests, Postgres in pgstore's. Production runs only
// Postgres, so what it does is what this asserts: a Mem that differs makes
// the unit tier prove the wrong thing about who may get in.
//
// The port has no way to delete a user, so what goes with one (the ON DELETE
// CASCADEs) is not here; pgstore's TestUserRowsDieWithTheUser covers that
// through SQL. Every parent this suite needs is a user, which the port makes,
// so unlike filetest it takes no seed hook.
package authtest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"apothecary/internal/account/auth"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// asRead is a time the way a timestamptz column hands it back: whole
// microseconds, truncated rather than rounded, in the process's zone.
func asRead(at time.Time) time.Time { return at.Truncate(time.Microsecond).Local() }

func readPtr(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	v := asRead(*at)
	return &v
}

func ptr(at time.Time) *time.Time { return &at }

func user(id, handle, email string) auth.User {
	return auth.User{ID: id, Handle: handle, Email: email, Name: "N " + id, CreatedAt: t0, UpdatedAt: t0}
}

func session(id, owner string) auth.Session {
	return auth.Session{ID: id, UserID: owner, CreatedAt: t0, ExpiresAt: t0.Add(time.Hour), LastSeenAt: t0, UserAgent: "ua"}
}

func token(id, owner, hash string) auth.Token {
	return auth.Token{ID: id, UserID: owner, Name: "n " + id, Hash: hash, Prefix: "apo_" + id, CreatedAt: t0}
}

func sshKey(id, owner, fingerprint string) auth.SSHKey {
	return auth.SSHKey{ID: id, UserID: owner, Name: "n " + id, Type: "ssh-ed25519", PublicKey: "ssh-ed25519 AAAA" + id, Fingerprint: fingerprint, CreatedAt: t0}
}

func password(id, owner string) auth.Method {
	return auth.Method{ID: id, UserID: owner, Kind: auth.KindPassword, Secret: []byte("hash " + id), CreatedAt: t0, UpdatedAt: t0}
}

func oauth(id, owner, subject string) auth.Method {
	return auth.Method{ID: id, UserID: owner, Kind: "oauth", Subject: subject, Config: map[string]any{"provider": "idp"}, CreatedAt: t0, UpdatedAt: t0}
}

// seedUsers makes u1 and u2, the owners most cases need.
func seedUsers(t *testing.T, db auth.DB) {
	t.Helper()
	for _, u := range []auth.User{user("u1", "alice", "a@x"), user("u2", "bob", "b@x")} {
		if err := db.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
}

// refused is a raw constraint error: Postgres maps neither a missing owner nor
// a duplicate session or token onto a sentinel, so a caller that branches on
// one would work on a twin that returned it and fail in production.
func refused(err error) bool {
	return err != nil && !errors.Is(err, auth.ErrConflict) && !errors.Is(err, auth.ErrNotFound)
}

func ids[T any](rows []T, id func(T) string) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, id(r))
	}
	return out
}

func methodIDs(rows []auth.Method) []string {
	return ids(rows, func(m auth.Method) string { return m.ID })
}
func tokenIDs(rows []auth.Token) []string {
	return ids(rows, func(k auth.Token) string { return k.ID })
}
func keyIDs(rows []auth.SSHKey) []string {
	return ids(rows, func(k auth.SSHKey) string { return k.ID })
}
func userIDs(rows []auth.User) []string { return ids(rows, func(u auth.User) string { return u.ID }) }

// New is a service for tests: real in every way but the cost of a new password
// hash, which would otherwise be most of a package's run.
func New(db auth.DB) *auth.Service {
	s := auth.New(db)
	s.HashCost = auth.CheapHash
	return s
}

// RunContract exercises one implementation. open must return a fresh, empty
// store.
func RunContract(t *testing.T, open func(t *testing.T) auth.DB) {
	t.Helper()
	ctx := context.Background()

	t.Run("user roundtrip", func(t *testing.T) {
		db := open(t)
		if n, err := db.CountUsers(ctx); err != nil || n != 0 {
			t.Fatalf("empty count = %d, %v", n, err)
		}
		// An empty table lists as nil, not as an empty slice.
		if list, err := db.ListUsers(ctx); list != nil || err != nil {
			t.Errorf("empty list = %#v, %v", list, err)
		}
		plain := user("u1", "alice", "a@x")
		full := user("u2", "Bob", "Bob@X")
		full.Admin = true
		full.DisabledAt, full.LastSeenAt = ptr(t0.Add(time.Hour)), ptr(t0.Add(2*time.Hour))
		full.UpdatedAt = t0.Add(3 * time.Hour)
		for _, u := range []auth.User{plain, full} {
			if err := db.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		for _, want := range []auth.User{plain, full} {
			want.CreatedAt, want.UpdatedAt = asRead(want.CreatedAt), asRead(want.UpdatedAt)
			want.DisabledAt, want.LastSeenAt = readPtr(want.DisabledAt), readPtr(want.LastSeenAt)
			if got, err := db.GetUser(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("roundtrip = %+v, %v, want %+v", got, err, want)
			}
		}
		if n, _ := db.CountUsers(ctx); n != 2 {
			t.Errorf("count = %d", n)
		}
		if _, err := db.GetUser(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("missing = %v", err)
		}
	})

	t.Run("timestamps are stored in microseconds", func(t *testing.T) {
		db := open(t)
		at := time.Date(2026, 3, 4, 7, 6, 7, 999_999_999, time.FixedZone("elsewhere", 2*60*60))
		u := user("u1", "alice", "a@x")
		u.CreatedAt, u.UpdatedAt, u.DisabledAt, u.LastSeenAt = at, at, &at, &at
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		want := asRead(at)
		got, err := db.GetUser(ctx, "u1")
		if err != nil || got.CreatedAt != want || got.UpdatedAt != want || *got.DisabledAt != want || *got.LastSeenAt != want {
			t.Errorf("user = %+v, %v, want every time %v", got, err, want)
		}
		if err := db.TouchUser(ctx, "u1", at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if got, _ := db.GetUser(ctx, "u1"); *got.LastSeenAt != asRead(at.Add(time.Second)) {
			t.Errorf("touched user = %v", got.LastSeenAt)
		}

		m := password("m1", "u1")
		m.CreatedAt, m.UpdatedAt, m.LastUsedAt = at, at, &at
		if err := db.PutMethod(ctx, m); err != nil {
			t.Fatal(err)
		}
		gm, err := db.GetMethod(ctx, "u1", auth.KindPassword)
		if err != nil || gm.CreatedAt != want || gm.UpdatedAt != want || *gm.LastUsedAt != want {
			t.Errorf("method = %+v, %v", gm, err)
		}

		s := session("s1", "u1")
		s.CreatedAt, s.ExpiresAt, s.LastSeenAt = at, at, at
		if err := db.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
		gs, err := db.GetSession(ctx, "s1")
		if err != nil || gs.CreatedAt != want || gs.ExpiresAt != want || gs.LastSeenAt != want {
			t.Errorf("session = %+v, %v", gs, err)
		}
		if err := db.TouchSession(ctx, "s1", at.Add(time.Second), at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if gs, _ := db.GetSession(ctx, "s1"); gs.LastSeenAt != asRead(at.Add(time.Second)) || gs.ExpiresAt != asRead(at.Add(time.Minute)) {
			t.Errorf("touched session = %+v", gs)
		}

		k := token("t1", "u1", "h1")
		k.CreatedAt, k.LastUsedAt, k.ExpiresAt = at, &at, &at
		if err := db.CreateToken(ctx, k); err != nil {
			t.Fatal(err)
		}
		gk, err := db.GetTokenByHash(ctx, "h1")
		if err != nil || gk.CreatedAt != want || *gk.LastUsedAt != want || *gk.ExpiresAt != want {
			t.Errorf("token = %+v, %v", gk, err)
		}
		if err := db.TouchToken(ctx, "t1", at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if gk, _ := db.GetTokenByHash(ctx, "h1"); *gk.LastUsedAt != asRead(at.Add(time.Second)) {
			t.Errorf("touched token = %v", gk.LastUsedAt)
		}

		key := sshKey("k1", "u1", "SHA256:one")
		key.CreatedAt, key.LastUsedAt = at, &at
		if err := db.CreateSSHKey(ctx, key); err != nil {
			t.Fatal(err)
		}
		gkey, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:one")
		if err != nil || gkey.CreatedAt != want || *gkey.LastUsedAt != want {
			t.Errorf("key = %+v, %v", gkey, err)
		}
		if err := db.TouchSSHKey(ctx, "k1", at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if gkey, _ := db.GetSSHKeyByFingerprint(ctx, "SHA256:one"); *gkey.LastUsedAt != asRead(at.Add(time.Second)) {
			t.Errorf("touched key = %v", gkey.LastUsedAt)
		}
	})

	// Postgres hands back copies: nothing a caller does to a value it wrote or
	// read reaches the row.
	t.Run("stored rows do not alias the caller's", func(t *testing.T) {
		db := open(t)
		seen, used := t0.Add(time.Minute), t0.Add(time.Hour)
		u := user("u1", "alice", "a@x")
		u.DisabledAt = &seen
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		seen = seen.Add(24 * time.Hour)
		got, _ := db.GetUser(ctx, "u1")
		*got.DisabledAt = t0
		if got, _ := db.GetUser(ctx, "u1"); !got.DisabledAt.Equal(t0.Add(time.Minute)) {
			t.Errorf("disabled_at = %v", got.DisabledAt)
		}

		m := oauth("m1", "u1", "sub")
		m.Secret, m.Config = []byte("key"), map[string]any{"nested": map[string]any{"a": "b"}}
		if err := db.PutMethod(ctx, m); err != nil {
			t.Fatal(err)
		}
		m.Secret[0] = 'X'
		m.Config["nested"].(map[string]any)["a"] = "changed"
		read, _ := db.GetMethod(ctx, "u1", "oauth")
		if string(read.Secret) != "key" || read.Config["nested"].(map[string]any)["a"] != "b" {
			t.Errorf("written values reached the row: %+v", read)
		}
		read.Secret[0] = 'X'
		read.Config["nested"].(map[string]any)["a"] = "changed"
		read, _ = db.GetMethod(ctx, "u1", "oauth")
		if string(read.Secret) != "key" || read.Config["nested"].(map[string]any)["a"] != "b" {
			t.Errorf("read values reached the row: %+v", read)
		}

		k := token("t1", "u1", "h1")
		k.ExpiresAt = &used
		if err := db.CreateToken(ctx, k); err != nil {
			t.Fatal(err)
		}
		used = used.Add(24 * time.Hour)
		gk, _ := db.GetTokenByHash(ctx, "h1")
		*gk.ExpiresAt = t0
		if gk, _ := db.GetTokenByHash(ctx, "h1"); !gk.ExpiresAt.Equal(t0.Add(time.Hour)) {
			t.Errorf("expires_at = %v", gk.ExpiresAt)
		}
	})

	t.Run("handle and email are unique whatever the case", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		if err := db.CreateUser(ctx, user("u3", "ALICE", "c@x")); !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "handle") {
			t.Errorf("handle in another case = %v", err)
		}
		if err := db.CreateUser(ctx, user("u3", "carol", "A@X")); !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "email") {
			t.Errorf("email in another case = %v", err)
		}
		// Handle before email when both are taken, by two different users.
		if err := db.CreateUser(ctx, user("u3", "alice", "b@x")); !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "handle") {
			t.Errorf("both taken = %v", err)
		}
		if err := db.CreateUser(ctx, user("u1", "carol", "c@x")); !errors.Is(err, auth.ErrConflict) {
			t.Errorf("duplicate id = %v", err)
		}
		if n, _ := db.CountUsers(ctx); n != 2 {
			t.Errorf("a refused create stored: count = %d", n)
		}
		if got, _ := db.GetUser(ctx, "u1"); got.Handle != "alice" || got.Email != "a@x" {
			t.Errorf("a duplicate id overwrote the row: %+v", got)
		}

		bob, _ := db.GetUser(ctx, "u2")
		bob.Handle = "ALICE"
		if err := db.UpdateUser(ctx, bob); !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "handle") {
			t.Errorf("update onto a taken handle = %v", err)
		}
		bob.Handle, bob.Email = "bob", "A@x"
		if err := db.UpdateUser(ctx, bob); !errors.Is(err, auth.ErrConflict) || !strings.Contains(err.Error(), "email") {
			t.Errorf("update onto a taken email = %v", err)
		}
		if got, _ := db.GetUser(ctx, "u2"); got.Handle != "bob" || got.Email != "b@x" {
			t.Errorf("a refused update wrote: %+v", got)
		}
		// Your own row is no conflict, and the case you gave is the case kept.
		bob.Handle, bob.Email = "BOB", "B@X"
		if err := db.UpdateUser(ctx, bob); err != nil {
			t.Errorf("re-casing your own = %v", err)
		}
		if got, _ := db.GetUser(ctx, "u2"); got.Handle != "BOB" || got.Email != "B@X" {
			t.Errorf("stored case = %+v", got)
		}
	})

	// The uniqueness and the login match are lower(a)=lower(b) under the
	// image's en_US.utf8: non-ASCII letters fold, and what lower() leaves
	// alone stays apart even where Unicode case folding would join it (ſ
	// folds to s, but lowers to itself).
	t.Run("case folding is lower()'s, not Unicode's", func(t *testing.T) {
		db := open(t)
		if err := db.CreateUser(ctx, user("u1", "sam", "ÉMILE@x")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateUser(ctx, user("u2", "carol", "émile@X")); !errors.Is(err, auth.ErrConflict) {
			t.Errorf("email differing in a non-ASCII case = %v", err)
		}
		if got, err := db.GetUserByLogin(ctx, "émile@x"); err != nil || got.ID != "u1" {
			t.Errorf("non-ASCII login = %+v, %v", got, err)
		}
		if _, err := db.GetUserByLogin(ctx, "ſam"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("a login lower() keeps apart matched: %v", err)
		}
		if err := db.CreateUser(ctx, user("u3", "ſam", "other@x")); err != nil {
			t.Errorf("a handle lower() keeps apart conflicted: %v", err)
		}
	})

	t.Run("login finds a handle or an email, in any case", func(t *testing.T) {
		db := open(t)
		if err := db.CreateUser(ctx, user("u1", "Alice", "Alice@Example.com")); err != nil {
			t.Fatal(err)
		}
		for _, login := range []string{"Alice", "alice", "ALICE", "Alice@Example.com", "alice@example.COM"} {
			if got, err := db.GetUserByLogin(ctx, login); err != nil || got.ID != "u1" {
				t.Errorf("login %q = %+v, %v", login, got, err)
			}
		}
		for _, login := range []string{"ghost", "", " alice", "alice@"} {
			if _, err := db.GetUserByLogin(ctx, login); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("login %q = %v", login, err)
			}
		}
	})

	t.Run("update writes the profile, not the record", func(t *testing.T) {
		db := open(t)
		u := user("u1", "alice", "a@x")
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if err := db.TouchUser(ctx, "u1", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		// A row read before the touch and written after must not lose it, nor
		// can an update move created_at.
		stale := u
		stale.Handle, stale.Email, stale.Name, stale.Admin = "al", "al@x", "Al", true
		stale.DisabledAt, stale.UpdatedAt = ptr(t0.Add(time.Hour)), t0.Add(2*time.Hour)
		stale.CreatedAt, stale.LastSeenAt = t0.Add(99*time.Hour), ptr(t0.Add(99*time.Hour))
		if err := db.UpdateUser(ctx, stale); err != nil {
			t.Fatal(err)
		}
		want := auth.User{ID: "u1", Handle: "al", Email: "al@x", Name: "Al", Admin: true,
			DisabledAt: readPtr(ptr(t0.Add(time.Hour))), LastSeenAt: readPtr(ptr(t0.Add(time.Minute))),
			CreatedAt: asRead(t0), UpdatedAt: asRead(t0.Add(2 * time.Hour))}
		if got, err := db.GetUser(ctx, "u1"); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("after update = %+v, %v, want %+v", got, err, want)
		}
		if err := db.UpdateUser(ctx, user("nobody", "x", "y")); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("update of nobody = %v", err)
		}
		if n, _ := db.CountUsers(ctx); n != 1 {
			t.Errorf("update created a row: count = %d", n)
		}
		if err := db.TouchUser(ctx, "nobody", t0); err != nil {
			t.Errorf("touch of nobody = %v", err)
		}
	})

	t.Run("users list by created_at, then id", func(t *testing.T) {
		db := open(t)
		late, b, a, early := user("late", "h1", "e1"), user("b", "h2", "e2"), user("a", "h3", "e3"), user("early", "h4", "e4")
		late.CreatedAt, early.CreatedAt = t0.Add(time.Minute), t0.Add(-time.Minute)
		for _, u := range []auth.User{late, b, a, early} {
			if err := db.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
		}
		list, err := db.ListUsers(ctx)
		if err != nil || !slices.Equal(userIDs(list), []string{"early", "a", "b", "late"}) {
			t.Errorf("list = %v, %v", userIDs(list), err)
		}
	})

	// The port knows no policy: a disabled user reads like any other, which is
	// how the service tells a refused account from a missing one.
	t.Run("a disabled user is still there", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		if err := db.CreateSession(ctx, session("s1", "u1")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateToken(ctx, token("t1", "u1", "h1")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateSSHKey(ctx, sshKey("k1", "u1", "SHA256:one")); err != nil {
			t.Fatal(err)
		}
		u, _ := db.GetUser(ctx, "u1")
		u.DisabledAt = ptr(t0.Add(time.Hour))
		if err := db.UpdateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if got, err := db.GetUser(ctx, "u1"); err != nil || !got.Disabled() {
			t.Errorf("get = %+v, %v", got, err)
		}
		if got, err := db.GetUserByLogin(ctx, "alice"); err != nil || !got.Disabled() {
			t.Errorf("by login = %+v, %v", got, err)
		}
		if list, _ := db.ListUsers(ctx); len(list) != 2 {
			t.Errorf("list = %+v", list)
		}
		if n, _ := db.CountUsers(ctx); n != 2 {
			t.Errorf("count = %d", n)
		}
		// Credentials are the service's to revoke; disabling touches none.
		if _, err := db.GetSession(ctx, "s1"); err != nil {
			t.Errorf("session = %v", err)
		}
		if _, err := db.GetTokenByHash(ctx, "h1"); err != nil {
			t.Errorf("token = %v", err)
		}
		if _, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:one"); err != nil {
			t.Errorf("key = %v", err)
		}
		// Enabling is writing nil back.
		u.DisabledAt = nil
		if err := db.UpdateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if got, _ := db.GetUser(ctx, "u1"); got.Disabled() || got.DisabledAt != nil {
			t.Errorf("re-enabled = %+v", got)
		}
	})

	t.Run("method roundtrip: nil reads back empty", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		if err := db.PutMethod(ctx, auth.Method{ID: "m1", UserID: "u1", Kind: "oauth", CreatedAt: t0, UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		// Not nil: the columns are NOT NULL DEFAULT '' and '{}'.
		got, err := db.GetMethod(ctx, "u1", "oauth")
		if err != nil || got.Secret == nil || len(got.Secret) != 0 || got.Config == nil || len(got.Config) != 0 {
			t.Errorf("nil secret and config = %#v, %#v, %v", got.Secret, got.Config, err)
		}
		used := t0.Add(time.Minute)
		full := auth.Method{ID: "m2", UserID: "u2", Kind: "passkey", Subject: "cred", Secret: []byte{0, 1, 0xff},
			Config: map[string]any{"label": "phone"}, CreatedAt: t0, UpdatedAt: t0.Add(time.Hour), LastUsedAt: &used}
		if err := db.PutMethod(ctx, full); err != nil {
			t.Fatal(err)
		}
		want := full
		want.CreatedAt, want.UpdatedAt, want.LastUsedAt = asRead(t0), asRead(t0.Add(time.Hour)), readPtr(&used)
		if got, err := db.GetMethod(ctx, "u2", "passkey"); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("roundtrip = %+v, %v, want %+v", got, err, want)
		}
	})

	// Config lives in a jsonb column: it comes back as JSON decodes it, and
	// what JSON cannot say never reaches the row.
	t.Run("method config is JSON", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		m := oauth("m1", "u1", "sub")
		m.Config = map[string]any{"n": 3, "list": []string{"a"}, "ok": true, "none": nil, "nested": map[string]string{"k": "v"}}
		if err := db.PutMethod(ctx, m); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetMethod(ctx, "u1", "oauth")
		want := map[string]any{"n": 3.0, "list": []any{"a"}, "ok": true, "none": nil, "nested": map[string]any{"k": "v"}}
		if err != nil || !reflect.DeepEqual(got.Config, want) {
			t.Errorf("config = %#v, %v, want %#v", got.Config, err, want)
		}
		bad := oauth("m2", "u1", "other")
		bad.Config = map[string]any{"x": make(chan int)}
		if err := db.PutMethod(ctx, bad); err == nil {
			t.Error("an unmarshalable config was stored")
		}
		bad.Config = map[string]any{"x": "a\x00b"}
		if err := db.PutMethod(ctx, bad); err == nil {
			t.Error("a NUL in config was stored")
		}
		if ms, _ := db.ListMethods(ctx, "u1"); len(ms) != 1 {
			t.Errorf("methods = %+v", ms)
		}
		// Text that only reads like the escape is text.
		quoted := oauth("m3", "u1", "quoted")
		quoted.Config = map[string]any{"x": `\u0000`}
		if err := db.PutMethod(ctx, quoted); err != nil {
			t.Fatal(err)
		}
		if got, err := db.FindMethodBySubject(ctx, "oauth", "quoted"); err != nil || got.Config["x"] != `\u0000` {
			t.Errorf("escaped backslash = %#v, %v", got.Config, err)
		}
	})

	t.Run("method lookups and scope", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		if err := db.PutMethod(ctx, password("m1", "u1")); err != nil {
			t.Fatal(err)
		}
		if err := db.PutMethod(ctx, oauth("m2", "u1", "iss|sub")); err != nil {
			t.Fatal(err)
		}
		if got, err := db.GetMethod(ctx, "u1", auth.KindPassword); err != nil || got.ID != "m1" || string(got.Secret) != "hash m1" {
			t.Errorf("get = %+v, %v", got, err)
		}
		if _, err := db.GetMethod(ctx, "u2", auth.KindPassword); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("another user's method = %v", err)
		}
		if _, err := db.GetMethod(ctx, "u1", "passkey"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("another kind = %v", err)
		}
		if got, err := db.FindMethodBySubject(ctx, "oauth", "iss|sub"); err != nil || got.ID != "m2" || got.UserID != "u1" {
			t.Errorf("by subject = %+v, %v", got, err)
		}
		// Subjects match byte for byte, and only within their kind.
		for _, c := range [][2]string{{"oauth", "ISS|SUB"}, {"oauth", "other"}, {"OAUTH", "iss|sub"}, {"passkey", "iss|sub"}} {
			if _, err := db.FindMethodBySubject(ctx, c[0], c[1]); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("by subject %v = %v", c, err)
			}
		}
		// An empty subject matches nothing: every password row has one.
		if _, err := db.FindMethodBySubject(ctx, auth.KindPassword, ""); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("empty subject = %v", err)
		}
		if ms, err := db.ListMethods(ctx, "u2"); ms != nil || err != nil {
			t.Errorf("nobody's methods = %#v, %v", ms, err)
		}
	})

	t.Run("method order: created_at, then id; the oldest of a kind", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		mk := func(id, subject string, at time.Time) auth.Method {
			m := oauth(id, "u1", subject)
			m.CreatedAt = at
			return m
		}
		for _, m := range []auth.Method{mk("late", "s1", t0.Add(time.Minute)), mk("b", "s2", t0), mk("a", "s3", t0), mk("early", "s4", t0.Add(-time.Minute))} {
			if err := db.PutMethod(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		ms, err := db.ListMethods(ctx, "u1")
		if err != nil || !slices.Equal(methodIDs(ms), []string{"early", "a", "b", "late"}) {
			t.Errorf("list = %v, %v", methodIDs(ms), err)
		}
		if got, err := db.GetMethod(ctx, "u1", "oauth"); err != nil || got.ID != "early" {
			t.Errorf("oldest of the kind = %+v, %v", got, err)
		}
	})

	t.Run("one password per user, one owner per subject", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		if err := db.PutMethod(ctx, password("m1", "u1")); err != nil {
			t.Fatal(err)
		}
		if err := db.PutMethod(ctx, password("m2", "u1")); !errors.Is(err, auth.ErrConflict) {
			t.Errorf("second password = %v", err)
		}
		if err := db.PutMethod(ctx, password("m3", "u2")); err != nil {
			t.Errorf("another user's password = %v", err)
		}
		// A use is the last-used time and nothing else: this is what stops a
		// login writing back a secret it read before a reset.
		before, err := db.GetMethod(ctx, "u2", auth.KindPassword)
		if err != nil {
			t.Fatal(err)
		}
		used := t0.Add(time.Hour)
		if err := db.TouchMethod(ctx, "m3", used); err != nil {
			t.Fatal(err)
		}
		after, err := db.GetMethod(ctx, "u2", auth.KindPassword)
		if err != nil || after.LastUsedAt == nil || !after.LastUsedAt.Equal(asRead(used)) {
			t.Fatalf("touched = %+v, %v", after, err)
		}
		after.LastUsedAt = before.LastUsedAt
		if !reflect.DeepEqual(after, before) {
			t.Errorf("a touch changed more than the last use:\n got %+v\nwant %+v", after, before)
		}
		if err := db.TouchMethod(ctx, "nobody", used); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("touching nothing = %v", err)
		}
		// Rotating is re-putting the one row, no second password.
		rotated := password("m1", "u1")
		rotated.Secret = []byte("rotated")
		if err := db.PutMethod(ctx, rotated); err != nil {
			t.Errorf("rotating a password = %v", err)
		}
		if got, err := db.GetMethod(ctx, "u1", auth.KindPassword); err != nil || string(got.Secret) != "rotated" {
			t.Errorf("rotated = %+v, %v", got, err)
		}
		if err := db.PutMethod(ctx, oauth("m4", "u1", "sub")); err != nil {
			t.Fatal(err)
		}
		// The security of "sign in with": nobody can take over a subject.
		if err := db.PutMethod(ctx, oauth("m5", "u2", "sub")); !errors.Is(err, auth.ErrConflict) {
			t.Errorf("subject claimed twice = %v", err)
		}
		if got, err := db.FindMethodBySubject(ctx, "oauth", "sub"); err != nil || got.UserID != "u1" {
			t.Errorf("subject's owner = %+v, %v", got, err)
		}
		// Unique per kind, byte for byte; several rows may have none.
		other := oauth("m6", "u2", "sub")
		other.Kind = "passkey"
		if err := db.PutMethod(ctx, other); err != nil {
			t.Errorf("same subject, other kind = %v", err)
		}
		if err := db.PutMethod(ctx, oauth("m7", "u2", "SUB")); err != nil {
			t.Errorf("subject in another case = %v", err)
		}
		for _, id := range []string{"m8", "m9"} {
			if err := db.PutMethod(ctx, oauth(id, "u1", "")); err != nil {
				t.Errorf("empty subject %s = %v", id, err)
			}
		}
		// An update hits the same index an insert does.
		moved := oauth("m7", "u2", "sub")
		if err := db.PutMethod(ctx, moved); !errors.Is(err, auth.ErrConflict) {
			t.Errorf("update onto a taken subject = %v", err)
		}
		if got, _ := db.FindMethodBySubject(ctx, "oauth", "SUB"); got.ID != "m7" {
			t.Errorf("refused update moved the subject: %+v", got)
		}
	})

	t.Run("re-putting a method updates it", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		m := oauth("m1", "u1", "sub")
		if err := db.PutMethod(ctx, m); err != nil {
			t.Fatal(err)
		}
		used := t0.Add(time.Minute)
		m.Subject, m.Secret, m.Config, m.UpdatedAt, m.LastUsedAt = "sub2", []byte("new"), map[string]any{"k": "v"}, t0.Add(time.Hour), &used
		if err := db.PutMethod(ctx, m); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetMethod(ctx, "u1", "oauth")
		if err != nil || got.Subject != "sub2" || string(got.Secret) != "new" || got.Config["k"] != "v" ||
			!got.UpdatedAt.Equal(t0.Add(time.Hour)) || got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
			t.Errorf("updated = %+v, %v", got, err)
		}
		// Clearing goes through too: an update replaces, it does not merge.
		m.Secret, m.Config, m.LastUsedAt = nil, nil, nil
		if err := db.PutMethod(ctx, m); err != nil {
			t.Fatal(err)
		}
		got, _ = db.GetMethod(ctx, "u1", "oauth")
		if len(got.Secret) != 0 || got.Config == nil || len(got.Config) != 0 || got.LastUsedAt != nil {
			t.Errorf("cleared = %+v", got)
		}
		// What identifies a method is not an update's to change. Deliberately
		// silent on the secret: this put names another user and still replaces
		// it, which is not behaviour to promise.
		m.UserID, m.Kind, m.CreatedAt = "u2", "passkey", t0.Add(9*time.Hour)
		if err := db.PutMethod(ctx, m); err != nil {
			t.Fatal(err)
		}
		got, err = db.GetMethod(ctx, "u1", "oauth")
		if err != nil || got.UserID != "u1" || got.Kind != "oauth" || !got.CreatedAt.Equal(t0) {
			t.Errorf("re-put changed the method's identity: %+v, %v", got, err)
		}
		if ms, _ := db.ListMethods(ctx, "u2"); ms != nil {
			t.Errorf("a method moved to another user: %+v", ms)
		}
		// The owner key is checked when a row is inserted, and this is none.
		m.UserID = "ghost"
		if err := db.PutMethod(ctx, m); err != nil {
			t.Errorf("re-put naming nobody = %v", err)
		}
	})

	t.Run("deleting a method", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		for _, m := range []auth.Method{password("m1", "u1"), oauth("m2", "u1", "sub"), password("m3", "u2")} {
			if err := db.PutMethod(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.DeleteMethod(ctx, "m1"); err != nil {
			t.Fatal(err)
		}
		if ms, _ := db.ListMethods(ctx, "u1"); !slices.Equal(methodIDs(ms), []string{"m2"}) {
			t.Errorf("after delete = %v", methodIDs(ms))
		}
		if _, err := db.GetMethod(ctx, "u1", auth.KindPassword); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("deleted method = %v", err)
		}
		// Unlike a session, token or key, a missing method is no error.
		if err := db.DeleteMethod(ctx, "m1"); err != nil {
			t.Errorf("second delete = %v", err)
		}
		// The slot is free again.
		if err := db.PutMethod(ctx, password("m4", "u1")); err != nil {
			t.Errorf("new password after delete = %v", err)
		}
	})

	t.Run("session roundtrip and touch", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		s := session("s1", "u1")
		s.UserAgent = ""
		for _, se := range []auth.Session{s, session("s2", "u1")} {
			if err := db.CreateSession(ctx, se); err != nil {
				t.Fatal(err)
			}
		}
		want := s
		want.CreatedAt, want.ExpiresAt, want.LastSeenAt = asRead(s.CreatedAt), asRead(s.ExpiresAt), asRead(s.LastSeenAt)
		if got, err := db.GetSession(ctx, "s1"); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("roundtrip = %+v, %v, want %+v", got, err, want)
		}
		if _, err := db.GetSession(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("missing = %v", err)
		}
		// Ids are the hash of the cookie: exact, never case-folded.
		if _, err := db.GetSession(ctx, "S1"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("id in another case = %v", err)
		}
		seen, exp := t0.Add(time.Minute), t0.Add(2*time.Hour)
		if err := db.TouchSession(ctx, "s1", seen, exp); err != nil {
			t.Fatal(err)
		}
		got, _ := db.GetSession(ctx, "s1")
		if !got.LastSeenAt.Equal(seen) || !got.ExpiresAt.Equal(exp) || !got.CreatedAt.Equal(t0) || got.UserID != "u1" {
			t.Errorf("touched = %+v", got)
		}
		if other, _ := db.GetSession(ctx, "s2"); !other.ExpiresAt.Equal(t0.Add(time.Hour)) {
			t.Errorf("touch reached another session: %+v", other)
		}
		if err := db.TouchSession(ctx, "nope", seen, exp); err != nil {
			t.Errorf("touch of nothing = %v", err)
		}
		if _, err := db.GetSession(ctx, "nope"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("touch made a row: %v", err)
		}
	})

	// Expiry is a column to the port and a rule to the service: the row reads
	// back past its date, so the service can delete it and answer 401.
	t.Run("expiry is the caller's", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		past := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Microsecond)
		s := session("s1", "u1")
		s.ExpiresAt = past
		if err := db.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
		if got, err := db.GetSession(ctx, "s1"); err != nil || !got.ExpiresAt.Equal(past) {
			t.Errorf("expired session = %+v, %v", got, err)
		}
		k := token("t1", "u1", "h1")
		k.ExpiresAt = &past
		if err := db.CreateToken(ctx, k); err != nil {
			t.Fatal(err)
		}
		if got, err := db.GetTokenByHash(ctx, "h1"); err != nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(past) {
			t.Errorf("expired token = %+v, %v", got, err)
		}
		// No expiry stays no expiry.
		if err := db.CreateToken(ctx, token("t2", "u1", "h2")); err != nil {
			t.Fatal(err)
		}
		if got, err := db.GetTokenByHash(ctx, "h2"); err != nil || got.ExpiresAt != nil || got.LastUsedAt != nil {
			t.Errorf("token without expiry = %+v, %v", got, err)
		}
	})

	t.Run("duplicate sessions and tokens are refused, not replaced", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		if err := db.CreateSession(ctx, session("s1", "u1")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateSession(ctx, session("s1", "u2")); !refused(err) {
			t.Errorf("duplicate session = %v", err)
		}
		if got, _ := db.GetSession(ctx, "s1"); got.UserID != "u1" {
			t.Errorf("a session changed hands: %+v", got)
		}
		if err := db.CreateToken(ctx, token("t1", "u1", "h1")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateToken(ctx, token("t1", "u2", "h2")); !refused(err) {
			t.Errorf("duplicate token id = %v", err)
		}
		if err := db.CreateToken(ctx, token("t2", "u2", "h1")); !refused(err) {
			t.Errorf("duplicate token hash = %v", err)
		}
		if got, err := db.GetTokenByHash(ctx, "h1"); err != nil || got.ID != "t1" || got.UserID != "u1" {
			t.Errorf("a token changed hands: %+v, %v", got, err)
		}
		if _, err := db.GetTokenByHash(ctx, "h2"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("a refused token is readable: %v", err)
		}
	})

	t.Run("credentials need a user", func(t *testing.T) {
		db := open(t)
		if err := db.PutMethod(ctx, password("m1", "ghost")); !refused(err) {
			t.Errorf("method for nobody = %v", err)
		}
		if err := db.CreateSession(ctx, session("s1", "ghost")); !refused(err) {
			t.Errorf("session for nobody = %v", err)
		}
		if err := db.CreateToken(ctx, token("t1", "ghost", "h1")); !refused(err) {
			t.Errorf("token for nobody = %v", err)
		}
		if err := db.CreateSSHKey(ctx, sshKey("k1", "ghost", "SHA256:one")); !refused(err) {
			t.Errorf("key for nobody = %v", err)
		}
		if _, err := db.GetSession(ctx, "s1"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("a refused session is readable: %v", err)
		}
		if _, err := db.GetTokenByHash(ctx, "h1"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("a refused token is readable: %v", err)
		}
		if _, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:one"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("a refused key is readable: %v", err)
		}
	})

	// What a text column cannot hold never gets in. Secrets are bytes and hold
	// anything. Lookups with such text are not asserted: Postgres answers with
	// a raw encoding error, where ErrNotFound is what a caller can act on.
	t.Run("text columns refuse NUL and invalid UTF-8", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		for _, bad := range []string{"a\x00b", "a\xffb"} {
			u := user("u3", "carol", "c@x")
			u.Name = bad
			if err := db.CreateUser(ctx, u); !refused(err) {
				t.Errorf("user name %q = %v", bad, err)
			}
			me, _ := db.GetUser(ctx, "u1")
			me.Name = bad
			if err := db.UpdateUser(ctx, me); !refused(err) {
				t.Errorf("updated name %q = %v", bad, err)
			}
			if err := db.PutMethod(ctx, oauth("m1", "u1", bad)); !refused(err) {
				t.Errorf("subject %q = %v", bad, err)
			}
			s := session("s1", "u1")
			s.UserAgent = bad
			if err := db.CreateSession(ctx, s); !refused(err) {
				t.Errorf("user agent %q = %v", bad, err)
			}
			k := token("t1", "u1", "h1")
			k.Name = bad
			if err := db.CreateToken(ctx, k); !refused(err) {
				t.Errorf("token name %q = %v", bad, err)
			}
			key := sshKey("k1", "u1", "SHA256:one")
			key.Name = bad
			if err := db.CreateSSHKey(ctx, key); !refused(err) {
				t.Errorf("key name %q = %v", bad, err)
			}
		}
		if n, _ := db.CountUsers(ctx); n != 2 {
			t.Errorf("count = %d", n)
		}
		if got, _ := db.GetUser(ctx, "u1"); got.Name != "N u1" {
			t.Errorf("a refused update wrote: %+v", got)
		}
		secret := password("m1", "u1")
		secret.Secret = []byte("a\x00\xffb")
		if err := db.PutMethod(ctx, secret); err != nil {
			t.Errorf("binary secret = %v", err)
		}
		if got, _ := db.GetMethod(ctx, "u1", auth.KindPassword); string(got.Secret) != "a\x00\xffb" {
			t.Errorf("secret = %q", got.Secret)
		}
	})

	t.Run("session deletes", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		for _, s := range []auth.Session{session("s1", "u1"), session("s2", "u1"), session("s3", "u2")} {
			if err := db.CreateSession(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.DeleteSession(ctx, "s1"); err != nil {
			t.Fatal(err)
		}
		if err := db.DeleteSession(ctx, "s1"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("second delete = %v", err)
		}
		if err := db.DeleteSessionsOf(ctx, "u1"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetSession(ctx, "s2"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("u1's session survived: %v", err)
		}
		if _, err := db.GetSession(ctx, "s3"); err != nil {
			t.Errorf("sign-out-everywhere reached another user: %v", err)
		}
		if err := db.DeleteSessionsOf(ctx, "ghost"); err != nil {
			t.Errorf("sessions of nobody = %v", err)
		}
	})

	t.Run("each delete takes its own table only", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		if err := db.PutMethod(ctx, password("m1", "u1")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateSession(ctx, session("s1", "u1")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateToken(ctx, token("t1", "u1", "h1")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateSSHKey(ctx, sshKey("k1", "u1", "SHA256:one")); err != nil {
			t.Fatal(err)
		}
		if err := db.DeleteSessionsOf(ctx, "u1"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetTokenByHash(ctx, "h1"); err != nil {
			t.Errorf("DeleteSessionsOf took a token: %v", err)
		}
		if err := db.DeleteTokensOf(ctx, "u1"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetTokenByHash(ctx, "h1"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("token survived: %v", err)
		}
		if _, err := db.GetMethod(ctx, "u1", auth.KindPassword); err != nil {
			t.Errorf("a delete took the password: %v", err)
		}
		// Keys outlive a disabled account's sessions and tokens by design.
		if _, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:one"); err != nil {
			t.Errorf("a delete took the key: %v", err)
		}
		if _, err := db.GetUser(ctx, "u1"); err != nil {
			t.Errorf("a delete took the user: %v", err)
		}
	})

	t.Run("tokens: lookup, list, scope", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		exp := t0.Add(time.Hour)
		a, b, late, early := token("a", "u1", "ha"), token("b", "u1", "hb"), token("late", "u1", "hl"), token("early", "u1", "he")
		a.ExpiresAt = &exp
		late.CreatedAt, early.CreatedAt = t0.Add(time.Minute), t0.Add(-time.Minute)
		for _, k := range []auth.Token{late, b, a, early, token("theirs", "u2", "ht")} {
			if err := db.CreateToken(ctx, k); err != nil {
				t.Fatal(err)
			}
		}
		want := a
		want.CreatedAt, want.ExpiresAt = asRead(a.CreatedAt), readPtr(a.ExpiresAt)
		if got, err := db.GetTokenByHash(ctx, "ha"); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("roundtrip = %+v, %v, want %+v", got, err, want)
		}
		for _, hash := range []string{"nope", "HA", ""} {
			if _, err := db.GetTokenByHash(ctx, hash); !errors.Is(err, auth.ErrNotFound) {
				t.Errorf("hash %q = %v", hash, err)
			}
		}
		list, err := db.ListTokens(ctx, "u1")
		if err != nil || !slices.Equal(tokenIDs(list), []string{"early", "a", "b", "late"}) {
			t.Errorf("list = %v, %v", tokenIDs(list), err)
		}
		if list, err := db.ListTokens(ctx, "ghost"); list != nil || err != nil {
			t.Errorf("nobody's tokens = %#v, %v", list, err)
		}
		used := t0.Add(time.Minute)
		if err := db.TouchToken(ctx, "a", used); err != nil {
			t.Fatal(err)
		}
		if got, _ := db.GetTokenByHash(ctx, "ha"); got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
			t.Errorf("last_used = %v", got.LastUsedAt)
		}
		if got, _ := db.GetTokenByHash(ctx, "hb"); got.LastUsedAt != nil {
			t.Errorf("touch reached another token: %v", got.LastUsedAt)
		}
		if err := db.TouchToken(ctx, "nope", used); err != nil {
			t.Errorf("touch of nothing = %v", err)
		}
		// One user cannot revoke another's token, or tell it from a missing one.
		if err := db.DeleteToken(ctx, "u2", "a"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("foreign delete = %v", err)
		}
		if _, err := db.GetTokenByHash(ctx, "ha"); err != nil {
			t.Errorf("foreign delete took the token: %v", err)
		}
		if err := db.DeleteToken(ctx, "u1", "a"); err != nil {
			t.Fatal(err)
		}
		if err := db.DeleteToken(ctx, "u1", "a"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("second delete = %v", err)
		}
		if err := db.DeleteTokensOf(ctx, "u1"); err != nil {
			t.Fatal(err)
		}
		if list, _ := db.ListTokens(ctx, "u1"); list != nil {
			t.Errorf("after DeleteTokensOf = %+v", list)
		}
		if list, _ := db.ListTokens(ctx, "u2"); len(list) != 1 {
			t.Errorf("DeleteTokensOf reached another user: %+v", list)
		}
		if err := db.DeleteTokensOf(ctx, "ghost"); err != nil {
			t.Errorf("tokens of nobody = %v", err)
		}
	})

	t.Run("ssh keys: roundtrip, type, list, scope", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		a, b, late, early := sshKey("a", "u1", "SHA256:a"), sshKey("b", "u1", "SHA256:b"), sshKey("late", "u1", "SHA256:l"), sshKey("early", "u1", "SHA256:e")
		late.CreatedAt, early.CreatedAt = t0.Add(time.Minute), t0.Add(-time.Minute)
		for _, k := range []auth.SSHKey{late, b, a, early, sshKey("theirs", "u2", "SHA256:t")} {
			if err := db.CreateSSHKey(ctx, k); err != nil {
				t.Fatal(err)
			}
		}
		want := a
		want.CreatedAt = asRead(a.CreatedAt)
		if got, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:a"); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("roundtrip = %+v, %v, want %+v", got, err, want)
		}
		// Fingerprints are base64: case matters, and "a" is not "A".
		if _, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:A"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("fingerprint in another case = %v", err)
		}
		list, err := db.ListSSHKeys(ctx, "u1")
		if err != nil || !slices.Equal(keyIDs(list), []string{"early", "a", "b", "late"}) {
			t.Errorf("list = %v, %v", keyIDs(list), err)
		}
		if list, err := db.ListSSHKeys(ctx, "ghost"); list != nil || err != nil {
			t.Errorf("nobody's keys = %#v, %v", list, err)
		}
		used := t0.Add(time.Minute)
		if err := db.TouchSSHKey(ctx, "a", used); err != nil {
			t.Fatal(err)
		}
		if got, _ := db.GetSSHKeyByFingerprint(ctx, "SHA256:a"); got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
			t.Errorf("last_used = %v", got.LastUsedAt)
		}
		if got, _ := db.GetSSHKeyByFingerprint(ctx, "SHA256:b"); got.LastUsedAt != nil {
			t.Errorf("touch reached another key: %v", got.LastUsedAt)
		}
		if err := db.TouchSSHKey(ctx, "nope", used); err != nil {
			t.Errorf("touch of nothing = %v", err)
		}
		if err := db.DeleteSSHKey(ctx, "u2", "a"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("foreign delete = %v", err)
		}
		if _, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:a"); err != nil {
			t.Errorf("foreign delete took the key: %v", err)
		}
		if err := db.DeleteSSHKey(ctx, "u1", "a"); err != nil {
			t.Fatal(err)
		}
		if err := db.DeleteSSHKey(ctx, "u1", "a"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("second delete = %v", err)
		}
	})

	// The Type column does not exist: it is read off the public key, so what a
	// caller says it is makes no difference.
	t.Run("an ssh key's type comes from the key", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		lying := sshKey("k1", "u1", "SHA256:one")
		lying.Type = "ssh-rsa"
		blank := sshKey("k2", "u1", "SHA256:two")
		blank.PublicKey, blank.Type = "ecdsa-sha2-nistp256 AAAA", ""
		empty := sshKey("k3", "u1", "SHA256:three")
		empty.PublicKey, empty.Type = "", "ssh-rsa"
		for _, k := range []auth.SSHKey{lying, blank, empty} {
			if err := db.CreateSSHKey(ctx, k); err != nil {
				t.Fatal(err)
			}
		}
		for fingerprint, want := range map[string]string{"SHA256:one": "ssh-ed25519", "SHA256:two": "ecdsa-sha2-nistp256", "SHA256:three": ""} {
			if got, err := db.GetSSHKeyByFingerprint(ctx, fingerprint); err != nil || got.Type != want {
				t.Errorf("%s = %q, %v, want %q", fingerprint, got.Type, err, want)
			}
		}
		list, err := db.ListSSHKeys(ctx, "u1")
		if err != nil || len(list) != 3 || list[0].Type == "ssh-rsa" {
			t.Errorf("list = %+v, %v", list, err)
		}
	})

	t.Run("an ssh key is unique across the instance", func(t *testing.T) {
		db := open(t)
		seedUsers(t, db)
		if err := db.CreateSSHKey(ctx, sshKey("k1", "u1", "SHA256:one")); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateSSHKey(ctx, sshKey("k2", "u2", "SHA256:one")); !errors.Is(err, auth.ErrConflict) {
			t.Errorf("duplicate fingerprint = %v", err)
		}
		if err := db.CreateSSHKey(ctx, sshKey("k1", "u2", "SHA256:two")); !errors.Is(err, auth.ErrConflict) {
			t.Errorf("duplicate id = %v", err)
		}
		if got, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:one"); err != nil || got.ID != "k1" || got.UserID != "u1" {
			t.Errorf("a key changed hands: %+v, %v", got, err)
		}
		if _, err := db.GetSSHKeyByFingerprint(ctx, "SHA256:two"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("a refused key is readable: %v", err)
		}
		if err := db.CreateSSHKey(ctx, sshKey("k3", "u2", "SHA256:ONE")); err != nil {
			t.Errorf("fingerprint in another case = %v", err)
		}
	})
}
