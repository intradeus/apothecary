package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/oklog/ulid/v2"

	"apothecary/internal/confstore"
	"apothecary/internal/envcheck"
	"apothecary/internal/pgstore"
	"apothecary/internal/platform/config"
	"apothecary/internal/resolve"
)

// cmdEnvcheck drives the §9 compliance gate from the CLI — the same
// envcheck.Run the API uses, printing steps as they complete. The
// check list is derived from the project's resolved configuration.
func cmdEnvcheck(args []string) error {
	fs := flag.NewFlagSet("envcheck", flag.ExitOnError)
	projectRef := fs.String("project", "", "project name or id")
	keep := fs.Bool("keep", false, "keep the workspace for inspection")
	_ = fs.Parse(args)
	if *projectRef == "" {
		return fmt.Errorf("usage: apothecaryd envcheck -project <name|id> [-keep]")
	}

	boot, err := config.FromEnv()
	if err != nil {
		return err
	}
	key, err := boot.Key()
	if err != nil {
		return err
	}
	ctx := context.Background()
	store, err := pgstore.Open(ctx, boot.PostgresDSN)
	if err != nil {
		return err
	}
	defer store.Close()

	reg := registry(false, boot)
	conf := confstore.New(store, key)
	conf.KindValid = reg.Valid

	project, err := conf.GetProjectByName(ctx, "", *projectRef)
	if err != nil {
		if project, err = conf.GetProject(ctx, *projectRef); err != nil {
			return fmt.Errorf("project %q not found (by name or id)", *projectRef)
		}
	}

	enginePort := 8080
	if _, p, err := splitHostPort(boot.Listen); err == nil {
		fmt.Sscanf(p, "%d", &enginePort)
	}
	resolver := resolve.New(conf, reg, boot.RunsDir, enginePort, boot.SandboxRuntime)
	defer resolver.Close()
	res, err := resolver.Resolve(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("resolve project %s: %w", project.Name, err)
	}

	// Tunnel probe target: the running engine if the port is taken, else a
	// local 204 listener (standalone runs).
	if stop, err := startProbeListener(enginePort); err == nil {
		defer stop()
	} else {
		fmt.Printf("note  port %d busy (engine running?) — testing the tunnel against it\n", enginePort)
	}

	runID := "envck" + strings.ToLower(ulid.Make().String()[16:])
	fmt.Printf("envcheck project %s, run id %s\n", project.Name, runID)
	failed := envcheck.Run(ctx, res, runID,
		envcheck.Options{EngineURL: engineURL(boot.Listen), Keep: *keep},
		func(s envcheck.CheckStep) {
			mark := map[string]string{"pass": "ok  ", "fail": "FAIL", "skip": "skip"}[s.Status]
			fmt.Printf("%s  %-28s %s\n", mark, s.ID, s.Detail)
		})
	if failed > 0 {
		return fmt.Errorf("envcheck: %d check(s) failed", failed)
	}
	fmt.Println("envcheck: all checks passed")
	return nil
}

// startProbeListener serves 204s on the host loopback for the tunnel check.
func startProbeListener(port int) (func(), error) {
	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, err
	}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close() }, nil
}
