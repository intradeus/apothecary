// Package seed ships the canonical v1 flow inside the binary (DESIGN2 §7):
// a fresh install gets a working flow with zero files. The embedded copy
// must stay byte-identical to flows/v1.yaml (locked by a test).
package seed

import (
	"context"
	_ "embed"

	"factory/internal/confstore"
)

//go:embed v1.yaml
var v1YAML string

// V1YAML exposes the embedded document (sync test + import tooling).
func V1YAML() string { return v1YAML }

// Flows inserts the embedded v1 flow when the flows table is empty.
// Idempotent; never overwrites user edits.
func Flows(ctx context.Context, svc *confstore.Service) error {
	rows, err := svc.ListFlows(ctx)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return nil
	}
	_, err = svc.SaveFlow(ctx, "v1", v1YAML)
	return err
}
