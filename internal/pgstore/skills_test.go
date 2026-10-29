package pgstore

import (
	"context"
	"testing"
	"time"

	"apothecary/internal/skills"
	"apothecary/internal/skillstore"
	"apothecary/internal/skillstore/skilltest"
)

func openSkills(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE skills, skillsets, skillset_skills, skillset_versions CASCADE`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSkillsContract(t *testing.T) {
	skilltest.RunDBContract(t, func(t *testing.T) skillstore.DB { return openSkills(t) })
}

// Every blob a skill's manifest or a version's snapshot names is live for
// the sweep — both tables, or edited-away bytes an old version still pins
// would vanish after the grace hour.
func TestLiveBlobSHAsIncludeSkills(t *testing.T) {
	s := openSkills(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	cur := "1111111111111111111111111111111111111111111111111111111111111111"
	old := "2222222222222222222222222222222222222222222222222222222222222222"
	sk := skillstore.Skill{ID: "s1", UserID: "u1", Name: "alpha", Hash: "h",
		Files: []skills.FileEntry{{Path: "SKILL.md", SHA256: cur, Size: 1, Mode: 0o644}}, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertSkill(ctx, sk); err != nil {
		t.Fatal(err)
	}
	set := skillstore.Skillset{ID: "set1", UserID: "u1", Name: "Set", SkillIDs: []string{"s1"}, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertSkillset(ctx, set); err != nil {
		t.Fatal(err)
	}
	v := skillstore.Version{SkillsetID: "set1", Seq: 1, Hash: "v", CreatedAt: now,
		Members: []skillstore.VersionMember{{SkillID: "s1", Name: "alpha", Hash: "h0",
			Files: []skills.FileEntry{{Path: "SKILL.md", SHA256: old, Size: 1, Mode: 0o644}}}}}
	if err := s.InsertVersion(ctx, v); err != nil {
		t.Fatal(err)
	}
	live, err := s.LiveBlobSHAs(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !live[cur] || !live[old] {
		t.Errorf("live = %v, want both %s and %s", live, cur[:4], old[:4])
	}
}
