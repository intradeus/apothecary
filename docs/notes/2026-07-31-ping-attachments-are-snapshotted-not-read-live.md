# Ping attachments are snapshotted, not read live

`ask_human`'s attachments (and a gate's reviewed document) were the one thing
the event log referenced without owning: `payload_refs` held repo-relative
paths and `PingArtifactAt` read them out of the live workspace. Two bugs fell
out of that, and they are the reason this changed:

- **The reference outlived the referent.** Once the workspace was reaped (or
  the run archived) an answered ping's attachment was simply gone, so the
  inbox could no longer show what a decision had been made about — in a system
  whose stated premise is that events are the source of truth.
- **It answered the wrong question.** The agent resumes the moment a ping is
  answered and often rewrites the very file it asked about, so re-opening the
  ping showed the file's CURRENT content rather than what the human approved.
  The audit trail lied, quietly.

Now `publishPingPayload` captures each non-URL ref at raise time through the
existing snapshot path (redacted, `Capture`d, an `artifact_published` event) as
scope `ping_payload`, keyed by ping id with the ref verbatim in `OutputName` —
which is what the read path matches on, since two refs can share a basename. No
schema change: the artifacts table already carries `ping_id` + `scope`.

Reads prefer the snapshot and need no live ping — the rows are keyed by ping
id, so an answered ping stays readable after the engine's in-memory registry
drops it, which is precisely the case that used to 404. The live workspace read
remains as the fallback for pings raised before this and for a capture that
failed. `ping_payload` joins the stage scopes in surviving `DeleteForRun`
(`artifacts.IsAuditScope`): what a human was shown when they approved something
is audit trail, not run input.

Naming, while we were here: "attachment" is the human/inbound file,
"snapshot" the engine-captured one. `GET /api/pings/{pid}/artifacts` still
addresses by ref (the ping's own name for it) and now marks which entries are
`durable`, with the artifact id for a direct link.
