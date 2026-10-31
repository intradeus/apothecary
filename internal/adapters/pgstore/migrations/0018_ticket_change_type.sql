-- Ticket change type (2026-10-29, TODO 40): what a ticket is — feat, fix,
-- hotfix, perf, docs or chore (ports.ChangeType) — which its base branch is named after
-- (feat/proj-123-…), and the tracker's own word for an imported issue's type
-- ("Bug") it was read from. Existing tickets are features until edited.
ALTER TABLE tickets
    ADD COLUMN IF NOT EXISTS change_type text NOT NULL DEFAULT 'feat',
    ADD COLUMN IF NOT EXISTS source_type text NOT NULL DEFAULT '';
