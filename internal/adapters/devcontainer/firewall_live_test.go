//go:build live

package devcontainer

// Live firewall verification against the real toolchain:
//
//	go test -tags live -run TestLiveFirewall -v ./internal/adapters/devcontainer/
//
// Requires docker + @devcontainers/cli + git. Builds the firewall feature
// (apt install iptables) into a repo-authored debian image and proves
// egress is actually dropped.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"apothecary/internal/ports"
	"apothecary/internal/wsimage"
)

func TestLiveFirewall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// A repo-authored image devcontainer (root user so the entrypoint's
	// iptables works); no compose, so the firewall feature applies.
	repo := t.TempDir()
	must(os.MkdirAll(filepath.Join(repo, ".devcontainer"), 0o755))
	must(os.WriteFile(filepath.Join(repo, ".devcontainer", "devcontainer.json"),
		[]byte(`{"image":"debian:bookworm-slim"}`), 0o644))
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x"}} {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	runsDir := t.TempDir()
	a := New(Config{
		RunsDir: runsDir, ProvisionTimeout: 8 * time.Minute, EnginePort: 8080,
		ShimBin: wsimage.New(runsDir, "").EnsureShim,
		// Firewall ON (default), no egress allowlist → default-deny.
	})
	defer a.Close()

	id, err := a.Provision(ctx, ports.EnvSpec{
		RunID:   "FWLIVE1",
		RepoURL: "file://" + repo,
		// A tiny allowlist so the positive path is covered too.
		EgressDomains: []string{"example.org"},
	})
	must(err)
	defer func() { _ = a.Destroy(context.WithoutCancel(ctx), id) }()
	t.Logf("provisioned firewalled %s", id)

	// The env reports itself firewalled (not open).
	open, err := a.EgressOpen(ctx, id)
	must(err)
	if open {
		t.Error("firewalled env must report EgressOpen=false")
	}

	// The firewall lock exists (waitReady already gated on it, but assert).
	if res, err := a.Exec(ctx, id, ports.Command{Script: "test -e /run/apothecary-firewall.done && echo LOCKED"}); err != nil || !strings.Contains(res.Output, "LOCKED") {
		t.Fatalf("firewall not initialized: %q %v", res.Output, err)
	}

	// NEGATIVE: a non-allowlisted host is dropped (curl absent in slim →
	// /dev/tcp with a short timeout; a DROP makes connect hang → timeout
	// non-zero exit).
	blocked, err := a.Exec(ctx, id, ports.Command{
		Script: `timeout 5 bash -c 'exec 3<>/dev/tcp/1.1.1.1/443' && echo REACHED || echo BLOCKED`})
	must(err)
	if !strings.Contains(blocked.Output, "BLOCKED") {
		t.Errorf("egress to 1.1.1.1:443 was NOT blocked: %q", blocked.Output)
	}

	// Foreign DNS is dead too.
	dns, err := a.Exec(ctx, id, ports.Command{Script: `getent hosts example.com && echo RESOLVED || echo NODNS`})
	must(err)
	if !strings.Contains(dns.Output, "NODNS") {
		t.Errorf("foreign DNS still works under the firewall: %q", dns.Output)
	}

	// LOOPBACK still works under DROP — the engine relay probe already
	// passed in waitReady, but assert the shim path explicitly.
	lo, err := a.Exec(ctx, id, ports.Command{
		Script: `timeout 3 bash -c 'exec 3<>/dev/tcp/127.0.0.1/8080' && echo LOOPBACK-OK || echo LOOPBACK-DEAD`})
	must(err)
	if !strings.Contains(lo.Output, "LOOPBACK-OK") {
		t.Errorf("loopback/relay broke under the firewall: %q", lo.Output)
	}
	t.Log("firewall verified: foreign egress + DNS dropped, loopback alive")
}
