package devcontainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"apothecary/internal/ports"
)

// A "dirty" workspace (Coder's word): its container was built from a
// devcontainer configuration that has since changed — an agent edited the
// Dockerfile, a human added a feature — so a rebuild would produce something
// different. Provision records a digest of the build inputs next to the run
// (configSumFile); ConfigDirty recomputes it from the checkout and compares.
// The checkout is a bind mount, so an edit made inside the container is
// visible here the moment it lands.

const configSumFile = "devcontainer.sum"

// configFiles lists the files whose contents decide what `devcontainer up`
// builds: everything under .devcontainer/, a root .devcontainer.json, and the
// Dockerfile / compose files the configuration points at outside that
// directory. Paths are relative to src, sorted and unique. A configuration
// that does not parse still yields the directory listing — the digest is
// then merely less complete, never an error.
func configFiles(src string) ([]string, error) {
	seen := map[string]bool{}
	add := func(abs string) {
		st, err := os.Lstat(abs)
		if err != nil || !st.Mode().IsRegular() {
			return
		}
		rel, err := filepath.Rel(src, abs)
		if err != nil {
			return
		}
		seen[filepath.ToSlash(rel)] = true
	}
	dir := filepath.Join(src, ".devcontainer")
	if _, err := os.Stat(dir); err == nil {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				add(p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	add(filepath.Join(src, ".devcontainer.json"))
	for _, cfg := range []string{filepath.Join(dir, "devcontainer.json"), filepath.Join(src, ".devcontainer.json")} {
		if _, err := os.Stat(cfg); err != nil {
			continue
		}
		for _, ref := range referencedBuildFiles(cfg) {
			add(ref)
		}
		break
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// referencedBuildFiles is the Dockerfile and compose files a devcontainer.json
// names, as absolute paths (relative references resolve against the file's
// own directory, as the CLI resolves them). Best-effort: unparseable → none.
func referencedBuildFiles(cfgPath string) []string {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}
	var cfg struct {
		Build struct {
			Dockerfile string `json:"dockerfile"`
		} `json:"build"`
		DockerFile        string          `json:"dockerFile"`
		DockerComposeFile json.RawMessage `json:"dockerComposeFile"`
	}
	if err := json.Unmarshal(stripJSONC(raw), &cfg); err != nil {
		return nil
	}
	var refs []string
	if cfg.Build.Dockerfile != "" {
		refs = append(refs, cfg.Build.Dockerfile)
	}
	if cfg.DockerFile != "" {
		refs = append(refs, cfg.DockerFile)
	}
	if len(cfg.DockerComposeFile) > 0 {
		var one string
		var many []string
		if json.Unmarshal(cfg.DockerComposeFile, &one) == nil && one != "" {
			refs = append(refs, one)
		} else if json.Unmarshal(cfg.DockerComposeFile, &many) == nil {
			refs = append(refs, many...)
		}
	}
	base := filepath.Dir(cfgPath)
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if r == "" {
			continue
		}
		if !filepath.IsAbs(r) {
			r = filepath.Join(base, r)
		}
		out = append(out, filepath.Clean(r))
	}
	return out
}

// configDigest hashes the build inputs: each file's path and contents, in
// configFiles order, so a rename counts as a change too.
func configDigest(src string) (string, error) {
	files, err := configFiles(src)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recordConfigDigest writes the digest of what was just built next to the run.
// Best-effort: a failure costs the dirty hint for this workspace, nothing else.
func (a *Adapter) recordConfigDigest(name, src string, plog func(string, ...any)) {
	sum, err := configDigest(src)
	if err == nil {
		err = os.WriteFile(filepath.Join(a.folderFor(name), configSumFile), []byte(sum+"\n"), 0o644)
	}
	if err != nil && plog != nil {
		plog("→ could not record the devcontainer config digest (no rebuild hint for this workspace): %v\n", err)
	}
}

// ConfigDirty implements ports.WorkspaceDirtyChecker: the checkout's build
// inputs no longer match the digest recorded when the container was built.
// A workspace with no recorded digest (built before this existed, or the
// write failed) reports clean — unknown is not dirty.
func (a *Adapter) ConfigDirty(_ context.Context, env ports.EnvID) (bool, error) {
	name := string(env)
	recorded, err := os.ReadFile(filepath.Join(a.folderFor(name), configSumFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current, err := configDigest(a.srcDir(name))
	if err != nil {
		return false, fmt.Errorf("digest %s devcontainer config: %w", name, err)
	}
	return current != strings.TrimSpace(string(recorded)), nil
}

var _ ports.WorkspaceDirtyChecker = (*Adapter)(nil)
