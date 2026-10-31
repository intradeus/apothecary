-- A ping says which phase it belongs to, and whether giving up on a debug
-- decision ends that phase run rather than the run (flow v4: a reaction's
-- dead end is one round over, never the run's end) — so the inbox can word
-- the button for what actually happens. Projected from ping_raised.phase and
-- .ends_phase; applying this migration rebuilds the projections, which fills
-- both for every existing ping.
ALTER TABLE pings ADD COLUMN IF NOT EXISTS phase      text    NOT NULL DEFAULT '';
ALTER TABLE pings ADD COLUMN IF NOT EXISTS ends_phase boolean NOT NULL DEFAULT false;
