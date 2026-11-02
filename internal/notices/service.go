package notices

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"apothecary/internal/ports/notifyport"
)

// mirrorTimeout bounds one channel copy of a new notice.
const mirrorTimeout = 30 * time.Second

// Raise is one problem to raise.
type Raise struct {
	To     []string // owner ids; "" entries mean "admins"
	Admins bool     // also every admin
	Kind   string
	Key    string // "" never deduplicates
	Title  string
	Body   string
	Link   string // CPUI path
}

// Service raises, clears, lists and dismisses notices. Raising never fails
// its caller: producers are background loops with nobody to tell, so store
// and channel errors are logged.
type Service struct {
	Store Store
	// Mirror gets a best-effort copy of each new notice (channel_failing
	// excepted); nil = in-app only.
	Mirror notifyport.NotificationChannel
	// Admins lists the ids of the users who hear install-level notices; nil = none.
	Admins    func(ctx context.Context) ([]string, error)
	PublicURL string           // APOTHECARY_PUBLIC_URL; "" = mirror without URL
	Log       *slog.Logger     // nil = slog.Default()
	Clock     func() time.Time // nil = time.Now
	NewID     func() string    // nil = ulid.Make().String()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	return ulid.Make().String()
}

// Raise stores r for each recipient and mirrors the new ones to their channels.
func (s *Service) Raise(ctx context.Context, r Raise) {
	r.Title, r.Body, r.Link = text(r.Title), text(r.Body), text(r.Link)
	for _, uid := range s.recipients(ctx, r) {
		n, inserted, err := s.Store.RaiseNotice(ctx, Notice{
			ID: s.newID(), UserID: uid, Kind: r.Kind, Key: r.Key,
			Title: r.Title, Body: r.Body, Link: r.Link, LastSeenAt: s.now(),
		})
		if err != nil {
			s.log().Warn("notice raise failed", "kind", r.Kind, "user", uid, "err", err)
			continue
		}
		if inserted && r.Kind != KindChannelFailing && s.Mirror != nil {
			s.mirror(ctx, n)
		}
	}
}

// mirror sends n to its owner's channels without holding up the producer.
func (s *Service) mirror(ctx context.Context, n Notice) {
	url := ""
	if s.PublicURL != "" && n.Link != "" {
		url = strings.TrimRight(s.PublicURL, "/") + n.Link
	}
	msg := notifyport.Notification{UserID: n.UserID, Type: n.Kind, Title: n.Title, Body: n.Body, URL: url}
	go func() {
		mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mirrorTimeout)
		defer cancel()
		if err := s.Mirror.Send(mctx, msg); err != nil {
			s.log().Warn("notice mirror failed", "kind", n.Kind, "user", n.UserID, "err", err)
		}
	}()
}

// recipients is r's users, each once, in order: the named owners, then the
// admins when asked for or when an owner is unknown.
func (s *Service) recipients(ctx context.Context, r Raise) []string {
	var out []string
	add := func(id string) {
		for _, have := range out {
			if have == id {
				return
			}
		}
		out = append(out, id)
	}
	admins := r.Admins
	for _, id := range r.To {
		if id == "" {
			admins = true
			continue
		}
		add(id)
	}
	if admins && s.Admins != nil {
		ids, err := s.Admins(ctx)
		if err != nil {
			s.log().Warn("notices: listing admins failed", "kind", r.Kind, "err", err)
		}
		for _, id := range ids {
			if id != "" {
				add(id)
			}
		}
	}
	return out
}

// Clear re-arms key: the next raise with it is a new notice. Notices stay
// listed until dismissed.
func (s *Service) Clear(ctx context.Context, key string) {
	if err := s.Store.ClearNotices(ctx, key, s.now()); err != nil {
		s.log().Warn("notice clear failed", "key", key, "err", err)
	}
}

func (s *Service) List(ctx context.Context, userID string) ([]Notice, error) {
	return s.Store.ListNotices(ctx, userID)
}

func (s *Service) Get(ctx context.Context, id string) (Notice, error) {
	return s.Store.GetNotice(ctx, id)
}

func (s *Service) Dismiss(ctx context.Context, id string) error {
	return s.Store.DismissNotice(ctx, id, s.now())
}

func (s *Service) DismissAll(ctx context.Context, userID string) error {
	return s.Store.DismissAllNotices(ctx, userID, s.now())
}

// text makes s storable: error text from a tracker or a webhook may hold
// what Postgres refuses as text.
func text(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "\uFFFD")
}
