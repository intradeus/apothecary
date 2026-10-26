package engine

// Taking the target in (PLAN6 M32/M33). The engine merges (or rebases) the
// subject branch onto its target; the agent never runs git. A conflict
// leaves the operation PAUSED in the working tree and fails the stage with
// the files named: whoever resolves it — the flow's resolver agent, or a
// human — edits those files, and the retried stage checks no marker is left
// and continues the operation itself.

import (
	"fmt"
	"strings"

	"apothecary/internal/actionspec"
	"apothecary/internal/event"
	"apothecary/internal/flow"
)

// actionFail records an outcome failure an agent or a retry can fix and
// routes it (on_fail, else the run is abandoned). Returns routed=true.
func (x *exec) actionFail(st *flow.Stage, reason, msg string) (bool, error) {
	if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: reason, Error: msg}); aerr != nil {
		return true, aerr
	}
	return true, x.takeEdge(st, flow.EdgeFail, msg)
}

// pausedOpScript prints "merge <MERGE_HEAD>", "rebase <onto>" or "none".
const pausedOpScript = `d=$(git rev-parse --git-dir); if [ -f "$d/MERGE_HEAD" ]; then echo "merge $(cat "$d/MERGE_HEAD")"; elif [ -d "$d/rebase-merge" ]; then echo "rebase $(cat "$d/rebase-merge/onto")"; elif [ -d "$d/rebase-apply" ]; then echo "rebase $(cat "$d/rebase-apply/onto")"; else echo none; fi`

// markersScript lists still-unmerged files that still carry conflict
// markers. Only the labelled markers count: a bare ======= is a legitimate
// line in many files.
const markersScript = `files=$(git diff --name-only --diff-filter=U); if [ -n "$files" ]; then printf '%s\n' "$files" | tr '\n' '\0' | xargs -0 grep -lE '^(<<<<<<< |>>>>>>> )' 2>/dev/null; fi; true`

// actionSyncTarget brings the subject's target branch into it.
func (x *exec) actionSyncTarget(st *flow.Stage) (bool, error) {
	co := x.subjectCheckout()
	if co == nil {
		return x.actionFail(st, event.ReasonInputMissing, noSubjectMsg(st))
	}
	strategy := st.Setting("strategy", actionspec.StrategyMerge)
	target := co.Source
	if target == "" {
		target = x.res.RepoRef.DefaultBranch
	}
	git := func(script string) (string, int, error) {
		res, err := x.gitIn(co.Dir, script)
		return strings.TrimSpace(res.Output), res.ExitCode, err
	}
	if out, code, err := git("git fetch origin " + shellQuote(target)); err != nil {
		return x.infraFail(st, "sync: "+err.Error())
	} else if code != 0 {
		return x.actionFail(st, event.ReasonExitNonzero, "fetching "+target+" failed: "+tail(out, 1<<10))
	}
	targetTip, _, err := git("git rev-parse " + shellQuote("origin/"+target))
	if err != nil {
		return x.infraFail(st, "sync: "+err.Error())
	}

	// A previous attempt paused on a conflict: continue it once resolved.
	paused, _, err := git(pausedOpScript)
	if err != nil {
		return x.infraFail(st, "sync: "+err.Error())
	}
	if op, at, _ := strings.Cut(paused, " "); op == "merge" || op == "rebase" {
		if op != strategy || strings.TrimSpace(at) != targetTip {
			// Paused on something else (another strategy, or a target that
			// has moved since): restore the branch exactly and start over.
			if _, _, err := git("git " + op + " --abort"); err != nil {
				return x.infraFail(st, "sync: "+err.Error())
			}
		} else {
			return x.continueSync(st, co.Dir, op, target, targetTip, git)
		}
	}

	if _, code, err := git(fmt.Sprintf("git merge-base --is-ancestor %s HEAD", shellQuote(targetTip))); err != nil {
		return x.infraFail(st, "sync: "+err.Error())
	} else if code == 0 {
		return false, nil // already contains the target: nothing to do
	}
	script := "git merge --no-edit " + shellQuote(targetTip)
	if strategy == actionspec.StrategyRebase {
		script = "git rebase " + shellQuote(targetTip)
	}
	out, code, err := git(script)
	if err != nil {
		return x.infraFail(st, "sync: "+err.Error())
	}
	if code != 0 {
		return x.syncStopped(st, strategy, target, out, git)
	}
	return x.recordSync(st, strategy, targetTip, git)
}

