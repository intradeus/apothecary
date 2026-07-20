package coder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"apothecary/workspace"
)

// Engine-pushed template (DESIGN2 §8.4): the embedded template tree is the
// source of truth; versions are named v<sha16 of the tree> and pushed only
// on drift, so users never touch terraform. The custom-template escape
// hatch (project.workspace.template set) bypasses this entirely — the
// engine then neither pushes nor passes the M9 parameters.
//
// CLI flags verified against Coder v2.35 at implementation; behavior, not
// flags, is normative (§12 precedent).

// templateVersionName hashes the embedded template tree.
func templateVersionName() string {
	h := sha256.New()
	_ = fs.WalkDir(workspace.Template, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := fs.ReadFile(workspace.Template, path)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(data))
		h.Write(data)
		return nil
	})
	return "v" + hex.EncodeToString(h.Sum(nil))[:16]
}

// EnsureTemplate pushes the embedded template as the active version when
// the provisioner's active version differs. Cached per adapter instance —
// one check per process lifetime is enough (the tree only changes with a
// apothecaryd release).
func (a *Adapter) EnsureTemplate(ctx context.Context) error {
	a.tmplMu.Lock()
	defer a.tmplMu.Unlock()
	if a.tmplEnsured {
		return nil
	}
	want := templateVersionName()

	if active, err := a.activeTemplateVersion(ctx); err == nil && active == want {
		a.tmplEnsured = true
		return nil
	}

	dir, err := os.MkdirTemp("", "apothecary-template-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := fs.WalkDir(workspace.Template, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Flatten template/ so the terraform files sit at the directory root.
		rel := strings.TrimPrefix(path, "template/")
		if d.IsDir() {
			if rel == "" || path == "template" {
				return nil
			}
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		data, rerr := fs.ReadFile(workspace.Template, path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(filepath.Join(dir, rel), data, 0o644)
	}); err != nil {
		return err
	}

	push := []string{"templates", "push", a.cfg.Template,
		"--directory", dir, "--name", want, "--yes"}
	_, stderr, code, err := a.run(ctx, push, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		errText := string(stderr)
		switch {
		// First contact with a fresh Coder deployment: the template may not
		// exist yet — create it from the same directory.
		case strings.Contains(errText, "not found") || strings.Contains(errText, "does not exist"):
			create := []string{"templates", "create", a.cfg.Template,
				"--directory", dir, "--yes"}
			_, cerr, ccode, err := a.run(ctx, create, nil)
			if err != nil {
				return err
			}
			if ccode != 0 {
				return fmt.Errorf("coder templates create %s: exit %d: %s", a.cfg.Template, ccode, tail(string(cerr), 2048))
			}
		// The wanted version exists but is not active (rollback, or an
		// earlier active-detection miss): promote instead of re-pushing.
		case strings.Contains(errText, "already exists") || strings.Contains(errText, "already in use"):
			promote := []string{"templates", "versions", "promote",
				"--template=" + a.cfg.Template, "--template-version=" + want}
			_, perr, pcode, err := a.run(ctx, promote, nil)
			if err != nil {
				return err
			}
			if pcode != 0 {
				return fmt.Errorf("coder templates versions promote %s: exit %d: %s", want, pcode, tail(string(perr), 2048))
			}
		default:
			return fmt.Errorf("coder templates push %s: exit %d: %s", a.cfg.Template, code, tail(errText, 2048))
		}
	}
	a.tmplEnsured = true
	return nil
}

// activeTemplateVersion returns the provisioner-side active version name.
// The versions-list JSON carries no "active" flag (found live) — the truth
// is the template's active_version_id, matched against the version ids.
func (a *Adapter) activeTemplateVersion(ctx context.Context) (string, error) {
	out, _, code, err := a.run(ctx, []string{"templates", "list", "--output", "json"}, nil)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("templates list: exit %d", code)
	}
	var templates []struct {
		Name            string `json:"name"`
		ActiveVersionID string `json:"active_version_id"`
	}
	if err := json.Unmarshal(out, &templates); err != nil {
		return "", err
	}
	activeID := ""
	for _, t := range templates {
		if t.Name == a.cfg.Template {
			activeID = t.ActiveVersionID
		}
	}
	if activeID == "" {
		return "", fmt.Errorf("template %s not found", a.cfg.Template)
	}
	vout, _, vcode, err := a.run(ctx, []string{
		"templates", "versions", "list", a.cfg.Template, "--output", "json"}, nil)
	if err != nil {
		return "", err
	}
	if vcode != 0 {
		return "", fmt.Errorf("versions list: exit %d", vcode)
	}
	var versions []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(vout, &versions); err != nil {
		return "", err
	}
	for _, v := range versions {
		if v.ID == activeID {
			return v.Name, nil
		}
	}
	return "", fmt.Errorf("active version %s not in versions list", activeID)
}
