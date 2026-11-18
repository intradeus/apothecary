-- An account's instance access is a role, not a flag: admin (a member who
-- also manages accounts), member (today's user) and guest (only what is
-- shared with it). Every admin stays one; everyone else is a member.

ALTER TABLE users ADD COLUMN role text NOT NULL DEFAULT 'member'
    CHECK (role IN ('admin', 'member', 'guest'));

UPDATE users SET role = 'admin' WHERE admin;

ALTER TABLE users DROP COLUMN admin;
