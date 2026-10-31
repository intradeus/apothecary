-- The built-in flow "default" is called "builder" (2026-10-24). What named
-- the built-in follows it: a project's default flow, its parameter binding,
-- its automations. A project's own flow named "default" (a saved copy) keeps
-- its name, and so does everything that names it. A no-op on a fresh
-- database and on a second run.
UPDATE projects p SET default_flow = 'builder'
 WHERE p.default_flow = 'default'
   AND NOT EXISTS (SELECT 1 FROM flows f WHERE f.project_id = p.id AND f.name = 'default');

UPDATE flow_bindings b SET flow_name = 'builder'
 WHERE b.flow_name = 'default'
   AND NOT EXISTS (SELECT 1 FROM flows f WHERE f.project_id = b.project_id AND f.name = 'default')
   AND NOT EXISTS (SELECT 1 FROM flow_bindings x
                    WHERE x.project_id = b.project_id AND x.flow_name = 'builder');

UPDATE automations a SET flow = 'builder'
 WHERE a.flow = 'default'
   AND NOT EXISTS (SELECT 1 FROM flows f WHERE f.project_id = a.project_id AND f.name = 'default');
