-- merge_request automations (flow v4): runs created on the merge requests
-- others open in a project's repository, once each. The kind joins the
-- check, and the repository it watches gets a column ("" = the primary).
ALTER TABLE automations DROP CONSTRAINT IF EXISTS automations_kind_check;
ALTER TABLE automations ADD CONSTRAINT automations_kind_check
    CHECK (kind IN ('tracker_query', 'cron', 'merge_request'));
ALTER TABLE automations ADD COLUMN IF NOT EXISTS repo text NOT NULL DEFAULT '';
