package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const profile = `mode: atomic
apothecary/a/x.go:1.1,2.2 3 1
apothecary/a/x.go:3.1,4.2 1 0
apothecary/a/x.go:1.1,2.2 3 0
apothecary/a/x.go:3.1,4.2 1 0
apothecary/b/y.go:1.1,2.2 2 0
apothecary/b/y.go:3.1,4.2 2 5
`

func TestParseCountsABlockOnceAcrossTestBinaries(t *testing.T) {
	rows, err := Parse(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	// Most missing first: b never ran 2 statements, a never ran 1.
	if rows[0].Package != "apothecary/b" || rows[0].Total != 4 || rows[0].Covered != 2 {
		t.Errorf("first = %+v", rows[0])
	}
	if rows[1].Package != "apothecary/a" || rows[1].Total != 4 || rows[1].Covered != 3 {
		t.Errorf("second = %+v", rows[1])
	}
}

func TestParseOrdersTiesByName(t *testing.T) {
	rows, err := Parse(strings.NewReader("mode: set\nz/z.go:1.1,2.2 1 0\na/a.go:1.1,2.2 1 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Package != "a" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestParseRefusesWhatIsNotAProfile(t *testing.T) {
	if _, err := Parse(strings.NewReader("mode: atomic\nnonsense\n")); err == nil {
		t.Fatal("want an error")
	}
}

func TestTotalAndPercent(t *testing.T) {
	sum := Total([]Row{{Total: 4, Covered: 3}, {Total: 4, Covered: 3}})
	if sum.Percent() != 75 || sum.Missing() != 2 {
		t.Errorf("sum = %+v", sum)
	}
	if (Row{}).Percent() != 100 {
		t.Error("an empty package has nothing to miss")
	}
}

func TestRenderEscapesAndShowsTheTotal(t *testing.T) {
	var b bytes.Buffer
	if err := Render(&b, []Row{{Package: "<evil>", Total: 2, Covered: 1}}, 90); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{"Backend coverage: 50.0%", "&lt;evil&gt;", "Floor 90.0%", `class="poor"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestRunWritesThePageAndHoldsTheFloor(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "p.out")
	if err := os.WriteFile(in, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "index.html")

	var stdout bytes.Buffer
	if err := run(in, out, 60, &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "62.5%") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if _, err := os.Stat(out); err != nil {
		t.Error(err)
	}

	err := run(in, out, 70, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "under the floor") {
		t.Errorf("err = %v", err)
	}
}

func TestRunNamesAMissingProfile(t *testing.T) {
	if err := run(filepath.Join(t.TempDir(), "none"), "", 0, &bytes.Buffer{}); err == nil {
		t.Fatal("want an error")
	}
}
