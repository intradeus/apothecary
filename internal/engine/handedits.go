package engine

// Hand edits (the plan's D16): at the debug leaf a person has the workspace,
// and what they changed there is part of the run's record — the run journal
// shows it beside their summary. The engine records which files, never the
// diff: a fingerprint of each checkout's working copy as the run parks, one
// as they continue, and the paths that differ. Taken without touching the
// person's files, index or stash: a temporary index stages everything
// (uncommitted and new files; git-ignored and git-excluded ones —
// artifacts/, .apothecary/ — left out) and writes it as a tree. Best effort:
// a workspace recreated while parked, or git failing, gives no list, and the
// record says so.

import (
	"regexp"
	"strings"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
)

// handEditsTimeout bounds each git call hand edits make, far below a
// stage's timeout: the park's runs before the debug ping is raised, in a
// workspace that may be what broke, and a person must never wait on a hung
// git to learn the run needs them. One that runs out reads as not known.
const handEditsTimeout = 30 * time.Second

// gitBrief runs a hand-edits git script in a checkout under
// handEditsTimeout.
func (x *exec) gitBrief(dir, script string) (envport.ExecResult, error) {
	return x.res.Envp.Exec(x.ctx, x.rs.EnvID, envport.Command{Script: script, Dir: dir, Timeout: handEditsTimeout, Scrub: x.redact.Scrub})
}

// workTreeScript prints a checkout's working copy as a git tree, from a copy
// of its index so the real one is never written.
const workTreeScript = `set -e
tmp=$(mktemp -u)
trap 'rm -f "$tmp"' EXIT
idx=$(git rev-parse --git-path index)
if [ -f "$idx" ]; then cp "$idx" "$tmp"; fi
export GIT_INDEX_FILE="$tmp"
git add -A >/dev/null 2>&1
git write-tree`

var treeRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// runCheckouts is the run's checkouts, or the primary alone for a run that
// records none.
func (x *exec) runCheckouts() []event.Checkout {
	if len(x.rs.Checkouts) > 0 {
		return x.rs.Checkouts
	}
	return []event.Checkout{{Name: event.PrimaryCheckoutName, Dir: event.PrimaryCheckoutDir, Primary: true}}
}

// workTrees is each checkout's working copy as a tree, by checkout name; a
// checkout whose tree cannot be taken is left out, and with no workspace
// there is none.
func (x *exec) workTrees() map[string]string {
	if x.rs.EnvID == "" {
		return nil
	}
	out := map[string]string{}
	for _, co := range x.runCheckouts() {
		res, err := x.gitBrief(co.Dir, workTreeScript)
		if err != nil || res.ExitCode != 0 {
			continue
		}
		lines := strings.Split(strings.TrimSpace(res.Output), "\n")
		if t := strings.TrimSpace(lines[len(lines)-1]); treeRe.MatchString(t) {
			out[co.Name] = t
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// handEdits compares each checkout's working copy now with the one the
// debug ping recorded as the run parked.
func (x *exec) handEdits(st *flow.Stage, pingID string) *event.HandEdits {
	var parked map[string]string
	if r := x.rs.RaisedPings[pingID]; r != nil {
		parked = r.Trees
	}
	now := x.workTrees()
	rec := &event.HandEdits{PingID: pingID, StageID: st.ID}
	for _, co := range x.runCheckouts() {
		ed := event.CheckoutEdits{Checkout: co.Name}
		before, after := parked[co.Name], now[co.Name]
		switch {
		case !treeRe.MatchString(before):
			ed.Unknown = "its working copy was not recorded as the run parked"
		case after == "":
			ed.Unknown = "its working copy could not be read at the continue"
		case before != after:
			res, err := x.gitBrief(co.Dir, "git diff-tree -r --no-renames --name-only "+before+" "+after)
			if err != nil || res.ExitCode != 0 {
				ed.Unknown = "the working copy it parked with is gone — the workspace was recreated while parked"
				break
			}
			for _, p := range strings.Split(strings.TrimSpace(res.Output), "\n") {
				if p = strings.TrimSpace(p); p != "" {
					ed.Paths = append(ed.Paths, p)
				}
			}
		}
		rec.Checkouts = append(rec.Checkouts, ed)
	}
	return rec
}
