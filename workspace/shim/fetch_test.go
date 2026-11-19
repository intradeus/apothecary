package main

// fetch: a missing repository is cloned with its history, a present one
// brought to the remote's head, all at once; the credential reaches the
// repository host as a header and nothing on disk ever holds it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// remoteRepo is a repository to clone from, by file:// URL.
func remoteRepo(t *testing.T) (string, func(msg string)) {
	t.Helper()
	dir := gitRepo(t)
	gitPath, _ := exec.LookPath("git")
	commit := func(msg string) {
		os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(msg+"\n"), 0o644)
		cmd := exec.Command(gitPath, "-C", dir, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qam", msg)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v %s", err, out)
		}
	}
	return "file://" + dir, commit
}

func TestFetchClonesThenFollows(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	urlA, commitA := remoteRepo(t)
	urlB, _ := remoteRepo(t)
	root := t.TempDir()
	a, b := filepath.Join(root, "acme-0abc", "api"), filepath.Join(root, "acme-0abc", "web")
	req := fetchRequest{Repos: []fetchRepo{{Dir: a, URL: urlA}, {Dir: b, URL: urlB}}}
	res, err := fetchAll(context.Background(), gitPath, []string{"file"}, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.OK || !r.Cloned || r.Head == "" {
			t.Errorf("first fetch = %+v", r)
		}
	}
	if res[0].Dir != a || res[1].Dir != b {
		t.Errorf("answers out of order: %+v", res)
	}
	// The whole history came.
	out, _ := exec.Command(gitPath, "-C", a, "log", "--format=%s").Output()
	if string(out) != "run task check\nadd the test job\n" {
		t.Errorf("history = %q", out)
	}
	// The remote moves on; the next fetch follows it, hard.
	commitA("a third commit")
	os.WriteFile(filepath.Join(a, "stray.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(a, "ci.yml"), []byte("edited locally\n"), 0o644)
	res, err = fetchAll(context.Background(), gitPath, []string{"file"}, req)
	if err != nil || !res[0].OK || res[0].Cloned {
		t.Fatalf("second fetch = %+v, %v", res, err)
	}
	if data, _ := os.ReadFile(filepath.Join(a, "ci.yml")); string(data) != "a third commit\n" {
		t.Errorf("ci.yml after the fetch = %q", data)
	}
	// A half-made clone is started over.
	os.RemoveAll(filepath.Join(b, ".git"))
	if res, _ := fetchAll(context.Background(), gitPath, []string{"file"}, req); !res[1].OK || !res[1].Cloned {
		t.Errorf("refetch of a broken folder = %+v", res[1])
	}
}

func TestFetchRefusesWhatIsNotAllowed(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	url, _ := remoteRepo(t)
	dir := filepath.Join(t.TempDir(), "w", "r")
	// file:// is not a transport unless it is listed.
	res, err := fetchAll(context.Background(), gitPath, []string{"https"}, fetchRequest{Repos: []fetchRepo{{Dir: dir, URL: url}}})
	if err != nil || res[0].OK {
		t.Errorf("a file:// fetch with only https allowed = %+v, %v", res, err)
	}
	for name, r := range map[string]fetchRequest{
		"relative folder": {Repos: []fetchRepo{{Dir: "w/r", URL: url}}},
		"unclean folder":  {Repos: []fetchRepo{{Dir: "/w/../r", URL: url}}},
		"named twice":     {Repos: []fetchRepo{{Dir: "/w/r", URL: url}, {Dir: "/w/r", URL: url}}},
		"no url":          {Repos: []fetchRepo{{Dir: "/w/r"}}},
		"an option":       {Repos: []fetchRepo{{Dir: "/w/r", URL: "--upload-pack=touch /tmp/x"}}},
	} {
		if _, err := fetchAll(context.Background(), gitPath, []string{"https"}, r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The credential rides a header to the repository host and nowhere else:
// not the remote URL, not the clone's config, not an error.
func TestFetchCredentialIsAHeaderOnly(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	const pass = "s3cret-token-value"
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, "no repository here, but "+pass+" was sent", http.StatusNotFound)
	}))
	defer srv.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "w", "r")
	res, err := fetchAll(context.Background(), gitPath, []string{"http"},
		fetchRequest{Repos: []fetchRepo{{Dir: dir, URL: srv.URL + "/o/r.git", User: "bot", Pass: pass}}})
	if err != nil || res[0].OK {
		t.Fatalf("fetch from a host with no repository = %+v, %v", res, err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("bot:"+pass))
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[0] != want {
		t.Errorf("the host saw %q, want the credential as a header", seen)
	}
	if strings.Contains(res[0].Error, pass) {
		t.Errorf("the error names the credential: %s", res[0].Error)
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), pass) || strings.Contains(string(data), want) {
				t.Errorf("%s holds the credential", p)
			}
		}
		return nil
	})
}

func TestFetchCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	url, _ := remoteRepo(t)
	dir := filepath.Join(t.TempDir(), "w", "r")
	gitPath, _ := exec.LookPath("git")
	body, _ := json.Marshal(fetchRequest{Repos: []fetchRepo{{Dir: dir, URL: url}}})
	r := shim(t, string(body), "fetch", "--protocols", "file", "--git", gitPath)
	var got fetchResult
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &got) != nil || !got.OK {
		t.Errorf("fetch = %+v", r)
	}
	if r := shim(t, "{nope", "fetch"); r.code != 2 {
		t.Errorf("a bad request = %+v", r)
	}
	if r := shim(t, `{"repos":[{"dir":"`+dir+`","url":"file:///nowhere"}]}`, "fetch", "--protocols", "file", "--git", gitPath); r.code != 1 || !strings.Contains(r.stdout, `"ok":false`) {
		t.Errorf("a failed fetch = %+v", r)
	}
}

func TestIdleWaitsForItsStop(t *testing.T) {
	cmd := shimCmd(t, "idle")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("idle returned on its own: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("idle on TERM = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle outlived its TERM")
	}
}

// Each repository's credential goes to its own host only: a fetch mixing
// hosts never hands one host's token to another.
func TestFetchCredentialStaysWithItsHost(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	var mu sync.Mutex
	seen := map[string][]string{}
	host := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[name] = append(seen[name], r.Header.Get("Authorization"))
			mu.Unlock()
			http.NotFound(w, r)
		}))
	}
	a, b := host("a"), host("b")
	defer a.Close()
	defer b.Close()
	root := t.TempDir()
	_, err = fetchAll(context.Background(), gitPath, []string{"http"}, fetchRequest{Repos: []fetchRepo{
		{Dir: filepath.Join(root, "w", "a"), URL: a.URL + "/a.git", User: "x", Pass: "token-of-a"},
		{Dir: filepath.Join(root, "w", "b"), URL: b.URL + "/b.git"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range seen["b"] {
		if h != "" {
			t.Errorf("host b was sent %q", h)
		}
	}
	if len(seen["a"]) == 0 || seen["a"][0] == "" {
		t.Errorf("host a got no credential: %v", seen["a"])
	}
	if _, err := fetchEnv(fetchRepo{Dir: "/w/r", URL: "not a url", Pass: "p"}); err == nil {
		t.Error("a credential for a URL with no host was taken")
	}
}
