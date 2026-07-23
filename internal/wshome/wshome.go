// Package wshome resolves the home directory of the user a harness actually
// runs as inside a workspace.
//
// Harness-native config — opencode's auth.json and agent markdown, codex's
// config.toml — is only found if it lands in THAT user's home. The §8
// pipeline image runs as `apothecary`, so a hardcoded /home/apothecary held
// for coder and for the devcontainer fallback image; but a repo-authored
// devcontainer runs the REPO's image as the repo's own user (root, vscode,
// node…). Files written to the wrong home are silently invisible: the
// harness starts with no provider configured and no agents, failing later
// and confusingly ("agent planner not found", "Model not found").
package wshome

import (
	"context"
	"strings"

	"apothecary/internal/ports"
)

// Resolve probes $HOME through the SAME exec path the harness itself runs
// under, so the answer is the right user's home by construction. Anything
// unexpected (probe error, non-absolute or whitespace-bearing path) falls
// back to fallback, keeping the pipeline-image behavior unchanged.
func Resolve(ctx context.Context, envp ports.EnvProvisioner, env ports.EnvID, fallback string) string {
	res, err := envp.Exec(ctx, env, ports.Command{Script: `printf %s "$HOME"`})
	if err != nil || res.ExitCode != 0 {
		return fallback
	}
	home := strings.TrimSpace(res.Output)
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home, " \t\r\n") {
		return fallback
	}
	return home
}
