package engine

import (
	"path"
	"strings"
	"time"

	"apothecary/internal/artifacts"
	"apothecary/internal/ports"
)

const artifactDeliverTimeout = 30 * time.Second

// artifactPath is the repo-relative destination for a delivered human
// artifact, under the git-excluded .apothecary/ tree — kept SEPARATE from the
// agent's own artifacts/ output so a cleanup step can't delete human inputs.
// The per-artifact <id8> subdir guarantees uniqueness without collision logic.
func (x *exec) artifactPath(m artifacts.Meta) string {
	name := m.Filename
	if name == "" {
		name = "file"
	}
	id8 := m.ID
	if len(id8) > 8 {
		id8 = id8[:8]
	}
	if m.Scope == artifacts.ScopePingAnswer {
		return ".apothecary/artifacts/ping-" + m.PingID + "/" + id8 + "/" + name
	}
	return ".apothecary/artifacts/goal/" + id8 + "/" + name
}

func (x *exec) artifactPaths(metas []artifacts.Meta) []string {
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		out = append(out, x.artifactPath(m))
	}
	return out
}

// deliverArtifacts writes each artifact into the live workspace (best-effort)
// and returns their repo-relative paths. Paths are deterministic from the
// ids, so they are safe to reference even before the workspace exists
// (envID == ""): ensureEnv re-delivers on the ready/recovery path.
func (x *exec) deliverArtifacts(metas []artifacts.Meta) []string {
	paths := x.artifactPaths(metas)
	if x.e.artifacts == nil || x.rs.envID == "" || len(metas) == 0 {
		return paths
	}
	dirs := map[string]struct{}{}
	for _, p := range paths {
		dirs[path.Dir(p)] = struct{}{}
	}
	quoted := make([]string, 0, len(dirs))
	for d := range dirs {
		quoted = append(quoted, shellQuote(d))
	}
	if _, err := x.res.Envp.Exec(x.ctx, x.rs.envID, ports.Command{
		Script: "mkdir -p " + strings.Join(quoted, " "), Dir: "repo", Timeout: artifactDeliverTimeout,
	}); err != nil {
		x.e.log.Warn("artifact mkdir", "run", x.rs.runID, "err", err)
		return paths
	}
	for i, m := range metas {
		data, _, err := x.e.artifacts.Bytes(x.ctx, m.ID)
		if err != nil {
			x.e.log.Warn("artifact read", "run", x.rs.runID, "artifact", m.ID, "err", err)
			continue
		}
		if err := x.res.Envp.WriteFile(x.ctx, x.rs.envID, paths[i], data, 0o644); err != nil {
			x.e.log.Warn("artifact write", "run", x.rs.runID, "artifact", m.ID, "err", err)
		}
	}
	return paths
}

// ticketArtifacts resolves the parent ticket's attachments by id. They are
// owned by the ticket (run_id empty), so ForRun cannot find them — the ids
// ride on run_created instead. Missing ones are skipped, not fatal.
func (x *exec) ticketArtifacts() []artifacts.Meta {
	if x.e.artifacts == nil {
		return nil
	}
	var out []artifacts.Meta
	for _, id := range x.rs.view.TicketArtifactIDs {
		m, err := x.e.artifacts.Meta(x.ctx, id)
		if err != nil {
			x.e.log.Warn("ticket artifact missing", "run", x.rs.runID, "artifact", id, "err", err)
			continue
		}
		out = append(out, m)
	}
	return out
}

// runAndTicketArtifacts is every artifact this run should see: its own rows
// (goal + ping-answer) plus the ticket's shared attachments, deduped by id.
func (x *exec) runAndTicketArtifacts() []artifacts.Meta {
	if x.e.artifacts == nil {
		return nil
	}
	metas, err := x.e.artifacts.ForRun(x.ctx, x.rs.runID)
	if err != nil {
		x.e.log.Warn("artifact list", "run", x.rs.runID, "err", err)
		metas = nil
	}
	seen := make(map[string]struct{}, len(metas))
	for _, m := range metas {
		seen[m.ID] = struct{}{}
	}
	for _, m := range x.ticketArtifacts() {
		if _, dup := seen[m.ID]; dup {
			continue
		}
		seen[m.ID] = struct{}{}
		metas = append(metas, m)
	}
	return metas
}

// deliverRunArtifacts re-delivers ALL of a run's artifacts (its own goal +
// ping-answer rows, plus the ticket's shared attachments) into the workspace
// — idempotent, called from ensureEnv so a restart re-materializes them.
func (x *exec) deliverRunArtifacts() {
	if x.e.artifacts == nil || x.rs.envID == "" {
		return
	}
	x.deliverArtifacts(x.runAndTicketArtifacts())
}

// goalArtifacts returns the attachments the human made to the task — the
// run's own plus the ticket's — for the prompt listing.
func (x *exec) goalArtifacts() []artifacts.Meta {
	var goals []artifacts.Meta
	for _, m := range x.runAndTicketArtifacts() {
		if m.Scope == artifacts.ScopeGoal || m.Scope == artifacts.ScopeTicketGoal {
			goals = append(goals, m)
		}
	}
	return goals
}

// pingAnswerPaths delivers a ping's answer attachments into the workspace and
// returns their paths (for the shim tool-return and the gate-reject feed).
func (x *exec) pingAnswerPaths(pingID string) []string {
	if x.e.artifacts == nil {
		return nil
	}
	metas, err := x.e.artifacts.ForPing(x.ctx, pingID, artifacts.ScopePingAnswer)
	if err != nil || len(metas) == 0 {
		return nil
	}
	return x.deliverArtifacts(metas)
}

// attachedFilesNote renders paths as a markdown block for feedback delivery.
func attachedFilesNote(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThe human attached these files — read them with your tools:\n")
	for _, p := range paths {
		b.WriteString("- " + p + "\n")
	}
	return b.String()
}
