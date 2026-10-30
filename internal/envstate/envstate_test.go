package envstate

import (
	"apothecary/internal/ports"
	"errors"
	"testing"
	"time"
)

func TestBadgesOverlayAndTTL(t *testing.T) {
	clk := time.Unix(1000, 0)
	s := New(nil, nil, func() time.Time { return clk }, nil)
	s.SetStatus("r1", ports.EnvStopped)

	// An in-flight op overlays the reconciled docker status.
	rel, err := s.Begin("r1", "rebuilding")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Badges()["r1"]; got != "rebuilding" {
		t.Errorf("badge = %q, want rebuilding", got)
	}
	// Failing the op leaves an "error" badge...
	rel(errors.New("boom"))
	if got := s.Badges()["r1"]; got != "error" {
		t.Errorf("badge = %q, want error", got)
	}
	// ...which ages out after the TTL, falling back to the docker status.
	clk = clk.Add(ErrorTTL + time.Second)
	if got := s.Badges()["r1"]; got != "stopped" {
		t.Errorf("badge after TTL = %q, want stopped (fallback)", got)
	}
}
