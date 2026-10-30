package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"apothecary/internal/bootstrap"
	"apothecary/internal/event"
	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
	"apothecary/internal/ports/repoport"
	"apothecary/internal/shell"
)

// actionCreateMR opens the run's draft merge requests: one per checkout that
// has a repository host port and changes to publish — the primary always, an
// extra repo when its branch is ahead of its source. Each targets the branch
// the checkout forked from, so a run started off another run's branch stacks
// on it. Idempotent via rs.MRs, rehydrated from mr_created on replay:
// re-entering after a crash never opens a second MR for a branch. The files
// of its attachments port are uploaded to each merge request's repository
// host just before it opens, and the body links them (attach.go).
//
// The narrow window the events alone cannot cover is a crash BETWEEN a
// repository host call returning and mr_created being appended — the MR
// exists, the run has no record of it, and re-running would open a duplicate.
// The action_started intent marker makes that window visible, and the FindMR
// reconciliation closes it by asking the repository host what the branch
// already has (PLAN5 M20). A reconciliation that cannot reach the repository
// host fails the stage rather than guessing: a duplicate MR is worse than a
// retry. mrHead is the merge request's head commit as the repository host
// reports it now, or "" when it cannot be asked (best effort: it only keeps
// the run from taking its own commit for someone else's push).
func (x *exec) mrHead(repoHost repoport.RepoHost, ref repoport.MRRef) string {
	st, err := repoHost.MRStatus(x.ctx, ref)
	if err != nil {
		x.e.log.Warn("could not read the new merge request's head", "run", x.rs.RunID, "mr", ref.URL, "err", err)
		return ""
	}
	return st.HeadSHA
}

func (x *exec) actionCreateMR(st *flow.Stage) (bool, error) {
	rs := x.rs
	// Which checkouts still need a merge request — reconciling first, before
	// anything is read or created: after a crash in the record window the
	// repository host may already have it.
	// In a phase opened for one merge request, only its checkout; otherwise
	// every checkout of the run.
	checkouts := rs.Checkouts
	if co := x.rs.SubjectCheckout(); co != nil {
		checkouts = []event.Checkout{*co}
	}
	var todo []event.Checkout
	for _, co := range checkouts {
		repoHost := x.res.RepoHostFor(co.Name)
		if repoHost == nil {
			continue // context-only extra repo
		}
		if rs.MRFor(co.Name, co.Branch) != nil {
			continue // already open
		}
		if !co.Primary {
			ahead, err := x.checkoutAhead(co)
			if err != nil {
				return x.infraFail(st, fmt.Sprintf("repo %s: %v", co.Name, err))
			}
			if !ahead {
				x.e.log.Info("no changes in extra repo; no merge request", "run", rs.RunID, "repo", co.Name)
				continue
			}
		}
		// Adopt what the repository host already has — after a crash in the
		// record window, or a merge request a person opened for the branch —
		// rather than open a second one.
		{
			ref, found, ferr := repoHost.FindMR(x.ctx, x.repoRefFor(co), co.Branch, co.Source)
			if ferr != nil {
				return x.infraFail(st, "cannot tell whether the merge request was already created: "+ferr.Error())
			}
			if found {
				x.e.log.Info("adopted an existing merge request", "run", rs.RunID, "stage", st.ID, "repo", co.Name, "mr", ref.URL)
				created := &event.MRCreated{Repo: co.Name, Branch: co.Branch, Target: co.Source, MRURL: ref.URL, MRIID: ref.IID,
					Head: x.mrHead(repoHost, ref)}
				if err := x.append(created); err != nil {
					return true, err
				}
				continue
			}
		}
		todo = append(todo, co)
	}
	if len(todo) == 0 {
		return false, nil
	}

	bodyPath := inputPath(st, "body")
	body, err := x.res.Envp.ReadFile(x.ctx, rs.EnvID, bodyPath)
	if err != nil {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInputMissing,
			Error: fmt.Sprintf("MR body %s not readable: %v", bodyPath, err)}); aerr != nil {
			return true, aerr
		}
		return true, x.abandon(st.ID, "", fmt.Sprintf("MR body artifact %s missing", bodyPath))
	}
	title := mrTitle(string(body), rs.View.TicketTitle)
	files, routed, err := x.attachmentFiles(st)
	if routed || err != nil {
		return true, err
	}
	if !rs.ActionPending[st.ID] {
		// Record the intent BEFORE the effect: if the process dies past this
		// point the next boot knows an MR may exist and reconciles instead of
		// creating a second one.
		if err := x.append(&event.ActionStarted{StageID: st.ID, Action: st.Action}); err != nil {
			return true, err
		}
	}
	for _, co := range todo {
		repoHost := x.res.RepoHostFor(co.Name)
		// Uploaded to each merge request's own repository: an upload is
		// hosted by, and shown in, the repository it went to.
		mrBody := string(body)
		if len(files) > 0 {
			repo := x.repoRefFor(co)
			mrBody, err = x.attach(mrBody, files, func(name, ct string, data []byte) (repoport.Attachment, error) {
				return repoHost.UploadAttachment(x.ctx, repo, name, ct, data)
			})
			if err != nil {
				if x.ctx.Err() != nil {
					return true, context.Cause(x.ctx)
				}
				return x.actionFail(st, event.ReasonInfra, "attaching files to the merge request failed: "+err.Error())
			}
		}
		ref, err := repoHost.CreateDraftMR(x.ctx, x.repoRefFor(co), co.Branch, co.Source, title, mrBody)
		if err != nil {
			return x.infraFail(st, "opening the merge request failed: "+err.Error())
		}
		// Labels are how the merge request is filed, not part of the work:
		// one the host refuses is logged, and the merge request stands.
		if labels := st.SettingList("labels"); len(labels) > 0 {
			if err := repoHost.AddMRLabels(x.ctx, ref, labels); err != nil {
				x.e.log.Warn("labelling the merge request", "run", rs.RunID, "mr", ref.URL, "labels", labels, "err", err)
			}
		}
		// mr_created is what makes the $MR_URL sentinel and the merge signals
		// work downstream, and what makes this action idempotent.
		created := &event.MRCreated{Repo: co.Name, Branch: co.Branch, Target: co.Source, MRURL: ref.URL, MRIID: ref.IID,
			Head: x.mrHead(repoHost, ref)}
		if err := x.append(created); err != nil {
			return true, err
		}
	}
	return false, nil
}

