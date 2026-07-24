package main

import (
	"testing"
)

func TestAuditorAggregatesAndFlushes(t *testing.T) {
	path := t.TempDir() + "/egress-audit.json"
	a := newAuditor(path)
	a.record("run1", "github.com", "140.82.121.4", 443, "tls", "allow", 100, 2000)
	a.record("run1", "github.com", "140.82.121.4", 443, "tls", "allow", 50, 1000) // same tuple → aggregate
	a.record("run1", "example.com", "93.184.216.34", 443, "tls", "deny", 0, 0)    // distinct
	a.record("run2", "github.com", "140.82.121.4", 443, "tls", "allow", 10, 10)   // distinct run
	a.flush()

	var af AuditFile
	if err := readJSON(path, &af); err != nil {
		t.Fatal(err)
	}
	if len(af.Records) != 3 {
		t.Fatalf("records = %d, want 3", len(af.Records))
	}
	var gh *AuditRecord
	for i := range af.Records {
		r := &af.Records[i]
		if r.Run == "run1" && r.Host == "github.com" {
			gh = r
		}
	}
	if gh == nil {
		t.Fatal("run1/github.com record missing")
	}
	if gh.Count != 2 || gh.BytesOut != 150 || gh.BytesIn != 3000 || gh.Verdict != "allow" {
		t.Errorf("aggregate = %+v", gh)
	}
	if gh.First.After(gh.Last) {
		t.Error("first must be <= last")
	}
}

func TestAuditorRetainEvictsDeadRuns(t *testing.T) {
	a := newAuditor(t.TempDir() + "/a.json")
	a.record("run1", "github.com", "1.1.1.1", 443, "tls", "allow", 1, 1)
	a.record("run2", "example.com", "2.2.2.2", 443, "tls", "deny", 0, 0)
	a.retain(map[string]bool{"run1": true}) // run2 torn down
	a.flush()
	var af AuditFile
	_ = readJSON(a.path, &af)
	if len(af.Records) != 1 || af.Records[0].Run != "run1" {
		t.Fatalf("retain did not evict dead run: %+v", af.Records)
	}
}

func TestProtoFor(t *testing.T) {
	for port, want := range map[int]string{443: "tls", 80: "http", 5432: "other"} {
		if got := protoFor(port); got != want {
			t.Errorf("protoFor(%d) = %q, want %q", port, got, want)
		}
	}
}
