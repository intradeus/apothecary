// Package pgtest gives tests a real Postgres: one throwaway container per test
// binary, started on first use and removed when the binary exits, however it
// exits (testcontainers' reaper), with a database of its own for each test.
// Packages run side by side and tests never see each other's rows, so nothing
// here needs a fixed port or a truncate.
//
// Tests skip unless TEST_POSTGRES is set, so `go test ./...` needs nothing
// running; `task go:test:pg` sets it. Once set, a missing docker daemon
// is a failure, not a skip: a run that asked for Postgres must not pass
// without it.
//
// Only tests import this: the container library must stay out of the daemon.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the Postgres the tests run on: the one deploy/ runs.
const Image = "postgres:16"

// EnableVar turns the tests on.
const EnableVar = "TEST_POSTGRES"

var (
	startOnce sync.Once
	serverDSN string
	startErr  error

	templateMu   sync.Mutex // a template can't be cloned twice at once
	templateName string
	templateErr  error
)

func start() {
	// The images are public: pull them anonymously. Unset, testcontainers
	// reads ~/.docker/config.json and asks every credential helper in it for
	// the user's registry logins — on macOS a keychain prompt, from a test.
	// An explicit DOCKER_AUTH_CONFIG still wins.
	if os.Getenv("DOCKER_AUTH_CONFIG") == "" {
		_ = os.Setenv("DOCKER_AUTH_CONFIG", "{}")
	}
	ctx := context.Background()
	c, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("test"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
		// Durability is the one thing a throwaway server doesn't need.
		testcontainers.WithCmdArgs("-c", "synchronous_commit=off", "-c", "full_page_writes=off", "-c", "max_connections=300"))
	if err != nil {
		startErr = fmt.Errorf("start %s: %w", Image, err)
		return
	}
	serverDSN, startErr = c.ConnectionString(ctx, "sslmode=disable")
}

// server is the container's DSN, or the test's end.
func server(t *testing.T) string {
	t.Helper()
	if os.Getenv(EnableVar) == "" {
		t.Skip(EnableVar + " not set: this test needs a real Postgres (task go:test:pg)")
	}
	startOnce.Do(start)
	if startErr != nil {
		t.Fatalf("pgtest: %v", startErr)
	}
	return serverDSN
}

// Empty returns the DSN of a new, empty database, dropped when the test ends.
func Empty(t *testing.T) string {
	t.Helper()
	return create(t, server(t), "")
}

// Migrated returns the DSN of a new database cloned from one migrate prepared,
// dropped when the test ends. migrate runs once per binary, on the first call:
// cloning takes milliseconds, migrating does not.
func Migrated(t *testing.T, migrate func(dsn string) error) string {
	t.Helper()
	admin := server(t)
	templateMu.Lock()
	defer templateMu.Unlock()
	if templateName == "" && templateErr == nil {
		templateName, templateErr = prepare(admin, migrate)
	}
	if templateErr != nil {
		t.Fatalf("pgtest: template: %v", templateErr)
	}
	return create(t, admin, templateName)
}

// prepare makes the database every clone starts from.
func prepare(admin string, migrate func(dsn string) error) (string, error) {
	name, err := newName()
	if err != nil {
		return "", err
	}
	if err := exec(admin, "CREATE DATABASE "+name); err != nil {
		return "", err
	}
	if err := migrate(on(admin, name)); err != nil {
		return "", err
	}
	return name, nil
}

func create(t *testing.T, admin, template string) string {
	t.Helper()
	name, err := newName()
	if err != nil {
		t.Fatal(err)
	}
	stmt := "CREATE DATABASE " + name
	if template != "" {
		stmt += " TEMPLATE " + template
	}
	if err := exec(admin, stmt); err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	t.Cleanup(func() {
		if err := exec(admin, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("pgtest: %v", err)
		}
	})
	return on(admin, name)
}

func newName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "t_" + hex.EncodeToString(b), nil
}

// on is the server's DSN aimed at another database.
func on(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		panic(err) // the DSN came from the container library
	}
	u.Path = "/" + name
	return u.String()
}

func exec(dsn, stmt string) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, stmt)
	return err
}
