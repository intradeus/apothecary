-- Files in the run's chats: a take-over message and an Ask question can
-- carry attachments. Such a file is owned by its run like a goal file, and
-- attachments.chat says which chat it came in on, so it is neither listed
-- with the task's files nor read as an answer's. chat_name is the name it
-- is delivered as in that chat's folder (image.png, image-2.png), given
-- when its message is accepted and never changed after: unique in the run's
-- chat, so a file sent later never takes the path an earlier one was
-- announced at. Every existing row keeps both NULL, so no CHECK touches one.
--
-- An Ask question records the files it carried, as message_queued does on
-- the run's log.
ALTER TABLE attachments ADD COLUMN chat text
    CHECK (chat IN ('takeover', 'ask')),
    ADD COLUMN chat_name text,
    ADD CONSTRAINT attachments_chat_owner_check
    CHECK (chat IS NULL OR (run_id IS NOT NULL AND ping_id IS NULL)),
    ADD CONSTRAINT attachments_chat_name_check
    CHECK ((chat IS NULL) = (chat_name IS NULL));
CREATE UNIQUE INDEX attachments_chat_name_idx
    ON attachments (run_id, chat, chat_name) WHERE chat IS NOT NULL;

ALTER TABLE ask_messages ADD COLUMN attachment_ids text[] NOT NULL DEFAULT '{}';
