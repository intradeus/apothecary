-- Stop-on-idle (docs/sandbox-runtimes-plan.md, phase 6): how long a sandbox
-- nobody uses stays up when its project does not say, in minutes; 0 is
-- never. A project's own setting lives in its workspace configuration.

ALTER TABLE instance_limits
    ADD COLUMN sandbox_idle_minutes integer NOT NULL DEFAULT 0 CHECK (sandbox_idle_minutes >= 0);
