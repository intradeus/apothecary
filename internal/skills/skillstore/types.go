// Package skillstore is a user's skill library: skills (Agent Skills
// folders whose bytes live in the blob store) and the versioned skillsets
// that group them. It owns the version policy — a skillset version is cut
// whenever its content hash changes — and hands the engine the trees a run
// pins. Storage is behind DB (pgstore, and Mem for tests); the format rules
// are package skills.
package skillstore

import (
	"context"
	"time"

	"apothecary/internal/skills"
)

// Source is where a remote skill or skillset came from, enough to fetch it
// again: the URL the user gave and its parsed parts, plus what the last
// fetch found.
type Source struct {
	Kind      string    `json:"kind"` // github | gitlab | archive
	URL       string    `json:"url"`
	Host      string    `json:"host,omitempty"`
	Owner     string    `json:"owner,omitempty"`
	Repo      string    `json:"repo,omitempty"`
	Ref       string    `json:"ref,omitempty"`
	Path      string    `json:"path,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Skill is a library entry: the current content (manifest + hash; the
// bytes are blobs) and, for an imported skill, its source.
type Skill struct {
	ID          string             `json:"id"`
	UserID      string             `json:"user_id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Files       []skills.FileEntry `json:"files"`
	Hash        string             `json:"hash"`
	Source      *Source            `json:"source,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// Skillset groups skills. SkillIDs is the CURRENT membership in order;
// HeadSeq is the latest version's number (0 only before the first cut,
// which Create performs).
type Skillset struct {
	ID          string    `json:"id"`
	UserID      string    `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Source      *Source   `json:"source,omitempty"`
	HeadSeq     int       `json:"head_seq"`
	SkillIDs    []string  `json:"skill_ids"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// VersionMember is one skill as a version snapshotted it.
type VersionMember struct {
	SkillID     string             `json:"skill_id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Hash        string             `json:"hash"`
	Files       []skills.FileEntry `json:"files"`
}

// Version is an immutable skillset snapshot: v{Seq} with its content hash
// and members.
type Version struct {
	SkillsetID string          `json:"skillset_id"`
	Seq        int             `json:"seq"`
	Hash       string          `json:"hash"`
	Members    []VersionMember `json:"members"`
	CreatedAt  time.Time       `json:"created_at"`
}

// DB is the persistence port. Errors are confstore's: ErrNotFound,
// ErrConflict (a unique clash), ErrReferenced (a delete something still
// needs), ErrInvalid (a reference to a missing row).
type DB interface {
	// UpsertSkill inserts or updates by id. A (user, name) or (user,
	// source url) clash with another row is ErrConflict.
	UpsertSkill(ctx context.Context, s Skill) error
	GetSkill(ctx context.Context, id string) (Skill, error)
	// GetSkillByName / GetSkillBySource find one owner's skill by its name
	// or by the source URL it was imported from (ErrNotFound otherwise).
	GetSkillByName(ctx context.Context, userID, name string) (Skill, error)
	GetSkillBySource(ctx context.Context, userID, url string) (Skill, error)
	// ListSkills lists one owner's skills by name; "" lists every row.
	ListSkills(ctx context.Context, userID string) ([]Skill, error)
	// DeleteSkill refuses (ErrReferenced) while a skillset lists the skill.
	DeleteSkill(ctx context.Context, id string) error

	// UpsertSkillset inserts or updates the row AND replaces its membership
	// with SkillIDs in order, in one transaction. HeadSeq is never written
	// here (InsertVersion owns it). A missing skill id is ErrInvalid.
	UpsertSkillset(ctx context.Context, s Skillset) error
	GetSkillset(ctx context.Context, id string) (Skillset, error)
	ListSkillsets(ctx context.Context, userID string) ([]Skillset, error)
	// SkillsetsContaining lists the skillsets whose membership names the skill.
	SkillsetsContaining(ctx context.Context, skillID string) ([]Skillset, error)
	// DeleteSkillset refuses (ErrReferenced) while a live run pins the
	// skillset; otherwise the versions and membership go with it.
	DeleteSkillset(ctx context.Context, id string) error

	// InsertVersion appends v and moves the skillset's head to v.Seq, in
	// one transaction. An existing (skillset, seq) is ErrConflict.
	InsertVersion(ctx context.Context, v Version) error
	GetVersion(ctx context.Context, skillsetID string, seq int) (Version, error)
	// ListVersions lists newest first.
	ListVersions(ctx context.Context, skillsetID string) ([]Version, error)
}
