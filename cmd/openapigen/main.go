// Command openapigen writes the engine's OpenAPI document.
//
// The document is generated from internal/httpapi's route table, never edited
// by hand: `task api:spec` regenerates it, and a test fails when the committed
// file has fallen behind the code.
package main

import (
	"flag"
	"fmt"
	"os"

	"apothecary/internal/httpapi"
)

func main() {
	out := flag.String("o", "api/openapi.yaml", "where to write the document")
	check := flag.Bool("check", false, "exit non-zero if the file is out of date instead of writing it")
	flag.Parse()

	doc, err := httpapi.Spec()
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapigen:", err)
		os.Exit(1)
	}
	data, err := httpapi.SpecYAML(doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapigen:", err)
		os.Exit(1)
	}
	if *check {
		have, err := os.ReadFile(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "openapigen: %s is missing — run `task api:spec`\n", *out)
			os.Exit(1)
		}
		if string(have) != string(data) {
			fmt.Fprintf(os.Stderr, "openapigen: %s is out of date — run `task api:spec`\n", *out)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "openapigen:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d paths)\n", *out, doc.Paths.Len())
}
