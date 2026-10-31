package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/project/resolution"
	"apothecary/internal/run"
	"apothecary/internal/run/event"
	"apothecary/internal/workspace/wsinfo"
)

const fixtureProject = "p1"

// appendTo appends straight to a store (the engine's AppendEvents without
// a run loop to serialize with).
type appendTo struct{ store *event.MemStore }

func (a appendTo) AppendEvents(ctx context.Context, runID string, ps ...event.Payload) error {
	_, err := a.store.Append(ctx, runID, ps...)
	return err
}

// fixture is a project with two services over a fake workspace supervisor,
// and the run page's control over it (no port exposure).
type fixture struct {
	store *event.MemStore
	envp  *fake.EnvProvisioner
	svcs  *fake.Services
	res   *resolution.Resolution
	c     *Control
}

func newFixture() *fixture {
	h := &fixture{store: event.NewMemStore(), envp: fake.NewEnvProvisioner(), svcs: fake.NewServices()}
	h.envp.ExecFunc = h.svcs.Exec(h.envp.ExecFunc)
	h.res = &resolution.Resolution{ProjectID: fixtureProject, Envp: h.envp, Services: []resolution.Service{
		{Name: "default", Start: "npm run dev", Health: resolution.ServiceHealth{HTTP: "http://localhost:3000/health", Timeout: 5 * time.Minute, Interval: 10 * time.Second},
			Ports: []resolution.ServicePort{{Port: 3000, Label: "app"}}},
		{Name: "db", Start: "docker compose up -d db", Stop: "docker compose stop db", Health: resolution.ServiceHealth{TCP: 5432, Timeout: time.Minute}},
	}}
	h.c = New(&run.Reader{Store: h.store}, resolution.StaticResolver{R: h.res}, appendTo{h.store}, nil, nil)
	return h
}

func TestServiceRefusedWithoutAWorkspace(t *testing.T) {
	h := newFixture()
	ctx := context.Background()
	if _, err := h.store.Append(ctx, "r1", &event.RunCreated{ProjectID: fixtureProject}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.StartService(ctx, "r1", "default", false); !errors.Is(err, wsinfo.ErrNoWorkspace) {
		t.Errorf("StartService = %v", err)
	}
	list, err := h.c.RunServices(ctx, "r1")
	if err != nil || list.Workspace || len(list.Services) != 2 || list.Services[0].State != "stopped" {
		t.Errorf("RunServices = %+v, %v", list, err)
	}
}
