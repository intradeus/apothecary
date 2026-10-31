package skillstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"apothecary/internal/confstore"
	"apothecary/internal/platform/filestore"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/skills"
)

const (
	maxSkillsetName = 100
	maxSkillsetDesc = 1024
)

// Store is the library's service: validation, blob-first writes, and the
// version policy (bump) that every content change funnels through.
type Store struct {
	db    DB
	blobs *filestore.Blobs
	now   func() time.Time
	newID func() string
}

func New(db DB, blobs *filestore.Blobs) *Store {
	return &Store{
		db:    db,
		blobs: blobs,
		now:   func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
		newID: func() string { return ulid.Make().String() },
	}
}

func invalid(err error) error { return fmt.Errorf("%v: %w", err, confstore.ErrInvalid) }

func invalidf(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, confstore.ErrInvalid)...)
}

// SkillInput is a skill's full content: the name it installs under and
// every file (SKILL.md included).
type SkillInput struct {
	Name  string
	Files []envport.TreeFile
}

// SkillsetInput is a skillset's editable surface.
type SkillsetInput struct {
	Name        string
	Description string
	SkillIDs    []string
}

// --- skills -----------------------------------------------------------------

func (s *Store) CreateSkill(ctx context.Context, userID string, in SkillInput) (Skill, error) {
	now := s.now()
	return s.writeSkill(ctx, Skill{ID: s.newID(), UserID: userID, CreatedAt: now}, in.Name, in.Files, nil)
}

// UpdateSkill replaces a skill's content (and may rename it: skillsets
// snapshot the name, so history is unaffected, and the sets that list it
// get a new version). Editing an imported skill is allowed; the next
// refresh overwrites the edit.
func (s *Store) UpdateSkill(ctx context.Context, id string, in SkillInput) (Skill, error) {
	cur, err := s.db.GetSkill(ctx, id)
	if err != nil {
		return Skill{}, err
	}
	return s.writeSkill(ctx, cur, in.Name, in.Files, nil)
}

// writeSkill is writeSkillQuiet plus the version policy: a version in
// every skillset that lists the skill, if its content hash moved.
func (s *Store) writeSkill(ctx context.Context, cur Skill, name string, files []envport.TreeFile, src *Source) (Skill, error) {
	out, err := s.writeSkillQuiet(ctx, cur, name, files, src)
	if err != nil {
		return Skill{}, err
	}
	if err := s.bumpContaining(ctx, out.ID); err != nil {
		return Skill{}, err
	}
	return out, nil
}

// writeSkillQuiet validates, stores the bytes (blob-first, row-second — a
// crash between the two leaves an unreferenced blob the sweep reclaims) and
// writes the row with src when given. No version is cut: callers that
// touch several skills at once (a skillset refresh) bump once at the end.
func (s *Store) writeSkillQuiet(ctx context.Context, cur Skill, name string, files []envport.TreeFile, src *Source) (Skill, error) {
	fm, norm, err := skills.Validate(name, files)
	if err != nil {
		return Skill{}, invalid(err)
	}
	for _, f := range norm {
		if _, err := s.blobs.Put(f.Data); err != nil {
			return Skill{}, fmt.Errorf("store %s: %w", f.Path, err)
		}
	}
	man := skills.Manifest(norm)
	cur.Name = name
	cur.Description = fm.Description
	cur.Files = man
	cur.Hash = skills.Hash(man)
	cur.UpdatedAt = s.now()
	if src != nil {
		cur.Source = src
	}
	if err := s.db.UpsertSkill(ctx, cur); err != nil {
		return Skill{}, err
	}
	return cur, nil
}

func (s *Store) GetSkill(ctx context.Context, id string) (Skill, error) {
	return s.db.GetSkill(ctx, id)
}

