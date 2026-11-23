package integrations

import (
	"context"
	"errors"
	"fmt"

	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
)

// projectNeeds is what a project's own settings ask of its sandbox's
// runtime, beyond what the workspace definition does: Docker in workspaces
// is the sandbox's daemon handed to the workspace, which the deployment's
// daemon door (door: APOTHECARY_WORKSPACE_DOCKER) left on auto allows only
// in a contained sandbox (its daemon is not its node's root); "on" and
// "off" are the operator's word, whatever the runtime.
func projectNeeds(w confstore.WorkspaceConfig, door string) envport.RuntimeCapabilities {
	auto := door != "on" && door != "off"
	return envport.RuntimeCapabilities{NestedContainers: w.Docker, Contained: w.Docker && auto}
}

// errDoorClosed: a workshop asks for Docker where the deployment hands no
// workspace its sandbox's daemon.
var errDoorClosed = errors.New("this deployment hands no workspace its sandbox's docker daemon (APOTHECARY_WORKSPACE_DOCKER=off): turn the workshop's Docker off")

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
	if w.Docker && r.workspaceDocker == "off" {
		return "", errDoorClosed
	}
	need := projectNeeds(w, r.workspaceDocker)
	rt, err := c.Choose(w.Runtime, need)
	if err != nil && need.Contained {
		// Refused for the door alone: say why a contained sandbox is asked.
		if _, plain := c.Choose(w.Runtime, envport.RuntimeCapabilities{NestedContainers: true}); plain == nil {
			err = fmt.Errorf("%w; the workshop's Docker hands its sandbox's docker daemon to the workspace, which this deployment (APOTHECARY_WORKSPACE_DOCKER=auto) allows only in a contained sandbox; or turn the workshop's Docker off", err)
		}
	}
	return rt, err
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
