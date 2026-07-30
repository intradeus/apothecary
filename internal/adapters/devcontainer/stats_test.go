package devcontainer

import (
	"context"
	"testing"
)

// docker stats speaks two unit dialects at once: binary (KiB/MiB) for memory,
// decimal (kB/MB) for net IO. One sample crosses both.
func TestSampleResourcesParsesDockerStats(t *testing.T) {
	statsRow := `{"BlockIO":"4.1MB / 0B","CPUPerc":"12.34%","Container":"apo-x-11","ID":"abc","MemPerc":"1.55%","MemUsage":"121.7MiB / 7.5GiB","Name":"apo-x-11","NetIO":"1.2kB / 648B","PIDs":"25"}` + "\n"
	r := &scriptRunner{responses: []response{
		{stdout: statsRow},
		{stdout: "16\n"}, // docker info NCPU (first sample only — cached after)
		{stdout: statsRow},
	}}
	a := newTestAdapter(t, r)
	s, err := a.SampleResources(context.Background(), "apo-x-11")
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUPerc != 12.34 {
		t.Errorf("CPUPerc = %v, want 12.34", s.CPUPerc)
	}
	if want := int64(127611699); s.MemUsed != want { // 121.7 MiB, rounded
		t.Errorf("MemUsed = %d, want %d", s.MemUsed, want)
	}
	if want := int64(8053063680); s.MemLimit != want { // 7.5 GiB
		t.Errorf("MemLimit = %d, want %d", s.MemLimit, want)
	}
	if s.NetRx != 1200 || s.NetTx != 648 {
		t.Errorf("NetRx/NetTx = %d/%d, want 1200/648", s.NetRx, s.NetTx)
	}
	if s.PIDs != 25 {
		t.Errorf("PIDs = %d, want 25", s.PIDs)
	}
	if !s.At.IsZero() {
		t.Error("At must be zero — the engine stamps it with its own clock")
	}
	if s.CPUs != 16 {
		t.Errorf("CPUs = %d, want 16", s.CPUs)
	}
	// Invariant C: the sandbox lives on the HOST daemon — the stats call must
	// never carry the inner DOCKER_HOST.
	for _, first := range []string{"stats", "info"} {
		if c := find(t, r.calls, "docker", first); envHas(c, "DOCKER_HOST=") {
			t.Errorf("%s call targeted the inner daemon: env %v", first, c.env)
		}
	}

	// The CPU count is cached: the second sample runs stats only, no info.
	s2, err := a.SampleResources(context.Background(), "apo-x-11")
	if err != nil {
		t.Fatal(err)
	}
	if s2.CPUs != 16 {
		t.Errorf("second sample CPUs = %d, want cached 16", s2.CPUs)
	}
	infos := 0
	for _, c := range r.calls {
		if len(c.args) > 0 && c.args[0] == "info" {
			infos++
		}
	}
	if infos != 1 {
		t.Errorf("docker info called %d times, want 1 (cached)", infos)
	}
}

// A container that exists but isn't running prints "--" columns; that is a
// gap (error), never a zero-valued sample.
func TestSampleResourcesNotRunningIsError(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stdout: `{"BlockIO":"--","CPUPerc":"--","MemUsage":"-- / --","NetIO":"--","PIDs":"--"}`},
	}}
	a := newTestAdapter(t, r)
	if _, err := a.SampleResources(context.Background(), "apo-x-11"); err == nil {
		t.Fatal("want error for a not-running container, got a sample")
	}
}

func TestSampleResourcesExitFailureIsError(t *testing.T) {
	r := &scriptRunner{responses: []response{
		{stderr: "No such container: apo-x-11", code: 1},
	}}
	a := newTestAdapter(t, r)
	if _, err := a.SampleResources(context.Background(), "apo-x-11"); err == nil {
		t.Fatal("want error for a missing container, got a sample")
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "0B", want: 0},
		{in: "648B", want: 648},
		{in: "1.2kB", want: 1200},
		{in: "4.1MB", want: 4100000},
		{in: "121.7MiB", want: 127611699},
		{in: "7.5GiB", want: 8053063680},
		{in: " 1.5GB ", want: 1500000000},
		{in: "--", wantErr: true},
		{in: "", wantErr: true},
		{in: "12XB", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.wantErr != (err != nil) {
			t.Errorf("parseSize(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
