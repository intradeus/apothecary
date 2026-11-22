package envport

import (
	"strings"
	"testing"
	"time"
)

func TestSandboxSizeString(t *testing.T) {
	for s, want := range map[SandboxSize]string{
		{}:                                "no CPU limit, no memory limit",
		{MilliCPU: 2000, MemoryMiB: 8192}: "2 CPUs, 8192 MiB",
		{MilliCPU: 1500}:                  "1.5 CPUs, no memory limit",
		{MilliCPU: 250, MemoryMiB: 512}:   "0.25 CPUs, 512 MiB",
	} {
		if got := s.String(); got != want {
			t.Errorf("%+v = %q, want %q", s, got, want)
		}
	}
	if !(SandboxSize{}).IsZero() || (SandboxSize{MemoryMiB: 1}).IsZero() {
		t.Error("IsZero")
	}
}

func TestSandboxSizesValidate(t *testing.T) {
	ok := SandboxPolicy{Default: SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}, Ceiling: SandboxSize{MilliCPU: 8000, MemoryMiB: 16384}}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	if err := (SandboxPolicy{}).Validate(); err != nil {
		t.Errorf("unset policy: %v", err)
	}
	for _, c := range []struct {
		p    SandboxPolicy
		want string
	}{
		{SandboxPolicy{Default: SandboxSize{MilliCPU: -1}}, "sandbox default size"},
		{SandboxPolicy{Ceiling: SandboxSize{MemoryMiB: -5}}, "sandbox size ceiling"},
		{SandboxPolicy{Default: SandboxSize{MemoryMiB: 9000}, Ceiling: SandboxSize{MemoryMiB: 8192}}, "above the ceiling"},
	} {
		if err := c.p.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want %q", c.p, err, c.want)
		}
	}
}

func TestSandboxSizesCheck(t *testing.T) {
	p := SandboxPolicy{Ceiling: SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}}
	if err := p.Check(SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}); err != nil {
		t.Errorf("at the ceiling: %v", err)
	}
	if err := p.Check(SandboxSize{}); err != nil {
		t.Errorf("unset: %v", err)
	}
	if err := p.Check(SandboxSize{MilliCPU: 4001}); err == nil || !strings.Contains(err.Error(), "4000 mCPU") || !strings.Contains(err.Error(), "System → Limits") {
		t.Errorf("cpu over: %v", err)
	}
	if err := p.Check(SandboxSize{MemoryMiB: 8193}); err == nil || !strings.Contains(err.Error(), "8192 MiB") {
		t.Errorf("memory over: %v", err)
	}
	if err := p.Check(SandboxSize{MemoryMiB: -1}); err == nil {
		t.Error("negative accepted")
	}
	if err := (SandboxPolicy{}).Check(SandboxSize{MilliCPU: 64000, MemoryMiB: 1 << 20}); err != nil {
		t.Errorf("no ceiling: %v", err)
	}
}

// A field's floor and top: a slip of the unit, or a number past what the
// store holds, is refused rather than saved (or failing the write).
func TestSandboxSizeBounds(t *testing.T) {
	for _, s := range []SandboxSize{
		{MilliCPU: MinMilliCPU, MemoryMiB: MinMemoryMiB},
		{MilliCPU: MaxMilliCPU, MemoryMiB: MaxMemoryMiB},
		{},
	} {
		if err := s.Validate("x"); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	for s, want := range map[SandboxSize]string{
		{MilliCPU: MinMilliCPU - 1}:   "CPU must be",
		{MilliCPU: MaxMilliCPU + 1}:   "CPU must be",
		{MilliCPU: -1}:                "CPU must be",
		{MemoryMiB: 4}:                "memory must be",
		{MemoryMiB: MaxMemoryMiB + 1}: "memory must be",
		{MemoryMiB: 1 << 40}:          "memory must be",
	} {
		if err := s.Validate("x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: err = %v, want %q", s, err, want)
		}
	}
}

