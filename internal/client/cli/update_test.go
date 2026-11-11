package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// releases serves a release's SHA256SUMS and this machine's binary.
func releases(t *testing.T, version string, bin []byte, sums string) {
	t.Helper()
	asset := assetName(runtime.GOOS, runtime.GOARCH)
	if sums == "" {
		sum := sha256.Sum256(bin)
		sums = hex.EncodeToString(sum[:]) + "  " + asset + "\n" + strings.Repeat("0", 64) + "  apothecary-plan9-386\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + version + "/SHA256SUMS":
			fmt.Fprint(w, sums)
		case "/" + version + "/" + asset:
			_, _ = w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := releaseBase
	releaseBase = srv.URL
	t.Cleanup(func() { releaseBase = old })
}

func installed(t *testing.T, r *rig) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "apothecary")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.env.Executable = exe
	return exe
}

// update installs the release of the engine's version, checked against its
// SHA256SUMS, in place of this binary.
func TestUpdateInstallsTheEnginesRelease(t *testing.T) {
	e := newFakeEngine(t)
	e.version = "v2.0.0"
	r := newRig(t)
	r.env.Version = "v1.0.0"
	r.signIn(t, e)
	exe := installed(t, r)
	releases(t, "v2.0.0", []byte("new binary"), "")
	if code := r.run("update", "--check"); code != 0 || !strings.Contains(r.out.String(), "the engine is v2.0.0: run apothecary update") {
		t.Errorf("check = %d %q", code, r.out)
	}
	if code := r.run("update"); code != 0 || !strings.Contains(r.out.String(), "Updated apothecary v1.0.0 → v2.0.0.") {
		t.Fatalf("update = %d %q %q", code, r.out, r.err)
	}
	got, _ := os.ReadFile(exe)
	info, _ := os.Stat(exe)
	if string(got) != "new binary" || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("installed %q, mode %v", got, info.Mode())
	}
	r.env.Version = "v2.0.0"
	if code := r.run("update"); code != 0 || !strings.Contains(r.out.String(), "matches the engine") {
		t.Errorf("up to date = %d %q", code, r.out)
	}
}

