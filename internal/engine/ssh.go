package engine

// The engine's side of the SSH door (TODO 46): which workspace a username
// names and whose run it is, how the door reaches that workspace's sidecar
// — the door's key written into the workspace first, then the sidecar's SSH
// socket through the provisioner — and what the run page tells a person
// about connecting. No run event is appended for a session: appending from
// outside the run loop is restricted, and a person's shell is not a step of
// the flow (the door logs it).

import (
	"context"
	"errors"
	"fmt"
	"net"

	"apothecary/internal/ports"
	"apothecary/internal/run"
)

// sshDoorKeyPath is where the door's public key lives in a workspace: the
// sidecar's SSH server admits that key and no other.
const sshDoorKeyPath = "/apothecary/ssh_door.pub"

// SSHTarget is a workspace as the door sees it: whose run, and a dial into
// its sidecar's SSH server.
type SSHTarget struct {
	RunID   string
	OwnerID string
	EnvID   string
	Dial    func(ctx context.Context) (net.Conn, error)
}

// SetSSHDoorKey installs the door's public key, an authorized_keys line.
func (e *Engine) SetSSHDoorKey(authorizedKey string) {
	e.mu.Lock()
	e.sshDoorKey = authorizedKey
	e.mu.Unlock()
}

// SSHTarget resolves a workspace name (a run's env id) to the live run it
// belongs to and a way into its sidecar. The ownership decision is the
// door's; this only says whose the workspace is. A name no live run carries,
// an archived run, and a workspace the engine knows to be stopped or gone
// are ErrNoWorkspace, so the door answers before a dial that cannot
// succeed.
func (e *Engine) SSHTarget(ctx context.Context, workspace string) (SSHTarget, error) {
	v, ok, err := e.runByEnv(ctx, workspace)
	if err != nil {
		return SSHTarget{}, err
	}
	if !ok || v.ProjectID == "" || v.EnvID != workspace || v.State == run.Archived {
		return SSHTarget{}, ErrNoWorkspace
	}
	if st, known := e.EnvStatuses()[v.RunID]; known && (st == ports.EnvStopped || st == ports.EnvAbsent) {
		return SSHTarget{}, ErrNoWorkspace
	}
	e.mu.Lock()
	doorKey := e.sshDoorKey
	e.mu.Unlock()
	projectID := v.ProjectID
	return SSHTarget{RunID: v.RunID, OwnerID: v.UserID, EnvID: workspace, Dial: func(ctx context.Context) (net.Conn, error) {
		if doorKey == "" {
			return nil, errors.New("the engine has no SSH door key")
		}
		res, err := e.resolver.Resolve(ctx, projectID)
		if err != nil {
			return nil, err
		}
		defer res.Close()
		wss, ok := res.Envp.(ports.WorkspaceSSH)
		if !ok {
			return nil, ErrNoSidecar
		}
		env := ports.EnvID(workspace)
		// The door's key goes in right before the dial: a rebuilt container
		// starts without it, and this is the one moment it is needed.
		if err := res.Envp.WriteFile(ctx, env, sshDoorKeyPath, []byte(doorKey), 0o600); err != nil {
			return nil, fmt.Errorf("writing the door key into %s: %w", workspace, err)
		}
		pc, err := wss.SSH(ctx, env)
		if err != nil {
			return nil, err
		}
		return pc.Dial(ctx)
	}}, nil
}

// runByEnv is the live run behind a workspace: the store's index when it
// keeps one (run.EnvLookup), else a scan of the views.
func (e *Engine) runByEnv(ctx context.Context, envID string) (run.View, bool, error) {
	if el, ok := e.store.(run.EnvLookup); ok {
		return el.RunByEnv(ctx, envID)
	}
	views, err := e.views(ctx)
	if err != nil {
		return run.View{}, false, err
	}
	for _, v := range views {
		if v.EnvID == envID {
			return v, true, nil
		}
	}
	return run.View{}, false, nil
}

// SSHInfo is what the run page tells a person about reaching a run's
// workspace: the name to log in as; the folder an editor opens — the
// devcontainer's workspace folder as the sidecar reports it, "" when the
// sidecar cannot say (the connection still works; the editor just asks);
// and the extensions the workspace's own editor runs, for a desktop editor
// to carry over.
type SSHInfo struct {
	Workspace  string
	Path       string
	Extensions []string
}

func (e *Engine) SSHInfo(ctx context.Context, runID string) (SSHInfo, error) {
	v, err := e.runView(ctx, runID)
	if err != nil {
		return SSHInfo{}, err
	}
	if v.ProjectID == "" || v.EnvID == "" || v.State == run.Archived {
		return SSHInfo{}, fmt.Errorf("run %s: %w", runID, ErrNoWorkspace)
	}
	info := SSHInfo{Workspace: v.EnvID}
	if res, err := e.resolver.Resolve(ctx, v.ProjectID); err == nil {
		if starter, ok := res.Envp.(ports.EditorStarter); ok {
			info.Extensions = starter.EditorExtensions(ctx, ports.EnvID(v.EnvID), res.EditorSettings)
		}
		res.Close()
	}
	// Starting the sidecar here is no waste: the connection needs it next.
	client, _, err := e.Workspace(ctx, runID, "")
	if err != nil {
		return info, nil
	}
	if h, err := client.Health(ctx); err == nil {
		info.Path = h.Workspace
		if info.Path == "" {
			info.Path = h.Repo
		}
	}
	return info, nil
}
