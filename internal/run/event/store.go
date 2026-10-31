package event

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"
)

// Store is the append-only event log. Postgres in production, MemStore in
// tests. Appending is only ever done by a run's single writer goroutine
// (DESIGN.md §2), so Append needs no cross-writer coordination beyond seq
// assignment.
type Store interface {
	// Append stamps and stores the payloads as consecutive events and
	// returns them with RunID/Seq/TS filled in.
	Append(ctx context.Context, runID string, payloads ...Payload) ([]Event, error)
	// Events returns a run's events with Seq > afterSeq, in order.
	Events(ctx context.Context, runID string, afterSeq int64) ([]Event, error)
	// RunIDs lists every run that has at least one event (boot replay).
	RunIDs(ctx context.Context) ([]string, error)
	// Watch replays events after afterSeq and then streams live appends
	// until ctx is cancelled. The channel is closed on cancellation.
	Watch(ctx context.Context, runID string, afterSeq int64) (<-chan Event, error)
}

// MemStore is the in-memory Store used by tests (and the substrate the §16
// scenario tests run on).
type MemStore struct {
	mu     sync.Mutex
	events map[string][]Event
	subs   map[string][]*sub

	// Clock is injectable for deterministic tests; defaults to time.Now.
	Clock func() time.Time
}

type sub struct {
	ch     chan Event
	cancel <-chan struct{}
}

func NewMemStore() *MemStore {
	return &MemStore{
		events: map[string][]Event{},
		subs:   map[string][]*sub{},
		Clock:  time.Now,
	}
}

func (s *MemStore) Append(ctx context.Context, runID string, payloads ...Payload) ([]Event, error) {
	if runID == "" {
		return nil, fmt.Errorf("append: empty run id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.Clock().UTC()
	next := int64(len(s.events[runID])) + 1
	out := make([]Event, 0, len(payloads))
	for i, p := range payloads {
		e := Event{RunID: runID, Seq: next + int64(i), TS: ts, Type: p.EventType(), Payload: p}
		s.events[runID] = append(s.events[runID], e)
		out = append(out, e)
	}
	for _, sb := range s.subs[runID] {
		for _, e := range out {
			select {
			case sb.ch <- e:
			case <-sb.cancel:
			}
		}
	}
	return out, nil
}

func (s *MemStore) Events(_ context.Context, runID string, afterSeq int64) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.events[runID]
	i := sort.Search(len(all), func(i int) bool { return all[i].Seq > afterSeq })
	return slices.Clone(all[i:]), nil
}

func (s *MemStore) RunIDs(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.events))
	for id := range s.events {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *MemStore) Watch(ctx context.Context, runID string, afterSeq int64) (<-chan Event, error) {
	s.mu.Lock()
	replay, _ := s.eventsLocked(runID, afterSeq)
	sb := &sub{ch: make(chan Event, 64+len(replay)), cancel: ctx.Done()}
	for _, e := range replay {
		sb.ch <- e
	}
	s.subs[runID] = append(s.subs[runID], sb)
	s.mu.Unlock()

	out := make(chan Event)
	go func() {
		defer close(out)
		defer s.unsubscribe(runID, sb)
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-sb.ch:
				select {
				case out <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (s *MemStore) eventsLocked(runID string, afterSeq int64) ([]Event, error) {
	all := s.events[runID]
	i := sort.Search(len(all), func(i int) bool { return all[i].Seq > afterSeq })
	return slices.Clone(all[i:]), nil
}

func (s *MemStore) unsubscribe(runID string, sb *sub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs := s.subs[runID]
	for i, x := range subs {
		if x == sb {
			s.subs[runID] = slices.Delete(subs, i, i+1)
			return
		}
	}
}
