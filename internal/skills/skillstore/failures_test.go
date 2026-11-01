package skillstore

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/run/event"
	"apothecary/internal/skills"
)

var errDB = errors.New("db down")

// brokenDB fails what a test names; conflicts counts InsertVersion's refusals
// left to give.
type brokenDB struct {
	*Mem
	fail      map[string]bool
	conflicts int
}

func (d *brokenDB) err(name string) error {
	if d.fail[name] {
		return errDB
	}
	return nil
}

func (d *brokenDB) UpsertSkill(ctx context.Context, s Skill) error {
	if err := d.err("UpsertSkill"); err != nil {
		return err
	}
	return d.Mem.UpsertSkill(ctx, s)
}

func (d *brokenDB) GetSkill(ctx context.Context, id string) (Skill, error) {
	if err := d.err("GetSkill"); err != nil {
		return Skill{}, err
	}
	return d.Mem.GetSkill(ctx, id)
}

func (d *brokenDB) GetSkillBySource(ctx context.Context, u, url string) (Skill, error) {
	if err := d.err("GetSkillBySource"); err != nil {
		return Skill{}, err
	}
	return d.Mem.GetSkillBySource(ctx, u, url)
}

func (d *brokenDB) DeleteSkill(ctx context.Context, id string) error {
	if err := d.err("DeleteSkill"); err != nil {
		return err
	}
	return d.Mem.DeleteSkill(ctx, id)
}

func (d *brokenDB) UpsertSkillset(ctx context.Context, s Skillset) error {
	if err := d.err("UpsertSkillset"); err != nil {
		return err
	}
	return d.Mem.UpsertSkillset(ctx, s)
}

func (d *brokenDB) GetSkillset(ctx context.Context, id string) (Skillset, error) {
	if err := d.err("GetSkillset"); err != nil {
		return Skillset{}, err
	}
	return d.Mem.GetSkillset(ctx, id)
}

func (d *brokenDB) ListSkillsets(ctx context.Context, u string) ([]Skillset, error) {
	if err := d.err("ListSkillsets"); err != nil {
		return nil, err
	}
	return d.Mem.ListSkillsets(ctx, u)
}

func (d *brokenDB) SkillsetsContaining(ctx context.Context, id string) ([]Skillset, error) {
	if err := d.err("SkillsetsContaining"); err != nil {
		return nil, err
	}
	return d.Mem.SkillsetsContaining(ctx, id)
}

func (d *brokenDB) InsertVersion(ctx context.Context, v Version) error {
	if d.conflicts > 0 {
		d.conflicts--
		return fmt.Errorf("seq taken: %w", confstore.ErrConflict)
	}
	if err := d.err("InsertVersion"); err != nil {
		return err
	}
	return d.Mem.InsertVersion(ctx, v)
}

func (d *brokenDB) GetVersion(ctx context.Context, id string, seq int) (Version, error) {
	if err := d.err("GetVersion"); err != nil {
		return Version{}, err
	}
	return d.Mem.GetVersion(ctx, id, seq)
}

func newBroken(t *testing.T) (*Store, *brokenDB) {
	t.Helper()
	s, mem := newStore(t)
	db := &brokenDB{Mem: mem, fail: map[string]bool{}}
	s.db = db
	return s, db
}

func wantErr(t *testing.T, name string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: err = %v, want %v", name, err, target)
	}
}

