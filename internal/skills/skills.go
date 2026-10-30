// Package skills is the Agent Skills format (agentskills.io): what a skill
// folder must contain, how its files may be named, and how its content is
// hashed. Pure functions over bytes — storage (skillstore), delivery
// (engine bootstrap) and per-harness installation (the adapters) live
// elsewhere and all agree on these rules.
package skills

import (
	"apothecary/internal/ports/envport"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	// ManifestFile is the one required file of a skill.
	ManifestFile = "SKILL.md"
	// WorkspaceDir is where the engine lays a run's skills down inside the
	// workspace, one directory per skill, before each harness copies them
	// into the directory it reads.
	WorkspaceDir = "/apothecary/skills"
	// MaxFileBytes caps one file; MaxSkillBytes the whole folder; MaxFiles
	// its file count. Skills are prose plus small scripts and assets — these
	// bound the base64-over-exec delivery and the inline API payloads.
	MaxFileBytes  = 1 << 20
	MaxSkillBytes = 8 << 20
	MaxFiles      = 200

	MaxNameLen          = 64
	MaxDescriptionLen   = 1024
	MaxCompatibilityLen = 500
)

// NameRe is the spec's name grammar: lowercase alphanumerics and single
// hyphens, no leading/trailing/consecutive hyphen. Length is checked apart.
var NameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Frontmatter is the SKILL.md header the spec defines. Fields the spec does
// not know (Claude Code's `argument-hint`, omp's `hide`…) are ignored, not
// refused: a skill written for one harness still installs for the others.
type Frontmatter struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	License       string `json:"license,omitempty"`
	Compatibility string `json:"compatibility,omitempty"`
}

// FileEntry is one file of a skill's content manifest: path, content hash,
// size and the permission bits it installs with.
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

// Member is what a skillset's hash is computed over: a skill's name and
// its content hash.
type Member struct {
	Name string
	Hash string
}

// ValidName enforces the spec's name rules.
func ValidName(name string) error {
	if name == "" || len(name) > MaxNameLen {
		return fmt.Errorf("skill name must be 1-%d characters", MaxNameLen)
	}
	if !NameRe.MatchString(name) {
		return fmt.Errorf("skill name %q: lowercase letters, digits and single hyphens only", name)
	}
	return nil
}

// CleanPath checks a file path inside a skill: relative, forward slashes,
// no empty, "." or ".." segments, no control characters. It returns the
// path unchanged (the rules leave nothing to normalise) so callers can use
// it as a predicate that also names the offending path.
func CleanPath(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return "", fmt.Errorf("invalid file path %q", p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("invalid file path %q", p)
		}
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid file path %q", p)
		}
	}
	return p, nil
}

// Mode decides the permission bits a skill file installs with: executable
// (0755) when it was executable at the source or lives under scripts/,
// else 0644. Nothing finer survives — skills travel between users and
// hosts, and only the executable bit means anything to an agent.
func Mode(path string, mode fs.FileMode) fs.FileMode {
	if mode.Perm()&0o111 != 0 || strings.HasPrefix(path, "scripts/") {
		return 0o755
	}
	return 0o644
}

