-- Sandbox sizes (docs/sandbox-runtimes-plan.md, phase 2): the size a
-- project that sets none gets, and the most any project may ask for. CPU in
-- thousandths of a core, memory in MiB; 0 is unset (no default, no ceiling).
-- A project's own size lives in its workspace configuration.

ALTER TABLE instance_limits
    ADD COLUMN sandbox_default_milli_cpu  integer NOT NULL DEFAULT 0 CHECK (sandbox_default_milli_cpu >= 0),
    ADD COLUMN sandbox_default_memory_mib integer NOT NULL DEFAULT 0 CHECK (sandbox_default_memory_mib >= 0),
    ADD COLUMN sandbox_max_milli_cpu      integer NOT NULL DEFAULT 0 CHECK (sandbox_max_milli_cpu >= 0),
    ADD COLUMN sandbox_max_memory_mib     integer NOT NULL DEFAULT 0 CHECK (sandbox_max_memory_mib >= 0);
