package channels

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"apothecary/internal/ports/notifyport"
)

type recorder struct {
	sent []notifyport.Notification
	err  error
}

func (r *recorder) Send(_ context.Context, n notifyport.Notification) error {
	r.sent = append(r.sent, n)
	return r.err
}

// factoryOf hands out one recorder per config "name".
func factoryOf(chans map[string]*recorder) Factory {
	return func(config map[string]any) (notifyport.NotificationChannel, error) {
		name, _ := config["name"].(string)
		r, ok := chans[name]
		if !ok {
			return nil, errors.New("bad config")
		}
		return r, nil
	}
}

type memStore struct {
	saved   map[string]string
	deleted []string
	saveErr error
	delErr  error
}

func (s *memStore) SaveChannel(_ context.Context, id, userID, kind string, _ map[string]any) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.saved == nil {
		s.saved = map[string]string{}
	}
	s.saved[id] = userID + "/" + kind
	return nil
}

func (s *memStore) DeleteChannel(_ context.Context, id string) error {
	if s.delErr != nil {
		return s.delErr
	}
	s.deleted = append(s.deleted, id)
	return nil
}

func TestAddValidatesKindAndConfig(t *testing.T) {
	r := NewRegistry()
	r.Register("slack", factoryOf(map[string]*recorder{"ok": {}}))
	if _, err := r.Add("u", "email", nil); err == nil || !strings.Contains(err.Error(), `unknown channel kind "email"`) || !strings.Contains(err.Error(), "slack") {
		t.Errorf("unknown kind = %v", err)
	}
	if _, err := r.Add("u", "slack", map[string]any{"name": "nope"}); err == nil || !strings.Contains(err.Error(), "channel slack: bad config") {
		t.Errorf("bad config = %v", err)
	}
	if got := r.List(""); len(got) != 0 {
		t.Errorf("a refused channel was kept: %+v", got)
	}
}

func TestAddAndDeleteWriteThrough(t *testing.T) {
	r := NewRegistry()
	r.Register("slack", factoryOf(map[string]*recorder{"ok": {}}))
	st := &memStore{}
	r.Persist(st)

	st.saveErr = errors.New("db down")
	if _, err := r.Add("u", "slack", map[string]any{"name": "ok"}); err == nil || !strings.Contains(err.Error(), "persist channel") {
		t.Errorf("save failure = %v", err)
	}
	if len(r.List("")) != 0 {
		t.Error("what could not be saved must not be sent to")
	}
	st.saveErr = nil
	e, err := r.Add("u", "slack", map[string]any{"name": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if st.saved[e.ID] != "u/slack" {
		t.Errorf("saved = %v", st.saved)
	}
	if got, ok := r.Get(e.ID); !ok || got != e {
		t.Errorf("get = %+v, %v", got, ok)
	}

	st.delErr = errors.New("db down")
	if err := r.Delete(e.ID); err == nil || !strings.Contains(err.Error(), "unpersist") {
		t.Errorf("delete failure = %v", err)
	}
	if _, ok := r.Get(e.ID); !ok {
		t.Error("a delete that failed to persist must keep the channel: it would come back at boot")
	}
	st.delErr = nil
	if err := r.Delete(e.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(e.ID); ok || len(st.deleted) != 1 {
		t.Errorf("after delete: %v", st.deleted)
	}
	if err := r.Delete(e.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("second delete = %v", err)
	}
}

func TestRestoreSkipsUnknownKindsAndKeepsTheStoredID(t *testing.T) {
	r := NewRegistry()
	r.Register("slack", factoryOf(map[string]*recorder{"ok": {}}))
	st := &memStore{}
	r.Persist(st)
	if err := r.Restore("id1", "u", "matrix", nil); err != nil {
		t.Errorf("an unwired kind is skipped, not a boot failure: %v", err)
	}
	if err := r.Restore("id2", "u", "slack", map[string]any{"name": "broken"}); err == nil || !strings.Contains(err.Error(), "restore channel id2 (slack)") {
		t.Errorf("bad config = %v", err)
	}
	if err := r.Restore("id3", "u", "slack", map[string]any{"name": "ok"}); err != nil {
		t.Fatal(err)
	}
	if e, ok := r.Get("id3"); !ok || e.UserID != "u" || e.Kind != "slack" {
		t.Errorf("restored = %+v, %v", e, ok)
	}
	if len(r.List("")) != 1 {
		t.Errorf("entries = %+v", r.List(""))
	}
	if len(st.saved) != 0 {
		t.Errorf("restoring must not write back: %v", st.saved)
	}
}

// A run's notification goes to its owner's channels and to no one else's; an
// entry with no owner hears everything, a notice with no owner reaches all.
func TestSendFansOutByOwner(t *testing.T) {
	chans := map[string]*recorder{"alice": {}, "bob": {}, "all": {}}
	r := NewRegistry()
	r.Register("k", factoryOf(chans))
	for name, user := range map[string]string{"alice": "a", "bob": "b", "all": ""} {
		if _, err := r.Add(user, "k", map[string]any{"name": name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Send(context.Background(), notifyport.Notification{UserID: "a", Title: "for a"}); err != nil {
		t.Fatal(err)
	}
	if len(chans["alice"].sent) != 1 || len(chans["bob"].sent) != 0 || len(chans["all"].sent) != 1 {
		t.Errorf("alice %d bob %d all %d", len(chans["alice"].sent), len(chans["bob"].sent), len(chans["all"].sent))
	}
	if err := r.Send(context.Background(), notifyport.Notification{Title: "notice"}); err != nil {
		t.Fatal(err)
	}
	if len(chans["alice"].sent) != 2 || len(chans["bob"].sent) != 1 || len(chans["all"].sent) != 2 {
		t.Errorf("alice %d bob %d all %d", len(chans["alice"].sent), len(chans["bob"].sent), len(chans["all"].sent))
	}
	if got := r.List("b"); len(got) != 1 || got[0].UserID != "b" {
		t.Errorf("list b = %+v", got)
	}
	if got := r.List(""); len(got) != 3 {
		t.Errorf("list all = %+v", got)
	}
}

// One channel failing is logged, reaches the others, and never fails the run.
func TestSendSurvivesAFailingChannel(t *testing.T) {
	chans := map[string]*recorder{"down": {err: errors.New("503 from the room")}, "up": {}}
	var logged bytes.Buffer
	r := NewRegistry()
	r.log = slog.New(slog.NewTextHandler(&logged, nil))
	r.Register("k", factoryOf(chans))
	for _, name := range []string{"down", "up"} {
		if _, err := r.Add("", "k", map[string]any{"name": name}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Send(context.Background(), notifyport.Notification{Title: "t"}); err != nil {
		t.Errorf("Send = %v", err)
	}
	if len(chans["up"].sent) != 1 {
		t.Error("the working channel was skipped")
	}
	if !strings.Contains(logged.String(), "503 from the room") {
		t.Errorf("log = %q", logged.String())
	}
}
