# Apothecary v4 — First-class interactions (light plan)

> Status: **draft — awaiting approval**. Covers the post-v3 request/response
> wave: the durable ping surface, richer questions, and review analytics.

## 1. The idea

The ping *process* is already request → response end to end (the agent's
ask_human tool call returns the human's answer as its result; gates route
on approve/reject; both sides land in the event log AND a transactional
`pings` projection). What is second-class is the **surface**: the API
reads the in-memory registry, answered history is invisible, questions
are free-text-only, and the M14 review workbench — which is generic —
is only reachable from gates.

v4 makes interactions first-class: queryable history, markdown + artifact
review for *any* ping, agent questions with declared answer options
(buttons in the UI), and the first analytics built on the resulting
dataset. The write path, engine routing, edges and feeds stay untouched
where possible; most of this is wiring what already exists.

## 2. M15 — The durable ping surface

- **API reads the projection, not the registry**: `GET /api/pings?status=
  pending|answered|all` (projection-backed; live registry stays the
  source for *answering*), plus `GET /api/pings/{id}` returning the full
  request/response pair (question, context, payload_ref, answer,
  feedback, answered_at). Fixes: history invisible, pings 404ing after
  their run ends.
- **Attribution**: `ping_raised` gains `stage_id` (gates know their
  stage; ask_human pings record the stage in flight). Migration 0007
  adds `stage_id` to the projection; `rebuild-projections` backfills.
- **Inbox history**: a Pending | Answered toggle; answered entries show
  the response inline. Run detail shows each gate's Q&A in the timeline.
- **Markdown everywhere**: ping `question`/`context` render as markdown
  (same escaped-HTML renderer as plans) in the inbox, the review page and
  history — agents already write markdown context; today it renders as
  plain text.

## 3. M16 — Richer questions (options + artifacts on ask_human)

The shim protocol grows two optional fields; everything else generalizes
existing machinery:

- **`options: string[]`** — the agent declares answer choices ("1", "2",
  "3" / "postgres", "sqlite"). The UI renders one button per option (plus
  an optional note field). Validation: when options are declared, the
  answer MUST be one of them — determinism is the point; the note rides
  in `feedback`. The agent receives `<option>` (+ `note: …`) as the tool
  result. Ping/event/projection gain `options` (jsonb); old shims baked
  into running workspaces simply never send it.
- **`artifact: path`** — the agent may point its question at a workspace
  file (`artifacts/comparison.md`). That sets `payload_ref`, which means
  the **M14 workbench works for any ping**: rendered markdown, anchored
  multi-comments, one composed reply — returned as the ask_human tool
  result instead of riding an on_reject feed. Review-with-anchors becomes
  a property of *interactions*, not of plan gates.
- Gates are unchanged (approve/reject are already "options").

## 4. M17 — Review analytics (first consumer of the dataset)

Bounded scope: one endpoint + one panel, computed from data that now all
exists (events: `stage_started.agent_hash`, `edge_taken`,
`ping_answered`; projection: pings with `stage_id`).

- `GET /api/projects/{id}/insights`: per flow stage — gate approvals vs
  rejections, on_fail/on_reject edge counts, and per **agent version
  (hash)**: runs, rejection rate, recent rejection feedback snippets.
  "Did prompt v2 reduce plan rejections?" becomes a glance.
- Project page gains an Insights panel. No new storage, no background
  jobs — SQL over events + pings at request time (single-user scale).

## 5. Decisions (recommendations — flag to change)

1. **Strict options**: buttons only when options are declared (+ optional
   note), no free-text override — the agent asked a closed question and
   must be able to branch on the answer deterministically.
2. **Artifact replies as tool results**: the composed multi-comment reply
   to an ask_human artifact review returns through the normal answer
   path (one text), NOT a new feed mechanism.
3. **History depth**: projection already holds everything forever; the
   API paginates (`?limit=`, newest first) rather than pruning.
4. Order M15 → M16 → M17 (M16 needs M15's surface; M17 needs M15's
   stage attribution). Each ships on explicit go.
