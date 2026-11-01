// Package eventtest is the event.Store contract, run against every
// implementation (MemStore in event's own tests, Postgres in pgstore's). It
// states what Postgres does: production runs nothing else, so a twin that
// differs makes the unit tier prove the wrong thing.
package eventtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"apothecary/internal/flow/reviewfile"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
)

// wait bounds every read of a stream: a store that loses an event fails here
// rather than hanging the binary.
const wait = 10 * time.Second

// overflow is more than any subscriber buffers.
const overflow = 300

// RunContract exercises one Store implementation. open must return a fresh,
// empty store.
func RunContract(t *testing.T, open func(t *testing.T) event.Store) {
	t.Helper()
	runAppend(t, open)
	runRead(t, open)
	runWatch(t, open)
	runConcurrent(t, open)
	runCapabilities(t, open)
}

func runAppend(t *testing.T, open func(t *testing.T) event.Store) {
	t.Run("append numbers each run from 1 without gaps", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		first, err := s.Append(ctx, "r1", &event.RunCreated{TicketTitle: "a"}, &event.EnvRequested{})
		if err != nil {
			t.Fatal(err)
		}
		second, err := s.Append(ctx, "r1", &event.EnvReady{EnvID: "e1"})
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.Append(ctx, "r2", &event.RunCreated{TicketTitle: "b"})
		if err != nil {
			t.Fatal(err)
		}
		if got := seqs(first, second); !slices.Equal(got, []int64{1, 2, 3}) {
			t.Errorf("r1 seqs = %v", got)
		}
		if other[0].Seq != 1 {
			t.Errorf("r2 starts at seq %d", other[0].Seq)
		}
		e := first[1]
		if e.RunID != "r1" || e.Type != event.TypeEnvRequested || e.TS.IsZero() || e.TS.Location() != time.UTC {
			t.Errorf("stamped = %+v", e)
		}
		if _, ok := e.Payload.(*event.EnvRequested); !ok {
			t.Errorf("payload = %#v", e.Payload)
		}
		if !first[0].TS.Equal(first[1].TS) {
			t.Errorf("one batch, two stamps: %v, %v", first[0].TS, first[1].TS)
		}
		if second[0].TS.Before(first[0].TS) {
			t.Errorf("clock went back: %v, then %v", first[0].TS, second[0].TS)
		}
		got, err := s.Events(ctx, "r1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if ss := seqs(got); !slices.Equal(ss, []int64{1, 2, 3}) {
			t.Errorf("stored seqs = %v", ss)
		}
	})

	t.Run("append of nothing is a no-op", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		evs, err := s.Append(ctx, "r1")
		if err != nil || evs == nil || len(evs) != 0 {
			t.Fatalf("empty append = %#v, %v", evs, err)
		}
		if ids, err := s.RunIDs(ctx); err != nil || len(ids) != 0 {
			t.Errorf("run ids = %v, %v", ids, err)
		}
		mustAppend(t, s, "r1", &event.EnvRequested{})
		if evs, err := s.Append(ctx, "r1"); err != nil || len(evs) != 0 {
			t.Fatalf("empty append on a run = %#v, %v", evs, err)
		}
		if evs := mustAppend(t, s, "r1", &event.EnvRequested{}); evs[0].Seq != 2 {
			t.Errorf("next seq = %d, want 2", evs[0].Seq)
		}
	})

	t.Run("append refuses an empty run id", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		if evs, err := s.Append(ctx, "", &event.EnvRequested{}); err == nil {
			t.Fatalf("appended %+v", evs)
		}
		if ids, _ := s.RunIDs(ctx); len(ids) != 0 {
			t.Errorf("run ids = %v", ids)
		}
	})

	t.Run("a refused batch stores nothing and takes no seq", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		mustAppend(t, s, "r1", &event.RunCreated{TicketTitle: "t"})
		ch, err := s.Watch(ctx, "r1", 1)
		if err != nil {
			t.Fatal(err)
		}
		for name, bad := range map[string]event.Payload{
			"unmarshalable":          unmarshalable{},
			"failing marshaler":      failing{},
			"invalid JSON":           &event.Unknown{UnknownType: "retired", Raw: json.RawMessage(`{`)},
			"NUL in a string":        &event.RunFailed{Reason: "a\x00b"},
			"NUL in a nested string": &event.EnvReady{EnvID: "e", Links: []event.Link{{Kind: "app", URL: "a\x00"}}},
			"NUL in a map key":       &event.RunCreated{Params: map[string]string{"a\x00": "v"}},
			"NUL in raw JSON":        &event.Unknown{UnknownType: "retired", Raw: json.RawMessage(`{"a":"\u0000"}`)},
			"unknown snapshot kind":  &event.SnapshotCaptured{SnapshotID: "s", StageID: "b", Kind: "not-a-kind", Filename: "f", Mime: "m"},
			"log with a path":        &event.SnapshotCaptured{SnapshotID: "s", StageID: "b", Kind: "log", Path: "p", Filename: "f", Mime: "m"},
			"log with a name":        &event.SnapshotCaptured{SnapshotID: "s", StageID: "b", Kind: "log", Name: "n", Filename: "f", Mime: "m"},
			"log with a ping":        &event.SnapshotCaptured{SnapshotID: "s", StageID: "b", Kind: "log", PingID: "p", Filename: "f", Mime: "m"},
			"output without a name":  &event.SnapshotCaptured{SnapshotID: "s", StageID: "b", Kind: "output", Path: "p", Filename: "f", Mime: "m"},
			"output without a path":  &event.SnapshotCaptured{SnapshotID: "s", StageID: "b", Kind: "output", Name: "n", Filename: "f", Mime: "m"},
			"ping file without ping": &event.SnapshotCaptured{SnapshotID: "s", StageID: "b", Kind: "ping_file", Path: "p", Filename: "f", Mime: "m"},
		} {
			if _, err := s.Append(ctx, "r1", &event.EnvRequested{}, bad); err == nil {
				t.Errorf("%s: appended", name)
			}
		}
		if got, err := s.Events(ctx, "r1", 0); err != nil || len(got) != 1 {
			t.Fatalf("refused batches left %+v, %v", got, err)
		}
		// The next append takes seq 2 and is the first thing a watcher hears.
		mustAppend(t, s, "r1", &event.EnvRequested{})
		if e := next(t, ch); e.Seq != 2 {
			t.Errorf("first heard = seq %d", e.Seq)
		}
	})

	t.Run("every snapshot kind the projection takes is stored", func(t *testing.T) {
		s := open(t)
		ps := []event.Payload{
			&event.SnapshotCaptured{SnapshotID: "s1", StageID: "b", Kind: "log", Filename: "f", Mime: "m"},
			&event.SnapshotCaptured{SnapshotID: "s2", StageID: "b", Kind: "output", Name: "n", Path: "p", Filename: "f", Mime: "m"},
			&event.SnapshotCaptured{SnapshotID: "s3", StageID: "b", Kind: "ping_file", PingID: "ping", Path: "p", Filename: "f", Mime: "m"},
		}
		if evs := mustAppend(t, s, "r1", ps...); len(evs) != len(ps) {
			t.Errorf("appended %d", len(evs))
		}
	})

	t.Run("a run id must be text", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		for _, id := range []string{"a\x00b", "a\xffb"} {
			if _, err := s.Append(ctx, id, &event.EnvRequested{}); err == nil {
				t.Errorf("Append(%q) succeeded", id)
			}
			if _, err := s.Events(ctx, id, 0); err == nil {
				t.Errorf("Events(%q) succeeded", id)
			}
			if _, err := s.Watch(ctx, id, 0); err == nil {
				t.Errorf("Watch(%q) succeeded", id)
			}
		}
		if ids, _ := s.RunIDs(ctx); len(ids) != 0 {
			t.Errorf("run ids = %q", ids)
		}
	})

	t.Run("a dead context does nothing", func(t *testing.T) {
		s := open(t)
		live := context.Background()
		mustAppend(t, s, "r1", &event.EnvRequested{})
		ctx, cancel := context.WithCancel(live)
		cancel()
		if _, err := s.Append(ctx, "r1", &event.EnvRequested{}); err == nil {
			t.Error("Append succeeded")
		}
		if _, err := s.Append(ctx, "r2", &event.EnvRequested{}); err == nil {
			t.Error("Append on a new run succeeded")
		}
		if _, err := s.Events(ctx, "r1", 0); err == nil {
			t.Error("Events succeeded")
		}
		if _, err := s.RunIDs(ctx); err == nil {
			t.Error("RunIDs succeeded")
		}
		if ch, err := s.Watch(ctx, "r1", 0); err == nil {
			t.Errorf("Watch succeeded: %v", ch)
		}
		if got, _ := s.Events(live, "r1", 0); len(got) != 1 {
			t.Errorf("events = %+v", got)
		}
		if ids, _ := s.RunIDs(live); !slices.Equal(ids, []string{"r1"}) {
			t.Errorf("run ids = %v", ids)
		}
	})
}

