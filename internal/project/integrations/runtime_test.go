package integrations

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
)

// offeringProvKind offers sysbox (its default) and privileged, unless its
// config says "broken"; every runtime needs nested containers.
type offeringProvKind struct{ countingProvKind }

func (offeringProvKind) Descriptor() kinds.Descriptor {
	return kinds.Descriptor{Port: "provisioner", Kind: "offering", Label: "offering"}
}

func (offeringProvKind) RuntimeChoice(cfg kinds.Config) (envport.RuntimeChoice, error) {
	if cfg.Str("broken", "") != "" {
		return envport.RuntimeChoice{}, errors.New("runtimes: unknown sandbox runtime \"gvisor\"")
	}
	approved := []envport.Runtime{envport.RuntimeSysbox}
	if cfg.Str("privileged", "") != "" {
		approved = append(approved, envport.RuntimePrivileged)
	}
	return envport.RuntimeChoice{Default: envport.RuntimeSysbox, Approved: approved,
		Needs: envport.RuntimeCapabilities{NestedContainers: true}}, nil
}

func offering(t *testing.T, values map[string]any) (*Resolver, *confstore.Service, confstore.Project) {
	t.Helper()
	r, svc, p, _, _ := setup(t)
	r.reg.RegisterProvisioner(offeringProvKind{countingProvKind{made: &atomic.Int32{}, closed: &atomic.Int32{}}})
	ctx := context.Background()
	in, err := svc.CreateIntegration(ctx, confstore.Integration{Port: confstore.PortProvisioner, Kind: "offering", Name: "o", Config: values})
	if err != nil {
		t.Fatal(err)
	}
	p.ProvisionerID = in.ID
	if p, err = svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	return r, svc, p
}

func resolveRuntime(t *testing.T, r *Resolver, id string) (envport.Runtime, error) {
	t.Helper()
	res, err := r.Resolve(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Release()
	return res.SandboxRuntime()
}

// A project's runtime is its own pick among the approved, else the default;
// one no longer approved resolves (everything else goes on working) but
// refuses a new sandbox, naming what is approved.
func TestResolveChoosesTheRuntime(t *testing.T) {
	r, svc, p := offering(t, map[string]any{"privileged": "on"})
	ctx := context.Background()
	if rt, err := resolveRuntime(t, r, p.ID); err != nil || rt != envport.RuntimeSysbox {
		t.Errorf("unset = %s, %v; want the default", rt, err)
	}
	p.Workspace.Runtime = envport.RuntimePrivileged
	if _, err := svc.UpdateProject(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	if rt, err := resolveRuntime(t, r, p.ID); err != nil || rt != envport.RuntimePrivileged {
		t.Errorf("picked = %s, %v", rt, err)
	}
	// The admin withdraws privileged.
	in, _ := svc.GetIntegration(ctx, p.ProvisionerID)
	if _, err := svc.UpdateIntegration(ctx, in.ID, in.Name, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRuntime(t, r, p.ID); err == nil || !strings.Contains(err.Error(), "not approved") || !strings.Contains(err.Error(), "approved: sysbox") {
		t.Errorf("withdrawn: %v", err)
	}
}

// The save check: an unapproved pick, a provisioner config that cannot be
// read, and a pick on a provisioner without a choice are refused; the
// default and an approved pick pass.
func TestCheckRuntime(t *testing.T) {
	r, _, p := offering(t, nil)
	ctx := context.Background()
	if err := r.CheckRuntime(ctx, p.ProvisionerID, confstore.WorkspaceConfig{Docker: true}); err != nil {
		t.Errorf("default: %v", err)
	}
	if err := r.CheckRuntime(ctx, p.ProvisionerID, confstore.WorkspaceConfig{Runtime: envport.RuntimeSysbox}); err != nil {
		t.Errorf("approved: %v", err)
	}
	if err := r.CheckRuntime(ctx, p.ProvisionerID, confstore.WorkspaceConfig{Runtime: envport.RuntimePrivileged}); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("unapproved: %v", err)
	}
	if err := r.CheckRuntime(ctx, "nope", confstore.WorkspaceConfig{}); err == nil {
		t.Error("an unknown provisioner passed")
	}

	broken, _, bp := offering(t, map[string]any{"broken": "yes"})
	if err := broken.CheckRuntime(ctx, bp.ProvisionerID, confstore.WorkspaceConfig{}); err == nil || !strings.Contains(err.Error(), "provisioner o:") {
		t.Errorf("broken offer: %v", err)
	}
	if _, err := resolveRuntime(t, broken, bp.ID); err == nil {
		t.Error("a broken offer resolved a runtime")
	}

	plain, _, pp, _, _ := setup(t)
	if err := plain.CheckRuntime(ctx, pp.ProvisionerID, confstore.WorkspaceConfig{}); err != nil {
		t.Errorf("no choice, none picked: %v", err)
	}
	if err := plain.CheckRuntime(ctx, pp.ProvisionerID, confstore.WorkspaceConfig{Runtime: envport.RuntimeSysbox}); err == nil || !strings.Contains(err.Error(), "offers no choice") {
		t.Errorf("no choice, one picked: %v", err)
	}
	if rt, err := resolveRuntime(t, plain, pp.ID); err != nil || rt != "" {
		t.Errorf("no choice resolves %q, %v", rt, err)
	}
}

func TestRuntimeChoiceOffered(t *testing.T) {
	r, _, p := offering(t, map[string]any{"privileged": "on"})
	ctx := context.Background()
	c, ok, err := r.RuntimeChoice(ctx, p.ProvisionerID)
	if err != nil || !ok || c.Default != envport.RuntimeSysbox || len(c.Approved) != 2 {
		t.Errorf("offer = %+v, %v, %v", c, ok, err)
	}
	plain, _, pp, _, _ := setup(t)
	if _, ok, err := plain.RuntimeChoice(ctx, pp.ProvisionerID); ok || err != nil {
		t.Errorf("no choice: %v, %v", ok, err)
	}
	if _, _, err := plain.RuntimeChoice(ctx, "nope"); err == nil {
		t.Error("an unknown provisioner offered")
	}
}

func TestProjectNeeds(t *testing.T) {
	if n := projectNeeds(confstore.WorkspaceConfig{Docker: true}); !n.NestedContainers {
		t.Errorf("docker = %+v", n)
	}
	if n := projectNeeds(confstore.WorkspaceConfig{}); n != (envport.RuntimeCapabilities{}) {
		t.Errorf("nothing = %+v", n)
	}
}
