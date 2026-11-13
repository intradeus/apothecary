// Package devcontainer holds the repo's own devcontainer (.devcontainer/) to
// its sources of truth: the Go version to go.mod, Node to .nvmrc,
// helm to CI, Postgres to pgtest, and the shape apothecary itself needs when
// it runs this repo (README, "Developing Apothecary with Apothecary").
package devcontainer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../../.."

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// argDefault is the default of `ARG NAME=value` in a Dockerfile.
func argDefault(t *testing.T, dockerfile, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)`).FindStringSubmatch(dockerfile)
	if m == nil {
		t.Fatalf("no ARG %s in .devcontainer/Dockerfile", name)
	}
	return m[1]
}

func TestGoImageMatchesGoMod(t *testing.T) {
	df := read(t, ".devcontainer/Dockerfile")
	gomod := regexp.MustCompile(`(?m)^go (\S+)$`).FindStringSubmatch(read(t, "go.mod"))
	if gomod == nil {
		t.Fatal("no go line in go.mod")
	}
	if got := argDefault(t, df, "GO_VERSION"); got != gomod[1] {
		t.Errorf("GO_VERSION = %s, go.mod says %s", got, gomod[1])
	}
	if !strings.Contains(df, "FROM golang:${GO_VERSION}-bookworm") {
		t.Error("the dev image must build FROM golang:${GO_VERSION}-bookworm")
	}
}

// One Node, everywhere: .nvmrc names the exact version, npm is the one that
// Node ships, and the engine image, the devcontainer, cpui's engines (which
// npm enforces: .npmrc engine-strict) and every CI setup-node take them from
// there. A different Node on one side is how a check passes on a laptop and
// fails in CI.
func TestOneNodeEverywhere(t *testing.T) {
	node := strings.TrimSpace(read(t, ".nvmrc"))
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(node) {
		t.Fatalf(".nvmrc = %q, want an exact version (e.g. 26.13.1)", node)
	}
	if deploy := regexp.MustCompile(`(?m)^FROM node:(\S+)-bookworm-slim`).FindStringSubmatch(read(t, "deploy/Dockerfile")); deploy == nil || deploy[1] != node {
		t.Errorf("deploy/Dockerfile builds the CPUI on node %v, .nvmrc says %s", deploy, node)
	}
	df := read(t, ".devcontainer/Dockerfile")
	if got := argDefault(t, df, "NODE_VERSION"); got != node {
		t.Errorf("devcontainer NODE_VERSION = %s, .nvmrc says %s", got, node)
	}
	if !strings.Contains(df, "FROM node:${NODE_VERSION}-bookworm-slim") {
		t.Error("the devcontainer must take node from node:${NODE_VERSION}-bookworm-slim")
	}
	pkg := read(t, "cpui/package.json")
	if eng := regexp.MustCompile(`"node":\s*"([^"]+)"`).FindStringSubmatch(pkg); eng == nil || eng[1] != node {
		t.Errorf("cpui/package.json engines.node = %v, want exactly %s", eng, node)
	}
	if npm := regexp.MustCompile(`"npm":\s*"(\d+\.\d+\.\d+)"`).FindStringSubmatch(pkg); npm == nil {
		t.Error("cpui/package.json engines.npm must name the exact npm that .nvmrc's Node ships")
	}
	if !strings.Contains(read(t, "cpui/.npmrc"), "engine-strict=true") {
		t.Error("cpui/.npmrc must set engine-strict=true, so another Node or npm refuses to install")
	}
	for _, wf := range []string{"ci.yml", "manual-release.yml", "pr-title.yml"} {
		y := read(t, ".github/workflows/"+wf)
		if regexp.MustCompile(`(?m)^\s+node-version:`).MatchString(y) {
			t.Errorf("%s names a Node version: use node-version-file: .nvmrc", wf)
		}
		uses := len(regexp.MustCompile(`uses: actions/setup-node@`).FindAllString(y, -1))
		if files := strings.Count(y, "node-version-file: .nvmrc"); files != uses {
			t.Errorf("%s: %d setup-node steps, %d read .nvmrc", wf, uses, files)
		}
	}
}

