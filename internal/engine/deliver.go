package engine

// Delivery: attachments → workspace (DESIGN-FILES §5.2). The human's files
// (ticket, run goal, ping answers, the run's chats) are materialized under
// the git-excluded .apothecary/ tree — kept SEPARATE from the agent's own
// artifacts/ output so a cleanup step can't delete human inputs. The other
// direction (workspace → record) lives in capture.go; snapshots have no
// delivery path, so the old "agent snapshot re-delivered as a human file"
// bug is untypeable.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
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
// attachmentsDir: its chat's (takeover/, ask/ — fixed words, no '#', so
// never a ping's reference), its ping's reference (build#3/decision#1 — an
// answer's files, its review comments' among them), or ticketFolder for the
// task's. A ping the run has no reference for keeps its id: never the case
// for a ping the engine recorded.
func attachmentFolder(a filestore.Attachment, pingRef func(pingID string) string) string {
	if a.Chat != "" {
		return a.Chat
	}
	if a.PingID == "" {
		return ticketFolder
	}
	if r := pingRef(a.PingID); r != "" {
		return r
	}
	return a.PingID
}

// attachmentPaths is the repo-relative path each attachment is delivered
// to, in the order given: attachmentsDir/<folder>/<name>. A chat's file is
// delivered as the ChatName its claim gave it, for good. Any other keeps its
// stored name, a second file with the same name in one folder name-2.ext:
// names are given in the order the files were created (then by id), so a
// path never depends on the order a caller passes them in, and a
// re-delivery after a restart gives the same ones. Every caller passes each
// such folder whole: a ping's files, or all of the task's.
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
		if a.Chat != "" {
			out[i] = attachmentsDir + "/" + folder + "/" + a.ChatName
			continue
		}
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
func (x *exec) deliverAttachments(atts []filestore.Attachment) []string {
	paths := attachmentPaths(atts, x.pingRef)
	x.writeAttachments(atts, paths)
	return paths
}

// writeAttachments is writeAttachmentFiles into the run's workspace, logged
// rather than failed: delivery is best-effort, and a restart re-delivers.
func (x *exec) writeAttachments(atts []filestore.Attachment, paths []string) {
	if x.e.attachments == nil || x.rs.EnvID == "" || len(atts) == 0 {
		return
	}
	if err := writeAttachmentFiles(x.ctx, x.res.Envp, x.rs.EnvID, x.e.attachments, atts, paths); err != nil {
		x.e.log.WarnContext(x.bg, "attachment delivery", logs.RunID, x.rs.RunID, "err", err)
	}
}

// writeAttachmentFiles writes atts[i] at paths[i] in a workspace: any subset
// of the run's files, at the paths attachmentPaths gives them. One file that
// cannot be read or written does not stop the others; the error names each.
//
// Files land 0444 read-only: the human's words must not be rewritten in
// place by a prompt-injected agent that then "quotes" them. Re-delivery
// removes the old copy first, since the workspace user cannot overwrite a
// read-only file.
func writeAttachmentFiles(ctx context.Context, envp envport.EnvProvisioner, envID envport.EnvID, store AttachmentStore, atts []filestore.Attachment, paths []string) error {
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
	if _, err := envp.Exec(ctx, envID, envport.Command{
		Script:  "mkdir -p " + strings.Join(mk, " ") + " && rm -f -- " + strings.Join(rm, " "),
		Dir:     "repo",
		Timeout: attachmentDeliverTimeout,
	}); err != nil {
		return fmt.Errorf("attachment mkdir: %w", err)
	}
	var errs []error
	for i, a := range atts {
		data, _, err := store.Bytes(ctx, a.ID)
		if err == nil {
			err = envp.WriteFile(ctx, envID, paths[i], data, 0o444)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("attachment %s: %w", a.ID, err))
		}
	}
	return errors.Join(errs...)
}

// chatFiles resolves ids among a run's files in one of its chats, with the
// path each is delivered to: its ChatName, given when it was claimed, so a
// file keeps the path the agent was first told whatever the chat gets
// later. Ids the chat does not hold are left out.
func chatFiles(ctx context.Context, store AttachmentStore, runID, chat string, ids []string) ([]filestore.Attachment, []string, error) {
	all, err := store.ForRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	var atts []filestore.Attachment
	for _, id := range ids {
		i := slices.IndexFunc(all, func(a filestore.Attachment) bool { return a.ID == id && a.Chat == chat })
		if i >= 0 && !slices.ContainsFunc(atts, func(a filestore.Attachment) bool { return a.ID == id }) {
			atts = append(atts, all[i])
		}
	}
	return atts, attachmentPaths(atts, func(string) string { return "" }), nil
}

