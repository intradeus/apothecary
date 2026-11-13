# DESIGN-FILES implemented — attachments + snapshots replace "artifacts"

All four phases of `docs/DESIGN-FILES.md` are in:

- **filestore + schema (0021/0022):** `internal/filestore` (content-addressed
  blobs under `<RunsDir>/blobs/<sha[:2]>/<sha>`; `Attachments` = upload
  policy, `Snapshots` = capture policy, `Mem` = the one shared test DB);
  `attachments` + `snapshots` tables with structural ownership / CHECK'd
  kinds; `internal/artifacts` and the `artifacts` table are gone, as are
  `tickets.artifact_ids` and `pings.payload_ref(s)` (now typed `files`+`link`
  columns). Blob deletion happens ONLY in the mark-and-sweep (never-unlink
  rule; 1h mtime grace closes the write race), with the live set from
  `pgstore.LiveBlobSHAs` honoring snapshot content retention.
- **Backend swap:** engine holds two narrow ports (`AttachmentStore`,
  `SnapshotStore` — the engine only WRITES snapshots; the ping-read trio and
  the live-workspace fallback are deleted, reads live in httpapi against the
  concrete stores). `engine/artifacts.go` split into `deliver.go` (0444
  read-only delivery, delete-then-write on re-delivery) and `capture.go`
  (ONE capture funnel; scrub is a required argument). Events:
  `artifact_published` → `snapshot_captured` (gains ping_id/mime/truncated/
  filename — everything a rebuild needs), `ping_raised` carries typed
  files/link (the five HasPrefix("http") sites are gone), `ping_answered`
  carries attachment_ids, `run_created.ticket_attachment_ids`. The snapshots
  table is a PROJECTION: projectTx folds snapshot_captured idempotently and
  rebuild-projections TRUNCATEs + re-folds it (blobs untouched).
- **API v2 + UI:** `/api/attachments` (+`/limits`), `/api/attachments/{id}`,
  `/api/snapshots/{id}` (ONE streaming serve helper: verified reads, nosniff,
  no-script CSP, active types forced to download — closes the inline-HTML
  hole), `/api/runs/{id}/files`, `/api/pings/{pid}/files` (server-side join;
  finished runs answer from the projection), `/api/tickets/{id}/attachments`;
  `attachment_ids` in every body. Stage logs serve from log snapshots — the
  runs-dir `<stage>-<iter>.log` files and `writeStageLog` are retired. UI:
  run-page "Files" panel over one fetch, FileChips replaces ArtifactList,
  AttachFiles mirrors the upload limits inline, PlanReview single-fetch with
  "not captured" visibility, answered pings render the reviewed doc from its
  snapshot, Settings gains the Storage & retention group
  (`retention.orphan_uploads_after`, `retention.snapshots_after`).
- **Vocabulary:** delivery dir is `.apothecary/attachments/`; repo-root
  reference dir moved to `examples/flows/`; flow-language.md §5 gained the
  terminology note (artifact = flow word only) and `snapshot_captured`;
  the retired `agentspec.Parse` importer and the stale `.apothecary/agents`
  comment are gone. Env knobs: `APOTHECARY_UPLOAD_MAX_BYTES` /
  `APOTHECARY_UPLOAD_MIME_ALLOW` (legacy ARTIFACT_* names still read) +
  `APOTHECARY_SNAPSHOT_MAX_BYTES`.

Old events decode as inert history: an event type the binary does not know
(`artifact_published`, or anything a future rename retires) comes back as
`event.Unknown` — every projection's type switch falls through it, SSE replay
serves the raw payload verbatim, and recovery / `rebuild-projections` work
across the cutover instead of bricking the boot on the first pre-cutover run
(the original strict decode did exactly that). Old runs simply render without
file lists (0022 dropped their rows); purging the dev DB remains the clean
option, never a requirement. A known type with a garbage payload still errors
loudly — that is corruption, not version skew.
