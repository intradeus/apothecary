// Package skilltest is the skillstore.DB contract, run against every
// implementation (Mem in skillstore's own tests, Postgres in pgstore's).
package skilltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"apothecary/internal/confstore"
	"apothecary/internal/skills"
	"apothecary/internal/skills/skillstore"
)

// RunDBContract exercises one DB implementation. open must return a fresh,
// empty store.
func RunDBContract(t *testing.T, open func(t *testing.T) skillstore.DB) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	entry := []skills.FileEntry{{Path: "SKILL.md", SHA256: sha("a"), Size: 1, Mode: 0o644}}
	skill := func(id, user, name string) skillstore.Skill {
		return skillstore.Skill{ID: id, UserID: user, Name: name, Description: "d", Files: entry, Hash: sha(name), CreatedAt: now, UpdatedAt: now}
	}

	t.Run("skills roundtrip, owner lists, unique name and source", func(t *testing.T) {
		db := open(t)
		ctx := context.Background()
		a := skill("s1", "u1", "alpha")
		a.Source = &skillstore.Source{Kind: "github", URL: "https://github.com/o/r/tree/main/alpha", Ref: "main", FetchedAt: now}
		if err := db.UpsertSkill(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertSkill(ctx, skill("s2", "u1", "beta")); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertSkill(ctx, skill("s3", "u2", "alpha")); err != nil {
			t.Fatalf("same name, other owner: %v", err)
		}
		got, err := db.GetSkill(ctx, "s1")
		if err != nil || got.Name != "alpha" || got.Source == nil || got.Source.URL != a.Source.URL || len(got.Files) != 1 || got.Files[0].Mode != 0o644 || !got.CreatedAt.Equal(now) {
			t.Fatalf("roundtrip = %+v, %v", got, err)
		}
		if err := db.UpsertSkill(ctx, skill("s4", "u1", "alpha")); !errors.Is(err, confstore.ErrConflict) {
			t.Errorf("duplicate name = %v, want ErrConflict", err)
		}
		dup := skill("s5", "u1", "gamma")
		dup.Source = &skillstore.Source{Kind: "github", URL: a.Source.URL, FetchedAt: now}
		if err := db.UpsertSkill(ctx, dup); !errors.Is(err, confstore.ErrConflict) {
			t.Errorf("duplicate source = %v, want ErrConflict", err)
		}
		a.Description = "edited"
		a.Hash = sha("edited")
		if err := db.UpsertSkill(ctx, a); err != nil {
			t.Fatalf("update: %v", err)
		}
		if got, _ := db.GetSkill(ctx, "s1"); got.Description != "edited" {
			t.Errorf("update not applied: %+v", got)
		}
		if by, err := db.GetSkillByName(ctx, "u1", "alpha"); err != nil || by.ID != "s1" {
			t.Errorf("by name = %+v, %v", by, err)
		}
		if _, err := db.GetSkillByName(ctx, "u2", "beta"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("by name, other owner = %v", err)
		}
		if by, err := db.GetSkillBySource(ctx, "u1", a.Source.URL); err != nil || by.ID != "s1" {
			t.Errorf("by source = %+v, %v", by, err)
		}
		if _, err := db.GetSkillBySource(ctx, "u1", "https://nope"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("by source, missing = %v", err)
		}
		mine, _ := db.ListSkills(ctx, "u1")
		all, _ := db.ListSkills(ctx, "")
		if len(mine) != 2 || mine[0].Name != "alpha" || mine[1].Name != "beta" || len(all) != 3 {
			t.Errorf("lists: mine=%d all=%d", len(mine), len(all))
		}
		if _, err := db.GetSkill(ctx, "nope"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("missing = %v", err)
		}
		if err := db.DeleteSkill(ctx, "s2"); err != nil {
			t.Fatal(err)
		}
		if err := db.DeleteSkill(ctx, "s2"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("second delete = %v", err)
		}
	})

	t.Run("skillsets: membership, restrict, versions, cascade", func(t *testing.T) {
		db := open(t)
		ctx := context.Background()
		for _, s := range []skillstore.Skill{skill("s1", "u1", "alpha"), skill("s2", "u1", "beta")} {
			if err := db.UpsertSkill(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		set := skillstore.Skillset{ID: "set1", UserID: "u1", Name: "Set", SkillIDs: []string{"s2", "s1"}, CreatedAt: now, UpdatedAt: now}
		if err := db.UpsertSkillset(ctx, set); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetSkillset(ctx, "set1")
		if err != nil || got.HeadSeq != 0 || len(got.SkillIDs) != 2 || got.SkillIDs[0] != "s2" || got.SkillIDs[1] != "s1" {
			t.Fatalf("skillset = %+v, %v", got, err)
		}
		bad := set
		bad.ID, bad.Name, bad.SkillIDs = "set2", "Bad", []string{"missing"}
		if err := db.UpsertSkillset(ctx, bad); !errors.Is(err, confstore.ErrInvalid) {
			t.Errorf("missing member = %v, want ErrInvalid", err)
		}
		if err := db.UpsertSkillset(ctx, skillstore.Skillset{ID: "set3", UserID: "u1", Name: "Set", CreatedAt: now, UpdatedAt: now}); !errors.Is(err, confstore.ErrConflict) {
			t.Errorf("duplicate name = %v, want ErrConflict", err)
		}
		if err := db.DeleteSkill(ctx, "s1"); !errors.Is(err, confstore.ErrReferenced) {
			t.Errorf("delete member = %v, want ErrReferenced", err)
		}
		sets, _ := db.SkillsetsContaining(ctx, "s1")
		if len(sets) != 1 || sets[0].ID != "set1" {
			t.Errorf("containing = %+v", sets)
		}

		v1 := skillstore.Version{SkillsetID: "set1", Seq: 1, Hash: sha("v1"), CreatedAt: now,
			Members: []skillstore.VersionMember{{SkillID: "s1", Name: "alpha", Description: "d", Hash: sha("alpha"), Files: entry}}}
		if err := db.InsertVersion(ctx, v1); err != nil {
			t.Fatal(err)
		}
		if err := db.InsertVersion(ctx, v1); !errors.Is(err, confstore.ErrConflict) {
			t.Errorf("duplicate seq = %v, want ErrConflict", err)
		}
		v2 := v1
		v2.Seq, v2.Hash = 2, sha("v2")
		if err := db.InsertVersion(ctx, v2); err != nil {
			t.Fatal(err)
		}
		if got, _ := db.GetSkillset(ctx, "set1"); got.HeadSeq != 2 {
			t.Errorf("head_seq = %d, want 2", got.HeadSeq)
		}
		// Re-upserting the row must not touch the head.
		set.Description = "x"
		set.SkillIDs = []string{"s1"}
		if err := db.UpsertSkillset(ctx, set); err != nil {
			t.Fatal(err)
		}
		if got, _ := db.GetSkillset(ctx, "set1"); got.HeadSeq != 2 || len(got.SkillIDs) != 1 || got.Description != "x" {
			t.Errorf("after upsert = %+v", got)
		}
		gv, err := db.GetVersion(ctx, "set1", 1)
		if err != nil || gv.Hash != sha("v1") || len(gv.Members) != 1 || gv.Members[0].Files[0].SHA256 != sha("a") {
			t.Errorf("version = %+v, %v", gv, err)
		}
		if _, err := db.GetVersion(ctx, "set1", 9); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("missing version = %v", err)
		}
		vs, _ := db.ListVersions(ctx, "set1")
		if len(vs) != 2 || vs[0].Seq != 2 || vs[1].Seq != 1 {
			t.Errorf("versions = %+v", vs)
		}
		if err := db.DeleteSkill(ctx, "s2"); err != nil {
			t.Fatalf("unlinked skill should delete: %v", err)
		}
		if err := db.DeleteSkillset(ctx, "set1"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetVersion(ctx, "set1", 1); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("versions should cascade: %v", err)
		}
		if err := db.DeleteSkill(ctx, "s1"); err != nil {
			t.Errorf("membership should cascade: %v", err)
		}
		if err := db.DeleteSkillset(ctx, "set1"); !errors.Is(err, confstore.ErrNotFound) {
			t.Errorf("second delete = %v", err)
		}
	})
}

func sha(s string) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = hexd[(int(s[i%len(s)])+i)%16]
	}
	return string(out)
}
