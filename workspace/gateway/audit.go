package main

import (
	"sync"
	"time"
)

// The auditor aggregates every egress connection the gateway sees into a compact
// per-(run,host,port,verdict) record with counts, byte totals and first/last
// timestamps — the durable audit trail. It is flushed to a file the engine
// ingests into Postgres. Aggregation (not per-event) keeps it bounded: a build
// touches thousands of connections but only tens of distinct hosts per run.

type auditKey struct {
	run     string
	host    string
	port    int
	verdict string
}

type auditVal struct {
	dstIP    string
	proto    string
	count    int64
	bytesOut int64
	bytesIn  int64
	first    time.Time
	last     time.Time
}

type auditor struct {
	path  string
	mu    sync.Mutex
	recs  map[auditKey]*auditVal
	dirty bool
}

func newAuditor(path string) *auditor {
	return &auditor{path: path, recs: map[auditKey]*auditVal{}}
}

func (a *auditor) record(run, host, dstIP string, port int, proto, verdict string, bytesOut, bytesIn int64) {
	if a == nil || a.path == "" {
		return
	}
	k := auditKey{run: run, host: host, port: port, verdict: verdict}
	now := time.Now().UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.recs[k]
	if v == nil {
		if len(a.recs) >= 8192 { // cap distinct tuples; new ones are dropped, not grown unbounded
			return
		}
		v = &auditVal{first: now}
		a.recs[k] = v
	}
	v.dstIP = dstIP
	v.proto = proto
	v.count++
	v.bytesOut += bytesOut
	v.bytesIn += bytesIn
	v.last = now
	a.dirty = true
}

// retain drops records for runs not in active (torn-down runs, already ingested
// by the engine), bounding the aggregate to live runs.
func (a *auditor) retain(active map[string]bool) {
	if a == nil || a.path == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.recs {
		if !active[k.run] {
			delete(a.recs, k)
			a.dirty = true
		}
	}
}

func (a *auditor) flush() {
	if a == nil || a.path == "" {
		return
	}
	a.mu.Lock()
	if !a.dirty {
		a.mu.Unlock()
		return
	}
	out := make([]AuditRecord, 0, len(a.recs))
	for k, v := range a.recs {
		out = append(out, AuditRecord{
			Run: k.run, Host: k.host, DstIP: v.dstIP, Port: k.port, Proto: v.proto,
			Verdict: k.verdict, Count: v.count, BytesOut: v.bytesOut, BytesIn: v.bytesIn,
			First: v.first, Last: v.last,
		})
	}
	a.dirty = false
	a.mu.Unlock()
	if err := writeAtomic(a.path, AuditFile{Records: out}); err != nil {
		return
	}
}

func protoFor(port int) string {
	switch port {
	case 443:
		return "tls"
	case 80:
		return "http"
	default:
		return "other"
	}
}