func runRead(t *testing.T, open func(t *testing.T) event.Store) {
	t.Run("payloads come back through the codec", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		for i, c := range payloadCases() {
			appended := mustAppend(t, s, "r1", c.in)
			got, err := s.Events(ctx, "r1", int64(i))
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if len(got) != 1 {
				t.Fatalf("%s: read %d events", c.name, len(got))
			}
			e := got[0]
			if e.RunID != "r1" || e.Seq != int64(i+1) || e.Type != c.in.EventType() {
				t.Errorf("%s: envelope = %+v", c.name, e)
			}
			if !e.TS.Equal(appended[0].TS.Truncate(time.Microsecond)) {
				t.Errorf("%s: ts = %v, appended %v", c.name, e.TS, appended[0].TS)
			}
			if !reflect.DeepEqual(e.Payload, c.want) {
				t.Errorf("%s: payload = %#v, want %#v", c.name, e.Payload, c.want)
			}
		}
	})

	t.Run("a retired type stays inert history", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		mustAppend(t, s, "r1", &event.Unknown{UnknownType: "artifact_published", Raw: json.RawMessage(`{"b":[1,{"c":"d"}],"a":"x"}`)})
		got, err := s.Events(ctx, "r1", 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("events = %+v, %v", got, err)
		}
		u, ok := got[0].Payload.(*event.Unknown)
		if !ok || got[0].Type != "artifact_published" || u.UnknownType != "artifact_published" {
			t.Fatalf("event = %+v", got[0])
		}
		// Same JSON, not the same bytes: jsonb keeps no formatting.
		var have, want any
		if err := json.Unmarshal(u.Raw, &have); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(`{"a":"x","b":[1,{"c":"d"}]}`), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(have, want) {
			t.Errorf("raw = %s", u.Raw)
		}
	})

	t.Run("a payload the codec cannot read fails the read", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		mustAppend(t, s, "r1", &event.EnvRequested{})
		mustAppend(t, s, "r1", undecodable{})
		_, err := s.Events(ctx, "r1", 0)
		if err == nil || !strings.Contains(err.Error(), "run r1 seq 2") {
			t.Fatalf("Events = %v", err)
		}
		if ch, err := s.Watch(ctx, "r1", 0); err == nil {
			t.Errorf("Watch replayed: %v", ch)
		}
		if got, err := s.Events(ctx, "r1", 2); err != nil || got != nil {
			t.Errorf("after the bad row = %+v, %v", got, err)
		}
	})

	t.Run("the log owns its bytes", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		in := &event.RunCreated{TicketTitle: "before", Params: map[string]string{"k": "before"}}
		mustAppend(t, s, "r1", in)
		in.TicketTitle = "written after"
		in.Params["k"] = "written after"
		read := func() *event.RunCreated {
			got, err := s.Events(ctx, "r1", 0)
			if err != nil || len(got) != 1 {
				t.Fatalf("events = %+v, %v", got, err)
			}
			return got[0].Payload.(*event.RunCreated)
		}
		p := read()
		if p.TicketTitle != "before" || p.Params["k"] != "before" {
			t.Fatalf("a write to the appended payload reached the log: %+v", p)
		}
		p.TicketTitle = "read and written"
		p.Params["k"] = "read and written"
		if p := read(); p.TicketTitle != "before" || p.Params["k"] != "before" {
			t.Errorf("a write to a read payload reached the log: %+v", p)
		}
	})

	t.Run("stored times are timestamptz: microseconds, local zone", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		var appended []event.Event
		for range 5 {
			appended = append(appended, mustAppend(t, s, "r1", &event.EnvRequested{})...)
		}
		got, err := s.Events(ctx, "r1", 0)
		if err != nil || len(got) != len(appended) {
			t.Fatalf("events = %+v, %v", got, err)
		}
		for i, e := range got {
			if e.TS.Nanosecond()%1000 != 0 || e.TS.Location() != time.Local {
				t.Errorf("seq %d: ts = %v", e.Seq, e.TS)
			}
			if !e.TS.Equal(appended[i].TS.Truncate(time.Microsecond)) {
				t.Errorf("seq %d: ts = %v, appended %v", e.Seq, e.TS, appended[i].TS)
			}
		}
	})

	t.Run("events after a seq", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		mustAppend(t, s, "r1", &event.RunCreated{}, &event.EnvRequested{}, &event.EnvReady{EnvID: "e"})
		mustAppend(t, s, "r2", &event.RunCreated{})
		for after, want := range map[int64][]int64{-1: {1, 2, 3}, 0: {1, 2, 3}, 1: {2, 3}, 2: {3}} {
			got, err := s.Events(ctx, "r1", after)
			if err != nil {
				t.Fatal(err)
			}
			if ss := seqs(got); !slices.Equal(ss, want) {
				t.Errorf("after %d = %v, want %v", after, ss, want)
			}
		}
		// nil, not empty: the list handlers serialize what they get.
		for _, after := range []int64{3, 4, 100} {
			if got, err := s.Events(ctx, "r1", after); err != nil || got != nil {
				t.Errorf("after %d = %#v, %v", after, got, err)
			}
		}
	})

	t.Run("an unknown run has no events and no id", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		mustAppend(t, s, "r1", &event.RunCreated{})
		if got, err := s.Events(ctx, "nope", 0); err != nil || got != nil {
			t.Errorf("events = %#v, %v", got, err)
		}
		if ids, err := s.RunIDs(ctx); err != nil || !slices.Equal(ids, []string{"r1"}) {
			t.Errorf("run ids = %v, %v", ids, err)
		}
	})

	t.Run("a run with no run_created is still a run", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		evs := mustAppend(t, s, "r1", &event.EnvReady{EnvID: "e"})
		if evs[0].Seq != 1 {
			t.Errorf("seq = %d", evs[0].Seq)
		}
		if ids, err := s.RunIDs(ctx); err != nil || !slices.Equal(ids, []string{"r1"}) {
			t.Errorf("run ids = %v, %v", ids, err)
		}
	})

	t.Run("run ids are distinct and sorted", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		if ids, err := s.RunIDs(ctx); err != nil || ids != nil {
			t.Fatalf("no runs = %#v, %v", ids, err)
		}
		// ULID-shaped: what the engine names runs, and where every collation
		// agrees with byte order.
		for _, id := range []string{"01kz", "01ka", "01k0", "01kz"} {
			mustAppend(t, s, id, &event.EnvRequested{}, &event.EnvRequested{})
		}
		if ids, err := s.RunIDs(ctx); err != nil || !slices.Equal(ids, []string{"01k0", "01ka", "01kz"}) {
			t.Errorf("run ids = %v, %v", ids, err)
		}
	})
}