func TestUpdateRefusals(t *testing.T) {
	e := newFakeEngine(t)
	e.version = "v2.0.0"
	r := newRig(t)
	r.env.Version = "v1.0.0"
	r.signIn(t, e)
	exe := installed(t, r)
	asset := assetName(runtime.GOOS, runtime.GOARCH)

	releases(t, "v2.0.0", []byte("new binary"), strings.Repeat("a", 64)+"  "+asset+"\n")
	if code := r.run("update"); code != 1 || !strings.Contains(r.err.String(), "does not match its SHA256SUMS") {
		t.Errorf("bad checksum = %d %q", code, r.err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Error("a binary that failed its checksum was installed")
	}
	releases(t, "v2.0.0", []byte("x"), "abc  apothecary-plan9-386\n")
	if code := r.run("update"); code != 1 || !strings.Contains(r.err.String(), "has no "+asset) {
		t.Errorf("no asset = %d %q", code, r.err)
	}
	releases(t, "v9.9.9", nil, "x")
	if code := r.run("update"); code != 1 || !strings.Contains(r.err.String(), "404") {
		t.Errorf("no release = %d %q", code, r.err)
	}
	e.version = "dev"
	if code := r.run("update"); code != 1 || !strings.Contains(r.err.String(), "development build") {
		t.Errorf("dev engine = %d %q", code, r.err)
	}
	e.version = ""
	if code := r.run("update"); code != 1 || !strings.Contains(r.err.String(), "does not say its version") {
		t.Errorf("no version = %d %q", code, r.err)
	}
	e.version = "v2.0.0"
	r.env.Executable = ""
	if code := r.run("update"); code != 1 || !strings.Contains(r.err.String(), "where this program is installed") {
		t.Errorf("no executable = %d %q", code, r.err)
	}
	// A directory this user cannot write to.
	if os.Geteuid() != 0 {
		releases(t, "v2.0.0", []byte("new"), "")
		ro := t.TempDir()
		r.env.Executable = filepath.Join(ro, "apothecary")
		_ = os.Chmod(ro, 0o500)
		t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
		if code := r.run("update"); code != 1 || !strings.Contains(r.err.String(), "install the release by hand") {
			t.Errorf("read-only = %d %q", code, r.err)
		}
	}
	_ = saveConfig(r.env, Config{URL: "ftp://x"})
	if code := r.run("update"); code != 1 {
		t.Errorf("bad saved url = %d", code)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer srv.Close()
	_ = saveConfig(r.env, Config{URL: srv.URL})
	if code := r.run("update"); code != 1 {
		t.Errorf("engine down = %d", code)
	}
}

func TestReleaseNamesAndSums(t *testing.T) {
	if assetName("windows", "arm64") != "apothecary-windows-arm64.exe" || assetName("darwin", "amd64") != "apothecary-darwin-amd64" {
		t.Error("asset names")
	}
	sums := []byte("ABC  apothecary-linux-amd64\nnot a line\ndef *apothecary-darwin-arm64\n")
	if s, ok := checksumOf(sums, "apothecary-linux-amd64"); !ok || s != "abc" {
		t.Errorf("linux = %q %v", s, ok)
	}
	if s, ok := checksumOf(sums, "apothecary-darwin-arm64"); !ok || s != "def" {
		t.Errorf("binary-mode line = %q %v", s, ok)
	}
	if _, ok := checksumOf(sums, "apothecary-windows-amd64.exe"); ok {
		t.Error("absent asset")
	}
}

// A client of another version than its engine says so, at most once a day
// asking; a build from source never does.
func TestDriftWarning(t *testing.T) {
	e := newFakeEngine(t)
	r := newRig(t)
	r.signIn(t, e)
	r.env.Version = "v1.0.0"
	e.version = "v2.0.0"
	p := &printer{out: r.out, err: r.err}
	forgetVersionCheck(r.env)
	asked := e.status
	warnDrift(t.Context(), r.env, p)
	if !strings.Contains(r.err.String(), "this client is v1.0.0 and the engine is v2.0.0: run apothecary update") || e.status != asked+1 {
		t.Fatalf("warning = %q (asked %d)", r.err, e.status-asked)
	}
	r.err.Reset()
	warnDrift(t.Context(), r.env, p)
	if e.status != asked+1 || !strings.Contains(r.err.String(), "run apothecary update") {
		t.Errorf("the second check must come from the day's answer: asked %d, %q", e.status-asked, r.err)
	}
	r.err.Reset()
	r.env.Version = "v2.0.0"
	warnDrift(t.Context(), r.env, p)
	if r.err.String() != "" {
		t.Errorf("same version = %q", r.err)
	}
	r.env.Version = "dev"
	warnDrift(t.Context(), r.env, p)
	if r.err.String() != "" {
		t.Errorf("dev = %q", r.err)
	}
	// Not signed in, or an engine that does not answer: nothing said.
	r.env.Version = "v1.0.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	srv.Close()
	_ = saveConfig(r.env, Config{URL: srv.URL})
	forgetVersionCheck(r.env)
	warnDrift(t.Context(), r.env, p)
	_ = saveConfig(r.env, Config{URL: "ftp://x"})
	warnDrift(t.Context(), r.env, p)
	_ = removeConfig(r.env)
	warnDrift(t.Context(), r.env, p)
	if r.err.String() != "" {
		t.Errorf("nothing to say = %q", r.err)
	}
	forgetVersionCheck(Env{})
}

// Windows moves the running binary aside before the new one takes its place.
func TestReplaceExecutableOnWindows(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "apothecary.exe")
	_ = os.WriteFile(exe, []byte("old"), 0o755)
	if err := replaceExecutable("windows", exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	old, _ := os.ReadFile(exe + ".old")
	if string(got) != "new" || string(old) != "old" {
		t.Errorf("exe %q, old %q", got, old)
	}
	if err := replaceExecutable("windows", filepath.Join(t.TempDir(), "absent.exe"), []byte("x")); err == nil {
		t.Error("nothing to move aside must fail")
	}
}

func TestDownloadLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "0123456789") }))
	defer srv.Close()
	if _, err := download(t.Context(), srv.URL, 5); err == nil || !strings.Contains(err.Error(), "larger than 5 bytes") {
		t.Errorf("too big = %v", err)
	}
	if b, err := download(t.Context(), srv.URL, 10); err != nil || string(b) != "0123456789" {
		t.Errorf("fits = %q %v", b, err)
	}
	if _, err := download(t.Context(), "http://[::1", 5); err == nil {
		t.Error("a bad URL")
	}
	srv.Close()
	if _, err := download(t.Context(), srv.URL, 5); err == nil {
		t.Error("nothing listening")
	}
}
