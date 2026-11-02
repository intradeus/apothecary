package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"apothecary/internal/flow"
	"apothecary/internal/flow/agentspec"
	"apothecary/internal/platform/logs"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/run/runcore"
)

func (x *exec) stageCommand(st *flow.Stage) error {
	rs := x.rs
	script := st.Run
	if st.RunRef != "" {
		script = x.res.Commands[st.RunRef]
		if script == "" {
			// The command was deleted from project.commands after run
			// creation. Executing "" would exit 0 and silently pass a gate.
			if err := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra,
				Error: fmt.Sprintf("run_ref %q no longer exists in project.commands", st.RunRef)}); err != nil {
				return err
			}
			return x.fail(fmt.Sprintf("stage %s: run_ref %q no longer exists in project.commands", st.ID, st.RunRef))
		}
	}
	fresh := rs.Ph != runcore.PhIn
	if fresh { // §2: on recovery, open command stages re-execute without a new attempt
		iter := rs.Iters[st.ID] + 1
		if err := x.append(&event.StageStarted{StageID: st.ID, Iteration: iter}); err != nil {
			return err
		}
	}
	started := x.e.clock()

	// Declared inputs are a precondition (FLOWLANG §4): a script whose input
	// is missing usually fails in a confusing way (or worse, succeeds having
	// done nothing). Checking first names the real problem — upstream wiring.
	if missing := x.missingPaths(st.Inputs); len(missing) > 0 {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInputMissing,
			Error: fmt.Sprintf("declared input(s) not readable: %s", strings.Join(missing, ", "))}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, "")
	}
	// What it reads with a schema must hold it (FLOWLANG §4): a script fed a
	// malformed file fails in a confusing way, or succeeds having done
	// nothing.
	if reason, msg := x.checkStructuredInputs(st); reason != "" {
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: reason, Error: msg}); aerr != nil {
			return aerr
		}
		return x.takeEdge(st, flow.EdgeFail, msg)
	}

	// Every execution starts from a clean slate, so the output check below
	// cannot pass on an earlier attempt's file (M18) — a re-execution after a
	// crash included: it redoes the script's whole work. After the input
	// check: the two never overlap in practice, and inputs must survive
	// regardless.
	x.clearOutputs(st.Outputs)

	// A command stage may bound its own wall clock; unset inherits the flow
	// default (FLOWLANG §2, PLAN5 M23).
	timeout := st.Timeout
	if timeout == 0 {
		timeout = rs.Flow.Defaults.Timeout
	}
	// Once in the primary, or — each_repo — once per checkout, stopping at
	// the first failure. Each checkout gets the full timeout.
	// On a subject, in its checkout (PLAN6 R12): the phase acts on that
	// merge request's branch only.
	dirs := []string{event.PrimaryCheckoutDir}
	if co := x.rs.SubjectCheckout(); co != nil {
		dirs = []string{co.Dir}
	} else if st.EachRepo {
		dirs = rs.CheckoutDirs()
	}
	var res envport.ExecResult
	var err error
	var combined strings.Builder
	marker := commandMarker(rs.RunID, st.ID)
	for _, dir := range dirs {
		cctx, cancel := context.WithTimeout(x.ctx, timeout)
		res, err = x.res.Envp.Exec(cctx, rs.EnvID, envport.Command{Script: script, Dir: dir, Timeout: timeout, Marker: marker, Scrub: x.redact.Scrub})
		cancel()
		if err != nil {
			break
		}
		if len(dirs) > 1 {
			fmt.Fprintf(&combined, "=== %s ===\n", dir)
		}
		combined.WriteString(res.Output)
		if res.ExitCode != 0 {
			break
		}
	}
	res.Output = combined.String()
	if err != nil {
		if x.ctx.Err() != nil {
			return context.Cause(x.ctx)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return x.failAttempt(st, runcore.ClassifyAttempt(st, runcore.AttemptObservation{TimedOut: true}), attemptSpend{})
		}
		if aerr := x.append(&event.StageFailed{StageID: st.ID, Reason: event.ReasonInfra, Error: err.Error()}); aerr != nil {
			return aerr
		}
		return x.fail("infrastructure error: " + err.Error())
	}
	x.publishLog(st.ID, rs.Iters[st.ID], res.Output)
	x.publishOutputs(st, rs.Iters[st.ID], st.Outputs)
	obs := runcore.AttemptObservation{ExitCode: res.ExitCode, Output: shell.Tail(res.Output, 8<<10)}
	if res.ExitCode == 0 {
		// A command that exits 0 without writing what it declared has not done
		// its job — the same contract agent stages carry. And what it writes
		// with a schema must hold it, before anything reads it.
		if obs.Missing = x.missingPaths(st.Outputs); len(obs.Missing) == 0 {
			obs.Invalid = x.checkStructuredOutputs(st)
		}
	}
	if f := runcore.ClassifyAttempt(st, obs); f != nil {
		return x.failAttempt(st, f, attemptSpend{})
	}
	if err := x.append(&event.StageCompleted{StageID: st.ID, DurationMS: x.e.clock().Sub(started).Milliseconds()}); err != nil {
		return err
	}
	x.rs.RouteSuccess(st, "")
	return nil
}

