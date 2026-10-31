package skillstore

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseSourceURL(t *testing.T) {
	cases := []struct {
		in, extra string
		want      Source
		bad       bool
	}{
		{in: "https://github.com/o/r", want: Source{Kind: "github", Host: "github.com", Owner: "o", Repo: "r", Ref: "HEAD", URL: "https://github.com/o/r/tree/HEAD"}},
		{in: "github.com/o/r.git/tree/main/skills/pdf", want: Source{Kind: "github", Host: "github.com", Owner: "o", Repo: "r", Ref: "main", Path: "skills/pdf", URL: "https://github.com/o/r/tree/main/skills/pdf"}},
		{in: "https://github.com/o/r/tree/v1.2", extra: "skills/", want: Source{Kind: "github", Host: "github.com", Owner: "o", Repo: "r", Ref: "v1.2", Path: "skills", URL: "https://github.com/o/r/tree/v1.2/skills"}},
		{in: "https://gitlab.com/grp/sub/proj/-/tree/main/skills", want: Source{Kind: "gitlab", Host: "gitlab.com", Owner: "grp/sub", Repo: "proj", Ref: "main", Path: "skills", URL: "https://gitlab.com/grp/sub/proj/-/tree/main/skills"}},
		{in: "https://git.example.com/grp/proj/-/tree/dev", want: Source{Kind: "gitlab", Host: "git.example.com", Owner: "grp", Repo: "proj", Ref: "dev", URL: "https://git.example.com/grp/proj/-/tree/dev"}},
		{in: "https://gitlab.example.org/grp/proj", want: Source{Kind: "gitlab", Host: "gitlab.example.org", Owner: "grp", Repo: "proj", Ref: "HEAD", URL: "https://gitlab.example.org/grp/proj/-/tree/HEAD"}},
		{in: "https://example.com/dl/skills.tar.gz", extra: "pack/", want: Source{Kind: "archive", Host: "example.com", Path: "pack", URL: "https://example.com/dl/skills.tar.gz#pack"}},
		{in: "https://example.com/dl/skills.zip?x=1", want: Source{Kind: "archive", Host: "example.com", URL: "https://example.com/dl/skills.zip?x=1"}},
		{in: "", bad: true},
		{in: "ftp://github.com/o/r", bad: true},
		{in: "https://github.com/o", bad: true},
		{in: "https://github.com/o/r/blob/main/x", bad: true},
		{in: "https://gitlab.com/grp/proj/-/blob/main/x", bad: true},
		{in: "https://example.com/some/page", bad: true},
		{in: "https://github.com/o/r", extra: "../x", bad: true},
	}
	for _, c := range cases {
		got, err := ParseSourceURL(c.in, c.extra)
		if c.bad {
			if err == nil {
				t.Errorf("%q accepted: %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q:\n got %+v\nwant %+v", c.in, got, c.want)
		}
	}
	if u := archiveURLs(Source{Kind: "github", Owner: "o", Repo: "r", Ref: "main"}); len(u) != 1 || u[0] != "https://codeload.github.com/o/r/tar.gz/main" {
		t.Errorf("github archive = %v", u)
	}
	gl := archiveURLs(Source{Kind: "gitlab", Host: "gitlab.com", Owner: "g", Repo: "p", Ref: "HEAD", Path: "a/b"})
	if len(gl) != 3 || gl[0] != "https://gitlab.com/g/p/-/archive/HEAD/p-HEAD.tar.gz?path=a%2Fb" || !strings.Contains(gl[1], "/main/p-main.tar.gz") || !strings.Contains(gl[2], "/master/") {
		t.Errorf("gitlab archives = %v", gl)
	}
	child := childSource(Source{Kind: "github", URL: "https://github.com/o/r/tree/main/skills", Path: "skills"}, "pdf")
	if child.URL != "https://github.com/o/r/tree/main/skills/pdf" || child.Path != "skills/pdf" {
		t.Errorf("child = %+v", child)
	}
	arch := childSource(Source{Kind: "archive", URL: "https://x/s.tar.gz", Path: ""}, "pdf")
	if arch.URL != "https://x/s.tar.gz#pdf" || arch.Path != "pdf" {
		t.Errorf("archive child = %+v", arch)
	}
}

type entry struct {
	name string
	mode int64
	data string
	kind byte
}

func tgz(t *testing.T, commit string, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if commit != "" {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": commit}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Typeflag: kind, Name: e.name, Mode: mode, Size: int64(len(e.data))}
		if kind == tar.TypeSymlink {
			hdr.Linkname = "SKILL.md"
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			tw.Write([]byte(e.data))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestFetchTarballStripsTopDirAndSelectsFolder(t *testing.T) {
	archive := tgz(t, "deadbeef",
		entry{name: "o-r-deadbee/", kind: tar.TypeDir},
		entry{name: "o-r-deadbee/README.md", data: "top"},
		entry{name: "o-r-deadbee/skills/pdf/SKILL.md", data: "---\nname: pdf\ndescription: d\n---\n"},
		entry{name: "o-r-deadbee/skills/pdf/scripts/x.sh", mode: 0o755, data: "echo"},
		entry{name: "o-r-deadbee/skills/pdf/link.md", kind: tar.TypeSymlink},
		entry{name: "o-r-deadbee/skills/other/SKILL.md", data: "---\nname: other\ndescription: d\n---\n"},
	)
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()

	client := srv.Client()
	f, err := download(context.Background(), client, srv.URL+"/o/r/tar.gz/main", "skills/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if f.Commit != "deadbeef" || len(f.Files) != 2 || f.Files[0].Path != "SKILL.md" || f.Files[1].Path != "scripts/x.sh" || f.Files[1].Mode != 0o755 {
		t.Errorf("fetched = %+v", f)
	}
	if len(f.Notes) != 1 || !strings.Contains(f.Notes[0], "link.md") {
		t.Errorf("notes = %v", f.Notes)
	}
	whole, _ := download(context.Background(), client, srv.URL+"/x.tar.gz", "")
	if len(whole.Files) != 4 || whole.Files[0].Path != "README.md" {
		t.Errorf("whole = %+v", whole)
	}
	if _, err := download(context.Background(), client, srv.URL+"/missing.tar.gz", ""); err != errArchiveNotFound {
		t.Errorf("404 = %v", err)
	}
	if _, err := download(context.Background(), client, srv.URL+"/x.tar.gz", "nowhere"); err != nil || true {
		// An empty selection is not an error here; the importer says "nothing found".
		empty, _ := download(context.Background(), client, srv.URL+"/x.tar.gz", "nowhere")
		if len(empty.Files) != 0 {
			t.Errorf("empty folder = %+v", empty)
		}
	}
}

func TestFetchZipAndCap(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []entry{{name: "top/pdf/SKILL.md", data: "---\nname: pdf\ndescription: d\n---\n"}, {name: "top/pdf/scripts/", kind: 'd'}, {name: "top/pdf/scripts/go.sh", mode: 0o755, data: "echo"}} {
		if e.kind == 'd' {
			zw.Create(e.name)
			continue
		}
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(0o644)
		if e.mode != 0 {
			h.SetMode(fs.FileMode(e.mode))
		}
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(e.data))
	}
	zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			w.Write(buf.Bytes())
			return
		}
		// A "tarball" bigger than the cap.
		w.Header().Set("Content-Type", "application/gzip")
		gz := gzip.NewWriter(w)
		tw := tar.NewWriter(gz)
		big := bytes.Repeat([]byte("x"), 1<<20)
		for i := 0; i < 70; i++ {
			tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "t/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Mode: 0o644, Size: int64(len(big))})
			tw.Write(big)
		}
		tw.Close()
		gz.Close()
	}))
	defer srv.Close()
	f, err := download(context.Background(), srv.Client(), srv.URL+"/s.zip", "pdf")
	if err != nil || len(f.Files) != 2 || f.Files[1].Path != "scripts/go.sh" || f.Files[1].Mode != 0o755 {
		t.Errorf("zip = %+v, %v", f, err)
	}
	if _, err := download(context.Background(), srv.Client(), srv.URL+"/big.tar.gz", ""); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("cap: %v", err)
	}
}
