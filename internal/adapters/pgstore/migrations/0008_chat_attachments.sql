-- Files in the run's chats: a take-over message and an Ask question can
-- carry attachments. Such a file is owned by its run like a goal file, and
-- attachments.chat says which chat it came in on, so it is neither listed
-- with the task's files nor read as an answer's. Every existing row keeps
-- chat NULL, so neither CHECK touches one.
--
-- An Ask question records the files it carried, as message_queued does on
-- the run's log.
ALTER TABLE attachments ADD COLUMN chat text
    CHECK (chat IN ('takeover', 'ask')),
    ADD CONSTRAINT attachments_chat_owner_check
    CHECK (chat IS NULL OR (run_id IS NOT NULL AND ping_id IS NULL));

ALTER TABLE ask_messages ADD COLUMN attachment_ids text[] NOT NULL DEFAULT '{}';
