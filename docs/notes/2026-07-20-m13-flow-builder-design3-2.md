# M13 — Flow builder (DESIGN3 §2)

The success chain is now an explicit edge behind an extensible kind
registry, and flows are edited as a graph. As designed, with these notes:

- flow model: `Stage.OnSuccess` (stage id or reserved `done`) + `Edges
  map[kind]*Edge`; `edgeKinds` registry drives applicability + feed rules.
  Adding a kind = one DocStage field + one registry row + one engine raise
  site. YAML sugar kept: absent on_success = next in document order (last
  = done) — every pre-M13 flow parses to identical routing (engine suite
  unchanged and green).
- Validation: success graph must be acyclic and reach `done` (a
  success-only cycle through command stages would burn no budget and loop
  forever); every stage must be reachable from the entry; `done` is a
  reserved id; auto_complete gates had to end the run (was
  "last stage").
- Engine: `advance()` follows the edge instead of pos++; `takeEdge` looks
  the edge up by kind (call sites just name the kind). Replay untouched —
  success transitions still emit no extra events.
- `flow.Doc*` (raw schema shape) exported with JSON tags; `ParseDoc`
  validates then returns it. `/api/flows/validate` returns `doc` — the
  single parsing authority; the editor never parses YAML itself (it only
  emits, via the `yaml` npm package).
- Layout: migration 0006 `flows.layout jsonb`, excluded from the hash;
  nil-layout saves keep the stored one (pg COALESCE + mem parity), so
  YAML-tab edits don't wipe positions and node drags never mint versions
  (verified live on a scratch PG: layout-only save → same hash, same
  version count; explicit-edge flow ran to completed).
- CPUI: `@xyflow/svelte` + `yaml` are the first runtime deps (bundled, no
  CDN). Graph tab = FlowGraph (canvas, typed colored handles:
  success/fail/reject) + FlowInspector (per-node forms, dropdowns via
  datalist — assistance with free text always available) + context picker
  (project → agents/models/run_refs). Serializer emits explicit
  on_success on every stage, stages in success-chain order; round-trip
  (doc → YAML → doc → YAML) verified byte-stable and the emitted YAML
  passes the Go parser.
- Not done (recorded): live per-node validation markers on the canvas
  (server errors render in the shared validation panel instead);
  changing the entry stage requires the YAML tab.
