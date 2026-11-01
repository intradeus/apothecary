package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/platform/shell"
	"apothecary/internal/ports/envport"
)

// devcontainerDef is the repo's own .devcontainer/devcontainer.json (or a root
// .devcontainer.json), built and entered with the reference @devcontainers/cli
// running against the sandbox's docker daemon. Image, Dockerfile and compose
// configurations all work, compose siblings included. Nothing of apothecary's
// is injected into the repo's build: the harnesses and code-server arrive as
// the bind-mounted tools bundle (/opt/apothecary/tools), like the shim.
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
func (d devcontainerDef) Up(ctx context.Context, e defEnv, spec envport.EnvSpec, o upOptions) (Workspace, error) {
	plog := o.plog
	if plog == nil {
		plog = func(string, ...any) {}
	}
	args := []string{"up",
		"--workspace-folder", e.src,
		"--id-label", label(e.name),
		"--id-label", managedLabel + "=true",
		"--mount", "type=bind,source=" + o.shimDir + ",target=/opt/apothecary",
		// The tools bundle (harness CLIs + code-server), read-only by way of
		// the sandbox's own mount of it: the CLI's --mount knows only
		// type/source/target. Nested under the shim mount — docker orders
		// mounts by target, and a compose override lists both as volumes.
		"--mount", "type=bind,source=" + o.toolsDir + ",target=/opt/apothecary/tools",
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
	// The repo is honored untouched: no features, no override config. Egress
	// is enforced externally by the per-project gateway, so the workspace
	// container needs no NET_ADMIN and compose siblings are covered too.
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
	//   * mode=max: intermediate stages too (a multi-stage Dockerfile's
	//     builders, the repo's own features).
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
	plog("→ building devcontainer (can take several minutes)\n")
	stdout, stderr, code, err := e.run(ctx, d.bin, args, nil, o.log, "BUILDX_BAKE_ENTITLEMENTS_FS=0")
	if err != nil {
		return Workspace{}, err
	}
	res, perr := parseUpResult(stdout)
	if code != 0 || perr != nil || res.Outcome != "success" {
		return Workspace{}, fmt.Errorf("devcontainer up %s: exit %d: %s%s", e.name, code,
			strings.TrimSpace(res.Message+" "+res.Description), shell.Tail("\n"+string(stderr), 2048))
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

// EditorExtensions reads customizations.vscode.extensions straight from the
// repo's devcontainer.json (the author's declared list — not extensions other
// features pull in). Reads the raw file (no CLI call); devcontainer.json is
// JSONC, so comments + trailing commas are stripped first. Best-effort: nil
// on any miss.
func (devcontainerDef) EditorExtensions(ctx context.Context, e defEnv) []string {
	cfg := devcontainerConfig(ctx, e.fs, e.src)
	if cfg == "" {
		return nil
	}
	data, err := e.fs.ReadFile(ctx, cfg)
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
		case c == '}' || c == ']':
			// Trailing commas: ,<ws>} or ,<ws>] → drop the comma. A string
			// ends in a quote, so this never reaches into one.
			end := len(bytes.TrimRight(out, " \t\r\n\f"))
			if end > 0 && out[end-1] == ',' {
				out = append(out[:end-1], out[end:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

var _ Definition = devcontainerDef{}
