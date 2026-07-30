-- The default_flow FK (0004) predates flow layering: built-in flows
-- (internal/builtin) resolve by name WITHOUT a flows row — the DB shadows
-- them, it never contains them — so the FK rejected any project whose
-- default_flow is a built-in ("default"), right after the service layer
-- validated it against exactly that layering. The race 0004 closed moves
-- into the statements themselves: UpsertProject only lands while the flow
-- still resolves (DB row or compiled-in name), and DeleteFlow only deletes
-- while no project references the name.
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_default_flow_fkey;