func (s *Store) ListSkills(ctx context.Context, userID string) ([]Skill, error) {
	return s.db.ListSkills(ctx, userID)
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error { return s.db.DeleteSkill(ctx, id) }

// SkillFiles reassembles a skill's current content from its manifest.
func (s *Store) SkillFiles(ctx context.Context, id string) ([]envport.TreeFile, error) {
	sk, err := s.db.GetSkill(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.files(sk.Name, sk.Files)
}

func (s *Store) files(name string, entries []skills.FileEntry) ([]envport.TreeFile, error) {
	out := make([]envport.TreeFile, 0, len(entries))
	for _, e := range entries {
		data, err := s.blobs.Bytes(e.SHA256)
		if err != nil {
			return nil, fmt.Errorf("skill %s: %s: %w", name, e.Path, err)
		}
		out = append(out, envport.TreeFile{Path: e.Path, Mode: fs.FileMode(e.Mode), Data: data})
	}
	return out, nil
}

// --- skillsets --------------------------------------------------------------

func (s *Store) CreateSkillset(ctx context.Context, userID string, in SkillsetInput) (Skillset, error) {
	now := s.now()
	set := Skillset{ID: s.newID(), UserID: userID, CreatedAt: now}
	return s.writeSkillset(ctx, set, in, nil)
}

func (s *Store) UpdateSkillset(ctx context.Context, id string, in SkillsetInput) (Skillset, error) {
	cur, err := s.db.GetSkillset(ctx, id)
	if err != nil {
		return Skillset{}, err
	}
	return s.writeSkillset(ctx, cur, in, nil)
}

func (s *Store) writeSkillset(ctx context.Context, set Skillset, in SkillsetInput, src *Source) (Skillset, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxSkillsetName {
		return Skillset{}, invalidf("skillset name must be 1-%d characters", maxSkillsetName)
	}
	if utf8.RuneCountInString(in.Description) > maxSkillsetDesc {
		return Skillset{}, invalidf("skillset description must be at most %d characters", maxSkillsetDesc)
	}
	ids, err := s.sameOwnerSkills(ctx, set.UserID, in.SkillIDs)
	if err != nil {
		return Skillset{}, err
	}
	set.Name = name
	set.Description = strings.TrimSpace(in.Description)
	set.SkillIDs = ids
	set.UpdatedAt = s.now()
	if src != nil {
		set.Source = src
	}
	if err := s.db.UpsertSkillset(ctx, set); err != nil {
		return Skillset{}, err
	}
	if _, _, err := s.bump(ctx, set.ID); err != nil {
		return Skillset{}, err
	}
	return s.db.GetSkillset(ctx, set.ID)
}

// sameOwnerSkills checks every member exists and belongs to the skillset's
// owner (another user's skill is "not found", never "forbidden"), and drops
// duplicates keeping the first position.
func (s *Store) sameOwnerSkills(ctx context.Context, userID string, ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		sk, err := s.db.GetSkill(ctx, id)
		if errors.Is(err, confstore.ErrNotFound) || (err == nil && sk.UserID != userID) {
			return nil, invalidf("skill %s not found", id)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *Store) GetSkillset(ctx context.Context, id string) (Skillset, error) {
	return s.db.GetSkillset(ctx, id)
}

func (s *Store) ListSkillsets(ctx context.Context, userID string) ([]Skillset, error) {
	return s.db.ListSkillsets(ctx, userID)
}

func (s *Store) DeleteSkillset(ctx context.Context, id string) error {
	return s.db.DeleteSkillset(ctx, id)
}

func (s *Store) ListVersions(ctx context.Context, id string) ([]Version, error) {
	return s.db.ListVersions(ctx, id)
}

// GetVersion returns v{seq}; seq 0 is the head.
func (s *Store) GetVersion(ctx context.Context, id string, seq int) (Version, error) {
	if seq <= 0 {
		set, err := s.db.GetSkillset(ctx, id)
		if err != nil {
			return Version{}, err
		}
		seq = set.HeadSeq
	}
	return s.db.GetVersion(ctx, id, seq)
}

// --- version policy ---------------------------------------------------------

// bumpContaining cuts a version in every skillset listing the skill whose
// content just changed.
func (s *Store) bumpContaining(ctx context.Context, skillID string) error {
	sets, err := s.db.SkillsetsContaining(ctx, skillID)
	if err != nil {
		return err
	}
	for _, set := range sets {
		if _, _, err := s.bump(ctx, set.ID); err != nil {
			return err
		}
	}
	return nil
}

// bumpAll bumps, once each, every skillset that lists any of the given
// skills, plus any skillset id given directly.
func (s *Store) bumpAll(ctx context.Context, ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if _, err := s.db.GetSkillset(ctx, id); err == nil {
			seen[id] = true
			continue
		}
		sets, err := s.db.SkillsetsContaining(ctx, id)
		if err != nil {
			return err
		}
		for _, set := range sets {
			seen[set.ID] = true
		}
	}
	for id := range seen {
		if _, _, err := s.bump(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// bump computes the skillset's content hash from its current members and
// appends a version when it differs from the head's. Two concurrent
// bumps race on the seq: the loser reloads and retries once.
func (s *Store) bump(ctx context.Context, setID string) (Version, bool, error) {
	for attempt := 0; ; attempt++ {
		set, err := s.db.GetSkillset(ctx, setID)
		if err != nil {
			return Version{}, false, err
		}
		members := make([]VersionMember, 0, len(set.SkillIDs))
		hashes := make([]skills.Member, 0, len(set.SkillIDs))
		for _, id := range set.SkillIDs {
			sk, err := s.db.GetSkill(ctx, id)
			if err != nil {
				return Version{}, false, err
			}
			members = append(members, VersionMember{SkillID: sk.ID, Name: sk.Name, Description: sk.Description, Hash: sk.Hash, Files: sk.Files})
			hashes = append(hashes, skills.Member{Name: sk.Name, Hash: sk.Hash})
		}
		hash := skills.SetHash(hashes)
		if set.HeadSeq > 0 {
			head, err := s.db.GetVersion(ctx, setID, set.HeadSeq)
			if err != nil && !errors.Is(err, confstore.ErrNotFound) {
				return Version{}, false, err
			}
			if err == nil && head.Hash == hash {
				return head, false, nil
			}
		}
		v := Version{SkillsetID: setID, Seq: set.HeadSeq + 1, Hash: hash, Members: members, CreatedAt: s.now()}
		err = s.db.InsertVersion(ctx, v)
		if errors.Is(err, confstore.ErrConflict) && attempt == 0 {
			continue
		}
		if err != nil {
			return Version{}, false, err
		}
		return v, true, nil
	}
}

// --- the engine's side ------------------------------------------------------

// ResolveSkillsets turns a run request's refs into pins: the skillset must
// be the project owner's (anyone else's is "not found"), seq 0 means the
// head, and the version must exist. Duplicates collapse to the first.
func (s *Store) ResolveSkillsets(ctx context.Context, userID string, refs []skills.Ref) ([]event.SkillsetPin, error) {
	var pins []event.SkillsetPin
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		set, err := s.db.GetSkillset(ctx, r.ID)
		if errors.Is(err, confstore.ErrNotFound) || (err == nil && set.UserID != userID) {
			return nil, fmt.Errorf("skillset %s: %w", r.ID, confstore.ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		seq := r.Seq
		if seq <= 0 {
			seq = set.HeadSeq
		}
		v, err := s.db.GetVersion(ctx, set.ID, seq)
		if errors.Is(err, confstore.ErrNotFound) {
			return nil, invalidf("skillset %s has no version v%d", set.Name, seq)
		}
		if err != nil {
			return nil, err
		}
		pins = append(pins, event.SkillsetPin{ID: set.ID, Name: set.Name, Seq: v.Seq, Hash: v.Hash})
	}
	return pins, nil
}

// SkillTrees assembles the trees a run installs from its pins: each member
// once (the same skill in two sets is one tree), bytes from the blob store.
// A pinned version that no longer exists (its skillset was deleted after
// the run finished and the run is being rebuilt) is skipped with a warning
// rather than failing the workspace — the flow pin degrades the same way.
func (s *Store) SkillTrees(ctx context.Context, pins []event.SkillsetPin) ([]skills.Tree, error) {
	seen := map[string]bool{}
	byName := map[string]string{}
	var out []skills.Tree
	for _, p := range pins {
		v, err := s.db.GetVersion(ctx, p.ID, p.Seq)
		if errors.Is(err, confstore.ErrNotFound) {
			slog.Warn("skills: pinned skillset version is gone; skipping", "skillset", p.Name, "id", p.ID, "seq", p.Seq)
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, m := range v.Members {
			if seen[m.SkillID] {
				continue
			}
			if other, ok := byName[m.Name]; ok && other != m.SkillID {
				slog.Warn("skills: two pinned skills share a name; keeping the first", "name", m.Name, "kept", other, "dropped", m.SkillID)
				continue
			}
			seen[m.SkillID] = true
			byName[m.Name] = m.SkillID
			files, err := s.files(m.Name, m.Files)
			if err != nil {
				return nil, err
			}
			out = append(out, skills.Tree{Name: m.Name, Files: files})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
