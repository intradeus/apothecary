package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"apothecary/internal/adapters/pgstore"
)

// TestMain lets a test run the real main in a child process, where its exits
// are what an operator's shell sees.
func TestMain(m *testing.M) {
	if raw := os.Getenv("MIGRATIONSUM_TEST_ARGV"); raw != "" {
		var argv []string
		if err := json.Unmarshal([]byte(raw), &argv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Args = append([]string{"migrationsum"}, argv...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWritesTheSumFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrations.sum")
	oldArgs, oldFlags, oldOut := os.Args, flag.CommandLine, os.Stdout
	defer func() { os.Args, flag.CommandLine, os.Stdout = oldArgs, oldFlags, oldOut }()
	os.Args = []string{"migrationsum", "-o", path}
	flag.CommandLine = flag.NewFlagSet("migrationsum", flag.ContinueOnError)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	main()
	w.Close()
	out, _ := io.ReadAll(r)
	if string(out) != "wrote "+path+"\n" {
		t.Errorf("output = %q", out)
	}
	want, err := pgstore.MigrationsSum()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
		t.Errorf("wrote %q, %v; want %q", got, err, want)
	}
}

func TestWriteFailureExits(t *testing.T) {
	argv, err := json.Marshal([]string{"-o", filepath.Join(t.TempDir(), "no", "such", "dir", "migrations.sum")})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	// GOCOVERDIR: a binary built with -cover warns on stderr without it.
	cmd.Env = []string{"MIGRATIONSUM_TEST_ARGV=" + string(argv), "GOCOVERDIR=" + t.TempDir()}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 || !strings.HasPrefix(stderr.String(), "migrationsum:") {
		t.Errorf("exit %v, %q", err, stderr.String())
	}
}
