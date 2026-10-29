package skillstore

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"apothecary/internal/ports"
	"apothecary/internal/skills"
)

// Remote skills are fetched the way curl would: a plain, unauthenticated
// GET of the PUBLIC archive the host serves for a ref — no API tokens, no
// git clone. Private repositories are out of scope by design.
const (
	// maxArchiveBytes caps one download (a GitHub tarball is the whole
	// repository: there is no server-side folder filter) AND the bytes kept
	// from it; maxUnpackedBytes caps what the decompressor may yield, so a
	// bomb is a clear error, not a memory spike.
	maxArchiveBytes  = 64 << 20
	maxUnpackedBytes = 512 << 20
	fetchTimeout     = 90 * time.Second
	userAgent        = "apothecary-skills/1"
	defaultRef       = "HEAD"
)

// Fetched is what one download yielded: the commit when the archive said
// (git archives carry it as a PAX comment), the regular files under the
// requested folder with paths relative to it, and notes about entries
// left out (symlinks, oversize files).
type Fetched struct {
	Commit string
	Files  []ports.TreeFile
	Notes  []string
}

// FetchFunc downloads a source's folder. Fetch is the real one; tests
// script their own.
type FetchFunc func(ctx context.Context, src Source) (Fetched, error)

// ParseSourceURL understands three shapes and canonicalises them into a
// Source whose URL is the identity a skill is unique by:
//
//	https://github.com/{owner}/{repo}[/tree/{ref}[/{path}]]
//	https://{gitlab host}/{namespace…}/{project}[/-/tree/{ref}[/{path}]]
//	https://{anything}/….tar.gz | .tgz | .zip        (a direct archive)
//
// extraPath is appended to the URL's folder (the import form's optional
// subfolder; the only way to point inside a direct archive). A ref with
// a "/" in it cannot be told from the path and is read as its first
// segment. No ref means HEAD.
func ParseSourceURL(raw, extraPath string) (Source, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Source{}, fmt.Errorf("a URL is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Source{}, fmt.Errorf("not an http(s) URL: %q", raw)
	}
	sub, err := cleanFolder(extraPath)
	if err != nil {
		return Source{}, err
	}
	segs := splitPath(u.Path)
	host := strings.ToLower(u.Host)

	if n := len(segs); n > 0 && isArchiveName(segs[n-1]) {
		src := Source{Kind: "archive", Host: host, Path: sub, URL: u.Scheme + "://" + u.Host + u.Path}
		if u.RawQuery != "" {
			src.URL += "?" + u.RawQuery
		}
		if sub != "" {
			src.URL += "#" + sub
		}
		return src, nil
	}

	if host == "github.com" || host == "www.github.com" {
		if len(segs) < 2 {
			return Source{}, fmt.Errorf("a GitHub URL needs an owner and a repository: %q", raw)
		}
		src := Source{Kind: "github", Host: "github.com", Owner: segs[0], Repo: strings.TrimSuffix(segs[1], ".git"), Ref: defaultRef}
		rest := segs[2:]
		if len(rest) > 0 {
			if rest[0] != "tree" || len(rest) < 2 {
				return Source{}, fmt.Errorf("a GitHub folder URL looks like https://github.com/owner/repo/tree/<ref>/<path>: %q", raw)
			}
			src.Ref = rest[1]
			src.Path = strings.Join(rest[2:], "/")
		}
		src.Path = joinFolder(src.Path, sub)
		src.URL = fmt.Sprintf("https://github.com/%s/%s/tree/%s", src.Owner, src.Repo, src.Ref)
		if src.Path != "" {
			src.URL += "/" + src.Path
		}
		return src, nil
	}

	// GitLab: the "/-/" marker separates the project path from the view;
	// a bare project URL on a gitlab host has none.
	dash := -1
	for i, s := range segs {
		if s == "-" {
			dash = i
			break
		}
	}
	if dash >= 0 || strings.Contains(host, "gitlab") {
		proj := segs
		var rest []string
		if dash >= 0 {
			proj, rest = segs[:dash], segs[dash+1:]
		}
		if len(proj) < 2 {
			return Source{}, fmt.Errorf("a GitLab URL needs a namespace and a project: %q", raw)
		}
		src := Source{Kind: "gitlab", Host: host, Owner: strings.Join(proj[:len(proj)-1], "/"), Repo: strings.TrimSuffix(proj[len(proj)-1], ".git"), Ref: defaultRef}
		if len(rest) > 0 {
			if rest[0] != "tree" || len(rest) < 2 {
				return Source{}, fmt.Errorf("a GitLab folder URL looks like https://%s/group/project/-/tree/<ref>/<path>: %q", host, raw)
			}
			src.Ref = rest[1]
			src.Path = strings.Join(rest[2:], "/")
		}
		src.Path = joinFolder(src.Path, sub)
		src.URL = fmt.Sprintf("%s://%s/%s/%s/-/tree/%s", u.Scheme, u.Host, src.Owner, src.Repo, src.Ref)
		if src.Path != "" {
			src.URL += "/" + src.Path
		}
		return src, nil
	}
	return Source{}, fmt.Errorf("unsupported URL %q: give a github.com or GitLab folder URL, or a link to a .tar.gz/.zip", raw)
}