// commandMarker is one command stage's envport.Command.Marker: the run and the
// stage, since a workspace only ever hosts one run. An attempt's exec client
// dies with its timeout or with the engine process; the adapter uses the
// marker to kill what the previous attempt left running before this one
// starts (see envport.Command.Marker).
func commandMarker(runID, stageID string) string { return runID + ":" + stageID }

func (x *exec) missingPaths(ports []flow.Port) []string {
	var missing []string
	for _, b := range ports {
		// An engine source is no file; an optional input may be absent.
		if b.Value == "" || flow.IsEngineSource(b.Value) || b.Optional {
			continue
		}
		path := flow.FilePath(b.Value)
		if b.Type == agentspec.TypeFiles {
			// A directory, there when it holds a file.
			if held, _ := x.dirFiles(path); len(held) == 0 {
				missing = append(missing, emptyDir(path))
			}
			continue
		}
		if _, err := x.res.Envp.ReadFile(x.ctx, x.rs.EnvID, path); err != nil {
			missing = append(missing, path)
		}
	}
	return missing
}

// clearOutputs deletes a stage's declared outputs before a FRESH attempt
// (PLAN5 M18). Without it the post-stage existence check passes on a file an
// EARLIER attempt wrote: attempt 2 could "succeed" without producing
// anything, and a stale verdict could route the run. Deleting first makes
// the existing check a freshness check — whatever exists afterwards was
// written by the attempt that just ran.
//
// Never called re-entering an agent's attempt after a crash: a resumed
// harness session continues mid-work, and its partial outputs must survive.
func (x *exec) clearOutputs(bindings []flow.Port) {
	var paths, dirs []string
	for _, b := range bindings {
		if b.Value == "" || flow.IsEngineSource(b.Value) {
			continue
		}
		if b.Type == agentspec.TypeFiles {
			// Without its slash: a symlink planted where the directory goes
			// is removed itself, never followed into what it points at.
			dirs = append(dirs, shell.Quote(strings.TrimSuffix(b.Value, "/")))
			continue
		}
		paths = append(paths, shell.Quote(b.Value))
	}
	var scripts []string
	if len(paths) > 0 {
		scripts = append(scripts, "rm -f -- "+strings.Join(paths, " "))
	}
	if len(dirs) > 0 {
		// A files output starts empty, and there: the attempt only writes
		// into it.
		list := strings.Join(dirs, " ")
		scripts = append(scripts, "rm -rf -- "+list+" && mkdir -p -- "+list)
	}
	// Best-effort: a workspace that cannot delete is not a stage failure —
	// the output contract still has the final say.
	for _, script := range scripts {
		if _, err := x.res.Envp.Exec(x.ctx, x.rs.EnvID, envport.Command{
			Script:  script,
			Dir:     "repo",
			Timeout: time.Minute,
		}); err != nil {
			x.e.log.WarnContext(x.bg, "clearing declared outputs failed", logs.RunID, x.rs.RunID, "err", err)
		}
	}
}
