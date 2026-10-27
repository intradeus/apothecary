package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"apothecary/internal/egressgw"
	"apothecary/internal/ports"
	wsassets "apothecary/workspace"
)

// devcontainerDef is the repo's own .devcontainer/devcontainer.json (or a root
// .devcontainer.json), built and entered with the reference @devcontainers/cli
// running against the sandbox's docker daemon. Image, Dockerfile and compose
// configurations all work, compose siblings included; apothecary's harness and
// code-server ride in as injected devcontainer features.
type devcontainerDef struct {
	bin string // the devcontainer CLI
}

func (devcontainerDef) Name() string { return "devcontainer" }

func (devcontainerDef) Detect(ctx context.Context, fsys sandboxFS, src string) bool {
	return devcontainerConfig(ctx, fsys, src) != ""
}

// devcontainerConfig is the config file the CLI will read, or "".
func devcontainerConfig(ctx context.Context, fsys sandboxFS, src string) string {
	for _, p := range []string{
		filepath.Join(src, ".devcontainer", "devcontainer.json"),
		filepath.Join(src, ".devcontainer.json"),
	} {
		if fsys.Exists(ctx, p) {
			return p
		}
	}
	return ""
}

// upResult is `devcontainer up`'s machine-readable last stdout line.
type upResult struct {
	Outcome               string `json:"outcome"`
	ContainerID           string `json:"containerId"`
	RemoteUser            string `json:"remoteUser"`
	RemoteWorkspaceFolder string `json:"remoteWorkspaceFolder"`
	Message               string `json:"message"`
	Description           string `json:"description"`
}

// Up is a single idempotent create-or-start: `devcontainer up` with our
// id-label creates the container on first call and restarts/reuses it on every
// later one (crash recovery), re-running the repo's lifecycle hooks exactly as
// the devcontainer spec prescribes.
func (d devcontainerDef) Up(ctx context.Context, e defEnv, spec ports.EnvSpec, o upOptions) (Workspace, error) {
	plog := o.plog
	if plog == nil {
		plog = func(string, ...any) {}
	}
	args := []string{"up",
		"--workspace-folder", e.src,
		"--id-label", label(e.name),
		"--id-label", managedLabel + "=true",
		"--mount", "type=bind,source=" + o.shimDir + ",target=/opt/apothecary",
		"--log-format", "json",
	}
	if spec.Project != "" {
		args = append(args, "--id-label", projectLabel+"="+spec.Project)
	}
	if o.removeExisting {
		// Force a from-scratch recreate: the CLI removes any container matching
		// the id-labels before (re)creating. The checkout (with agent commits)
		// is untouched; only the container is rebuilt.
		args = append(args, "--remove-existing-container")
	}
	// The repo is honored untouched; anything apothecary needs rides in as an
	// injected devcontainer feature (harness + code-server). Egress is enforced
	// externally by the per-project gateway, so the workspace container needs
	// no NET_ADMIN and compose siblings are covered too.
	features := map[string]any{}
	// The repo's image has no harness — install it as a feature.
	if len(spec.HarnessInstall) > 0 {
		if err := materializeHarnessFeature(ctx, e.fs, e.src, spec.HarnessInstall); err != nil {
			return Workspace{}, err
		}
		features["./apothecary-harness"] = map[string]any{}
	}
	// code-server (VS Code in the browser) is ALWAYS injected — installed at
	// build time, started lazily on demand. Extensions = the repo
	// devcontainer's own customizations.vscode.extensions ∪ the project list,
	// baked into the image (cached per list).
	if err := materializeCodeServerFeature(ctx, e.fs, e.src, spec.CodeServerExtensions); err != nil {
		return Workspace{}, err
	}
	features["./apothecary-codeserver"] = map[string]any{}
	blob, err := json.Marshal(features)
	if err != nil {
		return Workspace{}, err
	}
	args = append(args, "--additional-features", string(blob))
	if o.noCache {
		// The CLI drops every --cache-from of its own accord when this is set,
		// so the flags below stay harmless — and --cache-to still runs, which
		// is what makes one no-cache build repair the cache for the next ones.
		plog("→ rebuilding without the cache (fresh base image and layers)\n")
		args = append(args, "--build-no-cache")
	}
	// The build cache. A run's inner daemon is thrown away with the run, so
	// without this every run rebuilds the devcontainer image from nothing —
	// measured at 77s of a 90s provision on a small project. buildkit exports
	// the cache to the project gateway's writable registry and imports it on
	// the next run, which turns that into seconds.
	//
	// Three things make it safe rather than clever:
	//   * ignore-error: a wiped, restarting or wedged cache registry must cost
	//     a cache miss, never a failed build (verified: the export error is
	//     swallowed and the build exits 0).
	//   * mode=max: intermediate stages too, which is where devcontainer
	//     features and the code-server install live.
	//   * only with a gateway: no gateway, no registry to talk to, so the
	//     flags are simply absent and the build behaves as it always did.
	//
	// Cache EXPORT needs a buildkit driver that supports it; the sandbox's
	// docker:29-dind runs the containerd image store, which is exactly what
	// makes the default docker driver able to (docker's own error message
	// names it as the alternative to switching drivers).
	if o.gwIP != "" && o.project != "" {
		cacheRef := egressgw.BuildCacheRef(o.gwIP, o.project)
		imageRef := egressgw.BuildCacheImageRef(o.gwIP, o.project)
		args = append(args,
			// Both sources, every time: whichever exists restores, and a
			// missing one is a 404 buildkit shrugs at.
			"--cache-from", "type=registry,ref="+imageRef,
			"--cache-from", "type=registry,ref="+cacheRef,
			// Only a single-container devcontainer honors this; a compose one
			// ignores it, and pushBuildCache is what covers that case.
			"--cache-to", "type=registry,ref="+cacheRef+",mode=max,ignore-error=true")
	}
	// BUILDX_BAKE_ENTITLEMENTS_FS=0: compose-based devcontainers build via
	// `docker buildx bake`, which (buildx >=0.17) gates any fs.read outside
	// the compose dir behind an interactive entitlement prompt. `devcontainer
	// up` runs non-interactively, so the prompt can't be answered and the
	// build aborts with "additional privileges requested". Disabling the
	// filesystem entitlement check restores the pre-0.17 behavior. Scoped to
	// this call so it never affects the other docker/git subprocesses.
	plog("→ building devcontainer (image + features; can take several minutes)\n")
	stdout, stderr, code, err := e.run(ctx, d.bin, args, nil, o.log, "BUILDX_BAKE_ENTITLEMENTS_FS=0")
	if err != nil {
		return Workspace{}, err
	}
	res, perr := parseUpResult(stdout)
	if code != 0 || perr != nil || res.Outcome != "success" {
		return Workspace{}, fmt.Errorf("devcontainer up %s: exit %d: %s%s", e.name, code,
			strings.TrimSpace(res.Message+" "+res.Description), tail("\n"+string(stderr), 2048))
	}
	return Workspace{ContainerID: res.ContainerID, RemoteUser: res.RemoteUser, WorkspaceFolder: res.RemoteWorkspaceFolder}, nil
}

