package wsimage

import (
	"context"
	"strings"
	"testing"
)

// fakeDocker scripts the docker CLI: images "exist" once built.
type fakeDocker struct {
	built  []string
	images map[string]bool
	fail   string // tag whose build fails
}

func (f *fakeDocker) Run(_ context.Context, dir string, args ...string) (string, int, error) {
	switch args[0] {
	case "image": // inspect
		if f.images[args[2]] {
			return "", 0, nil
		}
		return "no such image", 1, nil
	case "build":
		tag := args[2]
		f.built = append(f.built, tag)
		if tag == f.fail {
			return "boom: fragment line 3 exploded", 1, nil
		}
		if f.images == nil {
			f.images = map[string]bool{}
		}
		f.images[tag] = true
		return "ok", 0, nil
	}
	return "", 0, nil
}

func TestComposeShape(t *testing.T) {
	df := Compose("apothecary/base:abc", "RUN apt-get install -y python3\n", []string{"RUN npm install -g opencode-ai@1.17.8"})
	lines := strings.Split(strings.TrimSpace(df), "\n")
	if lines[0] != "FROM apothecary/base:abc" || lines[1] != "USER root" {
		t.Fatalf("head = %v", lines[:2])
	}
	// Fragment runs as root, harness after it, apothecary user restored last.
	iFrag := strings.Index(df, "python3")
	iHarness := strings.Index(df, "opencode-ai")
	iUser := strings.LastIndex(df, "USER apothecary")
	if !(iFrag < iHarness && iHarness < iUser) {
		t.Fatalf("ordering wrong:\n%s", df)
	}
	if !strings.HasSuffix(strings.TrimSpace(df), "WORKDIR /home/apothecary") {
		t.Fatalf("tail wrong:\n%s", df)
	}
}

func TestTagIsContentAddressed(t *testing.T) {
	a := Tag(Compose("base:1", "RUN a", nil))
	b := Tag(Compose("base:1", "RUN a", nil))
	c := Tag(Compose("base:1", "RUN b", nil))
	d := Tag(Compose("base:2", "RUN a", nil))
	e := Tag(Compose("base:1", "RUN a", []string{"RUN h"}))
	if a != b {
		t.Error("same inputs, different tags")
	}
	for i, other := range []string{c, d, e} {
		if a == other {
			t.Errorf("input change %d did not change the tag", i)
		}
	}
	if !strings.HasPrefix(a, "apothecary/ws:") {
		t.Errorf("tag = %q", a)
	}
}

func TestEnsureBuildsOnceThenCaches(t *testing.T) {
	fd := &fakeDocker{}
	b := NewWithRunner(fd, t.TempDir(), "")
	df := Compose(b.BaseRef(), "RUN true", nil)

	tag, cached, err := b.Ensure(context.Background(), df)
	if err != nil || cached {
		t.Fatalf("first ensure: cached=%v err=%v", cached, err)
	}
	// Base then workspace were both built.
	if len(fd.built) != 2 || fd.built[0] != b.BaseRef() || fd.built[1] != tag {
		t.Fatalf("built = %v", fd.built)
	}
	_, cached, err = b.Ensure(context.Background(), df)
	if err != nil || !cached {
		t.Fatalf("second ensure: cached=%v err=%v", cached, err)
	}
	if len(fd.built) != 2 {
		t.Fatalf("cache hit rebuilt: %v", fd.built)
	}
}

func TestEnsureFailureSurfacesBuildOutput(t *testing.T) {
	fd := &fakeDocker{}
	b := NewWithRunner(fd, t.TempDir(), "")
	df := Compose(b.BaseRef(), "RUN false", nil)
	fd.fail = Tag(df)
	_, _, err := b.Ensure(context.Background(), df)
	if err == nil || !strings.Contains(err.Error(), "fragment line 3 exploded") {
		t.Fatalf("err = %v, want build output", err)
	}
}

func TestBaseOverrideMustExist(t *testing.T) {
	fd := &fakeDocker{}
	b := NewWithRunner(fd, t.TempDir(), "ghcr.io/x/base:v1")
	if b.BaseRef() != "ghcr.io/x/base:v1" {
		t.Fatalf("base ref = %q", b.BaseRef())
	}
	_, _, err := b.Ensure(context.Background(), Compose(b.BaseRef(), "", nil))
	if err == nil || !strings.Contains(err.Error(), "APOTHECARY_BASE_IMAGE") {
		t.Fatalf("missing override err = %v", err)
	}
	if len(fd.built) != 0 {
		t.Fatal("override path must never build the base")
	}
}

func TestPresetsCarryDomains(t *testing.T) {
	for _, p := range Presets() {
		if p.Name == "" || p.Label == "" || len(p.Lines) == 0 {
			t.Errorf("preset %+v incomplete", p)
		}
		for _, l := range p.Lines {
			up := strings.ToUpper(strings.Fields(l)[0])
			if up == "FROM" || up == "COPY" || up == "USER" {
				t.Errorf("preset %s contains forbidden directive: %s", p.Name, l)
			}
		}
	}
}
