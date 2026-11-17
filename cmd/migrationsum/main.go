// Command migrationsum writes internal/adapters/pgstore/migrations/migrations.sum.
//
// The file lists every migration's name and checksum under a total of them
// all, so two branches that each add a migration conflict on it in git.
// `task migrations:sum` regenerates it, and a test fails when the committed
// file has fallen behind the migrations.
package main

import (
	"flag"
	"fmt"
	"os"

	"apothecary/internal/adapters/pgstore"
)

func main() {
	out := flag.String("o", "internal/adapters/pgstore/migrations/migrations.sum", "where to write the sum file")
	flag.Parse()

	data, err := pgstore.MigrationsSum()
	if err == nil {
		err = os.WriteFile(*out, data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrationsum:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
}
