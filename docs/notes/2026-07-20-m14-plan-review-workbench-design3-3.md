# M14 — Plan review workbench (DESIGN3 §3)

Plans render in the UI; comments anchor to sections and submit as one
feedback prompt. As designed, with these notes:

- `Engine.PingArtifact`: active-ping registry → run view (store reduce) →
  resolve project → `Envp.ReadFile` on the still-alive workspace,
  released immediately. Path comes from the flow document, never the
  request; content capped at 1 MiB (tail — the end of a plan beats the
  start). Sentinels: ErrArtifactIsURL (409, mr_url gates keep the
  link-out), ErrNoArtifact (410), ErrPingNotFound (404).
- `GET /api/pings/{pid}/artifact` — unauthenticated like the rest of the
  CPUI ping surface (single-user, loopback).
- CPUI: `marked` dep (raw HTML in agent markdown is ESCAPED via a
  renderer override — plans are semi-trusted LLM output). `planreview.ts`
  lexes top-level blocks with cumulative line numbers + nearest heading;
  `composeFeedback` renders the deterministic "PLAN REVIEW — N comments"
  prompt (anchor + ≤200-char quote + text per item). Both verified by a
  strip-types node test (exact line/heading anchors, exact output).
- Review page `/pings/{id}`: block hover → "+" (selection inside the
  block becomes the quote), sticky review sidebar, drafts in localStorage
  keyed by ping id (survive reloads; cleared on submit), Request changes
  = reject via the EXISTING answer path — engine/edges/feed untouched;
  Approve confirms before discarding unsent comments. Inbox links
  artifact gates to it; mr_url gates keep the old behavior.
- Live smoke (scratch PG + dev stack): gate ping → artifact fetched from
  the workspace → reject with a composed review → `edge_taken.feed`
  carries the full PLAN REVIEW text (the replanning agent sees it as
  PREVIOUS ATTEMPT FEEDBACK) → re-plan → approve → run completed.
- Not covered by a dedicated test: the 409 mapping for mr_url payloads
  (three-line handler branch; engine sentinel is unit-visible).
