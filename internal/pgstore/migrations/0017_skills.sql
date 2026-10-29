-- Skills and skillsets (2026-10-29, TODO 38): a user's library of Agent
-- Skills folders (SKILL.md + support files), grouped into skillsets a run
-- picks at creation. Skills are mutable rows whose bytes live in the blob
-- store (files is the manifest: path, sha256, size, mode). Skillsets are
-- versioned: every change to a member's content or to the membership that
-- changes the set's hash appends an immutable version (seq 1, 2, …) whose
-- manifest snapshots each member's files — the run pins (skillset, seq).
-- Ownership is the plain user_id column every configured row carries
-- (0013); there is no organisation level.

CREATE TABLE IF NOT EXISTS skills (
    id          text PRIMARY KEY,
    user_id     text NOT NULL DEFAULT '',
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    files       jsonb NOT NULL DEFAULT '[]',
    hash        text NOT NULL,
    source      jsonb,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS skills_owner_name_idx ON skills (user_id, name);
-- A remote skill is imported once per owner: the same folder URL is the
-- same skill (refresh it, don't import it again).
CREATE UNIQUE INDEX IF NOT EXISTS skills_owner_source_idx ON skills (user_id, (source->>'url')) WHERE source IS NOT NULL;

CREATE TABLE IF NOT EXISTS skillsets (
    id          text PRIMARY KEY,
    user_id     text NOT NULL DEFAULT '',
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    source      jsonb,
    head_seq    integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS skillsets_owner_name_idx ON skillsets (user_id, name);

-- Current membership, ordered. RESTRICT: a skill a skillset still lists is
-- not deletable (the API answers 409, naming the sets).
CREATE TABLE IF NOT EXISTS skillset_skills (
    skillset_id text NOT NULL REFERENCES skillsets(id) ON DELETE CASCADE,
    skill_id    text NOT NULL REFERENCES skills(id) ON DELETE RESTRICT,
    position    integer NOT NULL,
    PRIMARY KEY (skillset_id, skill_id)
);
CREATE INDEX IF NOT EXISTS skillset_skills_skill_idx ON skillset_skills (skill_id);

-- Immutable snapshots: manifest = [{skill_id, name, description, hash,
-- files:[{path, sha256, size, mode}]}]. Deleting the skillset takes its
-- versions with it (a live run blocks the delete; a finished run keeps
-- its pins in the event log and the bytes stay reachable through blobs
-- only while some version still names them).
CREATE TABLE IF NOT EXISTS skillset_versions (
    skillset_id text NOT NULL REFERENCES skillsets(id) ON DELETE CASCADE,
    seq         integer NOT NULL,
    hash        text NOT NULL,
    manifest    jsonb NOT NULL,
    created_at  timestamptz NOT NULL,
    PRIMARY KEY (skillset_id, seq)
);