// childSource derives the source of one skill found inside a folder import.
func childSource(folder Source, child string) Source {
	src := folder
	src.Path = joinFolder(folder.Path, child)
	if folder.Kind == "archive" {
		base, _, _ := strings.Cut(folder.URL, "#")
		src.URL = base + "#" + src.Path
	} else {
		src.URL = folder.URL + "/" + child
	}
	return src
}

// displayName is what a folder import names its skillset: the folder, else
// the repository, else the archive file.
func (s Source) displayName() string {
	if s.Path != "" {
		return path.Base(s.Path)
	}
	if s.Repo != "" {
		return s.Repo
	}
	base, _, _ := strings.Cut(s.URL, "#")
	name := path.Base(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".zip"), ".tgz"), ".tar.gz"))
	if name == "" || name == "." || name == "/" {
		return "skills"
	}
	return name
}

// archiveURLs lists the public archive URLs to try, in order. Only a
// defaulted ref gets fallbacks: a GitLab host may not resolve HEAD, so
// main and master follow.
func archiveURLs(s Source) []string {
	switch s.Kind {
	case "github":
		return []string{fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/%s", s.Owner, s.Repo, url.PathEscape(s.Ref))}
	case "gitlab":
		refs := []string{s.Ref}
		if s.Ref == defaultRef {
			refs = append(refs, "main", "master")
		}
		var out []string
		for _, ref := range refs {
			u := fmt.Sprintf("https://%s/%s/%s/-/archive/%s/%s-%s.tar.gz", s.Host, s.Owner, s.Repo, url.PathEscape(ref), s.Repo, url.PathEscape(strings.ReplaceAll(ref, "/", "-")))
			if s.Path != "" {
				u += "?path=" + url.QueryEscape(s.Path)
			}
			out = append(out, u)
		}
		return out
	default:
		base, _, _ := strings.Cut(s.URL, "#")
		return []string{base}
	}
}

var errArchiveNotFound = errors.New("archive not found")

// Fetch downloads the source's archive over plain HTTP and returns the
// files under its folder.
func Fetch(ctx context.Context, src Source) (Fetched, error) {
	client := &http.Client{Timeout: fetchTimeout}
	var last error
	for _, u := range archiveURLs(src) {
		f, err := download(ctx, client, u, src.Path)
		if errors.Is(err, errArchiveNotFound) {
			last = err
			continue
		}
		if err != nil {
			return Fetched{}, fmt.Errorf("%s: %w", u, err)
		}
		return f, nil
	}
	return Fetched{}, fmt.Errorf("%s: no public archive found (is the repository public, and the ref right?): %w", src.URL, last)
}

func download(ctx context.Context, client *http.Client, u, folder string) (Fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Fetched{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return Fetched{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Fetched{}, errArchiveNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Fetched{}, fmt.Errorf("HTTP %s", resp.Status)
	}
	body := &cappedReader{r: resp.Body, left: maxArchiveBytes}
	ct := resp.Header.Get("Content-Type")
	if strings.HasSuffix(strings.ToLower(req.URL.Path), ".zip") || strings.Contains(ct, "application/zip") {
		return readZip(body, folder)
	}
	return readTarGz(body, folder)
}

// cappedReader errs (not EOFs) past its budget, so a too-big archive is a
// clear failure instead of a silently truncated import.
type cappedReader struct {
	r    io.Reader
	left int64
}

var (
	errTooLarge = fmt.Errorf("archive larger than %d MiB", maxArchiveBytes>>20)
	errTooMuch  = fmt.Errorf("archive holds more than %d MiB of files", maxArchiveBytes>>20)
)

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if err == nil && c.left <= 0 {
		// Peek for one more byte to tell "exactly at the cap" from "over".
		var one [1]byte
		if m, _ := c.r.Read(one[:]); m > 0 {
			return n, errTooLarge
		}
	}
	return n, err
}

type rawEntry struct {
	path string
	mode fs.FileMode
	data []byte
	size int64 // for oversize notes when data is dropped
}

func readTarGz(r io.Reader, folder string) (Fetched, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return Fetched{}, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(&cappedReader{r: gz, left: maxUnpackedBytes})
	var commit string
	var entries []rawEntry
	var notes []string
	var kept int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Fetched{}, fmt.Errorf("reading tar: %w", err)
		}
		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader:
			if c, ok := hdr.PAXRecords["comment"]; ok && commit == "" {
				commit = strings.TrimSpace(c)
			}
			continue
		case tar.TypeReg:
		case tar.TypeDir, tar.TypeXHeader:
			continue
		default:
			notes = append(notes, fmt.Sprintf("%s: skipped (not a regular file)", hdr.Name))
			continue
		}
		name := strings.TrimPrefix(path.Clean(hdr.Name), "./")
		if hdr.Size > skills.MaxFileBytes {
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return Fetched{}, err
			}
			entries = append(entries, rawEntry{path: name, size: hdr.Size})
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return Fetched{}, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}
		if kept += int64(len(data)); kept > maxArchiveBytes {
			return Fetched{}, errTooMuch
		}
		entries = append(entries, rawEntry{path: name, mode: fs.FileMode(hdr.Mode).Perm(), data: data, size: hdr.Size})
	}
	f := select_(entries, folder)
	f.Commit = commit
	f.Notes = append(notes, f.Notes...)
	return f, nil
}

