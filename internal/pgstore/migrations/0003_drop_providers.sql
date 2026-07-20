-- Provider port removal (post-M8 amendment, see IMPLEMENTATION_NOTES):
-- the harness owns model auth; provider integrations and the projects
-- linkage are gone.
ALTER TABLE projects DROP COLUMN provider_ids;
DELETE FROM integrations WHERE port = 'provider';
