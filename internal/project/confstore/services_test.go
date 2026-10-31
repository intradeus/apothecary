package confstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProjectServicesValidate(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	p := seedProject(t, svc)
	app := ServiceConfig{Name: "default", Start: "npm run dev",
		Health: ServiceHealth{HTTP: "http://localhost:3000/health", Timeout: "2m"},
		Ports:  []ServicePort{{Port: 3000, Label: "app"}, {Port: 6006, Label: "storybook", Path: "/docs"}}}
	db := ServiceConfig{Name: "db", Start: "docker compose up -d db", Stop: "docker compose stop db", Health: ServiceHealth{TCP: 5432}}

	p.Workspace.Services = []ServiceConfig{app, db}
	got, err := svc.UpdateProject(ctx, p.ID, p)
	if err != nil {
		t.Fatalf("valid services refused: %v", err)
	}
	if len(got.Workspace.Services) != 2 || got.Workspace.Services[1].Stop == "" {
		t.Errorf("services = %+v", got.Workspace.Services)
	}
	if h := got.Workspace.Services[0].Health; h.TimeoutOr() != 2*time.Minute || h.IntervalOr() != DefaultServiceInterval {
		t.Errorf("durations = %v %v", h.TimeoutOr(), h.IntervalOr())
	}

	for _, c := range []struct {
		name string
		mut  func(*ServiceConfig)
		want string
	}{
		{"no name", func(s *ServiceConfig) { s.Name = "" }, "must be a slug"},
		{"not a slug", func(s *ServiceConfig) { s.Name = "My App" }, "must be a slug"},
		{"duplicate", func(s *ServiceConfig) { s.Name = "db" }, `"db" is declared twice`},
		{"no start", func(s *ServiceConfig) { s.Start = " " }, "start is required"},
		{"two probes", func(s *ServiceConfig) { s.Health.TCP = 3000 }, "one probe"},
		{"bad url", func(s *ServiceConfig) { s.Health.HTTP = "localhost:3000" }, "http(s) URL"},
		{"bad tcp", func(s *ServiceConfig) { s.Health = ServiceHealth{TCP: 70000} }, "health.tcp"},
		{"bad timeout", func(s *ServiceConfig) { s.Health.Timeout = "soon" }, "health.timeout"},
		{"bad interval", func(s *ServiceConfig) { s.Health.Interval = "-1s" }, "health.interval"},
		{"bad port", func(s *ServiceConfig) { s.Ports[0].Port = 0 }, "port 0"},
		{"repeated port", func(s *ServiceConfig) { s.Ports[1].Port = 3000 }, "listed twice"},
		{"relative path", func(s *ServiceConfig) { s.Ports[1].Path = "docs" }, "must start with /"},
	} {
		bad := app
		bad.Ports = append([]ServicePort(nil), app.Ports...)
		c.mut(&bad)
		p.Workspace.Services = []ServiceConfig{bad, db}
		if _, err := svc.UpdateProject(ctx, p.ID, p); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}
