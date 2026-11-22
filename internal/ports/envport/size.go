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

// Validate refuses a negative field; what names the setting in the error.
func (s SandboxSize) Validate(what string) error {
	if s.MilliCPU < 0 || s.MemoryMiB < 0 {
		return fmt.Errorf("%s: CPU and memory must be positive, or 0 for unset (got %d mCPU, %d MiB)", what, s.MilliCPU, s.MemoryMiB)
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

// Check refuses a project's size above the ceiling, naming the field and the
// ceiling; an unset field is never above it.
func (p SandboxPolicy) Check(s SandboxSize) error {
	if err := s.Validate("sandbox size"); err != nil {
		return err
	}
	if c := p.Ceiling.MilliCPU; c > 0 && s.MilliCPU > c {
		return fmt.Errorf("sandbox CPU %d mCPU is above this instance's ceiling of %d mCPU (System → Limits)", s.MilliCPU, c)
	}
	if c := p.Ceiling.MemoryMiB; c > 0 && s.MemoryMiB > c {
		return fmt.Errorf("sandbox memory %d MiB is above this instance's ceiling of %d MiB (System → Limits)", s.MemoryMiB, c)
	}
	return nil
}

// Effective is the size a project's sandbox gets: each field its own, else
// the default; never above the ceiling (one lowered after the project was
// saved caps it).
func (p SandboxPolicy) Effective(s SandboxSize) SandboxSize {
	pick := func(own, def, ceil int) int {
		v := own
		if v <= 0 {
			v = def
		}
		if ceil > 0 && (v <= 0 || v > ceil) {
			v = ceil
		}
		return v
	}
	return SandboxSize{
		MilliCPU:  pick(s.MilliCPU, p.Default.MilliCPU, p.Ceiling.MilliCPU),
		MemoryMiB: pick(s.MemoryMiB, p.Default.MemoryMiB, p.Ceiling.MemoryMiB),
	}
}
