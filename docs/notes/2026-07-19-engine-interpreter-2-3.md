# Engine / interpreter (§2, §3)

- **Provision retry is engine-side**: one whole-Provision retry before
  `env_failed` (§2 "failed after 1 retry"). Adapters may additionally retry
  transient sub-steps internally, but the deterministic retry the state
  machine promises lives in the engine where the fakes can test it.
- **Crash-resume sessions**: only `stage_completed` carries a session id, so
  an open (crashed) stage resumes via the *last recorded session for that
  stage* (a previous iteration); with none, it re-Runs with a fresh §3.2
  prompt. The Resume reply is a fixed continuation message.
- **Recovery re-execution rules**: crash after `stage_failed` but before the
  edge decision → the failure is **routed directly** (no re-execution, no
  duplicate attempt events) using the output tail persisted in
  `stage_failed.error`; `action` stages instead re-enter their executor,
  which must be idempotent (create_mr via the recorded `mr_created`). Crash
  after `stage_completed` but before routing → route only; a stage whose agent
  declares a verdict output re-reads it at its bound path and does not
  re-append `verdict_received`.
- **Edge feed content IS persisted**: `edge_taken` carries a `feed` key
  (≤8 KB tail; extension over the §6 catalog) and `stage_failed.error`
  carries the command output tail for `exit_nonzero` — recovery never loses
  feedback. Likewise `ping_raised` gained an optional `context` key
  (mirrors the §9 pings.context column) so recovered pings keep it.
- **"iteration n of m"** in the §3.2 feedback header: n = attempt number,
  m = edge max + 1 (total allowed attempts).
- **Verdict source of truth**: `AgentResult.Verdict` if the adapter parsed
  one, else the engine reads `artifacts/verdict.json` itself. REJECT with no
  comments (or missing/unparsable file) is `stage_failed{output_contract}`.
- **ask_human answers in events**: a form's `answer` is `accept` (values in
  `content`, by field id) or `decline` (the reason in `feedback`); the shim
  receives the engine-composed text.
- **Gate reject on a gate with no `on_reject`** (e.g. final_gate) abandons the
  run (`unrecoverable failure with no on_reject`).
- **Budget checks are strict-greater** and run after each agent stage
  completion; command stages contribute no cost/turns.
- **Per-run bearer tokens are regenerated on every engine boot** (not
  persisted — they're secrets and events are not a secret store). Recovery
  **rewrites `/apothecary/run.env`** in a still-running environment with the
  fresh token; the shim (M3) re-reads the file on 401 before giving up.
- **Flow-hash drift on recovery** (flow file edited while a run was in
  flight) logs a warning and continues with the current file.
- Engine `Shutdown` cancels run goroutines *without* terminal events (runs
  resume via `Recover`); only `Abort` writes `run_failed{aborted}`.
- Bootstrap step 4 (.opencode generation from `.apothecary/agents/`) is a
  placeholder script until the opencode adapter lands (M3); the AGENTS.md
  symlink + git-exclude part is real.
