# Attachments in the take-over and Ask chats

Why: design work runs on sketches, screenshots and documents. Question
answers and run goals carried files; the two chats beside a run did not.
A person can now paste, pick or drop files into the take-over composer
and the Ask composer, and the agent reads them from its workspace.

What now:

- **A chat file is the run's, with a `chat` on its row** (`takeover` or
  `ask`, migration 0008). Without it, a run-owned file with no `ping_id`
  reads as a goal file: it would be listed under `# ATTACHED FILES` in
  every later stage's prompt and labelled "run goal" on the record. The
  CHECKs say a chat file has a run and no ping. `goalAttachments` leaves
  chat files out; the record labels them by their chat.
- **Fixed folders, `takeover/` and `ask/`**, under
  `.apothecary/attachments/`, like `ticket/`: no `#`, so never a ping's
  reference.
- **A chat file's name is given once, at its claim, and kept**
  (`attachments.chat_name`, unique per run and chat). The 2026-11-16
  naming rule computes a folder's paths from creation order, which holds
  for a ticket's or a ping's files: they arrive together. A chat's arrive
  one message at a time, and in any order: an older `image.png` sent after
  a newer one would sort first, take `image.png` from it, overwrite the
  announced file and move it to `image-2.png` on a restart's re-delivery.
  So the claim names each file past the names the run's chat holds, in the
  order the message gives them (`filestore.ChatFileName`), in the same
  transaction; Postgres queues claims into one chat on a transaction
  advisory lock, so two never read the same names taken. Delivery and
  re-delivery (`attachmentPaths`, `deliverRunAttachments`) use the stored
  name. A refused question's unclaim gives its name up: it was never
  announced.
- **The claim is all or nothing, and only once the message is accepted.**
  `ClaimAttachmentsForChat` takes every id or none
  (`filestore.ErrNotClaimable`, worded without "not found", which the Ask
  API would turn into a 404); `Attachments.ClaimForChat` also holds the
  8-file cap. A live take-over message claims in the run loop's message
  branch after the hold check, then delivers and names the files; a run
  parked on its holder claims inside `wakeHeld`'s build step, whose error
  now appends nothing, and its files arrive with the run's others on
  resume, named by `withPendingMessages`. An Ask question has the engine
  claim and write its files (`Engine.DeliverAskFiles`) before anything is
  recorded, so the read-only session stays read-only. A refused message or
  question records nothing, and its uploads stay unclaimed for the orphan
  sweep. An Ask question is refused after its claim too, when its files
  cannot be written: `DeliverAskFiles` then removes what it wrote and gives
  the claim back (`UnclaimAttachmentsForChat`), so a retry can send them.
  A run's first question opens its thread only once its files are in:
  until then the thread is not stored, and the run's creating lock is
  held so a second first question still finds one thread, not two.
- **Files alone are a message**: text is required only with no files.
  `message_queued` and an Ask question record their `attachment_ids`, so
  the transcript and the Ask pane show the files as chips after a reload.
- **A composer uploads before it sends, and locks while it does.** The
  box is read-only and the picker disabled (no pick, drop, paste or chip
  removal) until the files are up, so what is sent is what was on screen
  at Enter; merging edits made during the upload into a message already
  leaving has no right answer. Once they are up the box is free again: the
  Ask pane then clears only the files it sent, so files picked while the
  question is on its way stay for the next one. The Ask pane re-reads the
  run's files when a loaded question names one its list lacks, since the
  stream's replay can land before the pane exists and be dropped.

Tests: `filetest` (the chat claim all or nothing, its run and chat, its
names past the chat's whatever the creation order, the unclaim of exactly
that claim, the CHECKs and the unique name; Mem and Postgres),
`filestore_more_test.go` (`TestClaimForChatLimits`), `asktest`
(`attachment_ids` round trip), `deliver_test.go` (`TestAttachmentPaths`:
chat folders by their stored names, `-2`, order), `chatfiles_test.go` in
`engine` (live and parked take-over, `TestAChatFileKeepsItsPath`: two
`image.png` sent against their creation order, in both chats, keep path
and contents after the second claim and a re-delivery; the next stage's
ATTACHED FILES, `DeliverAskFiles`, a failed write undone) and in
`httpapi` (400 on a refused id, nothing
recorded, files alone), `ask/service_test.go` (delivery before recording,
the prompt's note, a refusal frees the thread and opens none); in the UI
`AttachFiles.test.ts` (`add()`, disabled), `Composer.test.ts` and
`AskView.test.ts` (the upload lock, the load re-read), `SessionView.test.ts` (chips on a take-over message),
`transcriptItems.test.ts`, `store.svelte.test.ts`, `RecordView.test.ts`,
`index.test.ts`, `mappers.test.ts`.