// takeOverFiles claims a holder's message's files into the run's take-over
// chat, all or nothing, and delivers them: the paths the message names.
// Only a refused claim fails it; delivery is the run loop's best-effort.
func (x *exec) takeOverFiles(userID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if x.e.attachments == nil {
		return nil, errAttachmentsOff
	}
	if err := x.e.attachments.ClaimForChat(x.ctx, userID, ids, x.rs.RunID, filestore.ChatTakeOver); err != nil {
		return nil, err
	}
	atts, paths := x.takeOverAttachments(ids)
	x.writeAttachments(atts, paths)
	return paths, nil
}

// takeOverFilePaths is where a holder's message's files were delivered:
// they reach the workspace with the run's others (deliverRunAttachments).
func (x *exec) takeOverFilePaths(ids []string) []string {
	if len(ids) == 0 || x.e.attachments == nil {
		return nil
	}
	_, paths := x.takeOverAttachments(ids)
	return paths
}

func (x *exec) takeOverAttachments(ids []string) ([]filestore.Attachment, []string) {
	atts, paths, err := chatFiles(x.ctx, x.e.attachments, x.rs.RunID, filestore.ChatTakeOver, ids)
	if err != nil {
		x.e.log.WarnContext(x.bg, "take-over files", logs.RunID, x.rs.RunID, "err", err)
	}
	return atts, paths
}

// errAttachmentsOff refuses files where this engine keeps none.
var errAttachmentsOff = errors.New("attachments are off on this server")

// DeliverAskFiles claims a question's files into the run's Ask chat, all or
// nothing, and writes them into its workspace (envp, envID: the run's, as
// the Ask pane resolved them), returning the paths the question names. A
// question whose files cannot be delivered is refused, and leaves nothing
// behind: what was written is removed and the claim given back, so the
// files are the asker's to send again.
func (e *Engine) DeliverAskFiles(ctx context.Context, envp envport.EnvProvisioner, envID, runID, userID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if e.attachments == nil {
		return nil, errAttachmentsOff
	}
	ctx, cancel := context.WithTimeout(ctx, attachmentDeliverTimeout)
	defer cancel()
	if err := e.attachments.ClaimForChat(ctx, userID, ids, runID, filestore.ChatAsk); err != nil {
		return nil, err
	}
	atts, paths, err := chatFiles(ctx, e.attachments, runID, filestore.ChatAsk, ids)
	if err == nil {
		err = writeAttachmentFiles(ctx, envp, envport.EnvID(envID), e.attachments, atts, paths)
	}
	if err != nil {
		e.undeliverAskFiles(context.WithoutCancel(ctx), envp, envport.EnvID(envID), runID, userID, ids, paths)
		return nil, fmt.Errorf("delivering the files: %w", err)
	}
	return paths, nil
}

// undeliverAskFiles undoes a refused question's delivery: its files leave
// the workspace and go back to the asker. Best-effort, on a fresh deadline —
// the delivery's may be what ran out; what it cannot undo it logs.
func (e *Engine) undeliverAskFiles(ctx context.Context, envp envport.EnvProvisioner, envID envport.EnvID, runID, userID string, ids, paths []string) {
	ctx, cancel := context.WithTimeout(ctx, attachmentDeliverTimeout)
	defer cancel()
	if len(paths) > 0 {
		rm := make([]string, 0, len(paths))
		for _, p := range paths {
			rm = append(rm, shell.Quote(p))
		}
		if _, err := envp.Exec(ctx, envID, envport.Command{
			Script:  "rm -f -- " + strings.Join(rm, " "),
			Dir:     "repo",
			Timeout: attachmentDeliverTimeout,
		}); err != nil {
			e.log.WarnContext(ctx, "ask files: removing a refused question's files", logs.RunID, runID, "err", err)
		}
	}
	if err := e.attachments.UnclaimForChat(ctx, userID, ids, runID, filestore.ChatAsk); err != nil {
		e.log.WarnContext(ctx, "ask files: giving back a refused question's files", logs.RunID, runID, "err", err)
	}
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
// Answer files are excluded: they arrive with their ping's feedback instead;
// so are the chats', which arrive with their message.
func (x *exec) goalAttachments() []filestore.Attachment {
	var goals []filestore.Attachment
	for _, a := range x.runAndTicketAttachments() {
		if a.PingID == "" && a.Chat == "" {
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

// withFilesNote is a human's words followed by the files they attached; a
// message of files alone is the note alone.
func withFilesNote(text string, paths []string) string {
	return strings.TrimSpace(text + attachedFilesNote(paths))
}