func readZip(r io.Reader, folder string) (Fetched, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return Fetched{}, err
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return Fetched{}, fmt.Errorf("not a zip archive: %w", err)
	}
	var entries []rawEntry
	var notes []string
	var kept int64
	for _, zf := range zr.File {
		if strings.HasSuffix(zf.Name, "/") || zf.FileInfo().IsDir() {
			continue
		}
		if !zf.Mode().IsRegular() {
			notes = append(notes, fmt.Sprintf("%s: skipped (not a regular file)", zf.Name))
			continue
		}
		name := strings.TrimPrefix(path.Clean(zf.Name), "./")
		if zf.UncompressedSize64 > uint64(skills.MaxFileBytes) {
			entries = append(entries, rawEntry{path: name, size: int64(zf.UncompressedSize64)})
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return Fetched{}, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, skills.MaxFileBytes+1))
		rc.Close()
		if err != nil {
			return Fetched{}, err
		}
		if kept += int64(len(data)); kept > maxArchiveBytes {
			return Fetched{}, errTooMuch
		}
		entries = append(entries, rawEntry{path: name, mode: zf.Mode().Perm(), data: data, size: int64(len(data))})
	}
	f := select_(entries, folder)
	f.Notes = append(notes, f.Notes...)
	return f, nil
}

// select_ strips the archive's single top-level directory when every
// entry shares one (GitHub and GitLab archives do), keeps what lies under
// folder, and re-roots the paths there.
func select_(entries []rawEntry, folder string) Fetched {
	top := ""
	shared := len(entries) > 0
	for _, e := range entries {
		first, _, ok := strings.Cut(e.path, "/")
		if !ok {
			shared = false
			break
		}
		if top == "" {
			top = first
		} else if first != top {
			shared = false
			break
		}
	}
	prefix := ""
	if shared {
		prefix = top + "/"
	}
	if folder != "" {
		prefix += folder + "/"
	}
	var f Fetched
	for _, e := range entries {
		if !strings.HasPrefix(e.path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(e.path, prefix)
		if rel == "" {
			continue
		}
		if e.data == nil && e.size > skills.MaxFileBytes {
			f.Notes = append(f.Notes, fmt.Sprintf("%s: skipped (%d bytes, max %d)", rel, e.size, skills.MaxFileBytes))
			continue
		}
		if _, err := skills.CleanPath(rel); err != nil {
			f.Notes = append(f.Notes, fmt.Sprintf("%s: skipped (unusable path)", rel))
			continue
		}
		f.Files = append(f.Files, ports.TreeFile{Path: rel, Mode: e.mode, Data: e.data})
	}
	return f
}

func splitPath(p string) []string {
	var out []string
	for seg := range strings.SplitSeq(strings.Trim(p, "/"), "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func isArchiveName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".tgz") || strings.HasSuffix(n, ".zip")
}

// cleanFolder normalises a user-given subfolder: slashes trimmed, no dot
// segments. Empty means the root.
func cleanFolder(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	if _, err := skills.CleanPath(p); err != nil {
		return "", fmt.Errorf("invalid folder %q", p)
	}
	return p, nil
}

func joinFolder(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "/" + b
	}
}
