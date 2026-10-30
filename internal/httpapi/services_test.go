package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"apothecary/internal/adapters/fake"
	"apothecary/internal/engine"
	"apothecary/internal/event"
	"apothecary/internal/ports"
	"apothecary/internal/resolution"
	"apothecary/internal/services"
)

func TestServiceEndpoints(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.res.Services = []resolution.Service{
		{Name: "default", Start: "npm run dev", Health: resolution.ServiceHealth{TCP: 3000, Timeout: time.Minute},
			Ports: []resolution.ServicePort{{Port: 3000}}},
	}
	svcs := fake.NewServices()
	svcs.Log["default"] = "listening on 3000\n"
	x.envp.ExecFunc = svcs.Exec(x.envp.ExecFunc)
	ctx := context.Background()
	// A run with a workspace, and one without.
	env, err := x.envp.Provision(ctx, ports.EnvSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.store.Append(ctx, "r1", &event.RunCreated{ProjectID: testProject, UserID: testUser, FlowName: "loop"}, &event.EnvReady{EnvID: string(env)}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.store.Append(ctx, "r2", &event.RunCreated{ProjectID: testProject, UserID: testUser, FlowName: "loop"}); err != nil {
		t.Fatal(err)
	}
	list := func(id string) services.RunServices {
		t.Helper()
		res, body := x.do(t, "GET", "/api/runs/"+id+"/services", nil, nil)
		var out services.RunServices
		if res.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("GET services = %d %s", res.StatusCode, body)
		}
		return out
	}
	if got := list("r1"); !got.Workspace || len(got.Services) != 1 || got.Services[0].State != "stopped" || got.Services[0].Ports[0].Address != "http://localhost:3000/" {
		t.Errorf("before = %+v", got)
	}
	res, body := x.do(t, "POST", "/api/runs/r1/services/default/start", nil, nil)
	var info services.ServiceInfo
	if res.StatusCode != http.StatusAccepted || json.Unmarshal(body, &info) != nil || info.By != event.ServiceByHuman {
		t.Fatalf("start = %d %s", res.StatusCode, body)
	}
	if got := list("r1"); !got.Services[0].Healthy {
		t.Errorf("after start = %+v", got)
	}
	if res, body := x.do(t, "GET", "/api/runs/r1/services/default/logs?tail=10", nil, nil); res.StatusCode != http.StatusOK || string(body) != "listening on 3000\n" {
		t.Errorf("logs = %d %q", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/runs/r1/services/default/restart", nil, nil); res.StatusCode != http.StatusAccepted {
		t.Errorf("restart = %d %s", res.StatusCode, body)
	}
	if res, body := x.do(t, "POST", "/api/runs/r1/services/default/stop", nil, nil); res.StatusCode != http.StatusAccepted {
		t.Errorf("stop = %d %s", res.StatusCode, body)
	}
	if got := list("r1"); got.Services[0].State != "stopped" {
		t.Errorf("after stop = %+v", got)
	}
	if res, _ := x.do(t, "POST", "/api/runs/r1/services/nope/start", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown service = %d", res.StatusCode)
	}
	if res, _ := x.do(t, "POST", "/api/runs/r2/services/default/start", nil, nil); res.StatusCode != http.StatusConflict {
		t.Errorf("no workspace = %d", res.StatusCode)
	}
	if got := list("r2"); got.Workspace {
		t.Errorf("r2 = %+v", got)
	}
	if res, _ := x.do(t, "GET", "/api/runs/nope/services", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown run = %d", res.StatusCode)
	}
}
