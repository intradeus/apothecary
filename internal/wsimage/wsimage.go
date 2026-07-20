// Package wsimage is the workspace image pipeline (DESIGN2 §8): it
// composes per-project Dockerfiles (base + user toolchain fragment +
// harness install layer), tags them content-addressed, and builds them
// through the docker CLI against the mounted socket — the same daemon the
// Coder docker provider uses, so built tags are directly runnable.
package wsimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"apothecary/workspace"
)

// Runner executes one docker CLI invocation in dir; swapped in tests.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (output string, exitCode int, err error)
}

type cliRunner struct{}

func (cliRunner) Run(ctx context.Context, dir string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode(), nil
	}
	if err != nil {
		return string(out), 0, err
	}
	return string(out), 0, nil
}

type Builder struct {
	docker  Runner
	runsDir string
	// baseOverride is APOTHECARY_BASE_IMAGE: an expert-supplied base ref that
	// replaces the embedded build entirely (envcheck is the gate, §8.1).
	baseOverride string

	mu sync.Mutex // one build at a time; docker layer-caches the rest
}

func New(runsDir, baseOverride string) *Builder {
	return &Builder{docker: cliRunner{}, runsDir: runsDir, baseOverride: baseOverride}
}

// NewWithRunner is the test constructor.
func NewWithRunner(r Runner, runsDir, baseOverride string) *Builder {
	return &Builder{docker: r, runsDir: runsDir, baseOverride: baseOverride}
}

func sha16(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// baseContextHash hashes the embedded base context (names + contents,
// sorted) — the base tag changes exactly when the invariants change.
func baseContextHash() string {
	entries, _ := fs.ReadDir(workspace.BaseContext, ".")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		data, _ := fs.ReadFile(workspace.BaseContext, n)
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// BaseRef is the base image reference: the override verbatim, else the
// content-addressed embedded build target.
func (b *Builder) BaseRef() string {
	if b.baseOverride != "" {
		return b.baseOverride
	}
	return "apothecary/base:" + baseContextHash()
}

// Compose renders the per-project Dockerfile (§8.2, exact shape).
// Fragment legality (no FROM/COPY/ADD/USER/ENTRYPOINT/CMD) is validated at
// project save (confstore.ValidateFragment); the build context contains
// only this file.
func Compose(baseRef, fragment string, harnessLines []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "FROM %s\n", baseRef)
	sb.WriteString("USER root\n")
	sb.WriteString("# ---- toolchain (project.workspace.dockerfile_fragment) ----\n")
	if strings.TrimSpace(fragment) != "" {
		sb.WriteString(strings.TrimRight(fragment, "\n"))
		sb.WriteString("\n")
	}
	sb.WriteString("# ---- harness ----\n")
	for _, line := range harnessLines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	sb.WriteString("USER apothecary\nWORKDIR /home/apothecary\n")
	return sb.String()
}

// Tag is the content-addressed workspace tag: the composed Dockerfile
// embeds the base ref, fragment and harness layer, so hashing it covers
// every input (§8.2).
func Tag(dockerfile string) string {
	return "apothecary/ws:" + sha16([]byte(dockerfile))
}

// Ensure makes the composed image present, building base and workspace
// layers as needed. cached=true means the tag already existed and nothing
// ran (§8.3: image_built{cached:true}).
func (b *Builder) Ensure(ctx context.Context, dockerfile string) (tag string, cached bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tag = Tag(dockerfile)
	if b.exists(ctx, tag) {
		return tag, true, nil
	}
	if err := b.ensureBase(ctx); err != nil {
		return tag, false, err
	}
	dir, err := os.MkdirTemp("", "apothecary-ws-*")
	if err != nil {
		return tag, false, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return tag, false, err
	}
	if err := b.build(ctx, dir, tag); err != nil {
		return tag, false, err
	}
	return tag, false, nil
}

func (b *Builder) ensureBase(ctx context.Context) error {
	ref := b.BaseRef()
	if b.exists(ctx, ref) {
		return nil
	}
	if b.baseOverride != "" {
		return fmt.Errorf("base image %s (APOTHECARY_BASE_IMAGE) is not present on the docker daemon", ref)
	}
	dir, err := os.MkdirTemp("", "apothecary-base-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	entries, err := fs.ReadDir(workspace.BaseContext, ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(workspace.BaseContext, e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return b.build(ctx, dir, ref)
}

func (b *Builder) exists(ctx context.Context, ref string) bool {
	_, code, err := b.docker.Run(ctx, "", "image", "inspect", ref)
	return err == nil && code == 0
}

// build runs `docker build` and persists the full log under
// runs_dir/builds/<tag-suffix>.log (§8.3).
func (b *Builder) build(ctx context.Context, dir, tag string) error {
	out, code, err := b.docker.Run(ctx, dir, "build", "-t", tag, ".")
	b.writeLog(tag, out)
	if err != nil {
		return fmt.Errorf("docker build %s: %w", tag, err)
	}
	if code != 0 {
		return fmt.Errorf("docker build %s: exit %d: %s", tag, code, tail(out, 4096))
	}
	return nil
}

func (b *Builder) writeLog(tag, out string) {
	if b.runsDir == "" {
		return
	}
	dir := filepath.Join(b.runsDir, "builds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := strings.ReplaceAll(tag, "/", "-")
	name = strings.ReplaceAll(name, ":", "-")
	_ = os.WriteFile(filepath.Join(dir, name+".log"), []byte(out), 0o644)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
