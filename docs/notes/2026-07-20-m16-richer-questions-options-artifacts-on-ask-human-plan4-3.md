# M16 — Richer questions: options + artifacts on ask_human (PLAN4 §3)

The shim protocol grows two optional fields; everything downstream
generalizes machinery that already existed. Notes:

- ask_human tool schema: `options?: string[]` (closed answer set, max 8,
  trimmed+deduped, ≤200 chars each) and `artifact?: path` (repo-relative,
  validated by flow.ValidArtifactPath — it becomes payload_ref, so the
  M14 review workbench works for ANY agent question, not just gates).
- Strict options (decision #1): when declared, AnswerPing refuses
  anything not in the set, naming the choices. Determinism is the point;
  the note field (feedback) carries anything beyond the pick.
- Recording: option answers keep the chosen option in
  ping_answered.answer with the note in feedback — the historical
  answer:"text" convention now applies only to free-text asks. The shim's
  tool result: free text returns bare (the old "text\n" prefix noise is
  gone); options return "<option>\n(note: …)".
- Event/projection: ping_raised.options; migration 0008 `options jsonb`;
  PingView/ReducePings carry it (EventPingStore parity for free).
- UI: inbox renders one button per option (+ optional note field); the
  review page's submit is now mode-aware — gates approve/request-changes,
  option asks answer via buttons with the composed review as the note,
  free asks send the composed review as the answer text.
- Old shims baked into running workspaces simply never send the new
  fields — no compat break; the base image content-hash change rebuilds
  the workspace image on next use as usual.
- Live smoke: migration 0008 applied; full dev loop green. Options
  covered end-to-end at the engine and HTTP layers (validation, delivery,
  durable recording); no live agent invokes options until a real codex/
  opencode run asks with them.
