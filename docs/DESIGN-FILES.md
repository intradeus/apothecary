# DESIGN-FILES — attachments, snapshots, and the end of "artifact" as a storage term

**Status: implemented** (all four phases; see the matching entry in
`IMPLEMENTATION_NOTES.md`). This document is the target architecture for the
file subsystem — the thing previously called "the artifact system" — and the
phased path that was taken. Back-compat was explicitly waived (the project is
unreleased): schema, event payloads and routes were rewritten, dev databases
are disposable (`rebuild-projections` after upgrading). Companion documents:
`DESIGN.md` (v1 engine), `flow-language.md` (the one place the word
"artifact" remains normative).

---

## §0 Diagnosis

One table, one Go struct, one route family and one word currently serve **four
unrelated things**:

1. **Stored blobs** — human uploads (ticket/goal/answer files) *and*
   engine-captured records (stage outputs, stage logs, ping payloads), fused
   into one `artifacts` table with a free-text `scope` column (7 values), one
   `Meta` struct whose fields mean different things per scope, and two entry
   policies (`Save` vs `Capture`) on one type.
2. **Agent-written repo paths** — `artifacts/plan.md`, the flow-language
   rendezvous between stages.
3. **Flow-language declared outputs** — the `# OUTPUT CONTRACT` interface.
4. **The repo's own `artifacts/` folder** — unrelated reference documents that
   merely collide with the name.

Every verified defect in the subsystem is a symptom of that fusion:

- `ArtifactsForRun` excludes stage scopes but **includes `ping_payload`**
  (pgstore.go: `scope NOT IN ('stage_output','stage_log')`), so agent-produced
  snapshots are re-**delivered** into workspaces as if they were human
  attachments, and the API re-filters them out with a `switch`.
- **Ticket attachments leak forever**: `DeleteTicket` never touches artifacts;
  the rows aren't `orphan` so the GC skips them; their `run_id` is `''` so the
  archive sweep skips them.
- `Meta.OutputName` is **triple-purposed**: the declared output's role
  (`"plan"`) for `stage_output`, empty for `stage_log`, and the verbatim
  workspace ref for `ping_payload` — matched by linear string compare.
- `PingRefs` reads the in-memory ping registry, so a **finished run lists zero
  ping files** even though durable snapshots exist in the DB.
- Scope logic exists in **three dialects**: Go predicates (`IsAuditScope` —
  never called outside tests), SQL literal lists (four copy-pasted), and an
  httpapi `switch`.
- Retention lives in **three homes**: the env reaper (a DB setting, envs
  only), an orphan GC hardcoded to `24*time.Hour` in `main.go`, and audit
  scopes that nothing ever deletes.
- The API grew **four response shapes** for "list of files", two byte-serving
  mechanisms, and a route named `/attempts` that returns artifacts while
  `/artifacts` returns only human uploads.
- **Security**: the upload allowlist admits `text/*` (hence `text/html`), and
  both byte routes serve inline with a sniffed or extension-guessed MIME — an
  agent- or human-authored HTML file executes on the control-plane origin when
  a human clicks it.

The redesign splits the stored-blob concept in two everywhere — **attachments**
(human → factory) and **snapshots** (engine-captured record) — and retreats the
word **artifact** to the flow language, where it is load-bearing, conventional
(CI vocabulary), and correct.

## §1 Glossary — one word, one meaning

| Term | Meaning | Surfaces |
|---|---|---|
| **artifact** | Flow language ONLY: a declared repo-relative file interface between stages, and the agent's own `artifacts/` output directory | `flow-language.md`, flow YAML, `# OUTPUT CONTRACT` prompt text, the ask_human tool schema and shim wire keys (agent-facing, unchanged), `flow.ValidArtifactPath` |
| **attachment** | A file a human gave the factory — an upload or a tracker import — owned by exactly one of Ticket / Run (goal) / Answer (run, ping), or *unclaimed* before its owner claims it | DB, Go types, API routes, UI |
| **snapshot** | An engine-captured, redacted-by-construction, immutable copy of workspace content at a moment that matters: a stage attempt's declared **output**, its **log**, or a **ping file** (what the agent showed the human, as shown) | DB, Go types, API routes, UI, the `snapshot_captured` event |
| **attempt** | One (stage, iteration) — unchanged | everywhere |

