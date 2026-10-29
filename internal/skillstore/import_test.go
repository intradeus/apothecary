package skillstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"apothecary/internal/confstore"
	"apothecary/internal/ports"
)

// folderFetch scripts a remote folder: skill name → description, plus a
// stray file at the root.
func folderFetch(commit string, skills map[string]string) FetchFunc {
	return func(_ context.Context, src Source) (Fetched, error) {
		f := Fetched{Commit: commit}
		for name, desc := range skills {
			f.Files = append(f.Files,
				ports.TreeFile{Path: name + "/SKILL.md", Data: []byte("---\nname: " + name + "\ndescription: " + desc + "\n---\n")},
				ports.TreeFile{Path: name + "/scripts/go.sh", Data: []byte("echo " + name)})
		}
		f.Files = append(f.Files, ports.TreeFile{Path: "README.md", Data: []byte("x")})
		return f, nil
	}
}

func TestImportFolderThenRefresh(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	state := map[string]string{"alpha": "A", "beta": "B", "bad name": "C"}
	im := &Importer{Store: s, Fetch: folderFetch("c1", state)}

	res, err := im.Import(ctx, "u1", "https://github.com/o/r/tree/main/skills", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Skillset == nil || res.Skillset.Name != "skills" || res.Skillset.HeadSeq != 1 || len(res.Skills) != 2 || res.Skillset.Source.Commit != "c1" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "bad name: skipped, skill name") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	alpha, _ := s.db.GetSkillByName(ctx, "u1", "alpha")
	if alpha.Source == nil || alpha.Source.URL != "https://github.com/o/r/tree/main/skills/alpha" || alpha.Source.Path != "skills/alpha" {
		t.Errorf("alpha source = %+v", alpha.Source)
	}
	if _, err := im.Import(ctx, "u1", "github.com/o/r/tree/main/skills", ""); !errors.Is(err, confstore.ErrConflict) {
		t.Errorf("re-import = %v", err)
	}
	// Unchanged refresh: no version. Changed content: v2. A vanished child
	// is unlinked and, unreferenced elsewhere, deleted; a new one joins.
	set, warnings, err := im.RefreshSkillset(ctx, res.Skillset.ID)
	if err != nil || set.HeadSeq != 1 || len(warnings) != 1 {
		t.Errorf("unchanged refresh = %+v %v %v", set, warnings, err)
	}
	state["alpha"] = "A2"
	delete(state, "beta")
	state["gamma"] = "G"
	im.Fetch = folderFetch("c2", state)
	set, _, err = im.RefreshSkillset(ctx, res.Skillset.ID)
	if err != nil || set.HeadSeq != 2 || len(set.SkillIDs) != 2 || set.Source.Commit != "c2" {
		t.Fatalf("changed refresh = %+v, %v", set, err)
	}
	if _, err := s.db.GetSkillByName(ctx, "u1", "beta"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("beta should be deleted: %v", err)
	}
	if g, err := s.db.GetSkillByName(ctx, "u1", "gamma"); err != nil || g.Source == nil {
		t.Errorf("gamma should exist: %v", err)
	}
	v, _ := s.GetVersion(ctx, set.ID, 2)
	if len(v.Members) != 2 || v.Members[0].Name != "alpha" || v.Members[0].Description != "A2" {
		t.Errorf("v2 = %+v", v.Members)
	}
	vs, _ := s.ListVersions(ctx, set.ID)
	if len(vs) != 2 {
		t.Errorf("one refresh with three changes must cut ONE version, got %d", len(vs))
	}
}

func TestImportSingleSkillAndAdoption(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	single := func(_ context.Context, src Source) (Fetched, error) {
		return Fetched{Commit: "s1", Files: []ports.TreeFile{{Path: "SKILL.md", Data: []byte("---\nname: pdf\ndescription: P\n---\n")}}}, nil
	}
	im := &Importer{Store: s, Fetch: single}
	res, err := im.Import(ctx, "u1", "https://github.com/o/r/tree/main/skills/pdf", "")
	if err != nil || res.Skill == nil || res.Skill.Name != "pdf" || res.Skillset != nil {
		t.Fatalf("single = %+v, %v", res, err)
	}
	if _, err := im.Import(ctx, "u1", "https://github.com/o/r/tree/main/skills/pdf", ""); !errors.Is(err, confstore.ErrConflict) {
		t.Errorf("same source again = %v", err)
	}
	// Another folder that also has a "pdf" at a DIFFERENT source: skipped by name.
	im.Fetch = folderFetch("f1", map[string]string{"pdf": "other", "docx": "D"})
	res2, err := im.Import(ctx, "u1", "https://github.com/x/y/tree/main/pack", "")
	if err != nil || len(res2.Skills) != 1 || res2.Skills[0].Name != "docx" || len(res2.Warnings) != 1 || !strings.Contains(res2.Warnings[0], "pdf: skipped, a skill with that name") {
		t.Errorf("other folder = %+v, %v", res2, err)
	}
	// The folder the single skill came from: the child IS that skill, so the
	// folder import adopts it (same source URL), refreshing its content.
	im.Fetch = folderFetch("f2", map[string]string{"pdf": "P2", "xlsx": "X"})
	res3, err := im.Import(ctx, "u1", "https://github.com/o/r/tree/main/skills", "")
	if err != nil || len(res3.Skills) != 2 || len(res3.Warnings) != 0 {
		t.Fatalf("parent folder = %+v, %v", res3, err)
	}
	if got, _ := s.db.GetSkill(ctx, res.Skill.ID); got.Description != "P2" || got.Hash == res.Skill.Hash {
		t.Errorf("adopted skill not refreshed: %+v", got)
	}
	// Refreshing the single skill: content back from its own source.
	im.Fetch = single
	sk, err := im.RefreshSkill(ctx, res.Skill.ID)
	if err != nil || sk.Description != "P" || sk.Source.Commit != "s1" {
		t.Errorf("refresh single = %+v, %v", sk, err)
	}
	// Both sets listing pdf moved.
	if set, _ := s.GetSkillset(ctx, res3.Skillset.ID); set.HeadSeq != 2 {
		t.Errorf("parent set head = %d, want 2", set.HeadSeq)
	}
	local, _ := s.CreateSkill(ctx, "u1", skillIn("local"))
	if _, err := im.RefreshSkill(ctx, local.ID); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("refresh local = %v", err)
	}
	empty := func(context.Context, Source) (Fetched, error) {
		return Fetched{Files: []ports.TreeFile{{Path: "README.md", Data: []byte("x")}}}, nil
	}
	im.Fetch = empty
	if _, err := im.Import(ctx, "u1", "https://github.com/n/o", ""); !errors.Is(err, confstore.ErrInvalid) || !strings.Contains(err.Error(), "no skills found") {
		t.Errorf("no skills = %v", err)
	}
	im.Fetch = func(context.Context, Source) (Fetched, error) { return Fetched{}, errors.New("HTTP 500") }
	if _, err := im.Import(ctx, "u1", "https://github.com/n/o", ""); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("fetch failure = %v", err)
	}
}
