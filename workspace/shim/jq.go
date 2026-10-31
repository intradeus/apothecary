package main

// jq is a full jq CLI for command-stage scripts: `shim jq <jq args>` hands the
// arguments, verbatim, to gojq's own command (github.com/itchyny/gojq/cli) —
// same flags (-r, -c, -e, --arg, --argjson, --slurp, files or stdin), same
// exit codes. Repo images carry no jq/python guarantee; the shim is bind-
// mounted into every workspace, so `apothecary-shim jq -r .subject
// artifacts/commit.json` works whatever the image. The version is pinned in
// the shim builder Dockerfile (internal/workspace/wsimage gojqVersion), which a test
// holds equal to the root go.mod's.

import (
	"os"

	"github.com/itchyny/gojq/cli"
)

func runJQ(args []string) {
	// cli.Run parses os.Args[1:]; "jq" stands in as the program name.
	os.Args = append([]string{"jq"}, args...)
	os.Exit(cli.Run())
}
