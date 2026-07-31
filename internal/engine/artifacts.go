package engine

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"apothecary/internal/artifacts"
	"apothecary/internal/event"
	"apothecary/internal/flow"
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

// --- per-attempt snapshots (PLAN5 M27) --------------------------------------

// publishOutputs captures a stage attempt's declared outputs into the blob
// store and links each one to the run's timeline with an artifact_published
// event. Called on BOTH outcomes: a failed attempt's partial outputs are
// exactly what a human wants when working out why it failed.
//
// With M18's delete-before-attempt, anything present here was written by the
// attempt that just ran — so a snapshot can never be polluted by a stale file
// from an earlier one.
//
// Best-effort throughout: an audit trail must never be able to fail a run.
func (x *exec) publishOutputs(st *flow.Stage, iter int, bindings []flow.IOBinding) {
	if x.e.artifacts == nil {
		return
	}
	for _, b := range bindings {
		if b.Value == "" || flow.IsSentinel(b.Value) {
			continue
		}
		data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, b.Value)
		if err != nil {
			continue // never written: the output contract reports it
		}
		x.publish(st.ID, iter, artifacts.ScopeStageOutput, b.Name, b.Value, data)
	}
}

// publishLog captures what an attempt printed. Published on success too, not
// only on failure: a passing attempt's log is part of the record. The 8 KB
// tails on stage_failed / edge_taken stay what they are — a replay-cheap
// inline preview and the prompt feed — with the whole log now one link away.
func (x *exec) publishLog(stageID string, iter int, output string) {
	if x.e.artifacts == nil || output == "" {
		return
	}
	x.publish(stageID, iter, artifacts.ScopeStageLog, "",
		fmt.Sprintf("%s-%d.log", stageID, iter), []byte(output))
}

// provisionLogStage is the pseudo-stage id the provisioning log is filed
// under — the artifact's StageID, the runs-dir log filename and the
// /logs/{stage} path all use it. The leading underscore keeps it outside the
// flow grammar's stage-id slug (ids must start with a letter), so it can
// never collide with a real stage's logs.
const provisionLogStage = "_provision"

// provisionLogTail bounds how much of the workspace exec log one capture
// reads; Capture tail-truncates further to the store's own limit.
const provisionLogTail = 1 << 20

// publishProvisionLog persists the workspace exec log as it stands when
// provisioning ends: the clone + container build + bootstrap output that
// otherwise lives only in the adapter's exec.log and dies with the workspace
// (Destroy removes the run folder). Deferred from ensureEnv on every exit
// path past its Provision call — env_ready, env_failed and an abort mid-build
// all leave the same durable record a stage attempt gets (runs-dir file +
// stage_log artifact). envName is the deterministic workspace name, which is
// the exec-log key even when Provision itself failed (rs.envID still empty).
func (x *exec) publishProvisionLog(envName string) {
	tailer, ok := x.res.Envp.(ports.ExecLogTailer)
	if !ok {
		return
	}
	env := x.rs.envID
	if env == "" {
		env = ports.EnvID(envName)
	}
	out, err := tailer.TailExecLog(x.bg, env, provisionLogTail)
	if err != nil || len(out) == 0 {
		return
	}
	iter := x.rs.iters[provisionLogStage] + 1
	x.rs.iters[provisionLogStage] = iter
	x.writeStageLog(provisionLogStage, iter, string(out))
	x.publishLog(provisionLogStage, iter, string(out))
}

