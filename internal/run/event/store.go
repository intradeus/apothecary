package event

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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
	// until ctx is cancelled. The channel is closed on cancellation, and when
	// the consumer falls too far behind: it re-syncs from the last seq it read.
	Watch(ctx context.Context, runID string, afterSeq int64) (<-chan Event, error)
}

// MemStore is the in-memory Store the unit tier runs on. It keeps what the
// events table keeps, JSON and a microsecond clock, and refuses what Postgres
// refuses, so a test on it proves what production does (eventtest holds the
// two together).
type MemStore struct {
	mu   sync.Mutex
	rows map[string][]row
	subs map[string][]*sub

	// Clock is injectable for deterministic tests; defaults to time.Now.
	Clock func() time.Time
}

// row is one stored event. Nothing a reader gets aliases it: every read
// decodes its own payload.
type row struct {
	seq     int64
	ts      time.Time
	typ     Type
	payload []byte
}

type sub struct {
	ch     chan Event
	cancel <-chan struct{}
	// overflow is closed by Append when the buffer is full: appends hold s.mu
	// and must never block on a slow consumer. The Watch goroutine exits; the
	// SSE client re-syncs via after_seq on reconnect.
	overflow chan struct{}
	dropOnce sync.Once
}

func NewMemStore() *MemStore {
	return &MemStore{
		rows:  map[string][]row{},
		subs:  map[string][]*sub{},
		Clock: time.Now,
	}
}

func (s *MemStore) Append(ctx context.Context, runID string, payloads ...Payload) ([]Event, error) {
	if runID == "" {
		return nil, fmt.Errorf("append: empty run id")
	}
	if err := usable(ctx, runID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.Clock().UTC()
	next := int64(len(s.rows[runID])) + 1
	out := make([]Event, 0, len(payloads))
	rows := make([]row, 0, len(payloads))
	for i, p := range payloads {
		data, err := encode(p)
		if err != nil {
			return nil, err
		}
		e := Event{RunID: runID, Seq: next + int64(i), TS: ts, Type: p.EventType(), Payload: p}
		out = append(out, e)
		rows = append(rows, row{seq: e.Seq, ts: ts.Truncate(time.Microsecond), typ: e.Type, payload: data})
	}
	if len(rows) == 0 {
		return out, nil
	}
	s.rows[runID] = append(s.rows[runID], rows...)
	for _, sb := range s.subs[runID] {
		for _, e := range out {
			select {
			case sb.ch <- e:
			case <-sb.cancel:
			default:
				sb.dropOnce.Do(func() { close(sb.overflow) })
			}
		}
	}
	return out, nil
}

func (s *MemStore) Events(ctx context.Context, runID string, afterSeq int64) ([]Event, error) {
	if err := usable(ctx, runID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	all := s.rows[runID]
	i := sort.Search(len(all), func(i int) bool { return all[i].seq > afterSeq })
	rows := all[i:]
	s.mu.Unlock()
	var out []Event
	for _, r := range rows {
		p, err := UnmarshalPayload(r.typ, r.payload)
		if err != nil {
			return nil, fmt.Errorf("run %s seq %d: %w", runID, r.seq, err)
		}
		out = append(out, Event{RunID: runID, Seq: r.seq, TS: r.ts.Local(), Type: r.typ, Payload: p})
	}
	return out, nil
}

func (s *MemStore) RunIDs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id := range s.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *MemStore) Watch(ctx context.Context, runID string, afterSeq int64) (<-chan Event, error) {
	// Subscribe first, then replay, deduplicating by seq: what Postgres has
	// to do, so what a test on this store sees.
	sb := &sub{ch: make(chan Event, 256), cancel: ctx.Done(), overflow: make(chan struct{})}
	s.mu.Lock()
	s.subs[runID] = append(s.subs[runID], sb)
	s.mu.Unlock()

	replay, err := s.Events(ctx, runID, afterSeq)
	if err != nil {
		s.unsubscribe(runID, sb)
		return nil, err
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		defer s.unsubscribe(runID, sb)
		last := afterSeq
		for _, e := range replay {
			select {
			case out <- e:
				last = e.Seq
			case <-ctx.Done():
				return
			}
		}
		for {
			select {
			case e := <-sb.ch:
				if e.Seq <= last {
					continue
				}
				select {
				case out <- e:
					last = e.Seq
				case <-ctx.Done():
					return
				}
			case <-sb.overflow:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
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

// usable refuses what never reaches a row: a dead context, and a run id that
// is not text to Postgres.
func usable(ctx context.Context, runID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.ContainsRune(runID, 0) || !utf8.ValidString(runID) {
		return fmt.Errorf("run id %q: invalid text", runID)
	}
	return nil
}

// encode is the payload as the events row holds it, or why Postgres would
// refuse the row.
func encode(p Payload) ([]byte, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", p.EventType(), err)
	}
	if hasNUL(data) {
		return nil, fmt.Errorf("%s: jsonb has no NUL character", p.EventType())
	}
	if sc, ok := p.(*SnapshotCaptured); ok {
		if err := sc.check(); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// hasNUL finds a \u0000 escape, not a backslash that happens to precede the
// same letters.
func hasNUL(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if bytes.HasPrefix(data[i+1:], []byte("u0000")) {
			return true
		}
		i++
	}
	return false
}

// check is the snapshots table's CHECK constraints, which the projection
// meets inside the append.
func (p *SnapshotCaptured) check() error {
	switch p.Kind {
	case "output", "log", "ping_file":
	default:
		return fmt.Errorf("snapshot %s: unknown kind %q", p.SnapshotID, p.Kind)
	}
	if (p.Kind == "ping_file") != (p.PingID != "") ||
		(p.Kind == "output") != (p.Name != "") ||
		(p.Kind != "log") != (p.Path != "") {
		return fmt.Errorf("snapshot %s: %s with ping %q, name %q, path %q", p.SnapshotID, p.Kind, p.PingID, p.Name, p.Path)
	}
	return nil
}
