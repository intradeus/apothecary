package engine

// Delivery: attachments → workspace (DESIGN-FILES §5.2). The human's files
// (ticket, run goal, ping answers) are materialized under the git-excluded
// .apothecary/ tree — kept SEPARATE from the agent's own artifacts/ output so
// a cleanup step can't delete human inputs. The other direction (workspace →
// record) lives in capture.go; snapshots have no delivery path, so the old
// "agent snapshot re-delivered as a human file" bug is untypeable.

import (
	"path"
	"sort"
	"strings"
	"time"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/logs"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/envport"
)

const attachmentDeliverTimeout = 30 * time.Second

// attachmentsDir is where the human's files are delivered, in the primary
// checkout.
const attachmentsDir = ".apothecary/attachments"

// ticketFolder holds the task's files: the ticket's, and those a person
// attached to the run's own direction.
const ticketFolder = "ticket"

// attachmentFolder is the folder an attachment is delivered to, under
// attachmentsDir: its ping's reference (build#3/decision#1 — an answer's
// files, its review comments' among them), or ticketFolder for the task's.
// A ping the run has no reference for keeps its id: never the case for a
// ping the engine recorded.
func attachmentFolder(a filestore.Attachment, pingRef func(pingID string) string) string {
	if a.PingID == "" {
		return ticketFolder
	}
	if r := pingRef(a.PingID); r != "" {
		return r
	}
	return a.PingID
}