func TestNewStampsRealIDsAndUTC(t *testing.T) {
	s := New(NewMem(), filestore.NewBlobs(t.TempDir()))
	a, err := s.CreateSkill(context.Background(), "u1", skillIn("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateSkill(context.Background(), "u1", skillIn("beta"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.ID == b.ID || a.CreatedAt.Location() != time.UTC || a.CreatedAt.Nanosecond()%1000 != 0 {
		t.Errorf("skill = %+v", a)
	}
}

func TestSkillFailures(t *testing.T) {
	ctx := context.Background()
	s, db := newBroken(t)
	sk, err := s.CreateSkill(ctx, "u1", skillIn("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSkill(ctx, sk.ID); err != nil || got.Name != "alpha" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if got, err := s.ListSkills(ctx, "u1"); err != nil || len(got) != 1 {
		t.Errorf("list = %+v, %v", got, err)
	}
	_, err = s.UpdateSkill(ctx, "missing", skillIn("alpha"))
	wantErr(t, "update missing", err, confstore.ErrNotFound)
	_, err = s.UpdateSkill(ctx, sk.ID, SkillInput{Name: "alpha"})
	wantErr(t, "no files", err, confstore.ErrInvalid)
	_, err = s.SkillFiles(ctx, "missing")
	wantErr(t, "files missing", err, confstore.ErrNotFound)

	db.fail["UpsertSkill"] = true
	_, err = s.CreateSkill(ctx, "u1", skillIn("beta"))
	wantErr(t, "row", err, errDB)
	db.fail["UpsertSkill"] = false

	db.fail["SkillsetsContaining"] = true
	_, err = s.UpdateSkill(ctx, sk.ID, skillIn("alpha"))
	wantErr(t, "version policy", err, errDB)
	db.fail["SkillsetsContaining"] = false

	// Bytes first: a store that cannot hold them writes no row.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.blobs = filestore.NewBlobs(file)
	_, err = s.CreateSkill(ctx, "u1", skillIn("gamma"))
	if err == nil || !strings.Contains(err.Error(), "store SKILL.md") {
		t.Errorf("blob write = %v", err)
	}
	if _, err := db.GetSkillByName(ctx, "u1", "gamma"); !errors.Is(err, confstore.ErrNotFound) {
		t.Errorf("a row without its bytes: %v", err)
	}
}

func TestSkillFilesReadTheBlobs(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sk, err := s.CreateSkill(ctx, "u1", skillIn("alpha", envport.TreeFile{Path: "scripts/go.sh", Data: []byte("echo")}))
	if err != nil {
		t.Fatal(err)
	}
	files, err := s.SkillFiles(ctx, sk.ID)
	if err != nil || len(files) != 2 || string(files[1].Data) != "echo" || files[1].Mode != 0o755 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	s.blobs = filestore.NewBlobs(t.TempDir())
	_, err = s.SkillFiles(ctx, sk.ID)
	if err == nil || !strings.Contains(err.Error(), "skill alpha: SKILL.md") {
		t.Errorf("a lost blob = %v", err)
	}
}

func TestSkillsetValidation(t *testing.T) {
	ctx := context.Background()
	s, db := newBroken(t)
	mine, err := s.CreateSkill(ctx, "u1", skillIn("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.CreateSkill(ctx, "u2", skillIn("beta"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		in   SkillsetInput
		want string
	}{
		{"no name", SkillsetInput{Name: "  "}, "skillset name"},
		{"long name", SkillsetInput{Name: strings.Repeat("é", maxSkillsetName+1)}, "skillset name"},
		{"long description", SkillsetInput{Name: "s", Description: strings.Repeat("é", maxSkillsetDesc+1)}, "description"},
		{"missing member", SkillsetInput{Name: "s", SkillIDs: []string{"gone"}}, "skill gone not found"},
		// Another user's skill does not exist, as far as anyone can tell.
		{"another's member", SkillsetInput{Name: "s", SkillIDs: []string{theirs.ID}}, "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateSkillset(ctx, "u1", tc.in)
			wantErr(t, tc.name, err, confstore.ErrInvalid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
	// Exactly at the limits, in characters and not bytes.
	got, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: strings.Repeat("é", maxSkillsetName), Description: "  d  "})
	if err != nil || got.Description != "d" {
		t.Errorf("at the limit = %+v, %v", got, err)
	}

	_, err = s.UpdateSkillset(ctx, "missing", SkillsetInput{Name: "s"})
	wantErr(t, "update missing", err, confstore.ErrNotFound)

	db.fail["GetSkill"] = true
	_, err = s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "s", SkillIDs: []string{mine.ID}})
	wantErr(t, "member read", err, errDB)
	if errors.Is(err, confstore.ErrInvalid) {
		t.Errorf("a store failure is not the caller's: %v", err)
	}
	db.fail["GetSkill"] = false

	db.fail["UpsertSkillset"] = true
	_, err = s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "s"})
	wantErr(t, "row", err, errDB)
	db.fail["UpsertSkillset"] = false

	db.fail["InsertVersion"] = true
	_, err = s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "s2"})
	wantErr(t, "version", err, errDB)
}

func TestSkillsetsListAndDelete(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	set, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListSkillsets(ctx, "u1"); err != nil || len(got) != 1 {
		t.Errorf("list = %+v, %v", got, err)
	}
	if got, _ := s.ListSkillsets(ctx, "u2"); len(got) != 0 {
		t.Errorf("another's = %+v", got)
	}
	if got, err := s.GetSkillset(ctx, set.ID); err != nil || got.Name != "s" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if err := s.DeleteSkillset(ctx, set.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "again", s.DeleteSkillset(ctx, set.ID), confstore.ErrNotFound)
}

