-- A workshop shared with other accounts: each member's role on it. The
-- owner (projects.user_id) is implicit and never a row here. A member goes
-- with the workshop and with the account.

CREATE TABLE project_members (
    project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('viewer', 'operator', 'maintainer')),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_members_user_idx ON project_members (user_id);