// Validate checks a skill folder named dirName and returns its frontmatter
// plus the normalised files: paths checked, modes decided by Mode, sorted
// by path. The spec's one structural rule — frontmatter name == directory
// name — is enforced here, so a skill is always addressable by the name it
// installs under.
func Validate(dirName string, files []envport.TreeFile) (Frontmatter, []envport.TreeFile, error) {
	if err := ValidName(dirName); err != nil {
		return Frontmatter{}, nil, err
	}
	if len(files) == 0 {
		return Frontmatter{}, nil, fmt.Errorf("a skill needs at least %s", ManifestFile)
	}
	if len(files) > MaxFiles {
		return Frontmatter{}, nil, fmt.Errorf("too many files: %d (max %d)", len(files), MaxFiles)
	}
	out := make([]envport.TreeFile, 0, len(files))
	seen := map[string]bool{}
	var total int64
	var md []byte
	found := false
	for _, f := range files {
		p, err := CleanPath(f.Path)
		if err != nil {
			return Frontmatter{}, nil, err
		}
		if seen[p] {
			return Frontmatter{}, nil, fmt.Errorf("duplicate file path %q", p)
		}
		seen[p] = true
		if int64(len(f.Data)) > MaxFileBytes {
			return Frontmatter{}, nil, fmt.Errorf("%s: larger than %d bytes", p, MaxFileBytes)
		}
		total += int64(len(f.Data))
		if total > MaxSkillBytes {
			return Frontmatter{}, nil, fmt.Errorf("skill larger than %d bytes in total", MaxSkillBytes)
		}
		if p == ManifestFile {
			md, found = f.Data, true
		}
		out = append(out, envport.TreeFile{Path: p, Mode: Mode(p, f.Mode), Data: f.Data})
	}
	if !found {
		return Frontmatter{}, nil, fmt.Errorf("%s is missing", ManifestFile)
	}
	fm, err := ParseFrontmatter(md)
	if err != nil {
		return Frontmatter{}, nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if fm.Name != dirName {
		return Frontmatter{}, nil, fmt.Errorf("%s: name %q must match the skill's directory name %q", ManifestFile, fm.Name, dirName)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return fm, out, nil
}

// ParseFrontmatter reads the YAML block between the leading `---` line and
// the next `---` line. The name rules are enforced (the name is the
// directory a skill installs as); the description must be present but is
// NOT held to the spec's 1024 characters — published skills exceed it
// (anthropics/skills' claude-api is 1078) and every harness loads them, so
// the store only truncates what it denormalises for display. The block is
// decoded loosely (any scalar becomes text, unknown keys are ignored) so a
// harness-specific header never blocks a skill.
func ParseFrontmatter(md []byte) (Frontmatter, error) {
	text := strings.ReplaceAll(string(md), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if strings.TrimRight(lines[0], " \t") != "---" {
		return Frontmatter{}, fmt.Errorf("must start with a '---' frontmatter line")
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			closing = i
			break
		}
	}
	if closing < 0 {
		return Frontmatter{}, fmt.Errorf("frontmatter is not closed by a '---' line")
	}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closing], "\n")), &raw); err != nil {
		return Frontmatter{}, fmt.Errorf("frontmatter: %v", err)
	}
	fm := Frontmatter{
		Name:          scalar(raw["name"]),
		Description:   strings.TrimSpace(scalar(raw["description"])),
		License:       strings.TrimSpace(scalar(raw["license"])),
		Compatibility: strings.TrimSpace(scalar(raw["compatibility"])),
	}
	if err := ValidName(fm.Name); err != nil {
		return Frontmatter{}, fmt.Errorf("frontmatter: %w", err)
	}
	if fm.Description == "" {
		return Frontmatter{}, fmt.Errorf("frontmatter: description is required")
	}
	fm.Description = truncate(fm.Description, MaxDescriptionLen)
	fm.Compatibility = truncate(fm.Compatibility, MaxCompatibilityLen)
	return fm, nil
}

// truncate cuts s to at most n runes, marking the cut.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// scalar renders a loosely decoded YAML value as text: strings as they
// are, numbers/bools via %v, anything structured as empty.
func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool, int, int64, float64, uint64:
		return fmt.Sprintf("%v", x)
	default:
		return ""
	}
}

// Manifest hashes each file and returns the entries sorted by path. Files
// are expected normalised (Validate); the mode recorded is Mode's.
func Manifest(files []envport.TreeFile) []FileEntry {
	out := make([]FileEntry, 0, len(files))
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		out = append(out, FileEntry{
			Path:   f.Path,
			SHA256: hex.EncodeToString(sum[:]),
			Size:   int64(len(f.Data)),
			Mode:   uint32(Mode(f.Path, f.Mode)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Hash is a skill's content identity: sha256 over its manifest (path,
// content hash, mode per file), order-independent.
func Hash(entries []FileEntry) string {
	sorted := append([]FileEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, e := range sorted {
		fmt.Fprintf(h, "%s\x00%s\x00%o\n", e.Path, e.SHA256, e.Mode)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SetHash is a skillset version's identity: sha256 over its members' names
// and content hashes, order-independent (install order never matters —
// names are unique within an owner).
func SetHash(members []Member) string {
	sorted := append([]Member(nil), members...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	for _, m := range sorted {
		fmt.Fprintf(h, "%s\x00%s\n", m.Name, m.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Ref is a run's request for a skillset: by id, at a version (0 = latest).
type Ref struct {
	ID  string `json:"id"`
	Seq int    `json:"seq,omitempty"`
}

// Tree is one installable skill: its name (the directory it installs as)
// and its normalised files.
type Tree struct {
	Name  string
	Files []envport.TreeFile
}