// continueSync finishes a paused merge or rebase whose markers are gone.
func (x *exec) continueSync(st *flow.Stage, dir, op, target, targetTip string, git func(string) (string, int, error)) (bool, error) {
	left, _, err := git(markersScript)
	if err != nil {
		return x.infraFail(st, "sync: "+err.Error())
	}
	if files := strings.Fields(left); len(files) > 0 {
		return x.actionFail(st, event.ReasonExitNonzero, fmt.Sprintf(
			"taking %s in: conflict markers remain in: %s. Resolve them in the working tree (edit the files, run no git), then retry this stage.",
			target, strings.Join(files, ", ")))
	}
	script := "git add -A && git commit --no-edit"
	if op == actionspec.StrategyRebase {
		// --skip when the resolution emptied the commit being replayed.
		script = `git add -A && { GIT_EDITOR=true git rebase --continue || { [ -z "$(git diff --name-only --diff-filter=U)" ] && git diff --cached --quiet && GIT_EDITOR=true git rebase --skip; }; }`
	}
	out, code, err := git(script)
	if err != nil {
		return x.infraFail(st, "sync: "+err.Error())
	}
	if code != 0 {
		// A rebase replays commit by commit: the next one may conflict too.
		return x.syncStopped(st, op, target, out, git)
	}
	if paused, _, _ := git(pausedOpScript); paused != "none" {
		return x.syncStopped(st, op, target, out, git)
	}
	return x.recordSync(st, op, targetTip, git)
}

// syncStopped reports a merge or rebase that stopped — on a conflict,
// normally — leaving it paused for the resolution.
func (x *exec) syncStopped(st *flow.Stage, op, target, output string, git func(string) (string, int, error)) (bool, error) {
	files, _, _ := git("git diff --name-only --diff-filter=U")
	if f := strings.Fields(files); len(f) > 0 {
		return x.actionFail(st, event.ReasonExitNonzero, fmt.Sprintf(
			"taking %s in (%s) conflicts in: %s. The %s is paused in the working tree with conflict markers in those files: resolve them there (edit the files, run no git), then retry this stage to continue it.",
			target, op, strings.Join(f, ", "), op))
	}
	// Not a conflict: restore the branch rather than leave it half-done.
	_, _, _ = git("git " + op + " --abort")
	return x.actionFail(st, event.ReasonExitNonzero, fmt.Sprintf("taking %s in (%s) failed: %s", target, op, tail(output, 1<<10)))
}

func (x *exec) recordSync(st *flow.Stage, strategy, targetTip string, git func(string) (string, int, error)) (bool, error) {
	head, _, err := git("git rev-parse HEAD")
	if err != nil {
		return x.infraFail(st, "sync: "+err.Error())
	}
	if err := x.append(&event.TargetSynced{StageID: st.ID, Subject: x.rs.subject, Strategy: strategy, TargetTip: targetTip, ResultTip: head}); err != nil {
		return true, err
	}
	return false, nil
}

// actionPushBranch pushes the run's branch: the subject's when the chain
// acts on one merge request, every checkout's otherwise. Leased against the
// tip this action last recorded — or, on a subject, the head when the phase
// started —
// so a rebased branch can be force-pushed without ever overwriting a commit
// the run did not see. A first push is a plain push.
func (x *exec) actionPushBranch(st *flow.Stage) (bool, error) {
	rs := x.rs
	cos := rs.checkouts
	if co := x.subjectCheckout(); co != nil {
		cos = []event.Checkout{*co}
	}
	for _, co := range cos {
		if co.Fork {
			return x.actionFail(st, event.ReasonInputInvalid, fmt.Sprintf("%s: the merge request comes from a fork — there is no branch here to push to", co.Name))
		}
		tip, code, err := func() (string, int, error) {
			res, err := x.gitIn(co.Dir, "git rev-parse HEAD")
			return strings.TrimSpace(res.Output), res.ExitCode, err
		}()
		if err != nil {
			return x.infraFail(st, "push: "+err.Error())
		}
		if code != 0 {
			return x.actionFail(st, event.ReasonExitNonzero, fmt.Sprintf("%s: cannot read HEAD: %s", co.Name, tip))
		}
		expected := rs.pushed[co.Name+"\x00"+co.Branch]
		if expected == "" && x.subjectCheckout() != nil {
			expected = rs.phaseTip
		}
		if expected == tip {
			continue // already there (a re-entry after the push landed)
		}
		script := "git push -u origin " + shellQuote("HEAD:refs/heads/"+co.Branch)
		if expected != "" {
			script = fmt.Sprintf("git push -u --force-with-lease=%s:%s origin %s",
				shellQuote("refs/heads/"+co.Branch), shellQuote(expected), shellQuote("HEAD:refs/heads/"+co.Branch))
		}
		res, err := x.gitIn(co.Dir, script)
		if err != nil {
			return x.infraFail(st, "push: "+err.Error())
		}
		if res.ExitCode != 0 {
			return x.actionFail(st, event.ReasonExitNonzero, fmt.Sprintf(
				"pushing %s (%s) was refused — the remote branch holds commits this run did not see, so nothing was overwritten: %s",
				co.Branch, co.Name, tail(res.Output, 1<<10)))
		}
		if err := x.append(&event.BranchPushed{StageID: st.ID, Repo: co.Name, Branch: co.Branch, Tip: tip}); err != nil {
			return true, err
		}
		rs.notePush(co.Name, co.Branch, tip)
	}
	return false, nil
}