// publishPingPayload snapshots the workspace files an agent attached to a
// ping, at the moment the ping is raised.
//
// Two things this buys, both of which the old read-it-live path got wrong: the
// attachment survives the workspace (an answered ping stays answerable after
// the reaper), and it is pinned to WHAT THE HUMAN WAS SHOWN — the agent keeps
// working, and a file it rewrites afterwards must not retroactively change the
// document a decision was recorded against.
//
// Best-effort, like every other capture: a ping must be raisable even if the
// snapshot fails, so a missing file only means the reader falls back to the
// live workspace.
func (x *exec) publishPingPayload(p *Ping, stageID string) {
	if x.e.artifacts == nil || x.rs.envID == "" {
		return
	}
	for _, ref := range p.refs() {
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
			continue // a URL payload (mr_url), not a file
		}
		data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, ref)
		if err != nil {
			x.e.log.Warn("snapshotting ping attachment", "run", x.rs.runID, "ping", p.ID, "ref", ref, "err", err)
			continue
		}
		data = []byte(x.redact.scrub(string(data)))
		m, err := x.e.artifacts.Capture(x.bg, artifacts.Meta{
			RunID: x.rs.runID, PingID: p.ID, Scope: artifacts.ScopePingPayload,
			Filename: pathBase(ref), StageID: stageID,
			// OutputName carries the ref VERBATIM: it is how the ping names
			// this attachment, and what the read path matches on (two refs can
			// share a basename).
			OutputName: ref,
		}, data)
		if err != nil {
			x.e.log.Warn("capturing ping attachment", "run", x.rs.runID, "ping", p.ID, "ref", ref, "err", err)
			continue
		}
		if err := x.append(&event.ArtifactPublished{
			StageID: stageID, Scope: artifacts.ScopePingPayload, Name: ref, Path: ref,
			ArtifactID: m.ID, SHA256: m.SHA256, Size: m.Size,
		}); err != nil {
			x.e.log.Warn("recording ping attachment", "run", x.rs.runID, "ping", p.ID, "err", err)
		}
	}
}

// pingSnapshot returns the bytes captured for one of a ping's attachments,
// matched on the ref the ping names it by (OutputName). ok=false means there
// is no snapshot — an old ping, or a capture that failed — and the caller
// falls back to reading the live workspace.
func (e *Engine) pingSnapshot(ctx context.Context, pingID, ref string) ([]byte, bool) {
	if e.artifacts == nil {
		return nil, false
	}
	metas, err := e.artifacts.ForPing(ctx, pingID, artifacts.ScopePingPayload)
	if err != nil {
		e.log.Warn("reading ping snapshots", "ping", pingID, "err", err)
		return nil, false
	}
	for _, m := range metas {
		if m.OutputName != ref {
			continue
		}
		data, _, err := e.artifacts.Bytes(ctx, m.ID)
		if err != nil {
			e.log.Warn("reading ping snapshot bytes", "ping", pingID, "artifact", m.ID, "err", err)
			return nil, false
		}
		return data, true
	}
	return nil, false
}

// PingSnapshots lists the durable snapshots of a ping's attachments (id + the
// ref each was taken from), for the API's attachment list.
func (e *Engine) PingSnapshots(ctx context.Context, pingID string) []artifacts.Meta {
	if e.artifacts == nil {
		return nil
	}
	metas, err := e.artifacts.ForPing(ctx, pingID, artifacts.ScopePingPayload)
	if err != nil {
		e.log.Warn("listing ping snapshots", "ping", pingID, "err", err)
		return nil
	}
	return metas
}

func (x *exec) publish(stageID string, iter int, scope, name, path string, data []byte) {
	// Snapshots outlive the workspace and its security boundary, so they get
	// the same scrub as everything else that leaves it (M21).
	data = []byte(x.redact.scrub(string(data)))
	m, err := x.e.artifacts.Capture(x.bg, artifacts.Meta{
		RunID: x.rs.runID, Scope: scope, Filename: pathBase(path),
		StageID: stageID, Iteration: iter, OutputName: name,
	}, data)
	if err != nil {
		x.e.log.Warn("capturing attempt artifact", "run", x.rs.runID, "stage", stageID, "err", err)
		return
	}
	if err := x.append(&event.ArtifactPublished{
		StageID: stageID, Iteration: iter, Scope: scope, Name: name, Path: path,
		ArtifactID: m.ID, SHA256: m.SHA256, Size: m.Size,
	}); err != nil {
		x.e.log.Warn("recording attempt artifact", "run", x.rs.runID, "stage", stageID, "err", err)
	}
}

// pathBase is the last path element, for the artifact's display filename.
func pathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
