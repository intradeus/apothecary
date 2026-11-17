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
  reference. Paths are computed over the chat's whole folder in creation
  order (`attachmentPaths`, the 2026-11-16 naming rule), so a second
  `image.png` is `image-2.png` and a restart's re-delivery
  (`deliverRunAttachments`) lands each file where the agent was told.
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
  sweep.
- **Files alone are a message**: text is required only with no files.
  `message_queued` and an Ask question record their `attachment_ids`, so
  the transcript and the Ask pane show the files as chips after a reload.

Tests: `filetest` (the chat claim all or nothing, its run and chat, the
CHECKs; Mem and Postgres), `filestore_more_test.go`
(`TestClaimForChatLimits`), `asktest` (`attachment_ids` round trip),
`deliver_test.go` (`TestAttachmentPaths`: chat folders, `-2`, order),
`chatfiles_test.go` in `engine` (live and parked take-over, the next
stage's ATTACHED FILES, `DeliverAskFiles`) and in `httpapi` (400 on a
refused id, nothing recorded, files alone), `ask/service_test.go`
(delivery before recording, the prompt's note, a refusal frees the
thread); in the UI `AttachFiles.test.ts` (`add()`), `Composer.test.ts`,
`AskView.test.ts`, `SessionView.test.ts` (chips on a take-over message),
`transcriptItems.test.ts`, `store.svelte.test.ts`, `RecordView.test.ts`,
`index.test.ts`, `mappers.test.ts`.
