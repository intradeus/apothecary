// Package devcontainer holds the repo's own devcontainer (.devcontainer/) to
// its sources of truth: the Go version to go.mod, Node to the deploy image,
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

func TestNodeMajorMatchesDeployImage(t *testing.T) {
	df := read(t, ".devcontainer/Dockerfile")
	deploy := regexp.MustCompile(`(?m)^FROM node:(\d+)-bookworm-slim`).FindStringSubmatch(read(t, "deploy/Dockerfile"))
	if deploy == nil {
		t.Fatal("deploy/Dockerfile does not build the CPUI on node:<major>-bookworm-slim")
	}
	if got := argDefault(t, df, "NODE_MAJOR"); got != deploy[1] {
		t.Errorf("NODE_MAJOR = %s, deploy/Dockerfile uses node %s", got, deploy[1])
	}
}

func TestHelmPinMatchesCI(t *testing.T) {
	df := read(t, ".devcontainer/Dockerfile")
	ci := regexp.MustCompile(`azure/setup-helm@\S+\s+with:\s+version: v(\S+)`).FindStringSubmatch(read(t, ".github/workflows/ci.yml"))
	if ci == nil {
		t.Fatal("ci.yml has no azure/setup-helm version")
	}
	if got := argDefault(t, df, "HELM_VERSION"); got != ci[1] {
		t.Errorf("HELM_VERSION = %s, CI lints with helm %s", got, ci[1])
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