// infraFail is a failure retrying cannot fix without a human — the repository
// host unreachable, the workspace gone: terminal, as create_mr's repository
// host errors always were. Returns routed=true.
func (x *exec) infraFail(st *flow.Stage, msg string) (bool, error) {
	if x.ctx.Err() != nil {
		return true, context.Cause(x.ctx)
	}
	if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra, Error: msg}); aerr != nil {
		return true, aerr
	}
	return true, x.fail(msg)
}

// gitIn runs one git script in a checkout directory of the workspace.
func (x *exec) gitIn(dir, script string) (envport.ExecResult, error) {
	return x.res.Envp.Exec(x.ctx, x.rs.EnvID, envport.Command{Script: script, Dir: dir, Timeout: x.rs.Flow.Defaults.Timeout})
}

// repoRefFor is the repository host reference of a checkout: the project's for
// the primary, the extra repo's own for the rest.
func (x *exec) repoRefFor(co event.Checkout) repoport.RepoRef {
	if co.Primary {
		return x.res.RepoRef
	}
	ref := repoport.RepoRef{URL: co.URL, Locator: co.Locator, DefaultBranch: co.Source}
	for _, xr := range x.res.ExtraRepos {
		if xr.Name == co.Name {
			ref.DefaultBranch = xr.DefaultBranch
		}
	}
	return ref
}

// checkoutAhead reports whether a checkout's branch has commits its source
// does not — i.e. whether the run changed that repository.
func (x *exec) checkoutAhead(co event.Checkout) (bool, error) {
	res, err := x.gitIn(co.Dir, fmt.Sprintf("git rev-list --count origin/%s..HEAD", co.Source))
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("git rev-list: %s", shell.Tail(res.Output, 512))
	}
	n, err := strconv.Atoi(strings.TrimSpace(res.Output))
	if err != nil {
		return false, fmt.Errorf("git rev-list: unexpected output %q", shell.Tail(res.Output, 128))
	}
	return n > 0, nil
}

// mrListMarkdown lists the run's merge requests for a gate's context, in
// review order.
func (x *exec) mrListMarkdown() string {
	rs := x.rs
	if len(rs.MRs) <= 1 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Merge requests to review, in order:\n")
	for i, mr := range rs.MRs {
		label := mr.Branch
		if mr.Repo != event.PrimaryCheckoutName {
			label = mr.Repo + ": " + mr.Branch
		}
		fmt.Fprintf(&b, "%d. %s → `%s`: %s\n", i+1, label, mr.Target, mr.Ref.URL)
	}
	return b.String()
}

// extraCheckouts is what bootstrap clones beside the primary.
func (x *exec) extraCheckouts() []bootstrap.ExtraCheckout {
	var out []bootstrap.ExtraCheckout
	for _, co := range x.rs.Checkouts {
		if co.Primary {
			continue
		}
		out = append(out, bootstrap.ExtraCheckout{Name: co.Name, Dir: co.Dir, URL: co.URL, Branch: co.Branch, Source: co.Source, Fetch: co.Fetch})
	}
	return out
}

// primaryFetch is the ref the primary's branch starts from when the run was
// created on a merge request in it ("" = it forks its source).
func (x *exec) primaryFetch() string {
	for _, co := range x.rs.Checkouts {
		if co.Primary {
			return co.Fetch
		}
	}
	return ""
}
