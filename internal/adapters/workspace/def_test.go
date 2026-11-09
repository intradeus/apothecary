package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"apothecary/internal/ports/envport"
)

func devEnv(t *testing.T, src string, r *ruleRunner) defEnv {
	t.Helper()
	return defEnv{name: "w", src: src, fs: osFS{}, run: r.Run}
}

func TestDefinitionUpFailureModes(t *testing.T) {
	d := devcontainerDef{bin: "devcontainer"}
	ctx := context.Background()
	o := upOptions{shimDir: "/s", toolsDir: "/t"}
	cases := []struct {
		name string
		resp response
		want string
	}{
		{"spawn", response{err: errors.New("no cli")}, "no cli"},
		{"exit with message", response{code: 1, stdout: `{"outcome":"error","message":"build failed","description":"step 3"}` + "\n", stderr: "tail of the log"}, "build failed step 3"},
		{"success with bad exit", response{code: 1, stdout: `{"outcome":"success"}` + "\n"}, "exit 1"},
		{"nonsense outcome", response{stdout: `{"outcome":"maybe"}` + "\n"}, "devcontainer up w: exit 0"},
		{"no output", response{}, "devcontainer up w"},
		{"garbage output", response{stdout: "not json\n"}, "devcontainer up w"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &ruleRunner{rules: []rule{{match: "up", resp: c.resp}}}
			ws, err := d.Up(ctx, devEnv(t, "/src", r), envport.EnvSpec{}, o) // nil plog must be fine
			if err == nil || !strings.Contains(err.Error(), c.want) || ws.ContainerID != "" {
				t.Errorf("ws=%+v err=%v", ws, err)
			}
		})
	}
}

func TestDefinitionUpFlags(t *testing.T) {
	d := devcontainerDef{bin: "dc"}
	ok := `{"outcome":"success","containerId":"c","remoteUser":"u","remoteWorkspaceFolder":"/wf"}` + "\n"
	r := &ruleRunner{rules: []rule{{match: "up", resp: response{stdout: "log line\n" + ok}}}}
	var said []string
	o := upOptions{shimDir: "/s", toolsDir: "/t", removeExisting: true, noCache: true, plog: func(f string, _ ...any) { said = append(said, f) }}
	ws, err := d.Up(context.Background(), devEnv(t, "/src", r), envport.EnvSpec{Project: "P1"}, o)
	if err != nil || ws != (Workspace{ContainerID: "c", RemoteUser: "u", WorkspaceFolder: "/wf"}) {
		t.Fatalf("ws=%+v err=%v", ws, err)
	}
	line := r.lines()[0]
	for _, want := range []string{"--remove-existing-container", "--build-no-cache", "--id-label apothecary.project=P1",
		"--skip-post-create", "--skip-post-attach", // the hooks run after root-init (RunUserCommands)
		"--mount type=bind,source=/s,target=/opt/apothecary", "--mount type=bind,source=/t,target=/opt/apothecary/tools"} {
		if !strings.Contains(line, want) {
			t.Errorf("up missing %q: %s", want, line)
		}
	}
	if strings.Contains(line, "--cache-from") {
		t.Errorf("cache flags with no gateway: %s", line)
	}
	if !envHas(r.snapshot()[0], "BUILDX_BAKE_ENTITLEMENTS_FS=0") {
		t.Errorf("env = %v", r.snapshot()[0].env)
	}
	if len(said) < 2 {
		t.Errorf("no progress said: %v", said)
	}
}

func TestDefinitionBestEffortReads(t *testing.T) {
	ctx := context.Background()
	d := devcontainerDef{bin: "dc"}
	for name, r := range map[string]rule{
		"exit":    fail("read-configuration", 1, ""),
		"spawn":   spawnFail("read-configuration"),
		"garbage": {match: "read-configuration", resp: response{stdout: "nope\n"}},
	} {
		e := devEnv(t, "/src", &ruleRunner{rules: []rule{r}})
		if d.IsCompose(ctx, e) {
			t.Errorf("%s: read as compose", name)
		}
		if p := d.DeclaredPorts(ctx, e); p != nil {
			t.Errorf("%s: ports = %v", name, p)
		}
	}
}

func TestDeclaredPortsShapes(t *testing.T) {
	out := `{"mergedConfiguration":{"forwardPorts":[3000,"8000","db:5432"],"appPort":9000},"configuration":{"appPort":["7000",7001]}}`
	e := devEnv(t, "/src", &ruleRunner{rules: []rule{{match: "read-configuration", resp: response{stdout: "banner\n" + out + "\n"}}}})
	got := devcontainerDef{}.DeclaredPorts(context.Background(), e)
	want := []int{3000, 7000, 7001, 8000, 9000}
	if len(got) != len(want) {
		t.Fatalf("ports = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ports = %v", got)
		}
	}
	// One half of the config missing is fine.
	e = devEnv(t, "/src", &ruleRunner{rules: []rule{{match: "read-configuration", resp: response{stdout: `{"configuration":{"forwardPorts":[1]}}` + "\n"}}}})
	if got := (devcontainerDef{}).DeclaredPorts(context.Background(), e); len(got) != 1 {
		t.Errorf("ports = %v", got)
	}
}

