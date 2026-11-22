package envport

import (
	"fmt"
	"strings"
	"time"
)

// SandboxSize is what a run's sandbox may use: CPU in thousandths of a core
// and memory in MiB. Zero in a field is "not set": no limit on the sandbox,
// or, in a project's setting, the instance's default.
type SandboxSize struct {
	MilliCPU  int `json:"milli_cpu"`
	MemoryMiB int `json:"memory_mib"`
}

// IsZero: nothing set.
func (s SandboxSize) IsZero() bool { return s == SandboxSize{} }

// String reads well in a log line: "2 CPUs, 8192 MiB"; unset fields are
// "no CPU limit", "no memory limit".
func (s SandboxSize) String() string {
	cpu, mem := "no CPU limit", "no memory limit"
	if s.MilliCPU > 0 {
		cpu = strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.3f", float64(s.MilliCPU)/1000), "0"), ".") + " CPUs"
	}
	if s.MemoryMiB > 0 {
		mem = fmt.Sprintf("%d MiB", s.MemoryMiB)
	}
	return cpu + ", " + mem
}

// Bounds of a set size field. The floor keeps a slip of the unit from
// becoming a sandbox that cannot start its docker daemon; the top keeps a
// field within what the store's columns and Kubernetes take (1000 cores,
// 16 TiB), so a typo is refused, never a failed write.
const (
	MinMilliCPU  = 100
	MaxMilliCPU  = 1_000_000
	MinMemoryMiB = 512
	MaxMemoryMiB = 16 << 20
)

// Validate refuses a field below its floor or above its top (0 is unset);
// what names the setting in the error.
func (s SandboxSize) Validate(what string) error {
	if s.MilliCPU != 0 && (s.MilliCPU < MinMilliCPU || s.MilliCPU > MaxMilliCPU) {
		return fmt.Errorf("%s: CPU must be 0 (unset) or between %d and %d mCPU, got %d", what, MinMilliCPU, MaxMilliCPU, s.MilliCPU)
	}
	if s.MemoryMiB != 0 && (s.MemoryMiB < MinMemoryMiB || s.MemoryMiB > MaxMemoryMiB) {
		return fmt.Errorf("%s: memory must be 0 (unset) or between %d and %d MiB, got %d", what, MinMemoryMiB, MaxMemoryMiB, s.MemoryMiB)
	}
	return nil
}

// SandboxPolicy is the instance's sandbox policy, an admin's setting: the
// size a project that sets none gets, the most any project may ask for, and
// how long a sandbox nobody uses stays up when its project does not say.
// Zero fields are unset (no default, no ceiling, never stopped).
type SandboxPolicy struct {
	Default     SandboxSize `json:"default"`
	Ceiling     SandboxSize `json:"ceiling"`
	IdleMinutes int         `json:"idle_minutes"`
}

// IdleNever is a project's idle setting that keeps its sandboxes up whatever
// the instance's default.
const IdleNever = -1

// IdleAfter is how long a project's sandbox may sit unused before it is
// stopped (0: never). own is the project's setting: 0 the instance's
// default, IdleNever never, else minutes.
func (p SandboxPolicy) IdleAfter(own int) time.Duration {
	m := own
	if m == 0 {
		m = p.IdleMinutes
	}
	if m <= 0 {
		return 0
	}
	return time.Duration(m) * time.Minute
}

// ValidateIdle refuses a project's idle setting below IdleNever.
func ValidateIdle(own int) error {
	if own < IdleNever {
		return fmt.Errorf("idle minutes must be %d (never), 0 (the instance's default) or a positive number, got %d", IdleNever, own)
	}
	return nil
}

// Validate: no negative field, and a default within the ceiling.
func (p SandboxPolicy) Validate() error {
	if p.IdleMinutes < 0 {
		return fmt.Errorf("the sandbox idle default must be 0 (never) or a positive number of minutes, got %d", p.IdleMinutes)
	}
	if err := p.Default.Validate("sandbox default size"); err != nil {
		return err
	}
	if err := p.Ceiling.Validate("sandbox size ceiling"); err != nil {
		return err
	}
	if err := p.Check(p.Default); err != nil {
		return fmt.Errorf("the sandbox default size is above the ceiling: %w", err)
	}
	return nil
}

// Check refuses a project's size out of bounds or above the ceiling, naming
// the field and the ceiling; an unset field is never above it.
func (p SandboxPolicy) Check(s SandboxSize) error { return p.CheckChange(SandboxSize{}, s) }

// CheckChange is Check for a project saved again with s, where prev is what
// it had: only a field the save changes is checked. A ceiling lowered under a
// project's size caps that size at the next provision (Effective); it does
// not make every later save of the project fail until someone lowers it.
func (p SandboxPolicy) CheckChange(prev, s SandboxSize) error {
	var changed SandboxSize // the fields the save changes; 0 where it keeps one
	if s.MilliCPU != prev.MilliCPU {
		changed.MilliCPU = s.MilliCPU
	}
	if s.MemoryMiB != prev.MemoryMiB {
		changed.MemoryMiB = s.MemoryMiB
	}
	if err := changed.Validate("sandbox size"); err != nil {
		return err
	}
	if c := p.Ceiling.MilliCPU; c > 0 && changed.MilliCPU > c {
		return fmt.Errorf("sandbox CPU %d mCPU is above this instance's ceiling of %d mCPU (System → Limits)", s.MilliCPU, c)
	}
	if c := p.Ceiling.MemoryMiB; c > 0 && changed.MemoryMiB > c {
		return fmt.Errorf("sandbox memory %d MiB is above this instance's ceiling of %d MiB (System → Limits)", s.MemoryMiB, c)
	}
	return nil
}

// Effective is the size a project's sandbox gets: each field its own, else
// the default; never above the ceiling (one lowered after the project was
// saved caps it). A field neither sets stays unset: the ceiling caps a
// size, it never becomes one (Cap).
func (p SandboxPolicy) Effective(s SandboxSize) SandboxSize {
	pick := func(own, def, ceil int) int {
		v := own
		if v <= 0 {
			v = def
		}
		if ceil > 0 && v > ceil {
			v = ceil
		}
		return v
	}
	return SandboxSize{
		MilliCPU:  pick(s.MilliCPU, p.Default.MilliCPU, p.Ceiling.MilliCPU),
		MemoryMiB: pick(s.MemoryMiB, p.Default.MemoryMiB, p.Ceiling.MemoryMiB),
	}
}

// Cap is the most a sandbox of the effective size may use where that size
// sets nothing: the ceiling's field. A provisioner limits the sandbox to it
// without reserving it (the sandbox asks for little and may grow to the
// cap), so a large ceiling never makes an unsized sandbox unschedulable.
func (p SandboxPolicy) Cap(effective SandboxSize) SandboxSize {
	var c SandboxSize
	if effective.MilliCPU <= 0 {
		c.MilliCPU = p.Ceiling.MilliCPU
	}
	if effective.MemoryMiB <= 0 {
		c.MemoryMiB = p.Ceiling.MemoryMiB
	}
	return c
}
