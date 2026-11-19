package main

// confine: launch a program under a Landlock rule set (docs/assistant-plan.md,
// "A session"). The assistant pod runs every session's agent process this
// way: it may read its own repositories, write its own state folder and
// execute a short list of programs — its harness, git, ripgrep, the shim —
// and nothing else. No shell is on the list, so the agent has none.
//
//	shim confine --spec '<json>' -- <argv>...
//	shim confine --check
//
// The rules apply to this process and everything it starts, and only
// tighten: a child can never widen them. A kernel without Landlock, or one
// that cannot enforce the whole spec, refuses the start (exit 3, with a
// message on stderr): an unconfined session is never the fallback.
//
// The spec names paths, which must exist; a path the kernel cannot open is
// an error, not a rule quietly dropped.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// confineSpec is what a confined process may touch. Every list holds
// absolute paths; a directory's rule covers everything beneath it.
type confineSpec struct {
	// Read: read files and list directories.
	Read []string `json:"read,omitempty"`
	// Write: read, create, change and remove files and directories.
	Write []string `json:"write,omitempty"`
	// Exec: run these files (and read them). A directory here lets every
	// file beneath it run, so the list names files.
	Exec []string `json:"exec,omitempty"`
	// Devices: read and write device files beneath these (/dev/null,
	// /dev/urandom, a tty), never create them.
	Devices []string `json:"devices,omitempty"`
}

// exitNoConfine is confine's exit code when the rules cannot be enforced:
// the caller tells it from the program's own failures.
const exitNoConfine = 3

// errNoLandlock: this kernel cannot confine a process.
var errNoLandlock = errors.New("this kernel does not enforce Landlock as sessions need it (ABI 6: Linux 6.12 or later, with landlock in its LSM list)")

// validate checks the spec's shape: absolute, clean paths.
func (s confineSpec) validate() error {
	for _, list := range [][]string{s.Read, s.Write, s.Exec, s.Devices} {
		for _, p := range list {
			if !filepath.IsAbs(p) || filepath.Clean(p) != p {
				return fmt.Errorf("confine: %q is not a clean absolute path", p)
			}
		}
	}
	return nil
}

func parseConfineArgs(args []string) (confineSpec, []string, error) {
	var spec confineSpec
	var raw string
	for len(args) > 0 {
		switch a := args[0]; {
		case a == "--":
			args = args[1:]
			if len(args) == 0 {
				return spec, nil, errors.New("confine: no program after --")
			}
			if raw == "" {
				return spec, nil, errors.New("confine: --spec is required")
			}
			dec := json.NewDecoder(strings.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&spec); err != nil {
				return spec, nil, fmt.Errorf("confine: spec: %w", err)
			}
			return spec, args, spec.validate()
		case a == "--spec" && len(args) > 1:
			raw, args = args[1], args[2:]
		case strings.HasPrefix(a, "--spec="):
			raw, args = strings.TrimPrefix(a, "--spec="), args[1:]
		default:
			return spec, nil, fmt.Errorf("confine: unexpected argument %q", a)
		}
	}
	return spec, nil, errors.New("confine: usage: shim confine --spec <json> -- <argv>...")
}

func runConfine(args []string) {
	// --check: whether this kernel confines at all (exit 0) or not (3),
	// for the pod to say so before any session is asked for.
	if len(args) == 1 && args[0] == "--check" {
		fmt.Println(landlockReport())
		if landlockABI() < minLandlockABI {
			os.Exit(exitNoConfine)
		}
		return
	}
	spec, argv, err := parseConfineArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	prog := argv[0]
	if !filepath.IsAbs(prog) {
		fmt.Fprintf(os.Stderr, "confine: %q: the program is named by its absolute path\n", prog)
		os.Exit(2)
	}
	if !slices.Contains(spec.Exec, prog) {
		fmt.Fprintf(os.Stderr, "confine: %s is not on the spec's exec list\n", prog)
		os.Exit(2)
	}
	// restrictAndExec only returns on failure.
	err = restrictAndExec(spec, argv, os.Environ())
	fmt.Fprintln(os.Stderr, "confine:", err)
	os.Exit(exitNoConfine)
}