func TestDetectFindsBothConfigLocations(t *testing.T) {
	ctx := context.Background()
	d := devcontainerDef{}
	src := t.TempDir()
	if d.Detect(ctx, osFS{}, src) {
		t.Error("detected nothing as something")
	}
	if devcontainerConfig(ctx, osFS{}, src) != "" {
		t.Error("config in an empty repo")
	}
	os.WriteFile(filepath.Join(src, ".devcontainer.json"), []byte("{}"), 0o644)
	if got := devcontainerConfig(ctx, osFS{}, src); got != filepath.Join(src, ".devcontainer.json") {
		t.Errorf("root config = %q", got)
	}
	os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o755)
	os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"), []byte("{}"), 0o644)
	if got := devcontainerConfig(ctx, osFS{}, src); got != filepath.Join(src, ".devcontainer", "devcontainer.json") {
		t.Errorf("the directory's config wins: %q", got)
	}
}

func TestConfigFilesFollowTheConfig(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(src, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".devcontainer/devcontainer.json", `{
		// comments are fine
		"build": {"dockerfile": "Dockerfile.dev"},
		"dockerFile": "../Dockerfile.root",
		"dockerComposeFile": ["../compose.yml", "missing.yml", ""],
	}`)
	write(".devcontainer/Dockerfile.dev", "FROM x")
	write(".devcontainer/scripts/init.sh", "true")
	write("Dockerfile.root", "FROM y")
	write("compose.yml", "services: {}")
	write("unrelated.txt", "no")
	got, err := devcontainerDef{}.ConfigFiles(ctx, osFS{}, src)
	if err != nil {
		t.Fatal(err)
	}
	want := ".devcontainer/Dockerfile.dev .devcontainer/devcontainer.json .devcontainer/scripts/init.sh Dockerfile.root compose.yml"
	if strings.Join(got, " ") != want {
		t.Errorf("files = %v", got)
	}
}

func TestConfigFilesSingleComposeAndAbsoluteRef(t *testing.T) {
	src := t.TempDir()
	abs := filepath.Join(src, "abs.Dockerfile")
	os.WriteFile(abs, []byte("FROM z"), 0o644)
	os.WriteFile(filepath.Join(src, "c.yml"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(src, ".devcontainer.json"), []byte(`{"dockerFile":"`+abs+`","dockerComposeFile":"c.yml"}`), 0o644)
	got, err := devcontainerDef{}.ConfigFiles(context.Background(), osFS{}, src)
	if err != nil || strings.Join(got, " ") != ".devcontainer.json abs.Dockerfile c.yml" {
		t.Errorf("files = %v, %v", got, err)
	}
}

// failFS fails the reads a digest needs.
type failFS struct {
	osFS
	walkErr, readErr error
}

func (f failFS) Walk(ctx context.Context, d string) ([]string, error) {
	if f.walkErr != nil {
		return nil, f.walkErr
	}
	return f.osFS.Walk(ctx, d)
}

func (f failFS) ReadFile(ctx context.Context, p string) ([]byte, error) {
	if f.readErr != nil && !strings.HasSuffix(p, ".json") {
		return nil, f.readErr
	}
	return f.osFS.ReadFile(ctx, p)
}

func TestDigestFailures(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o755)
	os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"), []byte("not json"), 0o644)
	os.WriteFile(filepath.Join(src, ".devcontainer", "Dockerfile"), []byte("FROM a"), 0o644)
	boom := errors.New("boom")
	d := devcontainerDef{}
	if _, err := d.ConfigFiles(ctx, failFS{walkErr: boom}, src); !errors.Is(err, boom) {
		t.Errorf("walk = %v", err)
	}
	if _, err := definitionDigest(ctx, d, failFS{walkErr: boom}, src); !errors.Is(err, boom) {
		t.Errorf("digest walk = %v", err)
	}
	if _, err := definitionDigest(ctx, d, failFS{readErr: boom}, src); !errors.Is(err, boom) {
		t.Errorf("digest read = %v", err)
	}
	// A config that does not parse still yields the directory listing.
	files, err := d.ConfigFiles(ctx, osFS{}, src)
	if err != nil || len(files) != 2 {
		t.Errorf("unparseable config = %v, %v", files, err)
	}
	// The digest sees a rename, not just contents.
	a, _ := definitionDigest(ctx, d, osFS{}, src)
	os.Rename(filepath.Join(src, ".devcontainer", "Dockerfile"), filepath.Join(src, ".devcontainer", "Dockerfile2"))
	b, _ := definitionDigest(ctx, d, osFS{}, src)
	if a == "" || a == b {
		t.Errorf("digests = %q, %q", a, b)
	}
}

