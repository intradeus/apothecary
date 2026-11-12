package notices_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/notices"
	"apothecary/internal/ports/notifyport"
)

// recorder is a channel that keeps what it was sent.
type recorder struct {
	mu   sync.Mutex
	sent []notifyport.Notification
}

func (r *recorder) Send(_ context.Context, n notifyport.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, n)
	return nil
}

func (r *recorder) snapshot() []notifyport.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notifyport.Notification(nil), r.sent...)
}

// waitSent waits for the mirror (it sends asynchronously) to have sent n
// notifications, then a moment more to catch any extra.
func (r *recorder) waitSent(t *testing.T, n int) []notifyport.Notification {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); len(r.snapshot()) < n && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	got := r.snapshot()
	if len(got) != n {
		t.Fatalf("sent %d notifications, want %d: %+v", len(got), n, got)
	}
	return got
}

func newService(admins ...string) (*notices.Service, *recorder) {
	rec := &recorder{}
	return &notices.Service{
		Store:  notices.NewMemStore(),
		Mirror: rec,
		Admins: func(context.Context) ([]string, error) { return admins, nil },
	}, rec
}

func list(t *testing.T, s *notices.Service, uid string) []notices.Notice {
	t.Helper()
	got, err := s.List(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAnOwnerWhoIsAnAdminGetsOneNotice(t *testing.T) {
	s, _ := newService("alice", "bob")
	s.ImageDrift(context.Background(), "alice", "int1", "cluster", "tools", errors.New("hash differs"))
	if got := list(t, s, "alice"); len(got) != 1 || got[0].Kind != notices.KindK8sImageDrift {
		t.Errorf("alice = %+v", got)
	}
	if got := list(t, s, "bob"); len(got) != 1 {
		t.Errorf("bob = %+v", got)
	}
}

func TestAnUnownedProblemGoesToTheAdmins(t *testing.T) {
	s, _ := newService("admin")
	s.ChannelSent(context.Background(), "ch1", "", "slack", errors.New("503"))
	got := list(t, s, "admin")
	if len(got) != 1 || got[0].Title != "Your slack channel is failing" || got[0].Link != "/system/channels" {
		t.Errorf("admin = %+v", got)
	}
}

func TestAdminsFailingStillReachesTheOwner(t *testing.T) {
	s, _ := newService()
	s.Admins = func(context.Context) ([]string, error) { return nil, errors.New("db down") }
	s.ImageDrift(context.Background(), "alice", "int1", "cluster", "shim", errors.New("x"))
	if got := list(t, s, "alice"); len(got) != 1 {
		t.Errorf("alice = %+v", got)
	}
}

func TestOnlyANewNoticeIsMirrored(t *testing.T) {
	s, rec := newService()
	ctx := context.Background()
	s.TrackerSync(ctx, "alice", "int1", "Jira", errors.New("401"))
	s.TrackerSync(ctx, "alice", "int1", "Jira", errors.New("401 again"))
	sent := rec.waitSent(t, 1)
	if sent[0].UserID != "alice" || sent[0].Type != notices.KindTrackerUnreachable || sent[0].Title != "Cannot sync tickets from Jira" {
		t.Errorf("sent = %+v", sent[0])
	}
	if got := list(t, s, "alice"); len(got) != 1 || got[0].Count != 2 || got[0].Body != "401 again" {
		t.Errorf("alice = %+v", got)
	}
	// Once cleared, the next failure is new, and mirrored again.
	s.TrackerSync(ctx, "alice", "int1", "Jira", nil)
	s.TrackerSync(ctx, "alice", "int1", "Jira", errors.New("401"))
	rec.waitSent(t, 2)
	if got := list(t, s, "alice"); len(got) != 2 {
		t.Errorf("alice = %+v", got)
	}
}

func TestAFailingChannelIsNeverMirrored(t *testing.T) {
	s, rec := newService()
	s.ChannelSent(context.Background(), "ch1", "alice", "discord", errors.New("404"))
	rec.waitSent(t, 0)
	if got := list(t, s, "alice"); len(got) != 1 {
		t.Errorf("alice = %+v", got)
	}
}

func TestTheMirrorLinksIntoThePublicUI(t *testing.T) {
	for _, tc := range []struct{ public, want string }{
		{"https://apo.example/", "https://apo.example/system/providers"},
		{"", ""},
	} {
		s, rec := newService()
		s.PublicURL = tc.public
		s.LoginExpired(context.Background(), "alice", "c1", "Codex", errors.New("503"))
		if sent := rec.waitSent(t, 1); sent[0].URL != tc.want {
			t.Errorf("public %q: url = %q, want %q", tc.public, sent[0].URL, tc.want)
		}
	}
	// A notice without a link has no URL either.
	s, rec := newService("admin")
	s.PublicURL = "https://apo.example"
	s.SSHDoor(context.Background(), errors.New("listener closed"))
	if sent := rec.waitSent(t, 1); sent[0].URL != "" {
		t.Errorf("url = %q", sent[0].URL)
	}
}

// Every keyed problem repeats into one notice, and once its producer says it
// is over, the next occurrence is a new notice.
func TestEachProblemRepeatsUntilItClears(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		kind         string
		raise, clear func(ctx context.Context, s *notices.Service)
	}{
		{notices.KindLoginSignedOut,
			func(ctx context.Context, s *notices.Service) {
				s.LoginSignedOut(ctx, "alice", "c1", "Codex", "openai", "invalid_grant")
			},
			func(ctx context.Context, s *notices.Service) { s.LoginRefreshed(ctx, "c1") }},
		{notices.KindLoginExpired,
			func(ctx context.Context, s *notices.Service) { s.LoginExpired(ctx, "alice", "c1", "Codex", boom) },
			func(ctx context.Context, s *notices.Service) { s.LoginRefreshed(ctx, "c1") }},
		{notices.KindTrackerUnreachable,
			func(ctx context.Context, s *notices.Service) { s.TrackerSync(ctx, "alice", "i1", "Jira", boom) },
			func(ctx context.Context, s *notices.Service) { s.TrackerSync(ctx, "alice", "i1", "Jira", nil) }},
		{notices.KindChannelFailing,
			func(ctx context.Context, s *notices.Service) { s.ChannelSent(ctx, "ch1", "alice", "slack", boom) },
			func(ctx context.Context, s *notices.Service) { s.ChannelSent(ctx, "ch1", "alice", "slack", nil) }},
		{notices.KindK8sImageDrift,
			func(ctx context.Context, s *notices.Service) { s.ImageDrift(ctx, "alice", "i1", "k8s", "shim", boom) },
			func(ctx context.Context, s *notices.Service) { s.ImageDrift(ctx, "alice", "i1", "k8s", "shim", nil) }},
		{notices.KindSSHDoorStopped,
			func(ctx context.Context, s *notices.Service) { s.SSHDoor(ctx, boom) },
			func(ctx context.Context, s *notices.Service) { s.SSHDoor(ctx, nil) }},
		{notices.KindReaperEnvsFailed,
			func(ctx context.Context, s *notices.Service) {
				s.ReaperEnvs(ctx, []string{"run r1 (workspace e1): boom"})
			},
			func(ctx context.Context, s *notices.Service) { s.ReaperEnvs(ctx, nil) }},
		{notices.KindReaperStorageFailed,
			func(ctx context.Context, s *notices.Service) { s.ReaperStorage(ctx, []string{"blob sweep: boom"}) },
			func(ctx context.Context, s *notices.Service) { s.ReaperStorage(ctx, nil) }},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			s, _ := newService("alice")
			ctx := context.Background()
			tc.raise(ctx, s)
			tc.raise(ctx, s)
			got := list(t, s, "alice")
			if len(got) != 1 || got[0].Kind != tc.kind || got[0].Count != 2 || got[0].Title == "" {
				t.Fatalf("after two raises = %+v", got)
			}
			tc.clear(ctx, s)
			if got := list(t, s, "alice"); len(got) != 1 {
				t.Errorf("clearing hid the notice: %+v", got)
			}
			tc.raise(ctx, s)
			if got := list(t, s, "alice"); len(got) != 2 || got[0].Count != 1 {
				t.Errorf("after clear and raise = %+v", got)
			}
		})
	}
}

