package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"apothecary/internal/ports/envport"
)

// --- what asks for the daemon ------------------------------------------------

// The scan is narrow on purpose: the feature (any registry, any tag, its old
// name), or a bind of dockerd's default socket in `mounts` or `runArgs` — and
// nothing else, so the documented privileged stance stays untouched.
func TestDaemonUseInConfig(t *testing.T) {
	cases := []struct{ name, cfg, want string }{
		{"feature with tag", `{"features":{"ghcr.io/devcontainers/features/docker-outside-of-docker:1":{}}}`,
			"feature ghcr.io/devcontainers/features/docker-outside-of-docker:1"},
		{"feature elsewhere, untagged", `{"features":{"ghcr.io/someone/features/docker-outside-of-docker":{"moby":false}}}`,
			"feature ghcr.io/someone/features/docker-outside-of-docker"},
		{"legacy feature name", `{"features":{"ghcr.io/devcontainers/features/docker-from-docker:1":{}}}`,
			"feature ghcr.io/devcontainers/features/docker-from-docker:1"},
		{"mount string", `{"mounts":["source=/var/run/docker.sock,target=/var/run/docker.sock,type=bind"]}`,
			"mounts[0] binds /var/run/docker.sock"},
		{"mount src=", `{"mounts":["type=bind,src=/var/run/docker.sock,dst=/x"]}`, "mounts[0] binds /var/run/docker.sock"},
		{"mount object", `{"mounts":[{"source":"/tmp/x","target":"/x","type":"bind"},{"source":"/run/docker.sock","target":"/var/run/docker.sock","type":"bind"}]}`,
			"mounts[1] binds /run/docker.sock"},
		{"runArgs -v", `{"runArgs":["--init","-v","/var/run/docker.sock:/var/run/docker.sock"]}`, "runArgs bind /var/run/docker.sock"},
		{"runArgs --volume=", `{"runArgs":["--volume=/var/run/docker.sock:/var/run/docker.sock:ro"]}`, "runArgs bind /var/run/docker.sock"},
		{"runArgs --mount", `{"runArgs":["--mount","type=bind,source=/var/run/docker.sock,target=/x"]}`, "runArgs bind /var/run/docker.sock"},
		{"comments and trailing commas", "{\n  // the daemon\n  \"mounts\": [\"source=/var/run/docker.sock,target=/var/run/docker.sock,type=bind\",],\n}",
			"mounts[0] binds /var/run/docker.sock"},
		// Never: the existing, documented stance.
		{"docker-in-docker", `{"features":{"ghcr.io/devcontainers/features/docker-in-docker:2":{}},"privileged":true}`, ""},
		{"runArgs --privileged", `{"runArgs":["--privileged"]}`, ""},
		{"another source, the socket as target", `{"mounts":["source=${localWorkspaceFolder}/../docker.sock,target=/var/run/docker.sock,type=bind"]}`, ""},
		{"other mounts", `{"mounts":["source=cache,target=/cache,type=volume"],"runArgs":["-v","/data:/data"]}`, ""},
		{"empty", `{}`, ""},
		{"unparseable", `{"features": [`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := daemonUseInConfig([]byte(c.cfg)); got != c.want {
				t.Errorf("daemonUseInConfig = %q, want %q", got, c.want)
			}
		})
	}
}

func TestComposeBindsDaemonSocket(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"short form", "services:\n  app:\n    volumes:\n      - /var/run/docker.sock:/var/run/docker-host.sock\n", true},
		{"short form, quoted, ro", "    volumes:\n      - \"/run/docker.sock:/var/run/docker.sock:ro\"\n", true},
		{"long form", "    volumes:\n      - type: bind\n        source: /var/run/docker.sock\n        target: /var/run/docker.sock\n", true},
		{"commented out", "    volumes:\n      # - /var/run/docker.sock:/var/run/docker.sock\n      - ..:/workspaces:cached\n", false},
		{"an env var naming it", "    environment:\n      DOCKER_HOST: unix:///var/run/docker.sock\n", false},
		{"other volumes", "    volumes:\n      - ${localWorkspaceFolder}:/workspaces/app:cached\n      - pgdata:/var/lib/postgresql/data\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := composeBindsDaemonSocket([]byte(c.yaml)); got != c.want {
				t.Errorf("composeBindsDaemonSocket = %v, want %v", got, c.want)
			}
		})
	}
}

