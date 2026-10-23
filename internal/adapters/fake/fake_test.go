package fake

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"apothecary/internal/ports"
)

func TestEnvLifecycleAndFiles(t *testing.T) {
	ctx := context.Background()
	p := NewEnvProvisioner()

	id, err := p.Provision(ctx, ports.EnvSpec{RunID: "r1", RepoURL: "u", Branch: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Status(ctx, id); st != ports.EnvRunning {
		t.Errorf("status = %s", st)
	}

	if err := p.WriteFile(ctx, id, "artifacts/plan.md", []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := p.ReadFile(ctx, id, "artifacts/plan.md")
	if err != nil || string(data) != "plan" {
		t.Errorf("read = %q, %v", data, err)
	}
	if _, err := p.ReadFile(ctx, id, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file err = %v, want fs.ErrNotExist", err)
	}

	res, err := p.Exec(ctx, id, ports.Command{Script: "make test"})
	if err != nil || res.ExitCode != 0 {
		t.Errorf("exec = %+v, %v", res, err)
	}
	if len(p.ExecCalls) != 1 || p.ExecCalls[0].Cmd.Script != "make test" {
		t.Errorf("exec calls = %+v", p.ExecCalls)
	}

	links, _ := p.Links(ctx, id)
	if len(links) == 0 {
		t.Error("no links")
	}

	if err := p.Destroy(ctx, id); err != nil {
		t.Fatal(err)
	}
	if st, _ := p.Status(ctx, id); st != ports.EnvAbsent {
		t.Errorf("status after destroy = %s", st)
	}
}

func TestProvisionErrsScriptRetries(t *testing.T) {
	p := NewEnvProvisioner()
	p.ProvisionErrs = []error{errors.New("transient")}
	if _, err := p.Provision(context.Background(), ports.EnvSpec{}); err == nil {
		t.Fatal("first provision should fail")
	}
	if _, err := p.Provision(context.Background(), ports.EnvSpec{}); err != nil {
		t.Fatalf("second provision should succeed: %v", err)
	}
}

func TestAgentRunnerScripting(t *testing.T) {
	ctx := context.Background()
	r := &AgentRunner{}

	res, err := r.Run(ctx, "env-1", ports.AgentSpec{Agent: "planner"}, ports.AgentInput{Prompt: "p"})
	if err != nil || res.Session == "" || res.TurnsUsed != 1 {
		t.Errorf("default run = %+v, %v", res, err)
	}

	r.RunFunc = func(_ context.Context, call RunCall) (ports.AgentResult, error) {
		return ports.AgentResult{
			Output:    "reviewed",
			TurnsUsed: 5,
			Usage:     ports.Usage{CostUSD: 0.25},
		}, nil
	}
	res, _ = r.Run(ctx, "env-1", ports.AgentSpec{Agent: "reviewer"}, ports.AgentInput{})
	if res.Output != "reviewed" || res.TurnsUsed != 5 || res.Session == "" {
		t.Errorf("scripted run = %+v", res)
	}

	res, err = r.Resume(ctx, "env-1", res.Session, ports.AgentInput{Prompt: "answer"})
	if err != nil || res.Session == "" {
		t.Errorf("resume = %+v, %v", res, err)
	}
	if len(r.RunCalls) != 2 || len(r.ResumeCalls) != 1 {
		t.Errorf("calls: %d runs, %d resumes", len(r.RunCalls), len(r.ResumeCalls))
	}
}

func TestForge(t *testing.T) {
	ctx := context.Background()
	f := &Forge{}

	if err := f.CheckAccess(ctx, ports.RepoRef{}); err != nil {
		t.Fatal(err)
	}
	ok, _ := f.FileExists(ctx, ports.RepoRef{}, "AGENTS.md")
	if !ok {
		t.Error("files exist by default")
	}
	f.MissingFiles = map[string]bool{".apothecary/agents/": true}
	ok, _ = f.FileExists(ctx, ports.RepoRef{}, ".apothecary/agents/")
	if ok {
		t.Error("missing file reported present")
	}

	ref, err := f.CreateDraftMR(ctx, ports.RepoRef{}, "apothecary/x", "", "Title", "body")
	if err != nil || ref.IID != 1 || ref.URL == "" {
		t.Fatalf("create MR = %+v, %v", ref, err)
	}
	if st, _ := f.MRStatus(ctx, ref); st.State != ports.MROpen {
		t.Errorf("state = %s", st.State)
	}
	f.SetMRState(ref.IID, ports.MRMerged)
	if st, _ := f.MRStatus(ctx, ref); st.State != ports.MRMerged {
		t.Errorf("state after merge = %s", st.State)
	}
}

func TestChannelNeverFailsSilently(t *testing.T) {
	c := &Channel{}
	if err := c.Send(context.Background(), ports.Notification{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if got := c.Sent(); len(got) != 1 || got[0].Title != "t" {
		t.Errorf("sent = %+v", got)
	}
	c.Err = errors.New("matrix down")
	if err := c.Send(context.Background(), ports.Notification{}); err == nil {
		t.Error("scripted error not returned")
	}
}