// helm, task, shellcheck, k3d and kubectl: the devcontainer's ARGs are the
// versions, and every workflow installs exactly those (a "3.x" would drift).
func TestToolPinsMatchCI(t *testing.T) {
	df := read(t, ".devcontainer/Dockerfile")
	helm, task, sc := argDefault(t, df, "HELM_VERSION"), strings.TrimPrefix(argDefault(t, df, "TASK_VERSION"), "v"), argDefault(t, df, "SHELLCHECK_VERSION")
	for _, wf := range []string{"ci.yml", "manual-release.yml"} {
		y := read(t, ".github/workflows/"+wf)
		// The actions are pinned to a commit, their release in a trailing comment.
		for _, m := range regexp.MustCompile(`azure/setup-helm@\S+(?:[ \t]+#[^\n]*)?\s+with:\s+version: v(\S+)`).FindAllStringSubmatch(y, -1) {
			if m[1] != helm {
				t.Errorf("%s installs helm %s, the devcontainer %s", wf, m[1], helm)
			}
		}
		for _, m := range regexp.MustCompile(`arduino/setup-task@\S+(?:[ \t]+#[^\n]*)?\s+with:\s+version: (\S+)`).FindAllStringSubmatch(y, -1) {
			if m[1] != task {
				t.Errorf("%s installs task %s, the devcontainer %s", wf, m[1], task)
			}
		}
	}
	ci := read(t, ".github/workflows/ci.yml")
	if !strings.Contains(ci, "azure/setup-helm@") || !strings.Contains(ci, "arduino/setup-task@") {
		t.Fatal("ci.yml installs no helm or no task")
	}
	if m := regexp.MustCompile(`SHELLCHECK_VERSION: (\S+)`).FindStringSubmatch(ci); m == nil || m[1] != sc {
		t.Errorf("ci.yml installs shellcheck %v, the devcontainer %s", m, sc)
	}
	sum := regexp.MustCompile(`amd64\) a=x86_64; sum=([0-9a-f]{64})`).FindStringSubmatch(df)
	if m := regexp.MustCompile(`SHELLCHECK_SHA256: ([0-9a-f]{64})`).FindStringSubmatch(ci); sum == nil || m == nil || m[1] != sum[1] {
		t.Error("ci.yml's shellcheck checksum is not the devcontainer's amd64 one")
	}
	// k3d and kubectl: the k8s sandbox suite's, the same release and the
	// same amd64 binary in CI as in the devcontainer.
	for _, tool := range []struct{ arg, sumVar, caseLabel string }{
		{"K3D_VERSION", "K3D_SHA256", "k3d"},
		{"KUBECTL_VERSION", "KUBECTL_SHA256", "kc"},
	} {
		want := argDefault(t, df, tool.arg)
		if m := regexp.MustCompile(tool.arg + `: (\S+)`).FindStringSubmatch(ci); m == nil || m[1] != want {
			t.Errorf("ci.yml installs %s %v, the devcontainer %s", tool.arg, m, want)
		}
		sum := regexp.MustCompile(`amd64\) a=amd64;.*\b` + tool.caseLabel + `=([0-9a-f]{64})`).FindStringSubmatch(df)
		if m := regexp.MustCompile(tool.sumVar + `: ([0-9a-f]{64})`).FindStringSubmatch(ci); sum == nil || m == nil || m[1] != sum[1] {
			t.Errorf("ci.yml's %s is not the devcontainer's amd64 checksum", tool.sumVar)
		}
	}
	// The Taskfile's guards read the same ARGs.
	tf := read(t, "Taskfile.yml")
	for _, arg := range []string{"HELM_VERSION", "TASK_VERSION", "SHELLCHECK_VERSION", "K3D_VERSION"} {
		if !strings.Contains(tf, "ARG "+arg+"=") {
			t.Errorf("Taskfile.yml does not read %s from .devcontainer/Dockerfile", arg)
		}
	}
}

// Compose paths resolve against .devcontainer/, so `..` is the checkout;
// under apothecary the folder above it is the run's own (sockets, shim) and
// must never be mounted. (The CLI does not expand ${localWorkspaceFolder} in
// compose files — `..` is the one spelling that works everywhere.)
func TestComposeMountsOnlyTheCheckout(t *testing.T) {
	compose := read(t, ".devcontainer/docker-compose.yml")
	if !strings.Contains(compose, "- ..:/workspaces/apothecary") {
		t.Error("the compose app service must mount .. (the checkout) at /workspaces/apothecary")
	}
	for _, line := range strings.Split(compose, "\n") {
		l := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if strings.HasPrefix(l, "../") || strings.HasPrefix(l, "${localWorkspaceFolder}") {
			t.Errorf("a volume reaches above the checkout, or uses a variable compose will not see: %s", line)
		}
	}
	cfg := stripJSONC(read(t, ".devcontainer/devcontainer.json"))
	if !regexp.MustCompile(`"workspaceFolder"\s*:\s*"/workspaces/apothecary"`).MatchString(cfg) {
		t.Error("devcontainer.json's workspaceFolder must be the compose mount target /workspaces/apothecary")
	}
}

// Docker in a run comes from the project's "Docker in workspaces" setting
// (the engine injects the feature), not from the repo's own config — so the
// devcontainer declares no docker feature, and a run never installs it twice.
func TestDevcontainerLeavesDockerToTheProject(t *testing.T) {
	cfg := stripJSONC(read(t, ".devcontainer/devcontainer.json"))
	if strings.Contains(cfg, "docker-outside-of-docker") || strings.Contains(cfg, "docker-in-docker") {
		t.Error("devcontainer.json declares a docker feature: the project's Docker setting provides it in a run")
	}
}

// Preflight needs AGENTS.md at the repo root.
func TestAgentsMDExists(t *testing.T) {
	if len(strings.TrimSpace(read(t, "AGENTS.md"))) == 0 {
		t.Error("AGENTS.md is empty")
	}
}

// stripJSONC drops // comments — enough for the repo's own devcontainer.json.
func stripJSONC(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "//"); i >= 0 && !strings.Contains(line[:i], `"`) {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
