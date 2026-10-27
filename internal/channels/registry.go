// Package channels holds the dynamic notification fan-out (§8 channel CRUD).
// The registry itself implements ports.NotificationChannel, so the engine
// sees one channel while the CPUI adds/removes concrete ones (Matrix in v1)
// at runtime. Fan-out is one-way and best-effort: errors are logged, never
// returned to the run (§4).
//
// Channels belong to a user: a run's notifications go to its owner's
// channels and no one else's. An entry with no owner receives everything —
// the shape of a bare registry in tests and dev mode.
package channels

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/oklog/ulid/v2"

	"apothecary/internal/ports"
)

// Factory builds a channel from its stored config (e.g. matrix: homeserver,
// access_token, room_id).
type Factory func(config map[string]any) (ports.NotificationChannel, error)

type Entry struct {
	ID     string         `json:"id"`
	UserID string         `json:"user_id,omitempty"`
	Kind   string         `json:"kind"`
	Config map[string]any `json:"config"`

	ch ports.NotificationChannel
}

// PersistentStore is implemented by the Postgres store (§9 channels table);
// nil keeps the registry memory-only (tests, dev mode).
type PersistentStore interface {
	SaveChannel(ctx context.Context, id, userID, kind string, config map[string]any) error
	DeleteChannel(ctx context.Context, id string) error
}

type Registry struct {
	mu      sync.Mutex
	ctors   map[string]Factory
	entries []*Entry
	store   PersistentStore
	log     *slog.Logger
}

func NewRegistry() *Registry {
	return &Registry{ctors: map[string]Factory{}, log: slog.Default()}
}

// Persist makes Add/Delete write through to the store.
func (r *Registry) Persist(store PersistentStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
}

// Restore re-registers a persisted channel under its stored id (boot path);
// unknown kinds are skipped with a warning — their adapter may not be wired
// yet (e.g. matrix before M5).
func (r *Registry) Restore(id, userID, kind string, config map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctor, ok := r.ctors[kind]
	if !ok {
		r.log.Warn("skipping persisted channel of unknown kind", "id", id, "kind", kind)
		return nil
	}
	ch, err := ctor(config)
	if err != nil {
		return fmt.Errorf("restore channel %s (%s): %w", id, kind, err)
	}
	r.entries = append(r.entries, &Entry{ID: id, UserID: userID, Kind: kind, Config: config, ch: ch})
	return nil
}

// Register makes a channel kind constructible (called at wiring time;
// "matrix" arrives with M5).
func (r *Registry) Register(kind string, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctors[kind] = f
}

// Add configures a channel for a user.
func (r *Registry) Add(userID, kind string, config map[string]any) (*Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctor, ok := r.ctors[kind]
	if !ok {
		known := make([]string, 0, len(r.ctors))
		for k := range r.ctors {
			known = append(known, k)
		}
		return nil, fmt.Errorf("unknown channel kind %q (supported: %v)", kind, known)
	}
	ch, err := ctor(config)
	if err != nil {
		return nil, fmt.Errorf("channel %s: %w", kind, err)
	}
	e := &Entry{ID: ulid.Make().String(), UserID: userID, Kind: kind, Config: config, ch: ch}
	if r.store != nil {
		if err := r.store.SaveChannel(context.Background(), e.ID, userID, kind, config); err != nil {
			return nil, fmt.Errorf("persist channel: %w", err)
		}
	}
	r.entries = append(r.entries, e)
	return e, nil
}

// Get is one entry by id, if it exists.
func (r *Registry) Get(id string) (*Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.ID == id {
			return e, true
		}
	}
	return nil, false
}

func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.entries {
		if e.ID == id {
			if r.store != nil {
				if err := r.store.DeleteChannel(context.Background(), id); err != nil {
					return fmt.Errorf("unpersist channel: %w", err)
				}
			}
			r.entries = append(r.entries[:i], r.entries[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("channel %s not found", id)
}

// List is one user's channels ("" = every entry).
func (r *Registry) List(userID string) []*Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if userID == "" || e.UserID == userID {
			out = append(out, e)
		}
	}
	return out
}

// Send fans out to the notification's owner's channels (every channel when
// it names no owner: an instance-wide notice). Always returns nil: channel
// failure must never fail a run.
func (r *Registry) Send(ctx context.Context, n ports.Notification) error {
	r.mu.Lock()
	entries := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if n.UserID == "" || e.UserID == "" || e.UserID == n.UserID {
			entries = append(entries, e)
		}
	}
	r.mu.Unlock()
	for _, e := range entries {
		if err := e.ch.Send(ctx, n); err != nil {
			r.log.Error("channel send failed", "kind", e.Kind, "id", e.ID, "err", err)
		}
	}
	return nil
}

var _ ports.NotificationChannel = (*Registry)(nil)