// parseUpResult extracts the result object from up's stdout (last
// non-empty line; --log-format json keeps log lines on stderr).
func parseUpResult(stdout []byte) (upResult, error) {
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	var res upResult
	if len(lines) == 0 {
		return res, fmt.Errorf("empty up output")
	}
	err := json.Unmarshal([]byte(lines[len(lines)-1]), &res)
	return res, err
}

// ExecArgs: `devcontainer exec` finds the container by our id-label and runs
// as the configuration's remote user with its userEnvProbe'd environment.
func (d devcontainerDef) ExecArgs(name string, remoteEnv ...string) (string, []string) {
	args := []string{"exec", "--id-label", label(name)}
	for _, kv := range remoteEnv {
		args = append(args, "--remote-env", kv)
	}
	return d.bin, args
}

// readConfiguration runs `devcontainer read-configuration` against the
// checkout; nil on any failure (best-effort callers).
func (d devcontainerDef) readConfiguration(ctx context.Context, e defEnv, into any) bool {
	stdout, _, code, err := e.run(ctx, d.bin,
		[]string{"read-configuration", "--workspace-folder", e.src, "--include-merged-configuration", "--log-format", "json"}, nil, nil)
	if err != nil || code != 0 {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	return len(lines) > 0 && json.Unmarshal([]byte(lines[len(lines)-1]), into) == nil
}

// IsCompose reports whether the repo's devcontainer is compose-based (its
// merged config carries dockerComposeFile). Best-effort: a read failure is
// treated as non-compose (the common image/Dockerfile case).
func (d devcontainerDef) IsCompose(ctx context.Context, e defEnv) bool {
	var rc struct {
		Configuration       map[string]json.RawMessage `json:"configuration"`
		MergedConfiguration map[string]json.RawMessage `json:"mergedConfiguration"`
	}
	if !d.readConfiguration(ctx, e, &rc) {
		return false
	}
	_, a1 := rc.Configuration["dockerComposeFile"]
	_, a2 := rc.MergedConfiguration["dockerComposeFile"]
	return a1 || a2
}

// DeclaredPorts reads forwardPorts/appPort from the merged configuration
// (best-effort; service-qualified entries yield nothing).
func (d devcontainerDef) DeclaredPorts(ctx context.Context, e defEnv) []int {
	var rc struct {
		Configuration       map[string]any `json:"configuration"`
		MergedConfiguration map[string]any `json:"mergedConfiguration"`
	}
	if !d.readConfiguration(ctx, e, &rc) {
		return nil
	}
	set := map[int]bool{}
	for _, cfg := range []map[string]any{rc.MergedConfiguration, rc.Configuration} {
		if cfg == nil {
			continue
		}
		collectPorts(cfg["forwardPorts"], set)
		collectPorts(cfg["appPort"], set)
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// collectPorts accepts the spec's port shapes: number, numeric string,
// or arrays of either ("host:port" service refs are skipped).
func collectPorts(v any, set map[int]bool) {
	switch t := v.(type) {
	case float64:
		set[int(t)] = true
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			set[n] = true
		}
	case []any:
		for _, e := range t {
			collectPorts(e, set)
		}
	}
}

// ConfigFiles lists the files whose contents decide what `devcontainer up`
// builds: everything under .devcontainer/, a root .devcontainer.json, and the
// Dockerfile / compose files the configuration points at outside that
// directory. A configuration that does not parse still yields the directory
// listing — the digest is then merely less complete, never an error.
func (devcontainerDef) ConfigFiles(ctx context.Context, fsys sandboxFS, src string) ([]string, error) {
	seen := map[string]bool{}
	add := func(abs string) {
		if !fsys.IsRegular(ctx, abs) {
			return
		}
		rel, err := filepath.Rel(src, abs)
		if err != nil {
			return
		}
		seen[filepath.ToSlash(rel)] = true
	}
	dir := filepath.Join(src, ".devcontainer")
	files, err := fsys.Walk(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		add(f)
	}
	add(filepath.Join(src, ".devcontainer.json"))
	if cfg := devcontainerConfig(ctx, fsys, src); cfg != "" {
		if raw, err := fsys.ReadFile(ctx, cfg); err == nil {
			for _, ref := range referencedBuildFiles(cfg, raw) {
				add(ref)
			}
		}
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
func referencedBuildFiles(cfgPath string, raw []byte) []string {
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

// writeEmbedded copies an embedded feature directory into dir (scripts 0755).
func writeEmbedded(ctx context.Context, fsys sandboxFS, src fs.FS, root, dir string) error {
	if err := fsys.MkdirAll(ctx, dir); err != nil {
		return err
	}
	return fs.WalkDir(src, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := fs.ReadFile(src, p)
		if rerr != nil {
			return rerr
		}
		mode := fs.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return fsys.WriteFile(ctx, filepath.Join(dir, filepath.Base(p)), data, mode)
	})
}

// materializeHarnessFeature writes the harness feature into the repo's
// .devcontainer so `up --additional-features` can install it. A repo-authored
// dev container runs the REPO's image, which knows nothing about
// opencode/codex — without this the first agent stage exits 127.
//
// The static feature (install.sh) owns the Node bootstrap; the per-project
// install commands are generated here into two files it reads:
// harness-binaries (what to probe) and harness-install.sh (guarded installs,
// so a harness already in the image is left alone).
func materializeHarnessFeature(ctx context.Context, fsys sandboxFS, src string, steps []ports.HarnessInstall) error {
	dir := filepath.Join(src, ".devcontainer", "apothecary-harness")
	if err := writeEmbedded(ctx, fsys, wsassets.HarnessFeature, "harness-feature", dir); err != nil {
		return err
	}
	var bins, reqs, script strings.Builder
	seenReq := map[string]bool{}
	script.WriteString("#!/bin/sh\n# Generated by apothecary — per-project harness installs.\nset -e\n")
	for _, s := range steps {
		if s.Binary != "" {
			bins.WriteString(s.Binary + "\n")
		}
		// Prerequisites are per-harness, so the feature installs only what
		// this project's harnesses actually need — no runtime is assumed.
		for _, r := range s.Requires {
			if r != "" && !seenReq[r] {
				seenReq[r] = true
				reqs.WriteString(r + "\n")
			}
		}
		for _, cmd := range s.Commands {
			if s.Binary == "" {
				fmt.Fprintf(&script, "%s\n", cmd)
				continue
			}
			// Guarded: an image that already ships the harness is untouched.
			fmt.Fprintf(&script, "if command -v %s >/dev/null 2>&1; then\n"+
				"  echo \"apothecary-harness: %s already present\"\nelse\n  %s\nfi\n",
				s.Binary, s.Binary, cmd)
		}
	}
	if err := fsys.WriteFile(ctx, filepath.Join(dir, "harness-binaries"), []byte(bins.String()), 0o644); err != nil {
		return err
	}
	if err := fsys.WriteFile(ctx, filepath.Join(dir, "harness-requires"), []byte(reqs.String()), 0o644); err != nil {
		return err
	}
	if err := fsys.WriteFile(ctx, filepath.Join(dir, "harness-install.sh"), []byte(script.String()), 0o755); err != nil {
		return err
	}
	return gitExclude(ctx, fsys, src, ".devcontainer/apothecary-harness/")
}

// codeServerExtDir is the image-baked, shared extensions dir the code-server
// feature installs into at build time and the engine starts code-server with.
// MUST match workspace/codeserver-feature/install.sh's EXT_DIR.
const codeServerExtDir = "/usr/local/share/apothecary-code-server/extensions"

// materializeCodeServerFeature writes the code-server feature into the repo's
// .devcontainer so `up --additional-features` installs it at build time. The
// extensions the feature pre-installs are the UNION of the repo devcontainer's
// own customizations.vscode.extensions and the project list — sorted+deduped
// into a generated `extensions` file, so an unchanged list is a build-cache hit.
func materializeCodeServerFeature(ctx context.Context, fsys sandboxFS, src string, projectExts []string) error {
	dir := filepath.Join(src, ".devcontainer", "apothecary-codeserver")
	if err := writeEmbedded(ctx, fsys, wsassets.CodeServer, "codeserver-feature", dir); err != nil {
		return err
	}
	set := map[string]bool{}
	for _, e := range append(repoExtensions(ctx, fsys, src), projectExts...) {
		if e = strings.TrimSpace(e); e != "" {
			set[e] = true
		}
	}
	exts := make([]string, 0, len(set))
	for e := range set {
		exts = append(exts, e)
	}
	sort.Strings(exts) // deterministic → stable feature content → stable build cache
	var b strings.Builder
	for _, e := range exts {
		b.WriteString(e + "\n")
	}
	if err := fsys.WriteFile(ctx, filepath.Join(dir, "extensions"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	return gitExclude(ctx, fsys, src, ".devcontainer/apothecary-codeserver/")
}

// repoExtensions reads customizations.vscode.extensions straight from the
// repo's devcontainer.json (the author's declared list — not extensions other
// features pull in). Reads the raw file (no CLI call) so it never disturbs the
// provision command sequence; devcontainer.json is JSONC, so comments +
// trailing commas are stripped first. Best-effort: empty on any miss.
func repoExtensions(ctx context.Context, fsys sandboxFS, src string) []string {
	cfg := devcontainerConfig(ctx, fsys, src)
	if cfg == "" {
		return nil
	}
	data, err := fsys.ReadFile(ctx, cfg)
	if err != nil {
		return nil
	}
	var rc struct {
		Customizations struct {
			VSCode struct {
				Extensions []string `json:"extensions"`
			} `json:"vscode"`
		} `json:"customizations"`
	}
	if json.Unmarshal(stripJSONC(data), &rc) == nil {
		return rc.Customizations.VSCode.Extensions
	}
	return nil
}

// gitExclude appends an entry to .git/info/exclude (idempotent) so the
// injected feature is invisible to `git status` and uncommittable.
func gitExclude(ctx context.Context, fsys sandboxFS, src, entry string) error {
	return fsys.AppendLineOnce(ctx, filepath.Join(src, ".git", "info", "exclude"), entry)
}

// stripJSONC turns JSONC into JSON stdlib encoding/json accepts: it removes
// // line and /* */ block comments (respecting string literals) and trailing
// commas before } or ]. Enough for devcontainer.json.
func stripJSONC(data []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++ // land on '/', loop's i++ steps past it
		default:
			out = append(out, c)
		}
	}
	// Trailing commas: ,<ws>} or ,<ws>] → drop the comma.
	return trailingCommaRE.ReplaceAll(out, []byte("$1"))
}

var trailingCommaRE = regexp.MustCompile(`,(\s*[}\]])`)

var _ Definition = devcontainerDef{}
