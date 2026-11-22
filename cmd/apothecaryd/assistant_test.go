package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/authz"
	"apothecary/internal/adapters/pgstore/pgtest"
	"apothecary/internal/analytics"
	"apothecary/internal/assistant"
	"apothecary/internal/platform/config"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/kinds"
	"apothecary/internal/project/tickets"
	"apothecary/internal/skills"
	"apothecary/internal/skills/skillstore"
)

// The assistant on: the boot wires its pods on Kubernetes, and refuses an
// engine its pods could not reach.
func TestAssistantWiring(t *testing.T) {
	boot := testBoot(t, pgtest.Empty(t))
	boot.K8s = config.K8s{
		Namespace: "sandboxes", EngineNamespace: "apothecary", EngineURL: "http://10.0.0.7:8080/",
		Kubeconfig: filepath.Join(t.TempDir(), "kubeconfig"), BrowserProxyListen: "127.0.0.1:0", Transport: "portforward",
	}
	boot.Assistant = config.Assistant{Enabled: true, IdleAfter: time.Minute, VolumeAfter: time.Hour, VolumeSize: "1Gi"}
	a, _ := booted(t, boot, false)
	if code, body := newClient(t, a).do("GET", "/api/auth/status", nil); code != 200 {
		t.Errorf("status = %d %s", code, body)
	}

	ctx := context.Background()
	for name, url := range map[string]string{"no engine URL": "", "not a URL": "::"} {
		b := *boot
		b.K8s.EngineURL = url
		if _, _, err := wireAssistant(ctx, &b, nil, nil, nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("%s: wired", name)
		}
	}
	off := *boot
	off.Assistant.Enabled = false
	svc, stop, err := wireAssistant(ctx, &off, nil, nil, nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	if err != nil || svc == nil || svc.Pods != nil || !slices.Contains(svc.FaceActions, string(authz.ProjectRead)) {
		t.Errorf("off = %+v, %v", svc, err)
	}
	stop()
}

func TestResolveIPAndRepoName(t *testing.T) {
	ctx := context.Background()
	if ip, err := resolveIP(ctx, "10.1.2.3"); err != nil || ip != "10.1.2.3" {
		t.Errorf("an address = %s, %v", ip, err)
	}
	if ip, err := resolveIP(ctx, "localhost"); err != nil || !strings.HasPrefix(ip, "127.") {
		t.Errorf("localhost = %s, %v", ip, err)
	}
	if _, err := resolveIP(ctx, "nowhere.invalid"); err == nil {
		t.Error("an unknown host resolved")
	}
	for raw, want := range map[string]string{
		"https://github.com/acme/api.git":  "api",
		"https://gitlab.example.com/g/web": "web",
		"https://example.com":              "repo",
		"::":                               "repo",
	} {
		if got := repoName(raw); got != want {
			t.Errorf("repoName(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The adapters over a real boot: the person's own harnesses, a workshop's
// repositories only for who may read it, session tokens, ticket types.
func TestAssistantAdapters(t *testing.T) {
	boot := testBoot(t, pgtest.Empty(t))
	a, _ := booted(t, boot, true)
	ctx := context.Background()
	authsvc := auth.New(a.store)
	authsvc.CheckGrant = authz.ValidateGrant
	az, err := authz.New(a.conf)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := authsvc.CreateUser(ctx, auth.NewUser{Handle: "lu", Email: "lu@example.test", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := authsvc.CreateUser(ctx, auth.NewUser{Handle: "kim", Email: "kim@example.test", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.conf.GetProjectByName(ctx, "", "dev")
	if err != nil {
		t.Fatal(err)
	}
	// The seed is ownerless; hand it to the owner as a dev would.
	db, err := pgx.Connect(ctx, boot.PostgresDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	for _, stmt := range []string{"UPDATE projects SET user_id=$1", "UPDATE integrations SET user_id=$1 WHERE port <> 'provisioner'"} {
		if _, err := db.Exec(ctx, stmt, owner.ID); err != nil {
			t.Fatal(err)
		}
	}

	// Repositories: the owner reads them, with the repository account's
	// credential; a stranger and an unknown workshop find nothing.
	repos := workshopRepos{conf: a.conf, r: a.resolver, users: authsvc, az: az}
	items, err := repos.Repos(ctx, owner.ID, p.ID)
	if err != nil || len(items) == 0 || items[0].Repo.Workshop != p.ID || items[0].Repo.Name == "" {
		t.Fatalf("owner's repos = %+v, %v", items, err)
	}
	if _, err := repos.Repos(ctx, stranger.ID, p.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a stranger's repos = %v", err)
	}
	if _, err := repos.Repos(ctx, owner.ID, "nope"); err == nil {
		t.Error("an unknown workshop's repos: no error")
	}
	if _, err := repos.Repos(ctx, "nobody", p.ID); err == nil {
		t.Error("an unknown person's repos: no error")
	}

	// Harnesses: only the person's own, of a kind the assistant runs.
	mine, err := a.conf.CreateIntegration(ctx, confstore.Integration{UserID: owner.ID, Port: confstore.PortHarness, Kind: "claude-code", Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	harnesses := personHarnesses{r: a.resolver}
	ins, err := harnesses.Integrations(ctx, owner.ID)
	if err != nil || len(ins) != 1 || ins[0].ID != mine.ID || ins[0].Kind != "claude-code" {
		t.Fatalf("owner's harnesses = %+v, %v", ins, err)
	}
	if ins, _ := harnesses.Integrations(ctx, stranger.ID); len(ins) != 0 {
		t.Errorf("a stranger's harnesses = %+v", ins)
	}
	h, err := harnesses.Open(ctx, owner.ID, mine.ID, nil)
	if err != nil || h.Kind() != "claude-code" {
		t.Fatalf("open mine = %+v, %v", h, err)
	}
	_, _ = h.EnvVars(), h.EgressDomains()
	_ = h.SecretValues()
	if _, err := harnesses.Open(ctx, stranger.ID, mine.ID, nil); err == nil {
		t.Error("a stranger opened my harness")
	}
	fakeH, err := kinds.FakeHarness{}.New(kinds.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fk := kindHarness{kind: "fake", h: fakeH}
	if ms, err := fk.Models(ctx); err != nil || len(ms) == 0 {
		t.Errorf("fake models = %v, %v", ms, err)
	}
	if fk.SecretValues() != nil && len(fk.SecretValues()) > 0 {
		t.Errorf("fake secrets = %v", fk.SecretValues())
	}
	if err := fk.SeedAuth(ctx, "env", func(string) {}); err != nil {
		t.Errorf("fake seed = %v", err)
	}

	// Session tokens: the person's own, scoped, revocable.
	tokens := sessionTokens{a: authsvc}
	id, secret, err := tokens.Mint(ctx, owner.ID, "Assistant session s1", []string{string(authz.ProjectRead), assistant.FillAction}, []string{p.ID}, time.Hour)
	if err != nil || id == "" || secret == "" {
		t.Fatalf("mint = %s %s %v", id, secret, err)
	}
	if err := tokens.Revoke(ctx, owner.ID, id); err != nil {
		t.Errorf("revoke = %v", err)
	}
	if list, _ := authsvc.ListTokens(ctx, owner.ID); len(list) != 0 {
		t.Errorf("tokens after revoke = %+v", list)
	}

	// A dashboard's panels: the analytics service's rule, through the page
	// checks; none served, none checked.
	checks := pageChecks{ticketTypes: ticketTypes{conf: a.conf}, dashboards: &analytics.Service{Store: a.store}}
	if err := checks.DashboardPanels(ctx, owner.ID, json.RawMessage(`[{"viz":"table","width":12,"query":{"grain":"runs"}}]`)); err != nil {
		t.Errorf("good panels = %v", err)
	}
	if err := checks.DashboardPanels(ctx, owner.ID, json.RawMessage(`[{"viz":"pie","width":12,"query":{"grain":"runs"}}]`)); err == nil {
		t.Error("a bad viz passed")
	}
	if err := checks.DashboardPanels(ctx, owner.ID, json.RawMessage(`{}`)); err == nil {
		t.Error("panels that are not a list passed")
	}
	if err := (pageChecks{}).DashboardPanels(ctx, owner.ID, json.RawMessage(`[]`)); err == nil {
		t.Error("no analytics, and panels checked")
	}

	// Ticket types: the workshop's source's.
	types := ticketTypes{conf: a.conf, live: tickets.NewLive(a.conf, a.resolver)}
	if _, err := types.TicketTypes(ctx, owner.ID, p.ID); err != nil {
		t.Errorf("ticket types = %v", err)
	}
	if _, err := types.TicketTypes(ctx, owner.ID, "nope"); err == nil {
		t.Error("an unknown workshop's ticket types: no error")
	}

	// The skill library: a ref that does not resolve is a skillset gone,
	// the store failing to answer is not.
	lib := skillLibrary{skillstore.New(a.store, a.blobs)}
	set, err := lib.CreateSkillset(ctx, owner.ID, skillstore.SkillsetInput{Name: "tickets"})
	if err != nil {
		t.Fatal(err)
	}
	if pins, err := lib.ResolveSkillsets(ctx, owner.ID, []skills.Ref{{ID: set.ID}}); err != nil || len(pins) != 1 {
		t.Fatalf("my skillset = %+v, %v", pins, err)
	}
	for name, ref := range map[string]skills.Ref{"another's": {ID: set.ID}, "deleted": {ID: "nope"}, "no such version": {ID: set.ID, Seq: 9}} {
		who := stranger.ID
		if name != "another's" {
			who = owner.ID
		}
		if _, err := lib.ResolveSkillsets(ctx, who, []skills.Ref{ref}); !errors.Is(err, assistant.ErrSkillsetGone) {
			t.Errorf("%s = %v", name, err)
		}
	}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := lib.ResolveSkillsets(gone, owner.ID, []skills.Ref{{ID: set.ID}}); err == nil || errors.Is(err, assistant.ErrSkillsetGone) {
		t.Errorf("the store failing = %v", err)
	}
}
