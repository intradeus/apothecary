# Resume — "try again" from a failed stage

A run that abandons/fails at a late stage (e.g. push) no longer needs a
full restart. New forward event `run_resumed{stage_id}`: run.Apply →
Running; state.go replay un-terminals, re-enters the stage
(pos = Index(stage)), and clears all edgeCounts (fresh on_fail/on_reject
budgets) + feed/lastFailure/gateAnswer. History stays immutable — resume
is an append, not a rewind. Engine.ResumeRun validates
(abandoned|failed, has a CurrentStage, has an EnvID — abandoned/failed
runs are never reaped, so the workspace persists), appends the event, and
re-queues the run; ensureEnv reuses the existing workspace (agent commits
intact), and only the failed stage re-executes. POST
/api/runs/{id}/resume (202, same run id). UI: "Try again (<stage>)"
button on abandoned/failed runs, beside Restart (from-scratch, new run).
Verified: abandon at push → resume → same EnvID, no env destroy, run
completes; completed runs refuse resume.
