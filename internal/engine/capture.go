package engine

// Capture: workspace → record (DESIGN-FILES §5.2). Everything the engine
// keeps from a workspace crosses HERE — scrubbed by construction (snapshots
// outlive the workspace security boundary), captured into the filestore, and
// linked to the run's timeline with a snapshot_captured event that carries
// everything a projection rebuild needs.
//
// Best-effort throughout: an audit trail must never be able to fail a run.
// Capture is part of RECORDING (unconditional, at attempt end); the output
// contract check is part of ROUTING (conditional, after) — a failed attempt's
// partial outputs are exactly what the human debugging it wants (§4).

import (
	"fmt"
	"path"

	"apothecary/internal/agentspec"
	"apothecary/internal/event"
	"apothecary/internal/filestore"
	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
)

// capture is the one funnel: scrub → store → snapshot_captured. Both failure
// modes are warn-only.
func (x *exec) capture(snap filestore.Snapshot, data []byte) {
	if x.e.snapshots == nil {
		return
	}
	snap.RunID = x.rs.runID
	stored, err := x.e.snapshots.Capture(x.bg, snap, x.redact.Scrub, data)
	if err != nil {
		x.e.log.Warn("capturing snapshot", "run", x.rs.runID, "stage", snap.StageID, "kind", snap.Kind, "err", err)
		return
	}
	if err := x.append(&event.SnapshotCaptured{
		StageID:    stored.StageID,
		Iteration:  stored.Iteration,
		Kind:       string(stored.Kind),
		Name:       stored.Name,
		Path:       stored.Path,
		PingID:     stored.PingID,
		SnapshotID: stored.ID,
		Filename:   stored.Filename,
		Mime:       stored.Mime,
		SHA256:     stored.SHA256,
		Size:       stored.Size,
		Truncated:  stored.Truncated,
	}); err != nil {
		x.e.log.Warn("recording snapshot", "run", x.rs.runID, "stage", snap.StageID, "err", err)
	}
}

// publishOutputs captures a stage attempt's declared outputs. Called on BOTH
// outcomes — with M18's delete-before-attempt, anything present here was
// written by the attempt that just ran, so a snapshot can never be polluted
// by a stale file from an earlier one.
func (x *exec) publishOutputs(st *flow.Stage, iter int, bindings []flow.Port) {
	if x.e.snapshots == nil {
		return
	}
	for _, b := range bindings {
		if b.Value == "" || flow.IsEngineSource(b.Value) {
			continue
		}
		if b.Type == agentspec.TypeFiles {
			x.publishDir(st, iter, b)
			continue
		}
		data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, b.Value)
		if err != nil {
			continue // never written: the output contract reports it
		}
		x.capture(filestore.Snapshot{
			Kind: filestore.KindOutput, StageID: st.ID, Iteration: iter,
			Name: b.Name, Path: b.Value, Filename: path.Base(b.Value),
		}, data)
	}
}

// publishDir captures each file a files output holds, as one output snapshot
// per file under the port's name — the first attachmentMaxFiles of them, and
// none past attachmentMaxBytes: the store tail-truncates what is too big,
// which keeps a log readable and ruins an image, so a file that large is
// left out (and logged) rather than kept broken.
func (x *exec) publishDir(st *flow.Stage, iter int, b flow.Port) {
	files, err := x.dirFiles(b.Value)
	if err != nil {
		x.e.log.Warn("listing a files output", "run", x.rs.runID, "stage", st.ID, "path", b.Value, "err", err)
	}
	for i, f := range files {
		if i == attachmentMaxFiles {
			x.e.log.Warn("files output holds more files than are captured", "run", x.rs.runID, "stage", st.ID,
				"path", b.Value, "files", len(files), "captured", attachmentMaxFiles)
			break
		}
		if f.Size > attachmentMaxBytes {
			x.e.log.Warn("file too large to capture", "run", x.rs.runID, "stage", st.ID, "path", f.Path, "size", f.Size)
			continue
		}
		data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, f.Path)
		if err != nil {
			continue
		}
		x.capture(filestore.Snapshot{
			Kind: filestore.KindOutput, StageID: st.ID, Iteration: iter,
			Name: b.Name, Path: f.Path, Filename: path.Base(f.Path),
		}, data)
	}
}

// publishLog captures what an attempt printed. Published on success too, not
// only on failure: a passing attempt's log is part of the record. The 8 KB
// tails on stage_failed / edge_taken stay what they are — a replay-cheap
// inline preview and the prompt feed — with the whole log one link away.
func (x *exec) publishLog(stageID string, iter int, output string) {
	if x.e.snapshots == nil || output == "" {
		return
	}
	x.capture(filestore.Snapshot{
		Kind: filestore.KindLog, StageID: stageID, Iteration: iter,
		Filename: fmt.Sprintf("%s-%d.log", stageID, iter),
	}, []byte(output))
}

// provisionLogStage is the pseudo-stage id the provisioning log is filed
// under — the snapshot's StageID, the runs-dir log filename and the
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
// all leave the same durable record a stage attempt gets. envName is the
// deterministic workspace name, which is the exec-log key even when Provision
// itself failed (rs.envID still empty).
func (x *exec) publishProvisionLog(envName string) {
	tailer, ok := x.res.Envp.(envport.ExecLogTailer)
	if !ok {
		return
	}
	env := x.rs.envID
	if env == "" {
		env = envport.EnvID(envName)
	}
	out, err := tailer.TailExecLog(x.bg, env, provisionLogTail)
	if err != nil || len(out) == 0 {
		return
	}
	iter := x.rs.iters[provisionLogStage] + 1
	x.rs.iters[provisionLogStage] = iter
	x.publishLog(provisionLogStage, iter, string(out))
}

// publishPingFiles snapshots the workspace files attached to a ping, at the
// moment the ping is raised. Two things this buys: the attachment survives
// the workspace (an answered ping stays answerable after the reaper), and it
// is pinned to WHAT THE HUMAN WAS SHOWN — the agent resumes the moment the
// ping is answered and must not retroactively change the document a decision
// was recorded against.
func (x *exec) publishPingFiles(p *Ping, stageID string) {
	if x.e.snapshots == nil || x.rs.envID == "" {
		return
	}
	for _, ref := range p.Files {
		data, err := x.res.Envp.ReadFile(x.ctx, x.rs.envID, ref)
		if err != nil {
			x.e.log.Warn("snapshotting ping file", "run", x.rs.runID, "ping", p.ID, "ref", ref, "err", err)
			continue
		}
		x.capture(filestore.Snapshot{
			Kind: filestore.KindPingFile, StageID: stageID, PingID: p.ID,
			Path: ref, Filename: path.Base(ref),
		}, data)
	}
}
