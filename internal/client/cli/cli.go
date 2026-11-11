// Package cli is the terminal client's commands: signing in, bringing a
// run's ports onto this machine, SSH into workspaces through the engine's
// HTTPS address, and keeping the client at the engine's version. Every
// command is thin: what it asks the engine goes through internal/client/api,
// and what it says goes through one printer, so a TUI can take their place
// without duplicating either.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"apothecary/internal/client/api"
)

// Env is everything a command touches outside itself, so a test can stand
// in for all of it.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// ConfigDir holds apothecary/: the sign-in (config.json) and the
	// forwards' daemon (forwards/). Home holds .ssh/config.
	ConfigDir, Home string
	Getenv          func(string) string
	// OpenBrowser opens a page in the person's browser; nil (or an error)
	// leaves them the printed address.
	OpenBrowser func(url string) error
	// SpawnDaemon starts the forwards' daemon, detached from this terminal.
	SpawnDaemon func() error
	// Hostname names this machine's sign-in; Executable is this binary's
	// path, which the ssh config runs and update replaces.
	Hostname, Executable string
	Version              string
}

// OSEnv is the real environment.
func OSEnv(version string) Env {
	cfgDir, _ := os.UserConfigDir()
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	exe, _ := os.Executable()
	env := Env{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		ConfigDir: cfgDir, Home: home, Getenv: os.Getenv,
		OpenBrowser: openBrowser, Hostname: host, Executable: exe, Version: version,
	}
	env.SpawnDaemon = func() error { return spawnDaemon(env) }
	return env
}

const usage = `apothecary — the terminal client of an Apothecary engine

usage: apothecary <command> [arguments]

  login <url>                  sign in: approve this machine in your browser
                               (or --token <personal token>, for scripts)
  logout                       sign out, revoking this machine's sign-in
  forward <run> [port[:local]]... [--same]
                               bring a run's ports to this machine, each on a
                               free local port (--same: the same port numbers);
                               with no port, every port it listens on, kept in
                               step. Runs in the background until stopped.
  forward status               what is forwarded, run by run
  forward stop <run> [port]... | --all
                               stop forwarding
  forward logs                 the background forwarder's log
  ssh-config                   route ssh <workspace>.apothecary (and VS Code
                               Remote-SSH) through the engine, once
  ssh-proxy <host>             the ssh ProxyCommand ssh-config installs
  update                       install the client release matching the engine
  version                      print the client's version

A run is named by its id or its workspace's name. Everything goes through
the engine's HTTPS address: no VPN, no other port.`

// errUsage is a command used wrong; the message says how.
type errUsage string

func (e errUsage) Error() string { return string(e) }

// Run runs one command line and returns the process's exit code.
func Run(ctx context.Context, args []string, env Env) int {
	p := &printer{out: env.Stdout, err: env.Stderr}
	if len(args) == 0 {
		p.say(usage)
		return 0
	}
	var err error
	switch args[0] {
	case "help", "-h", "--help":
		p.say(usage)
		return 0
	case "version", "--version":
		p.say("apothecary %s", env.Version)
		return 0
	case "login":
		err = cmdLogin(ctx, args[1:], env, p)
	case "logout":
		err = cmdLogout(ctx, env, p)
	case "forward":
		warnDrift(ctx, env, p)
		err = cmdForward(ctx, args[1:], env, p)
	case daemonCommand:
		err = runDaemon(ctx, env)
	case "ssh-config":
		warnDrift(ctx, env, p)
		err = cmdSSHConfig(env, p)
	case "ssh-proxy":
		err = cmdSSHProxy(ctx, args[1:], env)
	case "update":
		err = cmdUpdate(ctx, args[1:], env, p)
	default:
		err = errUsage(fmt.Sprintf("unknown command %q (apothecary help)", args[0]))
	}
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	if api.IsUnauthorized(err) {
		p.fail("%v — sign in again: apothecary login", err)
		return 1
	}
	var u errUsage
	if errors.As(err, &u) {
		p.fail("%v", err)
		return 2
	}
	p.fail("%v", err)
	return 1
}

// printer is the one place commands write to the person.
type printer struct {
	out, err io.Writer
}

func (p *printer) say(format string, a ...any) {
	fmt.Fprintf(p.out, strings.TrimSuffix(format, "\n")+"\n", a...)
}

func (p *printer) fail(format string, a ...any) {
	fmt.Fprintf(p.err, "apothecary: "+strings.TrimSuffix(format, "\n")+"\n", a...)
}

// signedIn is the client of the engine this machine signed in to.
func signedIn(env Env) (*api.Client, Config, error) {
	cfg, err := loadConfig(env)
	if err != nil {
		return nil, cfg, err
	}
	if cfg.URL == "" || cfg.Token == "" {
		return nil, cfg, errors.New("not signed in: apothecary login <url>")
	}
	c, err := api.New(cfg.URL, cfg.Token)
	return c, cfg, err
}
