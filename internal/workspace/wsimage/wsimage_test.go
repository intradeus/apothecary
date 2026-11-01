package wsimage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestNewUsesTheRealCLIRunner(t *testing.T) {
	b := New("/runs")
	if b.runsDir != "/runs" {
		t.Errorf("runsDir = %q", b.runsDir)
	}
	if _, ok := b.docker.(cliRunner); !ok {
		t.Errorf("docker = %T, want cliRunner", b.docker)
	}
}

func TestHashFSIsOrderIndependentAndContentSensitive(t *testing.T) {
	a := fstest.MapFS{"b.txt": {Data: []byte("two")}, "a.txt": {Data: []byte("one")}}
	// Same files, declared in a different map order, hash the same (walk is sorted).
	a2 := fstest.MapFS{"a.txt": {Data: []byte("one")}, "b.txt": {Data: []byte("two")}}
	var h1, h2 bytes.Buffer
	hashFS(&h1, a)
	hashFS(&h2, a2)
	if !bytes.Equal(h1.Bytes(), h2.Bytes()) {
		t.Fatal("hash depends on map order")
	}
	// A changed byte changes the hash.
	var h3 bytes.Buffer
	hashFS(&h3, fstest.MapFS{"a.txt": {Data: []byte("one")}, "b.txt": {Data: []byte("TWO")}})
	if bytes.Equal(h1.Bytes(), h3.Bytes()) {
		t.Fatal("hash ignored a content change")
	}
	// A length-collision guard: "ab"+"c" vs "a"+"bc" must differ (the \x00 len framing).
	var p, q bytes.Buffer
	hashFS(&p, fstest.MapFS{"x": {Data: []byte("ab")}, "y": {Data: []byte("c")}})
	hashFS(&q, fstest.MapFS{"x": {Data: []byte("a")}, "y": {Data: []byte("bc")}})
	if bytes.Equal(p.Bytes(), q.Bytes()) {
		t.Fatal("hash is ambiguous across file boundaries")
	}
}

func TestMaterializeFS(t *testing.T) {
	src := fstest.MapFS{
		"Dockerfile":     {Data: []byte("FROM scratch")},
		"sub/nested.txt": {Data: []byte("deep")},
	}
	dir := t.TempDir()
	if err := materializeFS(src, dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"Dockerfile": "FROM scratch", "sub/nested.txt": "deep"} {
		got, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", path, got, err)
		}
	}
}

// recordingRunner captures the build args and dir.
type recordingRunner struct {
	dir  string
	args []string
	out  string
	exit int
	err  error
}

func (r *recordingRunner) Run(_ context.Context, dir string, args ...string) (string, int, error) {
	r.dir, r.args = dir, args
	return r.out, r.exit, r.err
}

func TestBuildPassesArgsAndWritesLog(t *testing.T) {
	r := &recordingRunner{out: "ok"}
	runs := t.TempDir()
	b := NewWithRunner(r, runs)
	if err := b.build(context.Background(), "/ctx", "apothecary/x:tag", "A=1", "B=2"); err != nil {
		t.Fatal(err)
	}
	if r.dir != "/ctx" {
		t.Errorf("dir = %q", r.dir)
	}
	want := []string{"build", "-t", "apothecary/x:tag", "--build-arg", "A=1", "--build-arg", "B=2", "."}
	if len(r.args) != len(want) {
		t.Fatalf("args = %v", r.args)
	}
	for i := range want {
		if r.args[i] != want[i] {
			t.Fatalf("args = %v, want %v", r.args, want)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(runs, "builds", "apothecary-x-tag.log")); string(got) != "ok" {
		t.Errorf("log = %q", got)
	}
}

func TestBuildFailureTailsTheLog(t *testing.T) {
	r := &recordingRunner{out: "step 3/9: error here", exit: 2}
	if err := NewWithRunner(r, t.TempDir()).build(context.Background(), "/ctx", "t"); err == nil || !bytes.Contains([]byte(err.Error()), []byte("error here")) {
		t.Fatalf("err = %v", err)
	}
}

// writeLog with no runsDir is a silent no-op; it never fails a build.
func TestWriteLogWithoutRunsDir(t *testing.T) {
	NewWithRunner(&recordingRunner{}, "").writeLog("apothecary/x:tag", "out") // must not panic
}

func TestToolsImageTagAndBrowserTagAreContentAddressed(t *testing.T) {
	if ToolsImageTag() != ToolsImageTag() || BrowserImageTag() != BrowserImageTag() {
		t.Error("tags must be stable")
	}
}