// Saving a project again checks only what the save changes: a ceiling
// lowered under its size does not refuse every later save.
func TestSandboxSizesCheckChange(t *testing.T) {
	p := SandboxPolicy{Ceiling: SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}}
	prev := SandboxSize{MilliCPU: 8000, MemoryMiB: 16384} // saved under an older ceiling
	if err := p.CheckChange(prev, prev); err != nil {
		t.Errorf("unchanged: %v", err)
	}
	if err := p.CheckChange(prev, SandboxSize{MilliCPU: 8000, MemoryMiB: 2048}); err != nil {
		t.Errorf("memory lowered, CPU kept: %v", err)
	}
	if err := p.CheckChange(prev, SandboxSize{MilliCPU: 9000, MemoryMiB: 16384}); err == nil || !strings.Contains(err.Error(), "2000 mCPU") {
		t.Errorf("CPU raised: %v", err)
	}
	if err := p.CheckChange(prev, SandboxSize{MilliCPU: 8000, MemoryMiB: 20000}); err == nil || !strings.Contains(err.Error(), "4096 MiB") {
		t.Errorf("memory raised: %v", err)
	}
	if err := p.CheckChange(SandboxSize{MemoryMiB: 100}, SandboxSize{MemoryMiB: 100}); err != nil {
		t.Errorf("an older out-of-bounds field kept: %v", err)
	}
	if err := p.CheckChange(prev, SandboxSize{MilliCPU: 50, MemoryMiB: 16384}); err == nil {
		t.Error("a changed field below the floor is refused")
	}
}

func TestSandboxSizesEffective(t *testing.T) {
	p := SandboxPolicy{Default: SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}, Ceiling: SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}}
	for _, c := range []struct {
		own, want SandboxSize
	}{
		{SandboxSize{}, SandboxSize{MilliCPU: 2000, MemoryMiB: 4096}},
		{SandboxSize{MemoryMiB: 6144}, SandboxSize{MilliCPU: 2000, MemoryMiB: 6144}},
		{SandboxSize{MilliCPU: 9000, MemoryMiB: 99999}, SandboxSize{MilliCPU: 4000, MemoryMiB: 8192}}, // a ceiling lowered since
	} {
		if got := p.Effective(c.own); got != c.want {
			t.Errorf("Effective(%+v) = %+v, want %+v", c.own, got, c.want)
		}
	}
	// A ceiling without a default sizes nothing: it caps (Cap).
	ceilOnly := SandboxPolicy{Ceiling: SandboxSize{MilliCPU: 4000, MemoryMiB: 65536}}
	if got := ceilOnly.Effective(SandboxSize{}); !got.IsZero() {
		t.Errorf("ceiling only = %+v, want unset", got)
	}
	if got := ceilOnly.Cap(SandboxSize{}); got != ceilOnly.Ceiling {
		t.Errorf("Cap(unset) = %+v, want the ceiling", got)
	}
	if got := ceilOnly.Cap(SandboxSize{MemoryMiB: 4096}); got != (SandboxSize{MilliCPU: 4000}) {
		t.Errorf("Cap(memory set) = %+v, want the CPU ceiling alone", got)
	}
	if got := p.Cap(p.Effective(SandboxSize{})); !got.IsZero() {
		t.Errorf("a sized sandbox has no cap: %+v", got)
	}
	// Nothing set anywhere: no limits, as before sizes existed.
	if got := (SandboxPolicy{}).Effective(SandboxSize{}); !got.IsZero() {
		t.Errorf("unset = %+v", got)
	}
}

func TestIdleAfter(t *testing.T) {
	p := SandboxPolicy{IdleMinutes: 30}
	for own, want := range map[int]time.Duration{0: 30 * time.Minute, IdleNever: 0, 5: 5 * time.Minute} {
		if got := p.IdleAfter(own); got != want {
			t.Errorf("IdleAfter(%d) = %s, want %s", own, got, want)
		}
	}
	if got := (SandboxPolicy{}).IdleAfter(0); got != 0 {
		t.Errorf("no default: %s", got)
	}
	if ValidateIdle(-2) == nil || ValidateIdle(IdleNever) != nil || ValidateIdle(0) != nil || ValidateIdle(90) != nil {
		t.Error("ValidateIdle")
	}
	if (SandboxPolicy{IdleMinutes: -1}).Validate() == nil {
		t.Error("a negative idle default is refused")
	}
}
