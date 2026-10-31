package skillstore

import (
	"context"
	"errors"
	"testing"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run/event"
	"apothecary/internal/skills"
)

func md(name, desc string) []byte {
	return []byte("---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n")
}

func newStore(t *testing.T) (*Store, *Mem) {
	t.Helper()
	mem := NewMem()
	s := New(mem, filestore.NewBlobs(t.TempDir()))
	n := 0
	s.newID = func() string { n++; return "id" + string(rune('0'+n)) }
	return s, mem
}

func skillIn(name string, extra ...envport.TreeFile) SkillInput {
	files := append([]envport.TreeFile{{Path: "SKILL.md", Data: md(name, "does "+name)}}, extra...)
	return SkillInput{Name: name, Files: files}
}

func TestVersionPolicy(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	a, err := s.CreateSkill(ctx, "u1", skillIn("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateSkill(ctx, "u1", skillIn("beta", envport.TreeFile{Path: "scripts/go.sh", Data: []byte("echo")}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Description != "does alpha" || len(b.Files) != 2 || b.Files[1].Mode != 0o755 {
		t.Fatalf("skills = %+v / %+v", a, b)
	}
	set, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "Set", SkillIDs: []string{a.ID, b.ID, a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if set.HeadSeq != 1 || len(set.SkillIDs) != 2 {
		t.Fatalf("create should cut v1 and dedupe members: %+v", set)
	}
	set2, _ := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "Other", SkillIDs: []string{a.ID}})
	v1, _ := s.GetVersion(ctx, set.ID, 0)

	// Same content → no version.
	if _, err := s.UpdateSkill(ctx, a.ID, skillIn("alpha")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSkillset(ctx, set.ID); got.HeadSeq != 1 {
		t.Errorf("same-content edit cut a version: head=%d", got.HeadSeq)
	}
	// Member edit → one version per containing set.
	if _, err := s.UpdateSkill(ctx, a.ID, skillIn("alpha", envport.TreeFile{Path: "references/x.md", Data: []byte("more")})); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSkillset(ctx, set.ID)
	got2, _ := s.GetSkillset(ctx, set2.ID)
	if got.HeadSeq != 2 || got2.HeadSeq != 2 {
		t.Errorf("member edit: heads = %d / %d, want 2 / 2", got.HeadSeq, got2.HeadSeq)
	}
	v2, _ := s.GetVersion(ctx, set.ID, 2)
	if v2.Hash == v1.Hash || len(v2.Members) != 2 || len(v2.Members[0].Files) != 2 {
		t.Errorf("v2 = %+v", v2)
	}
	// Reorder → nothing; membership change → version; rename → version.
	if _, err := s.UpdateSkillset(ctx, set.ID, SkillsetInput{Name: "Set", SkillIDs: []string{b.ID, a.ID}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSkillset(ctx, set.ID); got.HeadSeq != 2 {
		t.Errorf("reorder cut a version: head=%d", got.HeadSeq)
	}
	if _, err := s.UpdateSkillset(ctx, set.ID, SkillsetInput{Name: "Set", SkillIDs: []string{b.ID}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSkillset(ctx, set.ID); got.HeadSeq != 3 {
		t.Errorf("membership change: head=%d, want 3", got.HeadSeq)
	}
	if _, err := s.UpdateSkill(ctx, b.ID, skillIn("gamma", envport.TreeFile{Path: "scripts/go.sh", Data: []byte("echo")})); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSkillset(ctx, set.ID); got.HeadSeq != 4 {
		t.Errorf("rename: head=%d, want 4", got.HeadSeq)
	}
	vs, _ := s.ListVersions(ctx, set.ID)
	if len(vs) != 4 || vs[0].Seq != 4 || vs[0].Members[0].Name != "gamma" || vs[3].Members[1].Name != "beta" {
		t.Errorf("history = %+v", vs)
	}
	// Guards.
	if err := s.DeleteSkill(ctx, b.ID); !errors.Is(err, confstore.ErrReferenced) {
		t.Errorf("delete member = %v", err)
	}
	if _, err := s.CreateSkill(ctx, "u1", SkillInput{Name: "bad", Files: []envport.TreeFile{{Path: "SKILL.md", Data: []byte("no frontmatter")}}}); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("bad skill = %v", err)
	}
	if _, err := s.CreateSkillset(ctx, "u2", SkillsetInput{Name: "Theirs", SkillIDs: []string{a.ID}}); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("other user's skill as member = %v", err)
	}
	if _, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: " "}); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("blank name = %v", err)
	}
	files, err := s.SkillFiles(ctx, a.ID)
	if err != nil || len(files) != 2 || string(files[1].Data) != "more" {
		t.Errorf("files = %+v, %v", files, err)
	}
}

func TestResolveAndTrees(t *testing.T) {
	s, mem := newStore(t)
	ctx := context.Background()
	a, _ := s.CreateSkill(ctx, "u1", skillIn("alpha"))
	b, _ := s.CreateSkill(ctx, "u1", skillIn("beta", envport.TreeFile{Path: "scripts/go.sh", Data: []byte("echo")}))
	set1, _ := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "One", SkillIDs: []string{a.ID}})
	set2, _ := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "Two", SkillIDs: []string{a.ID, b.ID}})
	s.UpdateSkill(ctx, a.ID, skillIn("alpha", envport.TreeFile{Path: "x", Data: []byte("x")})) // set1 v2, set2 v2

	pins, err := s.ResolveSkillsets(ctx, "u1", []skills.Ref{{ID: set1.ID}, {ID: set2.ID, Seq: 1}, {ID: set1.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins[0].Seq != 2 || pins[0].Name != "One" || pins[1].Seq != 1 || pins[1].Hash == "" {
		t.Fatalf("pins = %+v", pins)
	}
	if _, err := s.ResolveSkillsets(ctx, "u2", []skills.Ref{{ID: set1.ID}}); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("other user = %v", err)
	}
	if _, err := s.ResolveSkillsets(ctx, "u1", []skills.Ref{{ID: set1.ID, Seq: 9}}); !errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("missing version = %v", err)
	}

	trees, err := s.SkillTrees(ctx, pins)
	if err != nil {
		t.Fatal(err)
	}
	// alpha once (set1 v2 has it with 2 files; set2 v1's older alpha is the same skill id → skipped), beta from set2 v1.
	if len(trees) != 2 || trees[0].Name != "alpha" || len(trees[0].Files) != 2 || trees[1].Name != "beta" || trees[1].Files[1].Mode != 0o755 {
		t.Fatalf("trees = %+v", trees)
	}
	// A vanished pinned version is skipped, not fatal.
	trees, err = s.SkillTrees(ctx, []event.SkillsetPin{{ID: "gone", Seq: 1}, pins[1]})
	if err != nil || len(trees) != 2 {
		t.Errorf("gone pin: trees=%d err=%v", len(trees), err)
	}
	mem.SkillsetPinnedBy = func(id string) []string { return []string{"run1"} }
	if err := s.DeleteSkillset(ctx, set1.ID); !errors.Is(err, confstore.ErrReferenced) {
		t.Errorf("pinned delete = %v", err)
	}
	mem.SkillsetPinnedBy = nil
	if err := s.DeleteSkillset(ctx, set1.ID); err != nil {
		t.Fatal(err)
	}
}
