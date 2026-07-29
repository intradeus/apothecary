-- FLOWLANG v3 (PLAN5 M26): agents moved INTO the flow document, so the
-- per-project agent library is retired. A flow now carries the definition of
-- every agent it runs, which makes it self-contained, checks its contracts at
-- parse time, and pins prompts with the rest of the document at run creation.
--
-- Agent versioning is not lost — it is subsumed by flow versioning: every
-- agent edit is now a flow edit, and flow_versions is already
-- content-addressed. stage_started.agent_hash keeps recording which prompt
-- version produced each attempt, computed from the flow's inline definition.
--
-- Any agent authored here must be pasted into the flow that runs it before
-- this migration (single-user MVP: no automated carry-over).
DROP TABLE IF EXISTS agent_versions;
DROP TABLE IF EXISTS agents;
