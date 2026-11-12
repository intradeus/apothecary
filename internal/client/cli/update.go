package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"apothecary/internal/client/api"
)

// releaseBase is where the releases' files are downloaded from: GitHub
// Releases, <base>/<version>/<asset>. A var for tests.
var releaseBase = "https://github.com/intradeus/apothecary/releases/download"

// driftEvery is how often a command asks the engine its version.
const driftEvery = 24 * time.Hour

// cmdUpdate installs the client release whose version is the engine's: the
// client talks the API of the engine it is signed in to, so it follows that
// engine, not the newest release. The download is checked against the
// release's SHA256SUMS before it replaces this binary.
func cmdUpdate(ctx context.Context, args []string, env Env, p *printer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	check := fs.Bool("check", false, "only say whether an update is needed")
	if err := fs.Parse(args); err != nil {
		return errUsage("usage: apothecary update [--check]")
	}
	want, err := engineVersion(ctx, env)
	if err != nil {
		return err
	}
	if want == env.Version {
		p.say("apothecary %s matches the engine.", env.Version)
		return nil
	}
	if *check {
		p.say("This client is %s and the engine is %s: run apothecary update.", env.Version, want)
		return nil
	}
	if env.Executable == "" {
		return errors.New("cannot tell where this program is installed")
	}
	asset := assetName(runtime.GOOS, runtime.GOARCH)
	sums, err := download(ctx, releaseBase+"/"+want+"/SHA256SUMS", 1<<20)
	if err != nil {
		return fmt.Errorf("release %s: %w", want, err)
	}
	sum, ok := checksumOf(sums, asset)
	if !ok {
		return fmt.Errorf("release %s has no %s for this machine", want, asset)
	}
	bin, err := download(ctx, releaseBase+"/"+want+"/"+asset, 256<<20)
	if err != nil {
		return fmt.Errorf("release %s: %w", want, err)
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != sum {
		return fmt.Errorf("release %s: %s does not match its SHA256SUMS; nothing was installed", want, asset)
	}
	if err := replaceExecutable(runtime.GOOS, env.Executable, bin); err != nil {
		return err
	}
	forgetVersionCheck(env)
	p.say("Updated apothecary %s → %s.", env.Version, want)
	return nil
}

// engineVersion is the version of the engine this machine signed in to.
func engineVersion(ctx context.Context, env Env) (string, error) {
	cfg, err := loadConfig(env)
	if err != nil {
		return "", err
	}
	if cfg.URL == "" {
		return "", errors.New("not signed in: apothecary login <url>")
	}
	c, err := api.New(cfg.URL, "")
	if err != nil {
		return "", err
	}
	st, err := c.Status(ctx)
	if err != nil {
		return "", err
	}
	switch st.Version {
	case "":
		return "", errors.New("the engine does not say its version")
	case "dev":
		return "", errors.New("the engine is a development build: there is no release to match (build the client from the same tree)")
	}
	return st.Version, nil
}

// assetName is the release file of the client for an OS and architecture.
func assetName(goos, goarch string) string {
	name := "apothecary-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// checksumOf finds an asset's hex sha256 in a SHA256SUMS file.
func checksumOf(sums []byte, asset string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

func download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, limit)
	}
	return data, nil
}

// replaceExecutable puts bin where exe is, in one rename: a reader never
// sees half a binary. Windows cannot replace a running .exe, but can rename
// it: the old one steps aside first, and is removed by the next update.
func replaceExecutable(goos, exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".apothecary-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s (%w): run the update with the rights to replace it, or install the release by hand", exe, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if goos == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(tmp.Name(), exe)
}

// versionCheck is the last answer to "which version is the engine", kept so
// a command asks at most once a day.
type versionCheck struct {
	At     time.Time `json:"at"`
	URL    string    `json:"url"`
	Engine string    `json:"engine"`
}

func versionCheckPath(env Env) string {
	if env.ConfigDir == "" {
		return ""
	}
	return filepath.Join(env.ConfigDir, "apothecary", "version-check.json")
}

func forgetVersionCheck(env Env) {
	if path := versionCheckPath(env); path != "" {
		_ = os.Remove(path)
	}
}

// warnDrift says, at most once a day, when this client's version is not the
// engine's. Best-effort and quick: a slow or absent engine says nothing here.
func warnDrift(ctx context.Context, env Env, p *printer) {
	if env.Version == "" || env.Version == "dev" {
		return
	}
	cfg, err := loadConfig(env)
	if err != nil || cfg.URL == "" {
		return
	}
	path := versionCheckPath(env)
	var vc versionCheck
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &vc)
	}
	if vc.URL != cfg.URL || time.Since(vc.At) > driftEvery {
		c, err := api.New(cfg.URL, "")
		if err != nil {
			return
		}
		qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		st, err := c.Status(qctx)
		cancel()
		if err != nil {
			return
		}
		vc = versionCheck{At: time.Now(), URL: cfg.URL, Engine: st.Version}
		if data, err := json.Marshal(vc); err == nil && path != "" {
			_ = os.WriteFile(path, data, 0o600)
		}
	}
	if vc.Engine != "" && vc.Engine != "dev" && vc.Engine != env.Version {
		p.fail("this client is %s and the engine is %s: run apothecary update", env.Version, vc.Engine)
	}
}