// A compose devcontainer binds the socket in its compose file, which the
// configuration names; the result says which file. Misses ask for nothing.
func TestSandboxDaemonUseFollowsComposeRefs(t *testing.T) {
	ctx := context.Background()
	d := devcontainerDef{}
	src := t.TempDir()
	if got := d.SandboxDaemonUse(ctx, osFS{}, src); got != "" {
		t.Errorf("no config asks for %q", got)
	}
	os.MkdirAll(filepath.Join(src, ".devcontainer"), 0o755)
	os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"),
		[]byte(`{"dockerComposeFile": ["../compose.yml"], "service": "app"}`), 0o644)
	os.WriteFile(filepath.Join(src, "compose.yml"),
		[]byte("services:\n  app:\n    volumes:\n      - /var/run/docker.sock:/var/run/docker-host.sock\n"), 0o644)
	if got, want := d.SandboxDaemonUse(ctx, osFS{}, src), "compose.yml binds /var/run/docker.sock"; got != want {
		t.Errorf("SandboxDaemonUse = %q, want %q", got, want)
	}
	// The json still reads (failFS spares .json); the compose file does not.
	if got := d.SandboxDaemonUse(ctx, failFS{readErr: errors.New("io")}, src); got != "" {
		t.Errorf("an unreadable compose file asks for %q", got)
	}
}

// --- the door's state ------------------------------------------------------

// auto follows the sandbox's privilege (the host's word); on/off are the
// operator's either way.
func TestDaemonDoorFollowsTheSetting(t *testing.T) {
	cases := []struct {
		name, setting string
		unpriv, open  bool
	}{
		{"auto privileged", "", false, false},
		{"auto unprivileged", "auto", true, true},
		{"on privileged", "on", false, true},
		{"on unprivileged", "on", true, true},
		{"off unprivileged", "off", true, false},
		{"off privileged", "off", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newTestAdapter(t, &scriptRunner{})
			a.cfg.WorkspaceDocker = c.setting
			hostOf(t, a).unpriv = c.unpriv
			if a.doorOpen() != c.open {
				t.Fatalf("doorOpen = %v, want %v", a.doorOpen(), c.open)
			}
			// Nothing asked: never a refusal, whatever the door.
			if err := a.checkDaemonDoor(""); err != nil {
				t.Errorf("no ask refused: %v", err)
			}
			if err := a.checkDaemonDoor("feature x"); (err == nil) != c.open {
				t.Errorf("an ask with the door open=%v: err = %v", c.open, err)
			}
		})
	}
}

// --- the refusal ------------------------------------------------------------