func TestGetVersion(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	a, err := s.CreateSkill(ctx, "u1", skillIn("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "s", SkillIDs: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSkill(ctx, a.ID, SkillInput{Name: "alpha", Files: []envport.TreeFile{{Path: "SKILL.md", Data: md("alpha", "edited")}}}); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetVersion(ctx, set.ID, 0); err != nil || v.Seq != 2 {
		t.Errorf("head = %+v, %v", v, err)
	}
	if v, err := s.GetVersion(ctx, set.ID, 1); err != nil || v.Seq != 1 {
		t.Errorf("v1 = %+v, %v", v, err)
	}
	_, err = s.GetVersion(ctx, "missing", 0)
	wantErr(t, "no set", err, confstore.ErrNotFound)
	_, err = s.GetVersion(ctx, set.ID, 9)
	wantErr(t, "no version", err, confstore.ErrNotFound)
	if vs, err := s.ListVersions(ctx, set.ID); err != nil || len(vs) != 2 || vs[0].Seq != 2 {
		t.Errorf("versions = %+v, %v", vs, err)
	}
}

func TestBumpRacesAndFailures(t *testing.T) {
	ctx := context.Background()
	s, db := newBroken(t)
	a, err := s.CreateSkill(ctx, "u1", skillIn("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "s", SkillIDs: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	edit := func(desc string) error {
		_, err := s.UpdateSkill(ctx, a.ID, SkillInput{Name: "alpha", Files: []envport.TreeFile{{Path: "SKILL.md", Data: md("alpha", desc)}}})
		return err
	}

	// The loser of a race reloads and retries once.
	db.conflicts = 1
	if err := edit("two"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSkillset(ctx, set.ID); got.HeadSeq != 2 {
		t.Errorf("head = %d", got.HeadSeq)
	}
	// A second loss is reported, not looped on.
	db.conflicts = 2
	wantErr(t, "two losses", edit("three"), confstore.ErrConflict)
	db.conflicts = 0

	// No new content, no version.
	if _, v, err := s.bump(ctx, set.ID); err != nil || !v {
		t.Fatalf("catch up: %v, %v", v, err)
	}
	head, changed, err := s.bump(ctx, set.ID)
	if err != nil || changed || head.Seq != 3 {
		t.Errorf("unchanged = %+v, %v, %v", head, changed, err)
	}

	_, _, err = s.bump(ctx, "missing")
	wantErr(t, "no set", err, confstore.ErrNotFound)
	db.fail["GetVersion"] = true
	_, _, err = s.bump(ctx, set.ID)
	wantErr(t, "head read", err, errDB)
	db.fail["GetVersion"] = false
	db.fail["GetSkill"] = true
	_, _, err = s.bump(ctx, set.ID)
	wantErr(t, "member read", err, errDB)
	db.fail["GetSkill"] = false

	// A head pointing at nothing is not a failure: the next version starts.
	if err := db.Mem.InsertVersion(ctx, Version{SkillsetID: set.ID, Seq: 9}); err != nil {
		t.Fatal(err)
	}
	db.Mem.versions[set.ID] = db.Mem.versions[set.ID][:3]
	if v, created, err := s.bump(ctx, set.ID); err != nil || !created || v.Seq != 10 {
		t.Errorf("after a lost head = %+v, %v, %v", v, created, err)
	}
}

func TestBumpAll(t *testing.T) {
	ctx := context.Background()
	s, db := newBroken(t)
	a, err := s.CreateSkill(ctx, "u1", skillIn("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "s", SkillIDs: []string{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// Written without a version, as a refresh does.
	if _, err := s.writeSkillQuiet(ctx, a, "alpha", []envport.TreeFile{{Path: "SKILL.md", Data: md("alpha", "new")}}, nil); err != nil {
		t.Fatal(err)
	}
	// By skill, by set and twice over: one version.
	if err := s.bumpAll(ctx, []string{a.ID, set.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSkillset(ctx, set.ID); got.HeadSeq != 2 {
		t.Errorf("head = %d", got.HeadSeq)
	}
	db.fail["SkillsetsContaining"] = true
	wantErr(t, "containing", s.bumpAll(ctx, []string{a.ID}), errDB)
	wantErr(t, "containing one", s.bumpContaining(ctx, a.ID), errDB)
	db.fail["SkillsetsContaining"] = false

	if _, err := s.writeSkillQuiet(ctx, a, "alpha", []envport.TreeFile{{Path: "SKILL.md", Data: md("alpha", "newer")}}, nil); err != nil {
		t.Fatal(err)
	}
	db.fail["InsertVersion"] = true
	wantErr(t, "bump", s.bumpAll(ctx, []string{set.ID}), errDB)
	wantErr(t, "bump one", s.bumpContaining(ctx, a.ID), errDB)
}

func TestResolveSkillsets(t *testing.T) {
	ctx := context.Background()
	s, db := newBroken(t)
	mine, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.CreateSkillset(ctx, "u2", SkillsetInput{Name: "theirs"})
	if err != nil {
		t.Fatal(err)
	}
	pins, err := s.ResolveSkillsets(ctx, "u1", []skills.Ref{{ID: mine.ID}, {ID: mine.ID, Seq: 9}})
	if err != nil || len(pins) != 1 || pins[0].Seq != 1 || pins[0].Name != "mine" || pins[0].Hash == "" {
		t.Fatalf("pins = %+v, %v", pins, err)
	}
	if got, err := s.ResolveSkillsets(ctx, "u1", nil); err != nil || got != nil {
		t.Errorf("no refs = %+v, %v", got, err)
	}
	_, err = s.ResolveSkillsets(ctx, "u1", []skills.Ref{{ID: theirs.ID}})
	wantErr(t, "another's", err, confstore.ErrNotFound)
	_, err = s.ResolveSkillsets(ctx, "u1", []skills.Ref{{ID: "missing"}})
	wantErr(t, "missing", err, confstore.ErrNotFound)
	_, err = s.ResolveSkillsets(ctx, "u1", []skills.Ref{{ID: mine.ID, Seq: 9}})
	wantErr(t, "no such version", err, confstore.ErrInvalid)
	if err == nil || !strings.Contains(err.Error(), "mine has no version v9") {
		t.Errorf("err = %v", err)
	}
	db.fail["GetVersion"] = true
	_, err = s.ResolveSkillsets(ctx, "u1", []skills.Ref{{ID: mine.ID}})
	wantErr(t, "version read", err, errDB)
	db.fail["GetSkillset"] = true
	_, err = s.ResolveSkillsets(ctx, "u1", []skills.Ref{{ID: mine.ID}})
	wantErr(t, "set read", err, errDB)
}

func TestSkillTrees(t *testing.T) {
	ctx := context.Background()
	s, db := newBroken(t)
	mk := func(user, name string) Skill {
		sk, err := s.CreateSkill(ctx, user, skillIn(name))
		if err != nil {
			t.Fatal(err)
		}
		return sk
	}
	alpha, beta := mk("u1", "beta"), mk("u1", "alpha")
	twin := mk("u2", "beta")
	one, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "one", SkillIDs: []string{alpha.ID, beta.ID}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "two", SkillIDs: []string{beta.ID}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateSkillset(ctx, "u2", SkillsetInput{Name: "other", SkillIDs: []string{twin.ID}})
	if err != nil {
		t.Fatal(err)
	}
	pins := []event.SkillsetPin{
		{ID: one.ID, Seq: 1}, {ID: two.ID, Seq: 1},
		{ID: "gone", Name: "deleted since", Seq: 1}, // skipped, the workspace still builds
		{ID: other.ID, Seq: 1},                      // another skill of a name already taken
	}
	trees, err := s.SkillTrees(ctx, pins)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tr := range trees {
		names = append(names, tr.Name)
	}
	if !reflect.DeepEqual(names, []string{"alpha", "beta"}) {
		t.Errorf("trees = %v", names)
	}

	db.fail["GetVersion"] = true
	_, err = s.SkillTrees(ctx, pins)
	wantErr(t, "version read", err, errDB)
	db.fail["GetVersion"] = false

	s.blobs = filestore.NewBlobs(t.TempDir())
	if _, err := s.SkillTrees(ctx, pins); err == nil {
		t.Error("lost bytes are a failure, not a partial tree")
	}
}

func TestVersionNeedsItsSkillset(t *testing.T) {
	err := NewMem().InsertVersion(context.Background(), Version{SkillsetID: "gone", Seq: 1})
	wantErr(t, "insert", err, confstore.ErrInvalid)
}

// --- import -------------------------------------------------------------------

func singleSkill(name string) FetchFunc {
	return func(context.Context, Source) (Fetched, error) {
		return Fetched{Commit: "c1", Notes: []string{"link: skipped"}, Files: []envport.TreeFile{{Path: "SKILL.md", Data: md(name, "d")}}}, nil
	}
}

const repoURL = "https://github.com/o/r/tree/main/skills"

func TestImportRefusals(t *testing.T) {
	ctx := context.Background()
	empty := func(context.Context, Source) (Fetched, error) { return Fetched{}, nil }
	for _, tc := range []struct {
		name   string
		url    string
		fetch  FetchFunc
		target error
		want   string
	}{
		{"bad url", "ftp://x", empty, confstore.ErrInvalid, "not an http(s) URL"},
		{"download failed", repoURL, func(context.Context, Source) (Fetched, error) { return Fetched{}, errors.New("HTTP 500") }, confstore.ErrInvalid, "HTTP 500"},
		{"nothing there", repoURL, empty, confstore.ErrInvalid, "nothing found"},
		{"broken SKILL.md", repoURL, func(context.Context, Source) (Fetched, error) {
			return Fetched{Files: []envport.TreeFile{{Path: "SKILL.md", Data: []byte("no frontmatter")}}}, nil
		}, confstore.ErrInvalid, "SKILL.md"},
		{"no skills in the folder", repoURL, func(context.Context, Source) (Fetched, error) {
			return Fetched{Files: []envport.TreeFile{{Path: "docs/readme.md", Data: []byte("x")}}}, nil
		}, confstore.ErrInvalid, "no skills found"},
		{"only unusable children", repoURL, folderFetch("c", map[string]string{"Bad Name": "d"}), confstore.ErrInvalid, "no usable skills"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			res, err := (&Importer{Store: s, Fetch: tc.fetch}).Import(ctx, "u1", tc.url, "")
			wantErr(t, tc.name, err, tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
			if res.Skill != nil || res.Skillset != nil {
				t.Errorf("res = %+v", res)
			}
		})
	}
}

func TestImportSingleSkill(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	im := &Importer{Store: s, Fetch: singleSkill("alpha")}
	res, err := im.Import(ctx, "u1", repoURL, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Skill == nil || res.Skill.Name != "alpha" || res.Skill.Source.Commit != "c1" || res.Skill.Source.FetchedAt.IsZero() || len(res.Warnings) != 1 {
		t.Fatalf("res = %+v", res)
	}

	_, err = im.Import(ctx, "u1", repoURL, "")
	wantErr(t, "same source", err, confstore.ErrConflict)
	if err != nil && !strings.Contains(err.Error(), "as skill alpha") {
		t.Errorf("err = %v", err)
	}
	// Same skill name from elsewhere, the name is taken.
	_, err = im.Import(ctx, "u1", "https://github.com/o/other", "")
	wantErr(t, "same name", err, confstore.ErrConflict)
	if err != nil && !strings.Contains(err.Error(), "a skill named alpha") {
		t.Errorf("err = %v", err)
	}
	// Another owner has a library of their own.
	if _, err := im.Import(ctx, "u2", repoURL, ""); err != nil {
		t.Errorf("another owner = %v", err)
	}
}

func TestImportWriteFailures(t *testing.T) {
	ctx := context.Background()
	s, db := newBroken(t)
	im := &Importer{Store: s, Fetch: singleSkill("alpha")}
	db.fail["UpsertSkill"] = true
	_, err := im.Import(ctx, "u1", repoURL, "")
	wantErr(t, "row", err, errDB)
	db.fail["UpsertSkill"] = false

	db.fail["ListSkillsets"] = true
	_, err = im.Import(ctx, "u1", repoURL, "")
	wantErr(t, "list", err, errDB)
}

func TestImportFolderFailures(t *testing.T) {
	ctx := context.Background()
	two := folderFetch("c", map[string]string{"alpha": "A"})

	t.Run("a set of the name exists", func(t *testing.T) {
		s, _ := newStore(t)
		if _, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "skills"}); err != nil {
			t.Fatal(err)
		}
		_, err := (&Importer{Store: s, Fetch: two}).Import(ctx, "u1", repoURL, "")
		wantErr(t, "name", err, confstore.ErrConflict)
	})

	for _, tc := range []struct {
		name string
		fail string
	}{
		{"source lookup", "GetSkillBySource"},
		{"row", "UpsertSkillset"},
		{"version", "InsertVersion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := newBroken(t)
			// The top-level source lookup is best effort; a child's is not.
			db.fail[tc.fail] = true
			res, err := (&Importer{Store: s, Fetch: two}).Import(ctx, "u1", repoURL, "")
			wantErr(t, tc.name, err, errDB)
			if res.Skillset != nil {
				t.Errorf("res = %+v", res)
			}
		})
	}

	t.Run("a child that cannot be stored stops the import", func(t *testing.T) {
		s, db := newBroken(t)
		db.fail["UpsertSkill"] = true
		_, err := (&Importer{Store: s, Fetch: two}).Import(ctx, "u1", repoURL, "")
		wantErr(t, "row", err, errDB)
	})

	t.Run("the finished set must read back", func(t *testing.T) {
		s, _ := newStore(t)
		db := &failAfterVersion{Mem: s.db.(*Mem)}
		s.db = db
		_, err := (&Importer{Store: s, Fetch: two}).Import(ctx, "u1", repoURL, "")
		wantErr(t, "read back", err, errDB)
	})
}

// failAfterVersion stops reading skillsets once a version is cut.
type failAfterVersion struct {
	*Mem
	cut int
}

func (d *failAfterVersion) InsertVersion(ctx context.Context, v Version) error {
	d.cut++
	return d.Mem.InsertVersion(ctx, v)
}

func (d *failAfterVersion) GetSkillset(ctx context.Context, id string) (Skillset, error) {
	if d.cut > 0 {
		return Skillset{}, errDB
	}
	return d.Mem.GetSkillset(ctx, id)
}

// A folder of skills next to what the library already holds: a child imported
// on its own is adopted and refreshed, a child whose name is another skill's
// is left out and named.
func TestImportFolderAdoptsAndSkips(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	im := &Importer{Store: s, Fetch: singleSkill("alpha")}
	first, err := im.Import(ctx, "u1", repoURL+"/alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := s.CreateSkill(ctx, "u1", skillIn("beta"))
	if err != nil {
		t.Fatal(err)
	}
	im.Fetch = folderFetch("c2", map[string]string{"alpha": "fresh", "beta": "theirs", "gamma": "G"})
	res, err := im.Import(ctx, "u1", repoURL, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Skillset == nil || len(res.Skills) != 2 || res.Skills[0].ID != first.Skill.ID || res.Skills[0].Description != "fresh" {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "beta: skipped, a skill with that name already exists") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if got, _ := s.GetSkill(ctx, mine.ID); got.Source != nil || got.Description != "does beta" {
		t.Errorf("the person's skill was touched: %+v", got)
	}
}

func TestReasonDropsTheSentinel(t *testing.T) {
	for in, want := range map[error]string{
		fmt.Errorf("x: %w", confstore.ErrInvalid):  "x",
		fmt.Errorf("y: %w", confstore.ErrConflict): "y",
		errors.New("plain"):                        "plain",
	} {
		if got := reason(in); got != want {
			t.Errorf("reason(%v) = %q", in, got)
		}
	}
}

func TestChildDirs(t *testing.T) {
	got := childDirs([]envport.TreeFile{
		{Path: "README.md"},
		{Path: "z/SKILL.md", Mode: 0o644, Data: []byte("m")},
		{Path: "z/scripts/a.sh", Mode: 0o755},
		{Path: "a/SKILL.md", Data: []byte("m")},
		{Path: "docs/guide/SKILL.md", Data: []byte("m")}, // not directly under the child
	})
	if len(got) != 2 || got[0].name != "a" || got[1].name != "z" {
		t.Fatalf("children = %+v", got)
	}
	if len(got[1].files) != 2 || got[1].files[1].Path != "scripts/a.sh" || got[1].files[1].Mode != 0o755 {
		t.Errorf("re-rooted = %+v", got[1].files)
	}
	if fileAt(got[1].files, "nope") != nil {
		t.Error("no such file")
	}
}

func TestRefreshSkill(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	im := &Importer{Store: s, Fetch: singleSkill("alpha")}
	res, err := im.Import(ctx, "u1", repoURL, "")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Skill.ID
	hand, err := s.CreateSkill(ctx, "u1", skillIn("hand"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = im.RefreshSkill(ctx, "missing")
	wantErr(t, "missing", err, confstore.ErrNotFound)
	_, err = im.RefreshSkill(ctx, hand.ID)
	wantErr(t, "not imported", err, confstore.ErrInvalid)

	im.Fetch = func(context.Context, Source) (Fetched, error) { return Fetched{}, errors.New("HTTP 503") }
	_, err = im.RefreshSkill(ctx, id)
	wantErr(t, "download", err, confstore.ErrInvalid)
	im.Fetch = func(context.Context, Source) (Fetched, error) {
		return Fetched{Files: []envport.TreeFile{{Path: "other.md"}}}, nil
	}
	_, err = im.RefreshSkill(ctx, id)
	if err == nil || !strings.Contains(err.Error(), "SKILL.md is gone") {
		t.Errorf("gone = %v", err)
	}
	im.Fetch = func(context.Context, Source) (Fetched, error) {
		return Fetched{Files: []envport.TreeFile{{Path: "SKILL.md", Data: []byte("bad")}}}, nil
	}
	_, err = im.RefreshSkill(ctx, id)
	wantErr(t, "frontmatter", err, confstore.ErrInvalid)

	im.Fetch = func(context.Context, Source) (Fetched, error) {
		return Fetched{Commit: "c2", Files: []envport.TreeFile{{Path: "SKILL.md", Data: md("alpha", "newer")}}}, nil
	}
	got, err := im.RefreshSkill(ctx, id)
	if err != nil || got.Description != "newer" || got.Source.Commit != "c2" {
		t.Errorf("refreshed = %+v, %v", got, err)
	}
}

func TestRefreshSkillsetFailures(t *testing.T) {
	ctx := context.Background()
	s, db := newBroken(t)
	state := map[string]string{"alpha": "A", "beta": "B"}
	im := &Importer{Store: s, Fetch: folderFetch("c1", state)}
	res, err := im.Import(ctx, "u1", repoURL, "")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Skillset.ID
	hand, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "hand"})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = im.RefreshSkillset(ctx, "missing")
	wantErr(t, "missing", err, confstore.ErrNotFound)
	_, _, err = im.RefreshSkillset(ctx, hand.ID)
	wantErr(t, "not imported", err, confstore.ErrInvalid)

	im.Fetch = func(context.Context, Source) (Fetched, error) { return Fetched{}, errors.New("HTTP 503") }
	_, _, err = im.RefreshSkillset(ctx, id)
	wantErr(t, "download", err, confstore.ErrInvalid)
	im.Fetch = folderFetch("c2", state)

	db.fail["GetSkillBySource"] = true
	_, _, err = im.RefreshSkillset(ctx, id)
	wantErr(t, "children", err, errDB)
	db.fail["GetSkillBySource"] = false

	db.fail["UpsertSkillset"] = true
	_, _, err = im.RefreshSkillset(ctx, id)
	wantErr(t, "row", err, errDB)
	db.fail["UpsertSkillset"] = false

	delete(state, "beta")
	im.Fetch = folderFetch("c3", state)
	db.fail["SkillsetsContaining"] = true
	_, _, err = im.RefreshSkillset(ctx, id)
	wantErr(t, "referrers", err, errDB)
	db.fail["SkillsetsContaining"] = false

	state["beta"] = "B"
	if _, _, err := im.RefreshSkillset(ctx, id); err != nil {
		t.Fatal(err)
	}
	delete(state, "beta")
	db.fail["DeleteSkill"] = true
	_, _, err = im.RefreshSkillset(ctx, id)
	wantErr(t, "delete", err, errDB)
	db.fail["DeleteSkill"] = false

	state["alpha"] = "A2"
	db.fail["InsertVersion"] = true
	_, _, err = im.RefreshSkillset(ctx, id)
	wantErr(t, "version", err, errDB)
}

// A member another set still lists leaves this set and stays in the library.
func TestRefreshSkillsetKeepsASharedMember(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	state := map[string]string{"alpha": "A", "beta": "B"}
	im := &Importer{Store: s, Fetch: folderFetch("c1", state)}
	res, err := im.Import(ctx, "u1", repoURL, "")
	if err != nil {
		t.Fatal(err)
	}
	var beta string
	for _, sk := range res.Skills {
		if sk.Name == "beta" {
			beta = sk.ID
		}
	}
	if _, err := s.CreateSkillset(ctx, "u1", SkillsetInput{Name: "keeper", SkillIDs: []string{beta}}); err != nil {
		t.Fatal(err)
	}
	delete(state, "beta")
	set, _, err := im.RefreshSkillset(ctx, res.Skillset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.SkillIDs) != 1 {
		t.Errorf("members = %v", set.SkillIDs)
	}
	if _, err := s.GetSkill(ctx, beta); err != nil {
		t.Errorf("a skill another set lists must stay: %v", err)
	}
}

// --- remote -------------------------------------------------------------------

func TestParseSourceURLEdges(t *testing.T) {
	for _, tc := range []struct {
		in, extra string
		want      string
	}{
		{"https://gitlab.com/onlyone", "", "needs a namespace and a project"},
		{"https://gitlab.com/g/p/-/blob", "", "a GitLab folder URL"},
		{"https://example.com/-/tree/main", "", "needs a namespace"},
		{"http://[bad", "", "not an http(s) URL"},
		{"https://", "", "not an http(s) URL"},
		{"  ", "", "a URL is required"},
		{"https://github.com/o/r/tree", "", "a GitHub folder URL"},
	} {
		if got, err := ParseSourceURL(tc.in, tc.extra); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q = %+v, %v", tc.in, got, err)
		}
	}
	got, err := ParseSourceURL("https://www.github.com/o/r.git", "")
	if err != nil || got.Host != "github.com" || got.Repo != "r" {
		t.Errorf("www = %+v, %v", got, err)
	}
	got, err = ParseSourceURL("http://git.example:8080/g/s/p.git/-/tree/v1/a/b", "c")
	if err != nil || got.Owner != "g/s" || got.Repo != "p" || got.Path != "a/b/c" || got.URL != "http://git.example:8080/g/s/p/-/tree/v1/a/b/c" {
		t.Errorf("gitlab = %+v, %v", got, err)
	}
	got, err = ParseSourceURL("HTTPS://Example.com/x/Pack.TGZ?sig=abc", "sub/")
	if err != nil || got.Kind != "archive" || got.Host != "example.com" || got.URL != "https://Example.com/x/Pack.TGZ?sig=abc#sub" {
		t.Errorf("archive = %+v, %v", got, err)
	}
}

func TestDisplayName(t *testing.T) {
	for want, s := range map[string]Source{
		"pdf":    {Path: "skills/pdf", Repo: "r"},
		"r":      {Repo: "r"},
		"pack":   {URL: "https://x/dl/pack.tar.gz#sub"},
		"bundle": {URL: "https://x/dl/bundle.zip"},
		"tgz":    {URL: "https://x/dl/tgz.tgz"},
		"skills": {URL: ""},
	} {
		if got := s.displayName(); got != want {
			t.Errorf("%+v = %q, want %q", s, got, want)
		}
	}
}

func TestArchiveURLsEdges(t *testing.T) {
	// A named ref gets no fallbacks; a slashed one is escaped for the path and
	// dashed in the file name.
	got := archiveURLs(Source{Kind: "gitlab", Host: "gl.example", Owner: "g", Repo: "p", Ref: "rel/1"})
	if !reflect.DeepEqual(got, []string{"https://gl.example/g/p/-/archive/rel%2F1/p-rel-1.tar.gz"}) {
		t.Errorf("gitlab = %v", got)
	}
	if got := archiveURLs(Source{Kind: "archive", URL: "https://x/a.zip#sub"}); !reflect.DeepEqual(got, []string{"https://x/a.zip"}) {
		t.Errorf("archive = %v", got)
	}
}

func TestCleanFolderAndJoin(t *testing.T) {
	for in, want := range map[string]string{"": "", " /a/b/ ": "a/b"} {
		if got, err := cleanFolder(in); err != nil || got != want {
			t.Errorf("cleanFolder(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"..", "a/../b", "a//b"} {
		if _, err := cleanFolder(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, tc := range [][3]string{{"", "b", "b"}, {"a", "", "a"}, {"a", "b", "a/b"}} {
		if got := joinFolder(tc[0], tc[1]); got != tc[2] {
			t.Errorf("joinFolder = %q", got)
		}
	}
}

func TestFetchThroughAPublicArchive(t *testing.T) {
	ctx := context.Background()
	archive := tgz(t, "abc", entry{name: "top/SKILL.md", data: string(md("alpha", "d"))})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.tar.gz":
			_, _ = w.Write(archive)
		case "/down.tar.gz":
			w.WriteHeader(502)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := Fetch(ctx, Source{Kind: "archive", URL: srv.URL + "/ok.tar.gz#sub", Path: ""})
	if err != nil || got.Commit != "abc" || len(got.Files) != 1 || got.Files[0].Path != "SKILL.md" {
		t.Errorf("fetched = %+v, %v", got, err)
	}
	_, err = Fetch(ctx, Source{Kind: "archive", URL: srv.URL + "/missing.tar.gz"})
	if !errors.Is(err, errArchiveNotFound) || !strings.Contains(err.Error(), "is the repository public") {
		t.Errorf("404 = %v", err)
	}
	_, err = Fetch(ctx, Source{Kind: "archive", URL: srv.URL + "/down.tar.gz"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "/down.tar.gz") {
		t.Errorf("502 = %v", err)
	}
	if _, err := Fetch(ctx, Source{Kind: "archive", URL: "http://bad host/a.zip"}); err == nil {
		t.Error("an unparsable URL is an error")
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	u := gone.URL + "/a.tar.gz"
	gone.Close()
	if _, err := Fetch(ctx, Source{Kind: "archive", URL: u}); err == nil {
		t.Error("a dead host is an error")
	}
}

func TestDownloadPicksTheFormat(t *testing.T) {
	ctx := context.Background()
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	w, _ := zw.Create("SKILL.md")
	_, _ = w.Write(md("alpha", "d"))
	_ = zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipped.Bytes())
	}))
	defer srv.Close()
	// A .zip by its content type, whatever the path says.
	f, err := download(ctx, srv.Client(), srv.URL+"/archive", "")
	if err != nil || len(f.Files) != 1 {
		t.Errorf("zip = %+v, %v", f, err)
	}
}

func TestCorruptArchives(t *testing.T) {
	good := tgz(t, "", entry{name: "t/a", data: strings.Repeat("x", 4096)})
	for name, tc := range map[string]struct {
		read func() (Fetched, error)
		want string
	}{
		"not gzip": {func() (Fetched, error) { return readTarGz(strings.NewReader("plain text"), "") }, "not a gzip archive"},
		"gzip of nothing tar": {func() (Fetched, error) {
			return readTarGz(bytes.NewReader(gzipped(t, []byte("this is not a tar stream, padded well beyond one block "+strings.Repeat(".", 1024)))), "")
		}, "reading tar"},
		"cut short": {func() (Fetched, error) { return readTarGz(bytes.NewReader(good[:len(good)/2]), "") }, ""},
		"not zip":   {func() (Fetched, error) { return readZip(strings.NewReader("plain text"), "") }, "not a zip archive"},
		"zip read fails": {func() (Fetched, error) {
			return readZip(iotest.ErrReader(errors.New("connection reset")), "")
		}, "connection reset"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.read()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A tar's paths are the archive author's to choose. One climbing out of the
// folder is left out and named; one over the size limit is noted, unread.
func TestTarEntriesThatDoNotCount(t *testing.T) {
	big := strings.Repeat("x", int(skills.MaxFileBytes)+1)
	data := tgz(t, "",
		entry{name: "top/SKILL.md", data: "a"},
		entry{name: "top/../../etc/passwd", data: "root"},
		entry{name: "top/big.bin", data: big},
		entry{name: "top/sub/", kind: tar.TypeDir},
		entry{name: "top/fifo", kind: tar.TypeFifo},
	)
	f, err := readTarGz(bytes.NewReader(data), "")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, x := range f.Files {
		paths = append(paths, x.Path)
	}
	if strings.Contains(strings.Join(paths, " "), "passwd") {
		t.Errorf("a path climbed out: %v", paths)
	}
	notes := strings.Join(f.Notes, "\n")
	for _, want := range []string{"big.bin: skipped (", "fifo: skipped (not a regular file)"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes = %q", notes)
		}
	}
}

func TestZipEntriesThatDoNotCount(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, mode fs.FileMode, data []byte) {
		h := &zip.FileHeader{Name: name, Method: zip.Store}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(data)
	}
	add("SKILL.md", 0o644, []byte("a"))
	add("link", fs.ModeSymlink|0o777, []byte("SKILL.md"))
	add("big.bin", 0o644, make([]byte, skills.MaxFileBytes+1))
	add("../escape", 0o644, []byte("x"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := readZip(bytes.NewReader(buf.Bytes()), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Files) != 1 || f.Files[0].Path != "SKILL.md" {
		t.Errorf("files = %+v", f.Files)
	}
	notes := strings.Join(f.Notes, "\n")
	for _, want := range []string{"link: skipped (not a regular file)", "big.bin: skipped (", "escape: skipped (unusable path)"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes = %q", notes)
		}
	}
}

func TestSelect(t *testing.T) {
	e := func(p string) rawEntry { return rawEntry{path: p, data: []byte("x"), size: 1} }
	files := func(f Fetched) []string {
		var out []string
		for _, x := range f.Files {
			out = append(out, x.Path)
		}
		return out
	}
	// No shared top directory: nothing is stripped.
	if got := files(select_([]rawEntry{e("a/x"), e("b/y")}, "")); !reflect.DeepEqual(got, []string{"a/x", "b/y"}) {
		t.Errorf("two tops = %v", got)
	}
	if got := files(select_([]rawEntry{e("x"), e("a/y")}, "")); !reflect.DeepEqual(got, []string{"x", "a/y"}) {
		t.Errorf("a file at the top = %v", got)
	}
	if got := files(select_([]rawEntry{e("top/a/x"), e("top/b/y"), e("top/a")}, "a")); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("folder = %v", got)
	}
	if got := select_(nil, ""); got.Files != nil {
		t.Errorf("nothing = %+v", got)
	}
}

func TestCappedReader(t *testing.T) {
	read := func(data string, left int64) (string, error) {
		out, err := io.ReadAll(&cappedReader{r: strings.NewReader(data), left: left})
		return string(out), err
	}
	if got, err := read("abc", 10); got != "abc" || err != nil {
		t.Errorf("under = %q, %v", got, err)
	}
	if _, err := read("abcd", 3); !errors.Is(err, errTooLarge) {
		t.Errorf("over = %v", err)
	}
	if n, err := (&cappedReader{r: strings.NewReader("x"), left: 0}).Read(make([]byte, 1)); n != 0 || !errors.Is(err, errTooLarge) {
		t.Errorf("spent = %d, %v", n, err)
	}
}

// The peek in Read exists to tell an archive exactly at the cap from one over
// it: at the cap, the next read is the end of the file.
func TestCappedReaderExactlyAtTheCap(t *testing.T) {
	out, err := io.ReadAll(&cappedReader{r: strings.NewReader("abc"), left: 3})
	if string(out) != "abc" || err != nil {
		t.Errorf("at the cap = %q, %v", out, err)
	}
}

func TestIsArchiveName(t *testing.T) {
	for name, want := range map[string]bool{"a.tar.gz": true, "A.TGZ": true, "a.zip": true, "a.tar": false, "zip": false} {
		if got := isArchiveName(name); got != want {
			t.Errorf("%s = %v", name, got)
		}
	}
}
