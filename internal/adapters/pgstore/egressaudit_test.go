package pgstore

import (
	"testing"
	"time"

	"apothecary/internal/ports/egressport"
)

func TestEgressAuditUpsertAndQuery(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	rows := []egressport.EgressAuditRow{
		{Project: "p1", Run: "r1", Host: "github.com", DstIP: "1.1.1.1", Port: 443, Proto: "tls", Verdict: "allow", Count: 3, BytesOut: 100, BytesIn: 900, First: now, Last: now},
		{Project: "p1", Run: "r1", Host: "example.com", Port: 443, Verdict: "deny", Count: 5, First: now, Last: now.Add(time.Minute)},
		{Project: "p1", Run: "r2", Host: "github.com", Port: 443, Verdict: "allow", Count: 1, First: now, Last: now},
	}
	if err := s.UpsertEgressAudit(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// Re-ingest with a HIGHER count → monotonic GREATEST, no double-count.
	rows[0].Count = 8
	rows[0].BytesOut = 250
	if err := s.UpsertEgressAudit(ctx, rows); err != nil {
		t.Fatal(err)
	}

	got, err := s.QueryEgressAudit(ctx, egressport.EgressAuditFilter{Run: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("run r1 rows = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.Host == "github.com" && (r.Count != 8 || r.BytesOut != 250) {
			t.Errorf("upsert did not take GREATEST: %+v", r)
		}
	}

	// Filter by verdict.
	deny, _ := s.QueryEgressAudit(ctx, egressport.EgressAuditFilter{Project: "p1", Verdict: "deny"})
	if len(deny) != 1 || deny[0].Host != "example.com" {
		t.Errorf("verdict filter = %+v", deny)
	}
	// Filter by host substring across the project.
	gh, _ := s.QueryEgressAudit(ctx, egressport.EgressAuditFilter{Project: "p1", Host: "github"})
	if len(gh) != 2 {
		t.Errorf("host filter got %d, want 2 (r1+r2)", len(gh))
	}
}
