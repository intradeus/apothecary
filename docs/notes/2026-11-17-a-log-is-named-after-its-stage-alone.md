# A log is named after its stage alone

Why: `2026-11-16-one-naming-standard-for-attempts.md` named an attempt's
log after its reference (`review#5.log`, `_provision#1.log`), and only the
log: every other file keeps a name of its own and the reference stands beside
it — the journal's folder (`review#5/shot.png`), the attachments' folder
(`build#3/decision#1/spec.pdf`), the chip beside an output on the record
(`plan.md`). On the record, a log carried `#` and the output beside it did
not. A log's name is a label only: it is no file in the workspace (the engine
holds what a stage printed; the provisioning log's file goes with the
workspace), no agent reads it, and nothing resolves it.

What now: a log is `<stage>.log` (`review.log`, `_provision.log`), whatever
its attempt; the record's chip says which. No stored file name carries `#`:
`SafeName` turns it to `-` for the engine's names too, and the log's
exception (`engineName`) is gone. This corrects that note's "Files mirror
references" item and `2026-11-16-the-impasse-a-person-decides-after-rejections.md`'s
"`<n>.log` never repeat": the attempt number never repeats, and the log's
name no longer holds it. Two downloaded logs of one stage share a name, as
two downloaded `plan.md` do.

Tests: `internal/engine/artifacts_test.go` (`_provision.log`, `check.log`);
`internal/platform/filestore` `TestHashIsReservedInStoredNames` (a log's `#`
is turned like any other).
