package skillstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/skills"
)

// Importer brings remote folders into the library and keeps them fresh.
//
// Two shapes, decided by what the folder holds: a SKILL.md at its root is
// ONE skill (unique per owner by name and by source URL); otherwise every
// immediate child directory with a SKILL.md is a skill and the folder is a
// new skillset. Refreshing re-downloads at the same ref: a changed skill
// is rewritten in place, a skillset gains/loses members as the folder did,
// and the version policy cuts a version only if the content hash moved.
type Importer struct {
	Store *Store
	Fetch FetchFunc
}

// ImportResult says what an import made: a single skill, or a skillset and
// its skills. Warnings name children that were left out and why.
type ImportResult struct {
	Skill    *Skill    `json:"skill,omitempty"`
	Skillset *Skillset `json:"skillset,omitempty"`
	Skills   []Skill   `json:"skills,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

func (im *Importer) fetch(ctx context.Context, src Source) (Source, Fetched, error) {
	f, err := im.Fetch(ctx, src)
	if err != nil {
		return src, f, invalid(err)
	}
	src.Commit = f.Commit
	src.FetchedAt = im.Store.now()
	return src, f, nil
}

// Import creates a skill or a skillset from a public folder URL.
func (im *Importer) Import(ctx context.Context, userID, rawURL, folder string) (ImportResult, error) {
	src, err := ParseSourceURL(rawURL, folder)
	if err != nil {
		return ImportResult{}, invalid(err)
	}
	if ex, err := im.Store.db.GetSkillBySource(ctx, userID, src.URL); err == nil {
		return ImportResult{}, fmt.Errorf("%s is already imported as skill %s (refresh it instead): %w", src.URL, ex.Name, confstore.ErrConflict)
	}
	sets, err := im.Store.db.ListSkillsets(ctx, userID)
	if err != nil {
		return ImportResult{}, err
	}
	for _, set := range sets {
		if set.Source != nil && set.Source.URL == src.URL {
			return ImportResult{}, fmt.Errorf("%s is already imported as skillset %s (refresh it instead): %w", src.URL, set.Name, confstore.ErrConflict)
		}
	}
	src, fetched, err := im.fetch(ctx, src)
	if err != nil {
		return ImportResult{}, err
	}
	if len(fetched.Files) == 0 {
		return ImportResult{}, invalidf("%s: nothing found under %q", src.URL, src.Path)
	}

	// One skill: the folder itself is a skill directory.
	if md := fileAt(fetched.Files, skills.ManifestFile); md != nil {
		fm, err := skills.ParseFrontmatter(md)
		if err != nil {
			return ImportResult{}, invalidf("%s: %v", skills.ManifestFile, err)
		}
		if _, err := im.Store.db.GetSkillByName(ctx, userID, fm.Name); err == nil {
			return ImportResult{}, fmt.Errorf("a skill named %s already exists: %w", fm.Name, confstore.ErrConflict)
		}
		now := im.Store.now()
		sk, err := im.Store.writeSkill(ctx, Skill{ID: im.Store.newID(), UserID: userID, CreatedAt: now}, fm.Name, fetched.Files, &src)
		if err != nil {
			return ImportResult{}, err
		}
		return ImportResult{Skill: &sk, Warnings: fetched.Notes}, nil
	}

	// A folder of skills: one per child directory with a SKILL.md.
	children := childDirs(fetched.Files)
	if len(children) == 0 {
		return ImportResult{}, invalidf("%s: no skills found (no %s at the folder's root or in any of its subfolders)", src.URL, skills.ManifestFile)
	}
	name := src.displayName()
	for _, set := range sets {
		if set.Name == name {
			return ImportResult{}, fmt.Errorf("a skillset named %s already exists: %w", name, confstore.ErrConflict)
		}
	}
	res := ImportResult{Warnings: fetched.Notes}
	ids, members, touched, warnings, err := im.reconcileChildren(ctx, userID, src, children)
	if err != nil {
		return ImportResult{}, err
	}
	res.Skills = members
	res.Warnings = append(res.Warnings, warnings...)
	if len(ids) == 0 {
		return ImportResult{}, invalidf("%s: no usable skills (%s)", src.URL, strings.Join(res.Warnings, "; "))
	}
	set := Skillset{ID: im.Store.newID(), UserID: userID, CreatedAt: im.Store.now(), Name: name,
		Description: "Imported from " + src.URL, SkillIDs: ids, Source: &src}
	set.UpdatedAt = set.CreatedAt
	if err := im.Store.db.UpsertSkillset(ctx, set); err != nil {
		return ImportResult{}, err
	}
	// One bump for the new set and for every other set an adopted child
	// (already in the library, now refreshed) belongs to.
	if err := im.Store.bumpAll(ctx, append(touched, set.ID)); err != nil {
		return ImportResult{}, err
	}
	out, err := im.Store.db.GetSkillset(ctx, set.ID)
	if err != nil {
		return ImportResult{}, err
	}
	res.Skillset = &out
	return res, nil
}

// reconcileChildren writes a folder's child skills for an owner: a child
// whose source URL is already in the library IS that skill (adopted and
// refreshed — a single-skill import of the same folder, or an earlier
// import of this set); a child whose name belongs to some other skill is
// skipped with a warning, as is one that fails validation. Returns the
// member ids in folder order, the member rows, the ids whose content was
// (re)written, and the warnings.
func (im *Importer) reconcileChildren(ctx context.Context, userID string, src Source, children []childDir) (ids []string, members []Skill, touched []string, warnings []string, err error) {
	for _, child := range children {
		csrc := childSource(src, child.name)
		cur, err := im.Store.db.GetSkillBySource(ctx, userID, csrc.URL)
		if err != nil && !errors.Is(err, confstore.ErrNotFound) {
			return nil, nil, nil, nil, err
		}
		if errors.Is(err, confstore.ErrNotFound) {
			if _, err := im.Store.db.GetSkillByName(ctx, userID, child.name); err == nil {
				warnings = append(warnings, fmt.Sprintf("%s: skipped, a skill with that name already exists", child.name))
				continue
			}
			cur = Skill{ID: im.Store.newID(), UserID: userID, CreatedAt: im.Store.now()}
		}
		sk, err := im.Store.writeSkillQuiet(ctx, cur, child.name, child.files, &csrc)
		if err != nil {
			if errors.Is(err, confstore.ErrInvalid) || errors.Is(err, confstore.ErrConflict) {
				warnings = append(warnings, fmt.Sprintf("%s: skipped, %s", child.name, reason(err)))
				continue
			}
			return nil, nil, nil, nil, err
		}
		ids = append(ids, sk.ID)
		members = append(members, sk)
		touched = append(touched, sk.ID)
	}
	return ids, members, touched, warnings, nil
}

// RefreshSkill re-downloads a single imported skill.
func (im *Importer) RefreshSkill(ctx context.Context, id string) (Skill, error) {
	cur, err := im.Store.db.GetSkill(ctx, id)
	if err != nil {
		return Skill{}, err
	}
	if cur.Source == nil {
		return Skill{}, invalidf("skill %s was not imported; there is nothing to refresh", cur.Name)
	}
	src, fetched, err := im.fetch(ctx, *cur.Source)
	if err != nil {
		return Skill{}, err
	}
	md := fileAt(fetched.Files, skills.ManifestFile)
	if md == nil {
		return Skill{}, invalidf("%s: %s is gone", src.URL, skills.ManifestFile)
	}
	fm, err := skills.ParseFrontmatter(md)
	if err != nil {
		return Skill{}, invalidf("%s: %v", skills.ManifestFile, err)
	}
	return im.Store.writeSkill(ctx, cur, fm.Name, fetched.Files, &src)
}

// RefreshSkillset re-downloads an imported folder and reconciles the set's
// members: changed skills are rewritten, new children added, vanished ones
// unlinked (and deleted when no other set lists them). One version is cut
// at the end if anything moved, not one per touched skill.
func (im *Importer) RefreshSkillset(ctx context.Context, id string) (Skillset, []string, error) {
	set, err := im.Store.db.GetSkillset(ctx, id)
	if err != nil {
		return Skillset{}, nil, err
	}
	if set.Source == nil {
		return Skillset{}, nil, invalidf("skillset %s was not imported; there is nothing to refresh", set.Name)
	}
	src, fetched, err := im.fetch(ctx, *set.Source)
	if err != nil {
		return Skillset{}, nil, err
	}
	children := childDirs(fetched.Files)
	ids, _, touched, warnings, err := im.reconcileChildren(ctx, set.UserID, src, children)
	if err != nil {
		return Skillset{}, nil, err
	}
	warnings = append(fetched.Notes, warnings...)
	kept := map[string]bool{}
	for _, id := range ids {
		kept[id] = true
	}
	// Members the folder no longer has.
	var gone []string
	for _, mid := range set.SkillIDs {
		if !kept[mid] {
			gone = append(gone, mid)
		}
	}
	set.Source = &src
	set.SkillIDs = ids
	set.UpdatedAt = im.Store.now()
	if err := im.Store.db.UpsertSkillset(ctx, set); err != nil {
		return Skillset{}, nil, err
	}
	for _, mid := range gone {
		others, err := im.Store.db.SkillsetsContaining(ctx, mid)
		if err != nil {
			return Skillset{}, nil, err
		}
		if len(others) == 0 {
			if err := im.Store.db.DeleteSkill(ctx, mid); err != nil && !errors.Is(err, confstore.ErrNotFound) {
				return Skillset{}, nil, err
			}
		}
	}
	if err := im.Store.bumpAll(ctx, append(touched, set.ID)); err != nil {
		return Skillset{}, nil, err
	}
	out, err := im.Store.db.GetSkillset(ctx, set.ID)
	return out, warnings, err
}

// reason strips the sentinel suffix a store error carries, for a warning
// that names what was wrong ("SKILL.md: description must be …", not
// "invalid").
func reason(err error) string {
	msg := err.Error()
	for _, s := range []error{confstore.ErrInvalid, confstore.ErrConflict} {
		msg = strings.TrimSuffix(msg, ": "+s.Error())
	}
	return msg
}

type childDir struct {
	name  string
	files []envport.TreeFile
}

// childDirs groups files by first path segment, keeping the directories
// that hold a SKILL.md directly, with paths re-rooted at the child.
func childDirs(files []envport.TreeFile) []childDir {
	byDir := map[string][]envport.TreeFile{}
	for _, f := range files {
		dir, rest, ok := strings.Cut(f.Path, "/")
		if !ok {
			continue
		}
		byDir[dir] = append(byDir[dir], envport.TreeFile{Path: rest, Mode: f.Mode, Data: f.Data})
	}
	var out []childDir
	for dir, fs := range byDir {
		if fileAt(fs, skills.ManifestFile) != nil {
			out = append(out, childDir{name: dir, files: fs})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func fileAt(files []envport.TreeFile, p string) []byte {
	for _, f := range files {
		if f.Path == p {
			return f.Data
		}
	}
	return nil
}