func runWatch(t *testing.T, open func(t *testing.T) event.Store) {
	t.Run("watch replays, then streams in order", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		mustAppend(t, s, "r1", &event.RunCreated{TicketTitle: "t"}, &event.EnvRequested{})
		ch, err := s.Watch(ctx, "r1", 0)
		if err != nil {
			t.Fatal(err)
		}
		// Appended before the replay is read: it still comes after it.
		live := mustAppend(t, s, "r1", &event.EnvReady{EnvID: "e"})
		first := next(t, ch)
		if p, ok := first.Payload.(*event.RunCreated); !ok || first.Seq != 1 || p.TicketTitle != "t" {
			t.Errorf("replay[0] = %+v", first)
		}
		if e := next(t, ch); e.Seq != 2 || e.Type != event.TypeEnvRequested {
			t.Errorf("replay[1] = %+v", e)
		}
		e := next(t, ch)
		if p, ok := e.Payload.(*event.EnvReady); !ok || e.Seq != 3 || p.EnvID != "e" || !e.TS.Equal(live[0].TS) {
			t.Errorf("live = %+v", e)
		}
		more := mustAppend(t, s, "r1", &event.StageStarted{StageID: "plan"}, &event.EnvRequested{})
		for _, want := range more {
			if e := next(t, ch); e.Seq != want.Seq || e.Type != want.Type || e.RunID != "r1" {
				t.Errorf("live = %+v, want seq %d", e, want.Seq)
			}
		}
	})

	t.Run("watch from a seq has no gap and no repeat", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		mustAppend(t, s, "r1", &event.RunCreated{}, &event.EnvRequested{}, &event.EnvReady{EnvID: "e"})
		ch, err := s.Watch(ctx, "r1", 2)
		if err != nil {
			t.Fatal(err)
		}
		mustAppend(t, s, "r1", &event.EnvRequested{})
		for _, want := range []int64{3, 4} {
			if e := next(t, ch); e.Seq != want {
				t.Fatalf("seq = %d, want %d", e.Seq, want)
			}
		}
	})

	t.Run("watch ahead of the log waits for its seq", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		mustAppend(t, s, "r1", &event.RunCreated{})
		ch, err := s.Watch(ctx, "r1", 3)
		if err != nil {
			t.Fatal(err)
		}
		mustAppend(t, s, "r1", &event.EnvRequested{}, &event.EnvRequested{})
		mustAppend(t, s, "r1", &event.EnvRequested{}, &event.EnvRequested{})
		if e := next(t, ch); e.Seq != 4 {
			t.Fatalf("first heard = seq %d, want 4", e.Seq)
		}
		if e := next(t, ch); e.Seq != 5 {
			t.Errorf("then seq %d, want 5", e.Seq)
		}
	})

	t.Run("watch on an unknown run hears its first event", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		ch, err := s.Watch(ctx, "new", 0)
		if err != nil {
			t.Fatal(err)
		}
		mustAppend(t, s, "new", &event.RunCreated{})
		if e := next(t, ch); e.Seq != 1 || e.Type != event.TypeRunCreated {
			t.Errorf("event = %+v", e)
		}
	})

	t.Run("a watcher that joins mid-write misses nothing", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		const n = 60
		started := make(chan struct{})
		failed := make(chan error, 1)
		go func() {
			for i := range n {
				if _, err := s.Append(ctx, "r1", &event.EnvRequested{}); err != nil {
					failed <- err
					return
				}
				if i == n/3 {
					close(started)
				}
			}
		}()
		select {
		case <-started:
		case err := <-failed:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("writer stalled")
		}
		ch, err := s.Watch(ctx, "r1", 0)
		if err != nil {
			t.Fatal(err)
		}
		for want := int64(1); want <= n; want++ {
			select {
			case e, ok := <-ch:
				if !ok {
					t.Fatalf("stream closed at seq %d", want)
				}
				if e.Seq != want {
					t.Fatalf("seq = %d, want %d", e.Seq, want)
				}
			case err := <-failed:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatalf("no seq %d", want)
			}
		}
	})

	t.Run("every watcher hears its own run only", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		a, err := s.Watch(ctx, "r1", 0)
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.Watch(ctx, "r1", 0)
		if err != nil {
			t.Fatal(err)
		}
		other, err := s.Watch(ctx, "r2", 0)
		if err != nil {
			t.Fatal(err)
		}
		mustAppend(t, s, "r1", &event.RunCreated{})
		mustAppend(t, s, "r2", &event.EnvRequested{})
		for name, ch := range map[string]<-chan event.Event{"a": a, "b": b} {
			if e := next(t, ch); e.RunID != "r1" || e.Seq != 1 {
				t.Errorf("%s heard %+v", name, e)
			}
		}
		if e := next(t, other); e.RunID != "r2" || e.Type != event.TypeEnvRequested {
			t.Errorf("r2's watcher heard %+v", e)
		}
	})

	t.Run("cancelling ends the stream", func(t *testing.T) {
		for name, setup := range map[string]func(t *testing.T, s event.Store, ch <-chan event.Event){
			"idle, replay read": func(t *testing.T, s event.Store, ch <-chan event.Event) {
				next(t, ch)
				next(t, ch)
			},
			"replay unread": func(*testing.T, event.Store, <-chan event.Event) {},
			"live event unread": func(t *testing.T, s event.Store, ch <-chan event.Event) {
				next(t, ch)
				next(t, ch)
				mustAppend(t, s, "r1", &event.EnvRequested{})
			},
		} {
			t.Run(name, func(t *testing.T) {
				s := open(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				mustAppend(t, s, "r1", &event.RunCreated{}, &event.EnvRequested{})
				ch, err := s.Watch(ctx, "r1", 0)
				if err != nil {
					t.Fatal(err)
				}
				setup(t, s, ch)
				cancel()
				drain(t, ch)
				// A stream that has gone never holds an appender up.
				done := appendAsync(s, "r1", manyPayloads(overflow)...)
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(wait):
					t.Fatal("Append blocked on a cancelled watcher")
				}
			})
		}
	})

	t.Run("a watcher that stops reading loses the stream, not the appender", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		ch, err := s.Watch(ctx, "r1", 0)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-appendAsync(s, "r1", manyPayloads(overflow)...):
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(wait):
			t.Fatal("Append blocked on a slow watcher")
		}
		var seen int64
		for e := range ch { // must end by itself, before the context does
			seen++
			if e.Seq != seen {
				t.Fatalf("seq = %d, want %d", e.Seq, seen)
			}
		}
		if ctx.Err() != nil {
			t.Fatal("stream never closed")
		}
		if seen >= overflow {
			t.Errorf("read %d events through an overflowed stream", seen)
		}
		rest, err := s.Events(context.Background(), "r1", seen)
		if err != nil || int64(len(rest)) != overflow-seen {
			t.Errorf("re-sync got %d of %d, %v", len(rest), overflow-seen, err)
		}
	})

	t.Run("a replay is not limited by the live buffer", func(t *testing.T) {
		s := open(t)
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		mustAppend(t, s, "r1", manyPayloads(overflow)...)
		ch, err := s.Watch(ctx, "r1", 0)
		if err != nil {
			t.Fatal(err)
		}
		for want := int64(1); want <= overflow; want++ {
			if e := next(t, ch); e.Seq != want {
				t.Fatalf("seq = %d, want %d", e.Seq, want)
			}
		}
	})
}

