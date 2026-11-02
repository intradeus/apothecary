-- rebuild: projections
-- Review comments (2026-10-26): what a person said about the files a ping
-- showed — a gate over a plan and a folder of screenshots — one row per
-- comment, each on one file (path) and, on a text file, a stretch of its
-- lines (start_line 0: the whole file — always, on an image). A projection
-- of ping_answered.review, rebuilt with the pings it belongs to.
CREATE TABLE IF NOT EXISTS ping_review_comments (
    ping_id        text NOT NULL,
    run_id         text NOT NULL,
    idx            int  NOT NULL,
    path           text NOT NULL,
    start_line     int  NOT NULL DEFAULT 0,
    end_line       int  NOT NULL DEFAULT 0,
    heading        text NOT NULL DEFAULT '',
    quote          text NOT NULL DEFAULT '',
    body           text NOT NULL,
    attachment_ids jsonb,
    PRIMARY KEY (ping_id, idx)
);
CREATE INDEX IF NOT EXISTS ping_review_comments_path_idx ON ping_review_comments (run_id, path);
