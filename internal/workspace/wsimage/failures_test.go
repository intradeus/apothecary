package wsimage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type dockerResp struct {
	out  string
	code int
	err  error
}

// scriptDocker answers each docker verb from a script: a verb maps to
// (output, exitCode, err). "image" defaults to "not present" so a build runs.
type scriptDocker struct {
	resp    map[string]dockerResp
	cpMakes bool // cp really creates its destination (file for shim, dir+marker for tools)
	calls   []string
}

func (d *scriptDocker) Run(_ context.Context, _ string, args ...string) (string, int, error) {
	d.calls = append(d.calls, args[0])
	if r, ok := d.resp[args[0]]; ok {
		if args[0] == "cp" && d.cpMakes && r.err == nil && r.code == 0 {
			d.makeDest(args)
		}
		return r.out, r.code, r.err
	}
	switch args[0] {
	case "image":
		return "", 1, nil // not present: build
	case "cp":
		if d.cpMakes {
			d.makeDest(args)
		}
	}
	return "", 0, nil
}

func (d *scriptDocker) makeDest(args []string) {
	dst := args[2] // cid:/path dst
	if strings.Contains(args[1], ":/tools") {
		_ = os.MkdirAll(dst, 0o755)
		_ = os.WriteFile(filepath.Join(dst, toolsHashFile), []byte("h\n"), 0o644)
	} else {
		_ = os.WriteFile(dst, []byte("BIN"), 0o644)
	}
}

func resp(out string, code int, err error) dockerResp { return dockerResp{out, code, err} }

func TestEnsureShimBuildFailure(t *testing.T) {
	d := &scriptDocker{resp: map[string]dockerResp{"build": resp("compile error", 1, nil)}}
	if _, err := NewWithRunner(d, t.TempDir()).EnsureShim(context.Background()); err == nil || !strings.Contains(err.Error(), "docker build") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureShimExtractionFailures(t *testing.T) {
	mk := func(verb string, r dockerResp) *scriptDocker {
		return &scriptDocker{cpMakes: true, resp: map[string]dockerResp{verb: r}}
	}
	t.Run("create errors", func(t *testing.T) {
		if _, err := NewWithRunner(mk("create", resp("", 0, errors.New("daemon gone"))), t.TempDir()).EnsureShim(context.Background()); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("create non-zero", func(t *testing.T) {
		if _, err := NewWithRunner(mk("create", resp("no such image", 1, nil)), t.TempDir()).EnsureShim(context.Background()); err == nil || !strings.Contains(err.Error(), "docker create") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cp fails", func(t *testing.T) {
		d := mk("cp", resp("no space", 1, nil))
		if _, err := NewWithRunner(d, t.TempDir()).EnsureShim(context.Background()); err == nil || !strings.Contains(err.Error(), "docker cp shim") {
			t.Fatalf("err = %v", err)
		}
	})
}

// With the image already present, no build runs — straight to extraction.
func TestEnsureShimSkipsBuildWhenImagePresent(t *testing.T) {
	d := &scriptDocker{cpMakes: true, resp: map[string]dockerResp{"image": resp("[]", 0, nil)}}
	if _, err := NewWithRunner(d, t.TempDir()).EnsureShim(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range d.calls {
		if c == "build" {
			t.Error("a present image must not be rebuilt")
		}
	}
}

func TestEnsureToolsBuildAndExtractionFailures(t *testing.T) {
	mk := func(verb string, r dockerResp) *scriptDocker {
		return &scriptDocker{cpMakes: true, resp: map[string]dockerResp{verb: r}}
	}
	t.Run("build fails", func(t *testing.T) {
		if _, err := NewWithRunner(mk("build", resp("boom", 1, nil)), t.TempDir()).EnsureTools(context.Background()); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("create non-zero", func(t *testing.T) {
		if _, err := NewWithRunner(mk("create", resp("no image", 1, nil)), t.TempDir()).EnsureTools(context.Background()); err == nil || !strings.Contains(err.Error(), "docker create") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cp fails", func(t *testing.T) {
		if _, err := NewWithRunner(mk("cp", resp("no space", 1, nil)), t.TempDir()).EnsureTools(context.Background()); err == nil || !strings.Contains(err.Error(), "docker cp tools bundle") {
			t.Fatalf("err = %v", err)
		}
	})
}

// A cp that is a no-op leaves no /tools, so the rename of the (absent) tmp
// fails: an incomplete extraction is caught, never trusted.
func TestEnsureToolsIncompleteExtraction(t *testing.T) {
	d := &scriptDocker{}
	if _, err := NewWithRunner(d, t.TempDir()).EnsureTools(context.Background()); err == nil {
		t.Fatal("an extraction with no marker must fail")
	}
}

func TestEnsureBrowserImageBuildFailure(t *testing.T) {
	d := &scriptDocker{resp: map[string]dockerResp{"build": resp("", 0, errors.New("daemon gone"))}}
	if _, err := NewWithRunner(d, t.TempDir()).EnsureBrowserImage(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}