func TestTrackerListGrowthIsANoticePerBatch(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	s.TrackerListsGrew(ctx, "alice", "i1", "Jira", nil)
	s.TrackerListsGrew(ctx, "alice", "i1", "Jira", []string{`status "To Do": flow step todo`, `status "Done": flow step done, Done`})
	s.TrackerListsGrew(ctx, "alice", "i1", "Jira", []string{`type "Bug": change type fix (guessed)`})
	got := list(t, s, "alice")
	if len(got) != 2 || got[1].Body != "status \"To Do\": flow step todo\nstatus \"Done\": flow step done, Done" ||
		got[0].Title != "Jira has new tracker statuses or types to map" {
		t.Errorf("alice = %+v", got)
	}
}

// failingChannel refuses every send.
type failingChannel struct{}

func (failingChannel) Send(context.Context, notifyport.Notification) error {
	return errors.New("webhook 500")
}

func TestFailuresAreLoggedNotReturned(t *testing.T) {
	var buf syncBuffer
	s := &notices.Service{
		Store:  notices.NewMemStore(),
		Mirror: failingChannel{},
		Log:    slog.New(slog.NewTextHandler(&buf, nil)),
		Clock:  func() time.Time { return time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) },
	}
	s.TrackerSync(context.Background(), "alice", "i1", "Jira", errors.New("401"))
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	s.TrackerSync(dead, "alice", "i2", "GitHub", errors.New("401"))
	s.TrackerSync(dead, "alice", "i2", "GitHub", nil)
	for deadline := time.Now().Add(2 * time.Second); !strings.Contains(buf.String(), "notice mirror failed") && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	for _, want := range []string{"notice raise failed", "notice clear failed", "notice mirror failed"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, buf.String())
		}
	}
	got := list(t, s, "alice")
	if len(got) != 1 || !got[0].LastSeenAt.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("alice = %+v", got)
	}
}

// syncBuffer is a log sink the mirror's goroutine may write to.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