Two rules make the glossary enforceable: the server and UI never say
"artifact" again, and the flow language / agent-facing prompts never say
"attachment" for stage outputs. The two agent-facing uses of "attached" that
exist today (the prompt's `# ATTACHED FILES` section, the delivery directory)
describe human uploads, so they keep the attachment word.

Derived notions (not storage): the **attempt record** — the grouping
(stage, iteration) → {result, log snapshot, output snapshots} that the run
page renders — and the **payload reference**, a *typed* value on gates/pings
(`File{path}` or `Link{url}`), never again a string sniffed with
`HasPrefix("http")` (today: 4 Go sites + 1 Svelte site).

## §2 Domain model

| Concept | Identity | Owner | Mutable? | Storage |
|---|---|---|---|---|
| Artifact (flow) | (run, path) | the workspace | yes — it *is* the work | none; dies with the workspace |
| Attachment | attachment id | one of Ticket / Run / Answer, or unclaimed | content never; owner set exactly once | row + blob |
| Snapshot | snapshot id; attribution typed per kind | the run (its record) | never | event + projected row + blob |
| Blob | sha256 | nobody — reachability-governed | never | `<RunsDir>/blobs/` |

Invariants the model makes unrepresentable rather than checked:

- An attachment cannot carry a stage id or iteration (no such fields).
- A snapshot cannot be orphaned (its constructor demands an attribution) or
  unredacted (scrubbing is a required constructor argument — see §11).
- A stage log cannot have an output role; a ping file cannot have an
  iteration; a ping file must name its ping (DB CHECKs mirror the Go sums).
- Delivery iterates attachments; snapshots are not attachments, so the
  re-delivery bug becomes untypeable.

## §3 Storage

### §3.1 Blobs: content-addressed

Blobs live at `<RunsDir>/blobs/<sha256[:2]>/<sha256>`, shared by attachments
and snapshots. Write = stream → hash → temp file → rename; the rename is
idempotent, so identical content stores once.

Why CAS over the current random-id layout (this was the genuinely contested
call; the losing argument is recorded in §14):

- **Dedup is real here.** A ticket attachment is delivered to every run of the
  ticket; a retry loop re-captures an unchanged `plan.md` on every iteration.
  Under CAS both cost one blob.
- **Integrity comes free.** The path *is* the hash; reads verify (bounded by
  the 25 MiB caps, so affordable on every serve); "did the file change after
  the human approved it?" becomes an id comparison.
- **No refcounts.** GC is a mark-and-sweep: delete disk blobs whose sha is
  referenced by no live row (attachments ∪ snapshots). Hourly, idempotent,
  crash-safe — the same self-healing shape as today's orphan GC, generalized.

Two rules follow from sharing content between references:

- **Never unlink a blob on a failed row-insert** — another reference may share
  it. The sweep owns all deletion. (The current `os.Remove`-on-insert-failure
  is safe only because ids are random; under dedup it is a correctness bug and
  must not survive the port.)
- **Blob missing or corrupt at read** ⇒ serve a typed "content unavailable" /
  "content expired" with metadata intact — name, sha, size and attribution
  still render. Records outlive bytes (§7).

### §3.2 Schema

Migration 0021 creates the two tables; the old `artifacts` table and
`tickets.artifact_ids` are dropped in the phase that removes their last
consumer (§15). The free-text `scope` column ceases to exist: for attachments
ownership is **structural** (which owner columns are set), for snapshots it is
a CHECK'd **kind**.

A hard constraint shapes the FKs: `RebuildProjections` does
`TRUNCATE runs, pings`, and TRUNCATE refuses tables referenced by FKs — so
nothing may FK `runs` or `pings`. Only `tickets` (durable config plane) gets a
foreign key, and even that is a backstop: the store deletes attachment rows
itself so it can unlink blobs.

```sql
-- Human-given files. Ownership is structural: all owner columns NULL =
-- unclaimed (GC-eligible); ticket_id = ticket attachment (shared by every
-- run of the ticket); run_id alone = run-goal attachment; run_id + ping_id =
-- ping-answer file.
CREATE TABLE attachments (
    id         text PRIMARY KEY,
    ticket_id  text REFERENCES tickets(id) ON DELETE CASCADE, -- backstop only
    run_id     text,            -- no FK: runs is a TRUNCATEd projection
    ping_id    text,            -- no FK: pings is a TRUNCATEd projection
    filename   text   NOT NULL,
    mime       text   NOT NULL,
    size       bigint NOT NULL,
    sha256     text   NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ticket_id IS NULL OR run_id IS NULL),   -- exactly one owner
    CHECK (ping_id IS NULL OR run_id IS NOT NULL)  -- an answer file names its run
);
CREATE INDEX attachments_ticket_idx ON attachments (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX attachments_run_idx    ON attachments (run_id)    WHERE run_id IS NOT NULL;
CREATE INDEX attachments_ping_idx   ON attachments (ping_id)   WHERE ping_id IS NOT NULL;
CREATE INDEX attachments_orphan_idx ON attachments (created_at)
    WHERE ticket_id IS NULL AND run_id IS NULL;

-- Engine-captured record. Insert-only; never delivered into a workspace.
CREATE TABLE snapshots (
    id         text PRIMARY KEY,
    run_id     text NOT NULL,   -- no FK (projection)
    kind       text NOT NULL CHECK (kind IN ('output','log','ping_file')),
    stage_id   text NOT NULL,   -- '_provision' pseudo-stage allowed
    iteration  int  NOT NULL DEFAULT 0,
    ping_id    text,            -- ping_file only
    name       text,            -- declared output role ('plan'); NULL otherwise
    path       text,            -- workspace path captured; NULL for logs
    filename   text   NOT NULL,
    mime       text   NOT NULL,
    size       bigint NOT NULL,
    sha256     text   NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'ping_file') = (ping_id IS NOT NULL)),
    CHECK ((kind = 'output')    = (name IS NOT NULL)),
    CHECK ((kind <> 'log')      = (path IS NOT NULL))
);
CREATE INDEX snapshots_run_idx   ON snapshots (run_id, stage_id, iteration);
CREATE INDEX snapshots_ping_idx  ON snapshots (ping_id) WHERE ping_id IS NOT NULL;
CREATE INDEX snapshots_sweep_idx ON snapshots (created_at);
```

`name` and `path` as separate, CHECK'd columns dissolve the `OutputName`
triple-overload: an output's role lives in `name`, a captured file's workspace
path lives in `path`, and neither ever means anything else.

**Ticket links.** `tickets.artifact_ids` (jsonb) is dropped;
`attachments.ticket_id` is the single truth. `CreateRun` still pins the
ticket's attachment ids into `run_created` at start, preserving the
"runs are self-contained after ticket edits" semantics.

**Ping refs.** `pings.payload_refs` survives — but honestly, as a projection
of `ping_raised` (the pings table is already TRUNCATEd and rebuilt), never
again a second authority written independently of the event.

**Queries per consumer** (each served by an index above):

| Consumer | Query |
|---|---|
| Run page / delivery | `attachments WHERE run_id=$1 ORDER BY created_at` |
| Ticket page / prompt | `attachments WHERE ticket_id=$1 ORDER BY created_at` |
| Answer delivery | `attachments WHERE ping_id=$1 ORDER BY created_at` |
| Run timeline | `snapshots WHERE run_id=$1 ORDER BY stage_id, iteration, created_at` |
| Ping review | `snapshots WHERE ping_id=$1 ORDER BY created_at` |
| Archive | `DELETE FROM attachments WHERE run_id=$1 RETURNING id, sha256` |
| Ticket delete | `DELETE FROM attachments WHERE ticket_id=$1 RETURNING id, sha256` (via the store — the leak fix) |
| Orphan GC | `... WHERE ticket_id IS NULL AND run_id IS NULL AND created_at < $1` |
| Snapshot retention | `... WHERE created_at < $1` (content sweep, §7) |

### §3.3 The authority rule

> Run-scoped facts live in the run's event log and are the sole authority;
> everything queryable is a projection. Pre-run facts (unclaimed uploads,
> tickets and their attachments) are config-plane row-state, like tickets
> themselves. Blobs are meaningless bytes; a blob nothing references does not
> exist.

This is the system's founding premise ("events are the source of truth; the
UI is a projection") applied where it currently is not: today's artifacts
table is explicitly *not* a projection, `rebuild-projections` cannot rebuild
it, and — the smoking gun — the `artifact_published` event for a ping payload
doesn't carry the ping id, so the event log cannot answer "what did this ping
show the human", which is precisely the question snapshots exist to answer.

Concretely: the `snapshots` table becomes a projection of `snapshot_captured`,
maintained in the same append transaction as the runs/pings projections and
rebuilt by `rebuild-projections` (TRUNCATE + re-fold; blobs on disk are
untouched and rows re-point at them). Retention needs no events and no
tombstones because it deletes **blobs only** — a rebuilt row whose blob is
gone serves "content expired" (§3.1).

Attachments stay rows because uploads pre-date runs (ticket creation, orphan
uploads) and the event store is keyed `(run_id, seq)`; inventing a global
config-plane event stream for them would be a second event architecture with
one reader. The compromise is principled: **every run-relevant association is
pinned by an event** — `run_created` carries the attachment ids (ticket +
goal), `ping_answered` carries the answer's attachment ids — so the run's
history is self-contained even where the rows are the working index.

## §4 Events

- `artifact_published` → **`snapshot_captured`**:

  ```
  {kind: output|log|ping_file, stage_id, iteration?, name?, path?, ping_id?,
   snapshot_id, sha256, size, mime, truncated?}
  ```

  Gains over today: the **ping id** (closing the auditability hole), the
  **truncation flag** (today only a marker inside the content), the mime.
  Per-kind field coherence is validated by the typed payload. Events carry
  references, never bytes — replay stays cheap.
- `ping_raised`: `payload_ref`/`payload_refs` strings become typed
  `files: [{path}]` + `link?: {url}`. Gates construct `Link` at sentinel
  resolution ($MR_URL/$WORKSPACE) and `File` otherwise — the five scattered
  `HasPrefix("http")` checks become unwritable.
- `ping_answered` gains `attachment_ids` — today that association is written
  to the side table by the HTTP handler before the answer event and exists
  nowhere in the log.
- `run_created.ticket_artifact_ids` → `ticket_attachment_ids`.
- Old events do not replay. Stated cost, accepted: dev databases are
  disposable.
- `run.View` stays snapshot-lean — projecting file lists into every runs-list
  row would bloat it for nothing; the run page reads one endpoint (§8) that
  the `snapshots` index answers directly. The `_provision` iteration counter
  keeps its existing event fold.
- **Capture-before-contract-check is ratified as a feature**, not fixed as a
  bug: capture is part of *recording* (unconditional, at attempt end), the
  output-contract check is part of *routing* (conditional, after). A failed
  attempt's partial outputs are exactly what the human debugging it wants.
  Order: capture → check → route, deliberately.

## §5 Go structure

### §5.1 `internal/filestore` (replaces `internal/artifacts`)

```
internal/filestore/
  blobs.go        // CAS: Put / PutStream(r, cap) / Open(sha) / Sweep(live set)
  attachment.go   // type Attachment + the Attachments store (upload policy)
  snapshot.go     // type Snapshot + SnapshotKind + the Snapshots store (capture policy)
  name.go         // SafeName, detectMime, detectMimeBytes, newID — ported verbatim
```

```go
type Attachment struct {
    ID, TicketID, RunID, PingID string // owners; zero = unset; exactly one set post-claim
    Filename, Mime, SHA256      string
    Size                        int64
    CreatedAt                   time.Time
}
func (a Attachment) Unclaimed() bool

type SnapshotKind string
const (
    KindOutput   SnapshotKind = "output"
    KindLog      SnapshotKind = "log"
    KindPingFile SnapshotKind = "ping_file"
)  // the ONE home of kind predicates; SQL never lists kinds again

type Snapshot struct {
    ID, RunID string
    Kind      SnapshotKind
    StageID   string
    Iteration int
    PingID    string // KindPingFile only
    Name      string // output role; "" otherwise
    Path      string // workspace path captured; "" for logs
    Filename, Mime, SHA256 string
    Size      int64
    Truncated bool
    CreatedAt time.Time
}

// Upload policy: MIME allowlist + hard size reject (ErrTooLarge / ErrMimeType kept).
type Attachments struct{ ... }
func (s *Attachments) Save(ctx, filename, declaredMime string, r io.Reader) (Attachment, error)
func (s *Attachments) ClaimForTicket(ctx, ids []string, ticketID string) error // claims unclaimed only, one-shot
func (s *Attachments) ClaimForRun(ctx, ids []string, runID, pingID string) error
func (s *Attachments) Get / Bytes / Open(ctx, id) ...                          // Open streams (API)
func (s *Attachments) ForRun / ForTicket / ForPing(ctx, ...) ([]Attachment, error)
func (s *Attachments) DeleteForRun / DeleteForTicket(ctx, ...) error           // DeleteForTicket = the leak fix
func (s *Attachments) GCOrphans(ctx, olderThan) (int, error)
func (s *Attachments) Limits() UploadLimits                                    // {MaxBytes, MaxFiles, MimeAllow}

// Capture policy: no allowlist, tail-truncate + marker + Truncated flag; scrub required.
type Snapshots struct{ ... }
func (s *Snapshots) Capture(ctx, snap Snapshot, scrub func(string) string, data []byte) (Snapshot, error)
func (s *Snapshots) Get / Open(ctx, id) ...
func (s *Snapshots) ForRun / ForPing(ctx, ...) ([]Snapshot, error)
```

`AttachmentDB`/`SnapshotDB` are ports implemented by `*pgstore.Store` (new
`internal/pgstore/files.go`; one column-list const per table replaces the four
copy-pasted SELECT lists).

### §5.2 Engine: split by trust direction

`internal/engine/artifacts.go` (365 lines, both directions today) splits:

- **`deliver.go`** — attachments → workspace. Delivery root renames to
  `.apothecary/attachments/` (`goal/`, `ping-<id>/` subdirs unchanged); files
  are written **0444 read-only** — the human's words must not be forgeable in
  place by a prompt-injected agent that then "quotes" them. Still best-effort,
  still never fails a run.
- **`capture.go`** — workspace → record. ONE `capture()` helper replaces the
  near-duplicate `publish`/`publishPingPayload` pair (whose event fields
  currently diverge); it scrubs, captures, and returns the `snapshot_captured`
  payload for the caller's single-writer append.

The engine holds **two narrow ports** replacing the 7-method `ArtifactStore`:

```go
type AttachmentStore interface { // delivery + claims
    ClaimForRun(ctx, ids []string, runID, pingID string) error
    Get(ctx, id string) (filestore.Attachment, error)
    Bytes(ctx, id string) ([]byte, filestore.Attachment, error)
    ForRun(ctx, runID string) ([]filestore.Attachment, error)
    ForPing(ctx, pingID string) ([]filestore.Attachment, error)
    DeleteForRun(ctx, runID string) error
}
type SnapshotStore interface {   // the engine only ever WRITES snapshots
    Capture(ctx, s filestore.Snapshot, scrub func(string) string, data []byte) (filestore.Snapshot, error)
}
```

**The engine stops reading snapshots entirely.** `PingArtifact`,
`PingArtifactAt`, `PingSnapshots`, `pingSnapshot` and the live-workspace
fallback are deleted; reads move to httpapi against the concrete stores. This
removes the resolver/env hop from every API byte path, and an uncaptured ref
renders honestly as "not captured" instead of silently serving a file the
agent may have rewritten since the human saw it. (Both raise paths snapshot
synchronously before the human is notified, so the uncaptured case is a rare,
now-visible failure rather than a silent substitution.)

httpapi and `tickets.Factory` keep **concrete** `*filestore.*` handles — they
are the policy owners and read/serve side, wired by main; interface where a
fake is exercised (engine scenario tests), concrete at the composition edge.

`tickets.Factory` gains `Delete(ctx, id)` — attachments first, then the ticket
row — and `httpapi.deleteTicket` routes through it. The leak dies at the layer
that created the coupling.

### §5.3 Config

- `APOTHECARY_ARTIFACT_MAX_BYTES` → `APOTHECARY_UPLOAD_MAX_BYTES` (hard reject)
- `APOTHECARY_ARTIFACT_MIME_ALLOW` → `APOTHECARY_UPLOAD_MIME_ALLOW`
- new `APOTHECARY_SNAPSHOT_MAX_BYTES` (tail-truncate cap; defaults to the
  upload value)

### §5.4 Stage logs: one store

The runs-dir per-stage log files (`<run>/<stage>-<iter>.log`) are retired; the
log **snapshot is the log**, and `GET /api/runs/{id}/logs/{stage}` serves from
it. Fidelity note: logs beyond the capture cap arrive tail-truncated with the
marker and flag; the live tail during a running stage stays on the
workspace-log route (`ExecLogTailer`), which is a different thing (live, not
record).

## §6 Retention — audit gets a home

Two settings join the existing reaper group (`confstore/settings.go`
plumbing, one "Storage & retention" UI group):

- `retention.orphan_uploads_after` — default `24h`; replaces the hardcoded
  `24*time.Hour` in `cmd/apothecaryd/main.go`.
- `retention.snapshots_after` — default `never`; today's keep-forever posture
  as a *stated policy* instead of a SQL exclusion list. When set, the sweep
  deletes snapshot **blobs** older than the window (rows/events keep sha,
  size, name — a five-year-old run still shows *what* every attempt produced
  after the bytes are gone).

Archive keeps today's semantics — reclaim the env and the run's attachments;
snapshots untouched — but the "snapshots survive" guarantee becomes
structural (they live in their own table) instead of a scope list inside a
DELETE statement. Ticket deletion reclaims the ticket's attachments via the
store (blobs swept when unreferenced). `destroy_completed_after` stays where
it is: it governs workspaces, a different resource.

## §7 Failure modes

| # | Failure | Resulting state | Behavior |
|---|---|---|---|
| F1 | Crash after blob write, before row/event | Unreferenced blob | Sweep removes it; no record ever claimed it existed (capture is best-effort by contract) |
| F2 | Crash after event append | Consistent | Ordering guarantees the reference's target exists at append time |
| F3 | Blob missing at read (disk loss, partial RunsDir restore) | Reference without content | Typed "content unavailable" with metadata intact; loud log |
| F4 | Blob corrupt at read | sha mismatch | As F3, "content corrupt"; mismatched bytes are never served |
| F5 | Capture ok, crash before the attempt's outcome event | Snapshot attributed to an attempt with no result yet | Attempt record shows the capture; recovery replays the stage (record ≠ routing, §4) |
| F6 | Duplicate capture (retry / recovery re-entry) | Same sha ⇒ same blob; two events | Projection keeps latest per attribution key; the log honestly records both |
| F7 | Claim of a deleted/expired upload | Fails closed at claim time | 4xx naming the id |
| F8 | Ticket deleted while runs still reference its attachments | Claim rows go; blobs stay reachable via the runs' rows | Runs keep working; bytes swept when the last reference goes |
| F9 | Sweep crashes mid-pass | Some expired blobs remain | Next pass re-finds them; idempotent |

## §8 HTTP API

Posture unchanged: unauthenticated localhost API (DESIGN.md §8/§15); only the
ping-raise routes keep the per-run bearer.

Consistency rules: every list response is an object envelope (never a bare
array); nil store ⇒ 503 for writes, 404 for bytes, empty-200 for lists; all
bytes go through **one streaming helper** (`http.ServeContent` over the
store's `*os.File` — ranges for free, no whole-blob buffering).

| New | Replaces |
|---|---|
| `POST /api/attachments` | `POST /api/artifacts` (multipart, 8-file cap, 413/415 kept) |
| `GET /api/attachments/limits` → `{max_bytes, max_files, mime_allow}` | — (new; client-side mirroring) |
| `GET /api/attachments/{id}` | `GET /api/artifacts/{id}` (attachment half) |
| `GET /api/snapshots/{id}` | `GET /api/artifacts/{id}` (snapshot half) |
| `GET /api/runs/{id}/files` → `{attachments, snapshots}` | `/runs/{id}/artifacts` + `/runs/{id}/attempts` |
| `GET /api/pings/{pid}/files` → `{files: [{path, mime, size, snapshot_id?, captured}]}`, primary first | `/pings/{pid}/artifact` (singular — dies) + `/pings/{pid}/artifacts` + its `?path=` byte mode |
| `GET /api/tickets/{id}/attachments` | `/tickets/{id}/artifacts` |
| `attachment_ids` in request bodies | `artifact_ids` (tickets create/update, start-run, restart, answer) |

The ping files list is a **server-side merge** of the declared refs (pings
projection) with the captured snapshots — the client-side join in PlanReview
dies. The shim wire contract (tool arg `artifacts`, answer key `artifacts` of
delivered paths) keeps its keys: agent-facing vocabulary, aligned with the
flow-language term.

**Serving trust classes** (security fix): snapshot bytes are
workspace-authored and untrusted. Both byte routes send
`X-Content-Type-Options: nosniff` and a restrictive CSP; active content types
(html, svg, xml) are forced to `text/plain` or `Content-Disposition:
attachment`. Uploads pass a MIME allowlist that includes `text/*` — which
admits `text/html` — so the forcing applies to both routes, not just
snapshots. Images, PDF and plain text keep inline preview.

## §9 UI

- **Run page**: one "Files" panel, one `runFiles` fetch, filter
  `outputs | logs | your attachments`, SSE refetch key `snapshot_captured`.
  (Kept merged: a split panel doubles chrome for the same rows; the filter
  already separates.)
- **Ticket page / TicketForm**: "Attachments" stays; hint copy becomes
  `.apothecary/attachments/`.
- **PlanReview**: single `pingFiles` fetch; primary = `files[0]`; agent-side
  chips labeled "From the agent"; an uncaptured ref renders a disabled
  "not captured" chip.
- **pings/[id] answered view**: finally renders the reviewed document from its
  snapshot plus file chips — "what did I approve" is now answerable, so show
  it (replaces the "the workspace artifact itself may no longer exist"
  apology).
- **Components**: `ArtifactList.svelte` → `FileChips.svelte` (takes
  `{id, filename, mime, size, kind}` and picks `attachmentUrl`/`snapshotUrl`
  by kind); `AttachFiles.svelte` fetches `/api/attachments/limits` once and
  rejects over-size / over-count / off-list picks inline with the reason
  (also fixes PingCard's currently swallowed upload errors).
- **api.ts**: `uploadAttachments`, `attachmentUrl`/`snapshotUrl`, `runFiles`,
  `pingFiles`, `ticketAttachments`; types `Attachment`, `Snapshot`,
  `PingFile` regain server parity (today `artifact_id`/`durable` are served
  but untyped, `payload_refs` is typed but dead).

## §10 Flow language & repo hygiene

**"Artifact" survives in the flow language — deliberately.** It is welded into
the normative doc (~33 mentions), the builtin flows, the `# OUTPUT CONTRACT`
convention and the shim tool schema; it matches universal CI vocabulary; and
once storage stops using the word, every remaining occurrence has exactly one
meaning. The dev fake's prompt parser needs no change: the contract text it
cuts on never contains the word.

Changes that do land here:

- `flow-language.md` gains a short terminology note (artifact vs attachment
  vs snapshot) and §5/§9 updates (`snapshot_captured`,
  `.apothecary/attachments/`).
- The workspace `artifacts/` output dir **keeps its name** (it is a directory
  of artifacts, correctly named).
- The repo-root `artifacts/` reference folder moves to `examples/flows/`
  (an `examples/` dir already exists) — ending the name collision.
- Stale docs fixed: `artifacts/README.md` (points at a nonexistent
  `internal/builtin/agents/`), the `ports.AgentSpec` comment
  (`.apothecary/agents/` predates FLOWLANG v3), DESIGN.md §8's route list,
  and the retired `agentspec.Parse` importer.

## §11 Integrity & trust

- **sha256 becomes load-bearing** (§3.1): identity, dedup, verification,
  change detection.
- **Redaction is constitutive**: everything crossing workspace →
  control-plane goes through `Capture`, whose signature requires the scrub
  function — an unredacted snapshot is unrepresentable, and no `redacted`
  flag is needed on events. Attachments (control-plane → workspace) are never
  scrubbed; they originate outside the untrusted boundary.
- **Delivered attachments are 0444** in the workspace (§5.2).
- **Containment unchanged and universal**: every engine read/write of a
  workspace path (captures, ping files, deliveries, prompt inlining) flows
  through the adapter's `containmentGuard` (`readlink -f` against the
  checkout root); stated here as an invariant of the two doors — there is no
  unguarded byte path between engine and workspace.
- **Serving trust classes**: §8.

## §12 What dissolves by construction

| # | Today | Dissolved by |
|---|---|---|
| 1 | Ping files in `pings.payload_refs` jsonb AND ping_payload rows; finished runs list nothing (in-memory `PingRefs`) | One authority (`ping_raised` + `snapshot_captured`); lists are projections; no registry on read paths |
| 2 | `ArtifactsForRun` includes ping_payload → snapshots re-delivered into workspaces | Delivery iterates attachments; snapshots aren't attachments — untypeable |
| 3 | `ticket_goal` blobs immortal after ticket delete | `DeleteForTicket` in the factory + reachability sweep (F8) |
| 4 | URL-vs-path by `HasPrefix("http")` ×5 | `File`/`Link` typed at sentinel resolution |
| 5 | Event log can't answer "what did this ping show" (no ping id on the event) | `snapshot_captured.ping_id` |
| 6 | Scope logic in Go/SQL/httpapi dialects | Structural ownership + CHECK'd kind; predicates live in filestore only |
| 7 | Capture-before-check, accidentally | Ratified: record ≠ routing (§4) |
| 8 | Retention in three homes | One settings group (§6) |
| 9 | Naming collision ×3 + one package doing both directions | §5 splits + §10 renames |
| 10 | Prompt as machine interface; live inputs vs delivered paths mixed | §13 follow-up (structured `AgentInput`) |

## §13 Follow-up (recorded, not a phase)

`ports.AgentInput` grows structured fields — `Outputs []{Role, Path, Type}`,
`Attachments []{Path, Name, Mime}` — alongside the rendered prompt, so the
prompt becomes a *rendering* of data rather than the data itself; the dev
fake consumes the struct and stops parsing prompt text. Orthogonal to
storage; sequenced after the phases below.

## §14 Rejected alternatives

- **One table + CHECK'd kind enum.** Keeps every query wearing a kind
  predicate and keeps delete/deliver/audit distinctions as WHERE-lists — the
  exact failure mode that produced the re-delivery bug. Two tables make wrong
  queries unrepresentable.
- **Side-table authoritative, events as notifications** (today's de-facto
  model, made honest). Contradicts the founding premise, keeps
  `rebuild-projections` a lie for this subsystem, and the current schema
  already demonstrates the drift it invites (the event that forgot the ping
  id; the jsonb that duplicates the rows).
- **Everything as events, including uploads.** Uploads pre-date runs;
  tickets/projects/flows are row-state by settled architecture; a global
  config-plane event stream is a second architecture with one reader.
- **Blobs as `bytea` in Postgres.** Would genuinely eliminate F1/F3
  (single-store atomicity, one backup) — but capture content through
  WAL/vacuum is real bloat for a self-hosted single-node target, `RunsDir` is
  already mandatory infrastructure, and CAS degrades gracefully (garbage vs
  metadata-preserving miss). Revisit only if the deployment story drops the
  persistent runs dir.
- **Id-addressed blobs (status quo layout).** The near-miss. Argument for:
  port the code verbatim, no sweep machinery. Rejected because the sweep *is*
  the GC machinery either way (orphans need collecting under any scheme),
  refcounts aren't needed, and dedup + free verification are real wins here
  (ticket fan-out, retry re-captures).
- **Renaming "artifact" out of the flow language** (`files:`/`outputs:`).
  Maximal churn in the normative doc and agent-visible conventions for zero
  disambiguation gain once storage stops using the word.
- **A physically separate "attempt store".** The separation that matters is
  typed and event-anchored; a second byte store re-creates the two-log-stores
  problem §5.4 deletes.
- **Deleting audit content by deleting events.** The log is append-only;
  expiry applies to blobs only.

## §15 Phased implementation path

Each phase ships with `go test ./...` and `cd cpui && npm run check` green.

1. **filestore + schema (M).** Migration 0021 (create `attachments` +
   `snapshots`), `internal/filestore` complete (CAS, both stores, ported
   helpers), `internal/pgstore/files.go`, tests ported from
   `internal/artifacts` plus new ones: claim-once per owner, ticket-delete
   unlinks, sweep idempotence, truncation marker+flag, streaming `Open`,
   CAS dedup. Old world untouched and still serving.
   *Verify: new package + pgstore tests.*
2. **Backend swap (L).** Engine ports (`SetFiles`), `deliver.go`/`capture.go`
   split, event renames + snapshots projection (+ `rebuild-projections`),
   `tickets.Factory.Delete`, httpapi re-backed **behind the existing routes
   and response shapes** (old JSON built from new types — the UI runs
   unchanged), retention settings + reaper wiring, migration 0022 (drop
   `artifacts`, drop `tickets.artifact_ids`), delete `internal/artifacts` and
   `engine/artifacts.go`. Bugs fixed here: re-delivery (structural), ticket
   leak, GC knob, finished-run ping lists. Subtle: scenario-test fakes become
   the two ports (`TestAttemptSnapshotsSurviveRetries`,
   `TestPingAttachmentsAreSnapshotted`,
   `TestGoalArtifactsDeliveredAndPrompted`, provision-log tests); the
   answerPing claim-before-answer ordering is preserved.
   *Verify: full engine scenario suite, httpapi suite, tickets/confstore
   tests, dev-mode manual run.*
3. **API v2 + UI (L).** New routes/envelopes of §8, streaming + trust-class
   headers, limits route, delete old routes and the engine ping-read trio,
   `api.ts` + components per the §9 map, answered-ping rendering, settings UI
   for the retention knobs, stage-log route re-backed by snapshots (§5.4).
   *Verify: httpapi tests rewritten for the new shapes (nil-store matrix),
   `npm run check`, manual walkthrough: ticket → run → gate review →
   answer-with-files → archive.*
4. **Vocabulary sweep (S).** `.apothecary/attachments/` rename (delivery
   code, bootstrap git-excludes, prompt strings, tests, UI hint),
   `artifacts/` → `examples/flows/`, flow-language.md terminology note +
   §5/§9 updates, stale comments, env-var renames if not taken in phase 2.
   *Verify: engine tests asserting delivery paths;
   `grep -r '\.apothecary/artifacts'` returns nothing;
   `apothecaryd validate examples/flows/v3.yaml`.*