func runConcurrent(t *testing.T, open func(t *testing.T) event.Store) {
	t.Run("concurrent appends on different runs all land", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		const runs, each = 8, 5
		var wg sync.WaitGroup
		for r := range runs {
			wg.Go(func() {
				id := fmt.Sprintf("r%d", r)
				for range each {
					if _, err := s.Append(ctx, id, &event.EnvRequested{}, &event.EnvRequested{}); err != nil {
						t.Errorf("%s: %v", id, err)
					}
				}
			})
		}
		wg.Wait()
		ids, err := s.RunIDs(ctx)
		if err != nil || len(ids) != runs {
			t.Fatalf("run ids = %v, %v", ids, err)
		}
		for _, id := range ids {
			got, err := s.Events(ctx, id, 0)
			if err != nil || len(got) != 2*each {
				t.Fatalf("%s: %d events, %v", id, len(got), err)
			}
			checkGapless(t, got)
		}
	})

	// One writer per run is the rule (Store's doc). Racing it may lose: the
	// second transaction meets the first's key and is refused. What it may
	// never do is number two events alike, leave a gap, split a batch, or
	// keep a batch it refused.
	t.Run("concurrent appends on one run never corrupt the log", func(t *testing.T) {
		s := open(t)
		ctx := context.Background()
		const writers, each = 8, 5
		var (
			mu       sync.Mutex
			accepted [][]event.Event
			wg       sync.WaitGroup
		)
		for w := range writers {
			wg.Go(func() {
				for i := range each {
					tag := fmt.Sprintf("%d-%d", w, i)
					evs, err := s.Append(ctx, "r1", &event.EnvReady{EnvID: tag}, &event.EnvFailed{Error: tag})
					if err != nil {
						continue
					}
					mu.Lock()
					accepted = append(accepted, evs)
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if len(accepted) == 0 {
			t.Fatal("no append landed")
		}
		got, err := s.Events(ctx, "r1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2*len(accepted) {
			t.Fatalf("stored %d events for %d accepted batches", len(got), len(accepted))
		}
		checkGapless(t, got)
		for _, batch := range accepted {
			first, second := got[batch[0].Seq-1], got[batch[1].Seq-1]
			tag := batch[0].Payload.(*event.EnvReady).EnvID
			if batch[1].Seq != batch[0].Seq+1 {
				t.Errorf("batch %s split: seqs %d, %d", tag, batch[0].Seq, batch[1].Seq)
			}
			if p, ok := first.Payload.(*event.EnvReady); !ok || p.EnvID != tag {
				t.Errorf("seq %d = %+v, want batch %s", first.Seq, first.Payload, tag)
			}
			if p, ok := second.Payload.(*event.EnvFailed); !ok || p.Error != tag {
				t.Errorf("seq %d = %+v, want batch %s", second.Seq, second.Payload, tag)
			}
		}
	})
}

// runCapabilities covers the optional interfaces callers find by type
// assertion. A store without one has no row to read: its callers reduce the
// log, so a store with one must say what that reduction says.
func runCapabilities(t *testing.T, open func(t *testing.T) event.Store) {
	seed := func(t *testing.T, s event.Store) {
		t.Helper()
		mustAppend(t, s, "r1", &event.RunCreated{
			ProjectID: "p1", UserID: "u1", TicketID: "t1", TicketTitle: "Add login", TicketGoal: "goal",
			FlowName: "f", FlowHash: "h", RepoURL: "https://x.local/r.git", Branch: "feat/login-r1", SourceBranch: "main",
			Params: map[string]string{"model": "fake/one"}, By: "human",
		}, &event.EnvRequested{})
		mustAppend(t, s, "r1", &event.EnvReady{EnvID: "env-1", Links: []event.Link{{Kind: "app", URL: "http://x.local"}}})
		mustAppend(t, s, "r1", &event.StageStarted{StageID: "plan", Iteration: 1}, &event.PingRaised{PingID: "p-1", Type: event.PingGate, Question: "ok?"})
		mustAppend(t, s, "r1", &event.PingAnswered{PingID: "p-1", Answer: "approve"}, &event.MRCreated{MRURL: "https://x.local/mr/1", MRIID: 1, Branch: "feat/login-r1"})
		mustAppend(t, s, "r1", &event.EnvRebuildStarted{})
		mustAppend(t, s, "r2", &event.RunCreated{UserID: "u2", TicketTitle: "Other"})
	}

	t.Run("ViewLookup says what the log reduces to", func(t *testing.T) {
		s := open(t)
		vl, ok := s.(run.ViewLookup)
		if !ok {
			t.Skipf("%T keeps no run rows", s)
		}
		ctx := context.Background()
		seed(t, s)
		for _, id := range []string{"r1", "r2"} {
			v, found, err := vl.RunView(ctx, id)
			if err != nil || !found {
				t.Fatalf("%s: found=%v, %v", id, found, err)
			}
			checkView(t, s, id, v)
		}
		if v, found, err := vl.RunView(ctx, "nope"); err != nil || found || v.RunID != "" {
			t.Errorf("unknown run = %+v, %v, %v", v, found, err)
		}
	})

	t.Run("EnvLookup finds the live run behind a workspace", func(t *testing.T) {
		s := open(t)
		el, ok := s.(run.EnvLookup)
		if !ok {
			t.Skipf("%T keeps no env index", s)
		}
		ctx := context.Background()
		seed(t, s)
		v, found, err := el.RunByEnv(ctx, "env-1")
		if err != nil || !found {
			t.Fatalf("found=%v, %v", found, err)
		}
		checkView(t, s, "r1", v)
		if v, found, err := el.RunByEnv(ctx, "nope"); err != nil || found || v.RunID != "" {
			t.Errorf("unknown env = %+v, %v, %v", v, found, err)
		}
		// "" is every run without a workspace, so it names none of them.
		if v, found, err := el.RunByEnv(ctx, ""); err != nil || found || v.RunID != "" {
			t.Errorf("no env = %+v, %v, %v", v, found, err)
		}
		// A destroyed workspace has no run behind it...
		mustAppend(t, s, "r1", &event.EnvDestroyed{})
		if v, found, err := el.RunByEnv(ctx, "env-1"); err != nil || found {
			t.Errorf("destroyed = %+v, %v, %v", v, found, err)
		}
		// ...and should a rebuild race leave two, the newest wins.
		mustAppend(t, s, "r2", &event.EnvReady{EnvID: "shared"})
		mustAppend(t, s, "r3", &event.RunCreated{}, &event.EnvReady{EnvID: "shared"})
		if v, found, err := el.RunByEnv(ctx, "shared"); err != nil || !found || v.RunID != "r3" {
			t.Errorf("shared = %+v, %v, %v", v, found, err)
		}
	})

	t.Run("ViewSource lists every run, newest first", func(t *testing.T) {
		s := open(t)
		vs, ok := s.(run.ViewSource)
		if !ok {
			t.Skipf("%T keeps no run rows", s)
		}
		ctx := context.Background()
		seed(t, s)
		views, err := vs.RunViews(ctx)
		if err != nil || len(views) != 2 {
			t.Fatalf("views = %+v, %v", views, err)
		}
		if views[0].RunID != "r2" || views[1].RunID != "r1" {
			t.Errorf("order = %s, %s", views[0].RunID, views[1].RunID)
		}
		for _, v := range views {
			checkView(t, s, v.RunID, v)
		}
	})
}

// checkView compares a stored row with the run's log reduced: the JSON, which
// is what the row keeps, and the two fields it keeps beside it. Times differ
// below the microsecond, a row being folded from the events as appended.
func checkView(t *testing.T, s event.Store, id string, got run.View) {
	t.Helper()
	evs, err := s.Events(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := run.Reduce(evs)
	if a, b := viewJSON(t, got), viewJSON(t, want); a != b {
		t.Errorf("%s: row = %s\nreduced = %s", id, a, b)
	}
	if got.Dormant != want.Dormant || got.Rebuilding != want.Rebuilding {
		t.Errorf("%s: dormant=%v rebuilding=%v, reduced dormant=%v rebuilding=%v",
			id, got.Dormant, got.Rebuilding, want.Dormant, want.Rebuilding)
	}
}

func viewJSON(t *testing.T, v run.View) string {
	t.Helper()
	v.CreatedAt = v.CreatedAt.UTC().Truncate(time.Microsecond)
	v.UpdatedAt = v.UpdatedAt.UTC().Truncate(time.Microsecond)
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// unmarshalable is a payload json.Marshal refuses.
type unmarshalable struct{ C chan int }

func (unmarshalable) EventType() event.Type { return event.TypeRunCreated }

// failing is a payload whose own marshaler fails.
type failing struct{}

func (failing) EventType() event.Type        { return event.TypeRunCreated }
func (failing) MarshalJSON() ([]byte, error) { return nil, errors.New("no") }

// undecodable stores fine and cannot be read back as the type it claims.
type undecodable struct{}

func (undecodable) EventType() event.Type        { return event.TypeEnvReady }
func (undecodable) MarshalJSON() ([]byte, error) { return []byte(`{"env_id":7}`), nil }

// legacy is what an event of the pre-rename run_retried looks like.
type legacy struct {
	StageID string `json:"stage_id"`
}

func (legacy) EventType() event.Type { return "run_resumed" }

type payloadCase struct {
	name string
	in   event.Payload
	want event.Payload
}

func payloadCases() []payloadCase {
	one, two, summary, resolve := 1, 2, "kept", true
	lo := 0.5
	created := func() *event.RunCreated {
		return &event.RunCreated{
			TicketTitle: "Add login é", TicketGoal: "goal <&> \"quoted\"\nline", TicketChangeType: "feat",
			FlowName: "f", FlowHash: "h", RepoURL: "https://x.local/r.git", Branch: "feat/a", SourceBranch: "main",
			ProjectID: "p", UserID: "u", TicketID: "t", BranchBase: "feat/a", RunGoal: "again",
			Checkouts: []event.Checkout{
				{Name: "repo", Dir: "repo", URL: "u", Branch: "b", Source: "s", Primary: true},
				{Name: "x", Dir: "repos/x", URL: "v", Locator: "l", Branch: "c", Source: "t", Fetch: "f", Fork: true},
			},
			TicketAttachmentIDs: []string{"a2", "a1"},
			TicketSourceID:      "i", TicketSourceKind: "jira", TicketSourceKey: "K-1", TicketSourceURL: "https://x.local/K-1",
			Params: map[string]string{"model": "fake/one", "empty": ""}, By: "human",
			MR:        &event.AdoptedMR{Repo: "repo", IID: 7, URL: "https://x.local/mr/7", Head: "abc", Author: "a", Title: "t"},
			Skillsets: []event.SkillsetPin{{ID: "s", Name: "n", Seq: 3, Hash: "h"}},
		}
	}
	raised := func() *event.PingRaised {
		return &event.PingRaised{
			PingID: "p-1", Type: event.PingClarification, Question: "which?", Context: "ctx", Files: []string{"b.md", "a.md"},
			Link: "https://x.local", FileTypes: map[string]string{"b.md": "review_threads"}, StageID: "plan", Key: "k",
			AnswerKind: event.AnswerForm, Phase: "fix", Subject: "repo", TriggerKey: "tk", EndsPhase: true,
			Form: &event.Form{Fields: []event.Field{
				{ID: "a", Type: event.FieldString, Title: "A", Required: true, Options: []event.Option{{ID: "x", Label: "X"}}, MinLength: &one, MaxLength: &two},
				{ID: "b", Type: event.FieldNumber, Minimum: &lo, Default: 1.5},
				{ID: "c", Type: event.FieldArray, Default: []any{"x", "y"}},
			}},
		}
	}
	answered := func() *event.PingAnswered {
		return &event.PingAnswered{
			PingID: "p-1", Answer: "accept", Feedback: "fine",
			Content:       map[string]any{"s": "x", "n": 3.0, "b": true, "l": []any{"p", "q"}},
			AttachmentIDs: []string{"a1"},
			Review: []event.ReviewComment{
				{Path: "b.md", StartLine: 1, EndLine: 2, Heading: "H", Quote: "q", Body: "first", AttachmentIDs: []string{"a1"}},
				{Path: "a.md", Body: "second"},
			},
			Curated: map[string]reviewfile.Curation{"b.md": {Summary: &summary, Items: []reviewfile.CuratedItem{
				{Item: "notes/0", Keep: true, Body: "b", Resolve: &resolve},
			}}},
		}
	}
	same := func(name string, mk func() event.Payload) payloadCase {
		return payloadCase{name, mk(), mk()}
	}
	return []payloadCase{
		same("nested run_created", func() event.Payload { return created() }),
		same("ping with a form", func() event.Payload { return raised() }),
		same("answer with a review", func() event.Payload { return answered() }),
		same("no fields", func() event.Payload { return &event.EnvRequested{} }),
		same("nil list keeps its null", func() event.Payload { return &event.EnvReady{EnvID: "e"} }),
		same("snapshot", func() event.Payload {
			return &event.SnapshotCaptured{SnapshotID: "s", StageID: "b", Iteration: 2, Kind: "output", Name: "plan", Path: "p.md",
				Filename: "p.md", Mime: "text/markdown", SHA256: "ee", Size: 9, Truncated: true}
		}),
		same("agent tool", func() event.Payload {
			return &event.AgentTool{StageID: "b", ToolCallID: "t", Locations: []string{"a"}, Diffs: []event.AgentDiff{{Path: "a", NewText: "x"}}}
		}),
		{"empty collections are absent",
			&event.RunCreated{Params: map[string]string{}, Checkouts: []event.Checkout{}, TicketAttachmentIDs: []string{}},
			&event.RunCreated{}},
		{"an empty list without omitempty stays empty",
			&event.EnvReady{EnvID: "e", Links: []event.Link{}},
			&event.EnvReady{EnvID: "e", Links: []event.Link{}}},
		{"a backslash is not a NUL",
			&event.RunFailed{Reason: `a\u0000`},
			&event.RunFailed{Reason: `a\u0000`}},
		{"invalid UTF-8 is replaced",
			&event.RunFailed{Reason: "a\xffb"},
			&event.RunFailed{Reason: "a�b"}},
		{"a retired spelling decodes as its successor",
			legacy{StageID: "s"},
			&event.RunRetried{StageID: "s"}},
	}
}

func mustAppend(t *testing.T, s event.Store, runID string, ps ...event.Payload) []event.Event {
	t.Helper()
	evs, err := s.Append(context.Background(), runID, ps...)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func appendAsync(s event.Store, runID string, ps ...event.Payload) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := s.Append(context.Background(), runID, ps...)
		done <- err
	}()
	return done
}

func manyPayloads(n int) []event.Payload {
	out := make([]event.Payload, n)
	for i := range out {
		out[i] = &event.EnvRequested{}
	}
	return out
}

func seqs(batches ...[]event.Event) []int64 {
	var out []int64
	for _, b := range batches {
		for _, e := range b {
			out = append(out, e.Seq)
		}
	}
	return out
}

func checkGapless(t *testing.T, evs []event.Event) {
	t.Helper()
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("position %d holds seq %d", i, e.Seq)
		}
	}
}

func next(t *testing.T, ch <-chan event.Event) event.Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("stream closed")
		}
		return e
	case <-time.After(wait):
		t.Fatal("no event")
	}
	return event.Event{}
}

func drain(t *testing.T, ch <-chan event.Event) {
	t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("stream never closed")
		}
	}
}
