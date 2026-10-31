package skillstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"apothecary/internal/project/confstore"
	"apothecary/internal/skills"
)

// Mem is the in-memory DB for unit and httpapi tests. The one rule that
// depends on runs (a pinned skillset is not deletable) is an injectable
// hook, as in confstore.Mem.
type Mem struct {
	mu        sync.Mutex
	skills    map[string]Skill
	skillsets map[string]Skillset
	versions  map[string][]Version // skillset id → ascending seq

	// SkillsetPinnedBy lists the live runs pinning a skillset; nil = none.
	SkillsetPinnedBy func(skillsetID string) []string
}

func NewMem() *Mem {
	return &Mem{skills: map[string]Skill{}, skillsets: map[string]Skillset{}, versions: map[string][]Version{}}
}

func (m *Mem) UpsertSkill(_ context.Context, s Skill) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.skills {
		if x.ID == s.ID {
			continue
		}
		if x.UserID == s.UserID && x.Name == s.Name {
			return fmt.Errorf("skill %s: %w", s.Name, confstore.ErrConflict)
		}
		if s.Source != nil && x.Source != nil && x.UserID == s.UserID && x.Source.URL == s.Source.URL {
			return fmt.Errorf("skill from %s already exists as %s: %w", s.Source.URL, x.Name, confstore.ErrConflict)
		}
	}
	m.skills[s.ID] = cloneSkill(s)
	return nil
}

func (m *Mem) GetSkill(_ context.Context, id string) (Skill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.skills[id]
	if !ok {
		return Skill{}, fmt.Errorf("skill %s: %w", id, confstore.ErrNotFound)
	}
	return cloneSkill(s), nil
}

func (m *Mem) GetSkillByName(_ context.Context, userID, name string) (Skill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.skills {
		if s.UserID == userID && s.Name == name {
			return cloneSkill(s), nil
		}
	}
	return Skill{}, fmt.Errorf("skill %s: %w", name, confstore.ErrNotFound)
}

func (m *Mem) GetSkillBySource(_ context.Context, userID, url string) (Skill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.skills {
		if s.UserID == userID && s.Source != nil && s.Source.URL == url {
			return cloneSkill(s), nil
		}
	}
	return Skill{}, fmt.Errorf("skill from %s: %w", url, confstore.ErrNotFound)
}

func (m *Mem) ListSkills(_ context.Context, userID string) ([]Skill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Skill
	for _, s := range m.skills {
		if userID == "" || s.UserID == userID {
			out = append(out, cloneSkill(s))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) DeleteSkill(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.skills[id]; !ok {
		return fmt.Errorf("skill %s: %w", id, confstore.ErrNotFound)
	}
	var refs []string
	for _, set := range m.skillsets {
		for _, sid := range set.SkillIDs {
			if sid == id {
				refs = append(refs, set.Name)
			}
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return fmt.Errorf("skill is used by skillset %s: %w", strings.Join(refs, ", "), confstore.ErrReferenced)
	}
	delete(m.skills, id)
	return nil
}

func (m *Mem) UpsertSkillset(_ context.Context, s Skillset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.skillsets {
		if x.ID != s.ID && x.UserID == s.UserID && x.Name == s.Name {
			return fmt.Errorf("skillset %s: %w", s.Name, confstore.ErrConflict)
		}
	}
	for _, id := range s.SkillIDs {
		if _, ok := m.skills[id]; !ok {
			return fmt.Errorf("skillset %s references a missing skill %s: %w", s.Name, id, confstore.ErrInvalid)
		}
	}
	c := cloneSkillset(s)
	if cur, ok := m.skillsets[s.ID]; ok {
		c.HeadSeq = cur.HeadSeq
	} else {
		c.HeadSeq = 0
	}
	m.skillsets[s.ID] = c
	return nil
}

func (m *Mem) GetSkillset(_ context.Context, id string) (Skillset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.skillsets[id]
	if !ok {
		return Skillset{}, fmt.Errorf("skillset %s: %w", id, confstore.ErrNotFound)
	}
	return cloneSkillset(s), nil
}

func (m *Mem) ListSkillsets(_ context.Context, userID string) ([]Skillset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Skillset
	for _, s := range m.skillsets {
		if userID == "" || s.UserID == userID {
			out = append(out, cloneSkillset(s))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) SkillsetsContaining(_ context.Context, skillID string) ([]Skillset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Skillset
	for _, s := range m.skillsets {
		for _, id := range s.SkillIDs {
			if id == skillID {
				out = append(out, cloneSkillset(s))
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) DeleteSkillset(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.skillsets[id]
	if !ok {
		return fmt.Errorf("skillset %s: %w", id, confstore.ErrNotFound)
	}
	if m.SkillsetPinnedBy != nil {
		if runs := m.SkillsetPinnedBy(id); len(runs) > 0 {
			sort.Strings(runs)
			return fmt.Errorf("skillset %s is pinned by run %s: %w", s.Name, strings.Join(runs, ", "), confstore.ErrReferenced)
		}
	}
	delete(m.skillsets, id)
	delete(m.versions, id)
	return nil
}

func (m *Mem) InsertVersion(_ context.Context, v Version) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.skillsets[v.SkillsetID]
	if !ok {
		return fmt.Errorf("skillset %s: %w", v.SkillsetID, confstore.ErrInvalid)
	}
	for _, x := range m.versions[v.SkillsetID] {
		if x.Seq == v.Seq {
			return fmt.Errorf("skillset %s v%d: %w", v.SkillsetID, v.Seq, confstore.ErrConflict)
		}
	}
	m.versions[v.SkillsetID] = append(m.versions[v.SkillsetID], cloneVersion(v))
	if v.Seq > s.HeadSeq {
		s.HeadSeq = v.Seq
		s.UpdatedAt = v.CreatedAt
		m.skillsets[v.SkillsetID] = s
	}
	return nil
}

func (m *Mem) GetVersion(_ context.Context, skillsetID string, seq int) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.versions[skillsetID] {
		if v.Seq == seq {
			return cloneVersion(v), nil
		}
	}
	return Version{}, fmt.Errorf("skillset %s v%d: %w", skillsetID, seq, confstore.ErrNotFound)
}

func (m *Mem) ListVersions(_ context.Context, skillsetID string) ([]Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs := m.versions[skillsetID]
	out := make([]Version, 0, len(vs))
	for i := len(vs) - 1; i >= 0; i-- {
		out = append(out, cloneVersion(vs[i]))
	}
	return out, nil
}

func cloneSkill(s Skill) Skill {
	s.Files = append([]skills.FileEntry(nil), s.Files...)
	if s.Source != nil {
		src := *s.Source
		s.Source = &src
	}
	return s
}

func cloneSkillset(s Skillset) Skillset {
	s.SkillIDs = append([]string{}, s.SkillIDs...)
	if s.Source != nil {
		src := *s.Source
		s.Source = &src
	}
	return s
}

func cloneVersion(v Version) Version {
	members := make([]VersionMember, len(v.Members))
	for i, m := range v.Members {
		m.Files = append([]skills.FileEntry(nil), m.Files...)
		members[i] = m
	}
	v.Members = members
	return v
}