// doorRepo seeds a repo-authored checkout whose devcontainer uses the
// docker-outside-of-docker feature.
func doorRepo(t *testing.T, a *Adapter, name string) {
	t.Helper()
	src := seedRepoDevcontainer(t, a, name)
	cfg := `{"image": "debian:bookworm-slim", "features": {"ghcr.io/devcontainers/features/docker-outside-of-docker:1": {}}}`
	if err := os.WriteFile(filepath.Join(src, ".devcontainer", "devcontainer.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

var doorSpec = envport.EnvSpec{RunID: "run1", Project: "01HACME", RepoURL: "https://g.local/r.git"}

// A closed door (auto, privileged) refuses the repo as soon as the checkout
// is read — nothing is built, nothing is created, and the error names what
// asked and both ways out.
func TestProvisionRefusesDaemonDoorWhenClosed(t *testing.T) {
	r := &scriptRunner{}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	doorRepo(t, a, "apothecary-run1")
	_, err := a.Provision(context.Background(), doorSpec)
	if err == nil {
		t.Fatal("expected the daemon-door refusal")
	}
	for _, want := range []string{
		"feature ghcr.io/devcontainers/features/docker-outside-of-docker:1",
		"root on the host",
		hostOf(t, a).unprivilegedHint(), // how this host gets unprivileged
		"APOTHECARY_WORKSPACE_DOCKER=on",
		"Unprivileged sandboxes",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should say %q: %v", want, err)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("nothing may run before the refusal: %v", r.calls)
	}
}

// off is off, even where the sandbox is unprivileged; the message says so
// instead of sending the operator to the host's unprivileged setup.
func TestProvisionRefusesDaemonDoorWhenOff(t *testing.T) {
	r := &scriptRunner{}
	a := newTestAdapter(t, r)
	a.cfg.WorkspaceDocker = "off"
	hostOf(t, a).unpriv = true
	armSandbox(t, a, "apothecary-run1")
	doorRepo(t, a, "apothecary-run1")
	_, err := a.Provision(context.Background(), doorSpec)
	if err == nil || !strings.Contains(err.Error(), "APOTHECARY_WORKSPACE_DOCKER=off") || strings.Contains(err.Error(), hostOf(t, a).unprivilegedHint()) {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("nothing may run before the refusal: %v", r.calls)
	}
}

// An open door — auto on an unprivileged host, or the operator's "on" under a
// privileged one — provisions the same repo, verbatim: the repo declares the
// feature itself, so nothing is injected.
func TestProvisionOpensDaemonDoor(t *testing.T) {
	for _, c := range []struct {
		name, setting string
		unpriv        bool
	}{
		{"auto, unprivileged", "auto", true},
		{"on, privileged", "on", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &scriptRunner{responses: provisionOK()}
			a := newTestAdapter(t, r)
			a.cfg.WorkspaceDocker = c.setting
			hostOf(t, a).unpriv = c.unpriv
			armSandbox(t, a, "apothecary-run1")
			doorRepo(t, a, "apothecary-run1")
			if _, err := a.Provision(context.Background(), doorSpec); err != nil {
				t.Fatal(err)
			}
			up := find(t, r.calls, "devcontainer", "up")
			if strings.Contains(strings.Join(up.args, " "), "--additional-features") {
				t.Errorf("the repo declares the feature; nothing to inject: %v", up.args)
			}
		})
	}
}

// --- the project's Docker setting -------------------------------------------

// A project that turns Docker on gets the standard feature injected into a
// repo that does not declare it (the CLI's --additional-features: the repo's
// files stay untouched), through the same door.
func TestProvisionInjectsDockerForTheProject(t *testing.T) {
	r := &scriptRunner{responses: provisionOK()}
	a := newTestAdapter(t, r)
	a.cfg.WorkspaceDocker = "on"
	armSandbox(t, a, "apothecary-run1")
	seedRepoDevcontainer(t, a, "apothecary-run1") // {} — declares nothing
	spec := doorSpec
	spec.Docker = true
	if _, err := a.Provision(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	joined := strings.Join(up.args, " ")
	want := `--additional-features {"` + dockerOutsideOfDockerFeature + `":{}}`
	if !strings.Contains(joined, want) {
		t.Errorf("up must inject the feature for the project: %v", up.args)
	}
	if strings.Contains(joined, "--override-config") {
		t.Errorf("the repo's config must stay untouched: %v", up.args)
	}
}

// A repo that already declares the feature is left alone even with the
// project's setting on: injecting it twice would install it twice.
func TestProjectDockerDefersToTheRepo(t *testing.T) {
	r := &scriptRunner{responses: provisionOK()}
	a := newTestAdapter(t, r)
	a.cfg.WorkspaceDocker = "on"
	armSandbox(t, a, "apothecary-run1")
	doorRepo(t, a, "apothecary-run1")
	spec := doorSpec
	spec.Docker = true
	if _, err := a.Provision(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	up := find(t, r.calls, "devcontainer", "up")
	if strings.Contains(strings.Join(up.args, " "), "--additional-features") {
		t.Errorf("the repo declares the feature; nothing to inject: %v", up.args)
	}
}

// The project's setting is an ask like the repo's: a closed door refuses it,
// naming the setting.
func TestProjectDockerRefusedWhenDoorClosed(t *testing.T) {
	r := &scriptRunner{}
	a := newTestAdapter(t, r)
	armSandbox(t, a, "apothecary-run1")
	seedRepoDevcontainer(t, a, "apothecary-run1")
	spec := doorSpec
	spec.Docker = true
	_, err := a.Provision(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "the project's Docker setting") || !strings.Contains(err.Error(), "APOTHECARY_WORKSPACE_DOCKER=on") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("nothing may run before the refusal: %v", r.calls)
	}
	// Off: no injection and no refusal — the repo asked for nothing itself.
	spec.Docker = false
	r2 := &scriptRunner{responses: provisionOK()}
	a2 := newTestAdapter(t, r2)
	armSandbox(t, a2, "apothecary-run1")
	seedRepoDevcontainer(t, a2, "apothecary-run1")
	if _, err := a2.Provision(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(find(t, r2.calls, "devcontainer", "up").args, " "), "--additional-features") {
		t.Error("nothing asked for docker; nothing to inject")
	}
}

// --- hooks after ownership --------------------------------------------------

// `up` skips the repo's lifecycle hooks; root-init hands the checkout to the
// remote user; then the hooks run — in that order, against the same
// container, so a non-root user's postCreateCommand meets a tree it owns.
func TestProvisionRunsHooksAfterRootInit(t *testing.T) {
	a, r := provisionRepo(t, provisionOK())
	up := find(t, r.calls, "devcontainer", "up")
	for _, want := range []string{"--skip-post-create", "--skip-post-attach"} {
		if !strings.Contains(strings.Join(up.args, " "), want) {
			t.Errorf("up must leave the hooks to run-user-commands (%q): %v", want, up.args)
		}
	}
	initAt, hooksAt := -1, -1
	for i, c := range r.calls {
		switch {
		case c.bin == "docker" && len(c.args) > 2 && c.args[0] == "exec" && c.args[1] == "-u":
			initAt = i
			script := c.args[len(c.args)-1]
			// upOK's remote user and workspace folder.
			if !strings.Contains(script, "chown -R 'vscode': '/workspaces/src'") {
				t.Errorf("root-init must give the remote user the checkout: %s", script)
			}
			if strings.Index(script, "chown -R") > strings.Index(script, `"$H/repo"`) {
				t.Errorf("ownership must come before the repo link: %s", script)
			}
		case c.bin == "devcontainer" && c.args[0] == "run-user-commands":
			hooksAt = i
			joined := strings.Join(c.args, " ")
			for _, want := range []string{"--workspace-folder " + a.srcDir("apothecary-run1"), "--id-label apothecary.env=apothecary-run1", "--log-format json"} {
				if !strings.Contains(joined, want) {
					t.Errorf("run-user-commands missing %q: %v", want, c.args)
				}
			}
			if !envHas(c, "DOCKER_HOST=unix://"+hostOf(t, a).innerSock("apothecary-run1")) {
				t.Errorf("hooks must run against the inner daemon: %v", c.env)
			}
		}
	}
	if initAt < 0 || hooksAt < 0 || initAt > hooksAt {
		t.Errorf("expected root-init then run-user-commands, got init@%d hooks@%d: %v", initAt, hooksAt, r.calls)
	}
}

// A failing hook fails the provision, as it did when `up` ran it, with the
// CLI's message; a CLI that prints no result line is fine when it exits 0.
func TestRunUserCommandsFailureModes(t *testing.T) {
	d := devcontainerDef{bin: "devcontainer"}
	ctx := context.Background()
	cases := []struct {
		name string
		resp response
		want string
	}{
		{"spawn", response{err: errors.New("no cli")}, "no cli"},
		{"hook failed", response{code: 1, stdout: `{"outcome":"error","message":"postCreateCommand failed","description":"exit 2"}` + "\n", stderr: "npm ERR!"}, "postCreateCommand failed exit 2"},
		{"outcome error, exit 0", response{stdout: `{"outcome":"error","message":"no container"}` + "\n"}, "no container"},
		{"exit 1, no result line", response{code: 1, stderr: "boom"}, "exit 1"},
		{"ok, quiet", response{}, ""},
		{"ok, result line", response{stdout: `{"outcome":"success"}` + "\n"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &ruleRunner{rules: []rule{{match: "run-user-commands", resp: c.resp}}}
			err := d.RunUserCommands(ctx, devEnv(t, "/src", r), upOptions{})
			switch {
			case c.want == "" && err != nil:
				t.Errorf("err = %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}
