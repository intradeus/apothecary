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
//
// A connection counts the moment it is let through and its bytes when it
// ends: a download that runs for minutes is on the trail from its start.

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
	// provisioning: some of these connections got through only because the
	// run's egress was open while it provisioned.
	provisioning bool
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

// record is a whole connection at once: a refusal, or one already done.
func (a *auditor) record(run, host, dstIP string, port int, proto, verdict string, bytesOut, bytesIn int64) {
	a.connect(run, host, dstIP, port, proto, verdict, false)
	a.transfer(run, host, port, verdict, bytesOut, bytesIn)
}

// connect counts a connection as it starts. provisioning: the policy alone
// would have refused it; the run's provisioning window let it through.
func (a *auditor) connect(run, host, dstIP string, port int, proto, verdict string, provisioning bool) {
	if a == nil {
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
	v.provisioning = v.provisioning || provisioning
	v.last = now
	a.dirty = true
}

// transfer adds what a connection carried, once it ends. A tuple the cap
// kept out stays out.
func (a *auditor) transfer(run, host string, port int, verdict string, bytesOut, bytesIn int64) {
	if a == nil {
		return
	}
	k := auditKey{run: run, host: host, port: port, verdict: verdict}
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.recs[k]
	if v == nil {
		return
	}
	v.bytesOut += bytesOut
	v.bytesIn += bytesIn
	v.last = time.Now().UTC()
	a.dirty = true
}

// retain drops records for runs not in active (torn-down runs, already ingested
// by the engine), bounding the aggregate to live runs.
func (a *auditor) retain(active map[string]bool) {
	if a == nil {
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
			First: v.first, Last: v.last, Provisioning: v.provisioning,
		})
	}
	a.dirty = false
	a.mu.Unlock()
	if err := writeAtomic(a.path, AuditFile{Records: out}); err != nil {
		return
	}
}

// snapshot returns the aggregated records, all runs or one.
func (a *auditor) snapshot(run string) []AuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditRecord, 0, len(a.recs))
	for k, v := range a.recs {
		if run != "" && k.run != run {
			continue
		}
		out = append(out, AuditRecord{
			Run: k.run, Host: k.host, DstIP: v.dstIP, Port: k.port, Proto: v.proto,
			Verdict: k.verdict, Count: v.count, BytesOut: v.bytesOut, BytesIn: v.bytesIn,
			First: v.first, Last: v.last, Provisioning: v.provisioning,
		})
	}
	return out
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
