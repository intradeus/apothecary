package integrations

import (
	"context"
	"fmt"

	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
)

// projectNeeds is what a project's own settings ask of its sandbox's
// runtime, beyond what the workspace definition does: Docker in workspaces
// is a daemon in the sandbox.
func projectNeeds(w confstore.WorkspaceConfig) envport.RuntimeCapabilities {
	return envport.RuntimeCapabilities{NestedContainers: w.Docker}
}

// offer is what the provisioner integration offers projects; false for a
// kind that offers no choice of runtime.
func (r *Resolver) offer(in confstore.Integration) (envport.RuntimeChoice, bool, error) {
	pk, err := r.reg.Provisioner(in.Kind)
	if err != nil {
		return envport.RuntimeChoice{}, false, err
	}
	o, ok := pk.(kinds.RuntimeOffering)
	if !ok {
		return envport.RuntimeChoice{}, false, nil
	}
	c, err := o.RuntimeChoice(kindConfig(in))
	return c, err == nil, err
}

// chooseRuntime is the runtime a project with workspace w runs under on the
// provisioner integration in: its own pick, else the default, checked
// against what is approved and what it needs. A provisioner without a
// choice takes only the empty pick.
func (r *Resolver) chooseRuntime(in confstore.Integration, w confstore.WorkspaceConfig) (envport.Runtime, error) {
	c, ok, err := r.offer(in)
	if err != nil {
		return "", fmt.Errorf("provisioner %s: %w", in.Name, err)
	}
	if !ok {
		if w.Runtime != "" {
			return "", fmt.Errorf("provisioner %s offers no choice of sandbox runtime: clear the workshop's runtime", in.Name)
		}
		return "", nil
	}
	return c.Choose(w.Runtime, projectNeeds(w))
}

// RuntimeChoice is what the provisioner integration offers workshops (the
// project form's runtime pick); ok is false for a kind without a choice.
func (r *Resolver) RuntimeChoice(ctx context.Context, provisionerID string) (envport.RuntimeChoice, bool, error) {
	in, err := r.integration(ctx, provisionerID, confstore.PortProvisioner)
	if err != nil {
		return envport.RuntimeChoice{}, false, err
	}
	return r.offer(in)
}

// CheckRuntime refuses a workshop whose runtime the provisioner does not
// approve or that cannot give what the workshop needs: what saving one
// checks. The refusal names the fix.
func (r *Resolver) CheckRuntime(ctx context.Context, provisionerID string, w confstore.WorkspaceConfig) error {
	in, err := r.integration(ctx, provisionerID, confstore.PortProvisioner)
	if err != nil {
		return err
	}
	_, err = r.chooseRuntime(in, w)
	return err
}