// attachmentPaths is the repo-relative path each attachment is delivered
// to, in the order given: attachmentsDir/<folder>/<stored name>, a second
// file with the same name in one folder name-2.ext. Names are given in the
// order the files were created (then by id), so a path never depends on the
// order a caller passes them in, and a re-delivery after a restart gives the
// same ones. Every caller passes each folder whole: a ping's files, or all
// of the task's.
func attachmentPaths(atts []filestore.Attachment, pingRef func(pingID string) string) []string {
	order := make([]int, len(atts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := atts[order[i]], atts[order[j]]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	taken := map[string]map[string]bool{}
	out := make([]string, len(atts))
	for _, i := range order {
		a := atts[i]
		folder := attachmentFolder(a, pingRef)
		if taken[folder] == nil {
			taken[folder] = map[string]bool{}
		}
		out[i] = attachmentsDir + "/" + folder + "/" + filestore.UniqueName(filestore.SafeName(a.Filename), taken[folder])
	}
	return out
}

// pingRef is a ping's reference, as the run recorded it ("" for one it has
// not raised).
func (x *exec) pingRef(pingID string) string {
	if p := x.rs.RaisedPings[pingID]; p != nil {
		return p.Ref
	}
	return ""
}

// deliverAttachments writes each attachment into the live workspace
// (best-effort) and returns their repo-relative paths. Paths are
// deterministic from the log and the files (attachmentPaths), so they are
// safe to reference even before the workspace exists (EnvID == ""):
// ensureEnv re-delivers on the ready/recovery path.
//
// Files land 0444 read-only: the human's words must not be rewritten in
// place by a prompt-injected agent that then "quotes" them. Re-delivery
// removes the old copy first, since the workspace user cannot overwrite a
// read-only file.
func (x *exec) deliverAttachments(atts []filestore.Attachment) []string {
	paths := attachmentPaths(atts, x.pingRef)
	if x.e.attachments == nil || x.rs.EnvID == "" || len(atts) == 0 {
		return paths
	}
	dirs := map[string]struct{}{}
	for _, p := range paths {
		dirs[path.Dir(p)] = struct{}{}
	}
	mk := make([]string, 0, len(dirs))
	for d := range dirs {
		mk = append(mk, shell.Quote(d))
	}
	rm := make([]string, 0, len(paths))
	for _, p := range paths {
		rm = append(rm, shell.Quote(p))
	}
	if _, err := x.res.Envp.Exec(x.ctx, x.rs.EnvID, envport.Command{
		Script:  "mkdir -p " + strings.Join(mk, " ") + " && rm -f -- " + strings.Join(rm, " "),
		Dir:     "repo",
		Timeout: attachmentDeliverTimeout,
	}); err != nil {
		x.e.log.WarnContext(x.bg, "attachment mkdir", logs.RunID, x.rs.RunID, "err", err)
		return paths
	}
	for i, a := range atts {
		data, _, err := x.e.attachments.Bytes(x.ctx, a.ID)
		if err != nil {
			x.e.log.WarnContext(x.bg, "attachment read", logs.RunID, x.rs.RunID, "attachment", a.ID, "err", err)
			continue
		}
		if err := x.res.Envp.WriteFile(x.ctx, x.rs.EnvID, paths[i], data, 0o444); err != nil {
			x.e.log.WarnContext(x.bg, "attachment write", logs.RunID, x.rs.RunID, "attachment", a.ID, "err", err)
		}
	}
	return paths
}

// ticketAttachments resolves the parent ticket's attachments by id. They are
// owned by the ticket, so ForRun cannot find them — the ids ride on
// run_created instead. Missing ones are skipped, not fatal.
func (x *exec) ticketAttachments() []filestore.Attachment {
	if x.e.attachments == nil {
		return nil
	}
	var out []filestore.Attachment
	for _, id := range x.rs.View.TicketAttachmentIDs {
		a, err := x.e.attachments.Get(x.ctx, id)
		if err != nil {
			x.e.log.WarnContext(x.bg, "ticket attachment missing", logs.RunID, x.rs.RunID, "attachment", id, "err", err)
			continue
		}
		out = append(out, a)
	}
	return out
}

// runAndTicketAttachments is every file this run should see: its own rows
// (goal + ping answers) plus the ticket's shared attachments, deduped by id.
func (x *exec) runAndTicketAttachments() []filestore.Attachment {
	if x.e.attachments == nil {
		return nil
	}
	atts, err := x.e.attachments.ForRun(x.ctx, x.rs.RunID)
	if err != nil {
		x.e.log.WarnContext(x.bg, "attachment list", logs.RunID, x.rs.RunID, "err", err)
		atts = nil
	}
	seen := make(map[string]struct{}, len(atts))
	for _, a := range atts {
		seen[a.ID] = struct{}{}
	}
	for _, a := range x.ticketAttachments() {
		if _, dup := seen[a.ID]; dup {
			continue
		}
		seen[a.ID] = struct{}{}
		atts = append(atts, a)
	}
	return atts
}

// deliverRunAttachments re-delivers ALL of a run's attachments (its own goal
// + answer files, plus the ticket's shared ones) into the workspace —
// idempotent, called from ensureEnv so a restart re-materializes them.
func (x *exec) deliverRunAttachments() {
	if x.e.attachments == nil || x.rs.EnvID == "" {
		return
	}
	x.deliverAttachments(x.runAndTicketAttachments())
}

// goalAttachments returns the files attached to the TASK — the run's own
// goal files plus the ticket's — for the prompt's ATTACHED FILES section.
// Answer files are excluded: they arrive with their ping's feedback instead.
func (x *exec) goalAttachments() []filestore.Attachment {
	var goals []filestore.Attachment
	for _, a := range x.runAndTicketAttachments() {
		if a.PingID == "" {
			goals = append(goals, a)
		}
	}
	return goals
}

// pingAnswerFiles delivers a ping's answer attachments into the workspace and
// returns where each landed (for the shim tool-return and the resume
// message).
func (x *exec) pingAnswerFiles(pingID string) []answerFile {
	atts := x.pingAnswerAttachments(pingID)
	return answerFilesOf(atts, x.deliverAttachments(atts))
}

// answerFileRefs says where a ping's answer attachments are delivered,
// without writing them: a gate's verdict names them, and the stage that
// reads it gets them with the workspace (deliverRunAttachments).
func (x *exec) answerFileRefs(pingID string) []answerFile {
	atts := x.pingAnswerAttachments(pingID)
	return answerFilesOf(atts, attachmentPaths(atts, x.pingRef))
}

func (x *exec) pingAnswerAttachments(pingID string) []filestore.Attachment {
	if x.e.attachments == nil {
		return nil
	}
	atts, err := x.e.attachments.ForPing(x.ctx, pingID)
	if err != nil {
		x.e.log.WarnContext(x.bg, "answer attachments", logs.RunID, x.rs.RunID, "ping", pingID, "err", err)
		return nil
	}
	return atts
}

func answerFilesOf(atts []filestore.Attachment, paths []string) []answerFile {
	out := make([]answerFile, 0, len(atts))
	for i, a := range atts {
		out = append(out, answerFile{id: a.ID, path: paths[i]})
	}
	return out
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