func TestConfigDirtyFailures(t *testing.T) {
	ctx := context.Background()
	a := newTestAdapter(t, &ruleRunner{})
	// The record itself unreadable: an error, not "clean".
	os.MkdirAll(filepath.Join(a.folderFor("w"), configSumFile), 0o755)
	if _, err := a.ConfigDirty(ctx, "w"); err == nil {
		t.Error("an unreadable record read as clean")
	}
	// A recorded digest that cannot be recomputed.
	a = newTestAdapter(t, &ruleRunner{})
	a.defs = []Definition{brokenDef{devcontainerDef{}}}
	os.MkdirAll(a.folderFor("w"), 0o755)
	os.WriteFile(filepath.Join(a.folderFor("w"), configSumFile), []byte("abc\n"), 0o644)
	if _, err := a.ConfigDirty(ctx, "w"); err == nil || !strings.Contains(err.Error(), "digest w devcontainer config") {
		t.Errorf("err = %v", err)
	}
	// Recording failing is only a note: the workspace is fine.
	var note string
	a.recordConfigDigest("w", a.srcDir("w"), func(f string, _ ...any) { note = f })
	if !strings.Contains(note, "no rebuild hint") {
		t.Errorf("note = %q", note)
	}
}

type brokenDef struct{ devcontainerDef }

func (brokenDef) ConfigFiles(context.Context, sandboxFS, string) ([]string, error) {
	return nil, errors.New("cannot list")
}

func TestEditorExtensionsMisses(t *testing.T) {
	ctx := context.Background()
	d := devcontainerDef{}
	src := t.TempDir()
	r := &ruleRunner{}
	if got := d.EditorExtensions(ctx, devEnv(t, src, r)); got != nil {
		t.Errorf("no config = %v", got)
	}
	cfg := filepath.Join(src, ".devcontainer.json")
	os.WriteFile(cfg, []byte("{nope"), 0o644)
	if got := d.EditorExtensions(ctx, devEnv(t, src, r)); got != nil {
		t.Errorf("garbage = %v", got)
	}
	e := devEnv(t, src, r)
	e.fs = failFS{readErr: errors.New("x")}
	os.Rename(cfg, filepath.Join(src, ".devcontainer.conf"))
	os.WriteFile(filepath.Join(src, ".devcontainer.json"), []byte(`{"customizations":{"vscode":{"extensions":["a.b"]}}}`), 0o644)
	if got := d.EditorExtensions(ctx, e); len(got) != 1 {
		t.Errorf("declared = %v", got)
	}
}

func TestStripJSONCStrings(t *testing.T) {
	in := `{"url": "http://x/*not a comment*/", "q": "say \"//hi\"", /* block */ "n": 1, // line
	"a": [1, 2,], }`
	want := "{\"url\": \"http://x/*not a comment*/\", \"q\": \"say \\\"//hi\\\"\",  \"n\": 1, \n\t\"a\": [1, 2] }"
	if got := string(stripJSONC([]byte(in))); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// A comment at the end of the file, with no newline, ends cleanly.
	if got := string(stripJSONC([]byte(`{} // bye`))); got != "{} " {
		t.Errorf("trailing comment = %q", got)
	}
	// An unterminated block comment swallows the rest, without running off.
	if got := string(stripJSONC([]byte(`{} /* open`))); got != "{} " {
		t.Errorf("open block = %q", got)
	}
}

// The comma rule looks at the whole text, strings included; only comments
// respect string literals.
func TestStripJSONCLeavesCommasInStrings(t *testing.T) {
	in := `{"cmd": "echo a,]"}`
	if got := string(stripJSONC([]byte(in))); got != in {
		t.Errorf("got %q", got)
	}
	// A value ending in a comma, then a real trailing comma behind a comment.
	for in, want := range map[string]string{
		`{"a": "b,"}`:                     `{"a": "b,"}`,
		`[1, /* c */ ]`:                   `[1  ]`,
		"[1,\n// c\n]":                    "[1\n\n]",
		`{"a": [1, 2,], "b": {"c": 3,},}`: `{"a": [1, 2], "b": {"c": 3}}`,
	} {
		if got := string(stripJSONC([]byte(in))); got != want {
			t.Errorf("stripJSONC(%q) = %q, want %q", in, got, want)
		}
	}
}
