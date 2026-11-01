package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jq delegates to gojq's own CLI: real jq flags, a file operand or stdin.
func TestJQ(t *testing.T) {
	file := filepath.Join(t.TempDir(), "commit.json")
	if err := os.WriteFile(file, []byte(`{"subject":"feat: jq in the shim","n":2}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A file operand, -r: the raw string, no JSON quotes.
	out, err := shimCmd(t, "jq", "-r", ".subject", file).CombinedOutput()
	if err != nil || string(out) != "feat: jq in the shim\n" {
		t.Errorf("jq -r .subject <file> = %q, %v", out, err)
	}

	// stdin, -r with --arg.
	cmd := shimCmd(t, "jq", "-r", "--arg", "p", "n=", `$p + (.n | tostring)`)
	cmd.Stdin = strings.NewReader(`{"n":2}`)
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "n=2\n" {
		t.Errorf("jq -r --arg <stdin> = %q, %v", out, err)
	}

	// -e: a null result is a non-zero exit, as with jq.
	cmd = shimCmd(t, "jq", "-e", ".missing")
	cmd.Stdin = strings.NewReader(`{}`)
	if err := cmd.Run(); err == nil {
		t.Error("jq -e on a null result must exit non-zero")
	}
}
