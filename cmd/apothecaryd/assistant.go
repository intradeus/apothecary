package main

// The assistant's wiring (docs/assistant-plan.md): its service always, so
// the panel can say why it does not run; its pods on Kubernetes when it
// is on. The adapters below give the service what it declares: the
// person's own harness integrations, a workshop's repositories as the
// person may read them, the session tokens, the ticket types.

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/authz"
	assistantpod "apothecary/internal/adapters/assistant"
	"apothecary/internal/adapters/egressgw"
	"apothecary/internal/adapters/kube"
	"apothecary/internal/assistant"
	"apothecary/internal/httpapi"
	"apothecary/internal/platform/config"
	"apothecary/internal/ports/assistantport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/integrations"
	"apothecary/internal/project/kinds"
	"apothecary/internal/project/tickets"
)

// wireAssistant builds the assistant over the store and the resolver. stop
// ends its sweeps.
func wireAssistant(ctx context.Context, boot *config.Boot, store assistant.Store, conf *confstore.Service, resolver *integrations.Resolver,
	live *tickets.Live, authsvc *auth.Service, az *authz.Authorizer, log *slog.Logger) (*assistant.Service, func(), error) {
	var faceActions []string
	for _, a := range httpapi.MCPActions() {
		faceActions = append(faceActions, string(a))
	}
	svc := &assistant.Service{
		Store:       store,
		Harnesses:   personHarnesses{r: resolver},
		Workshops:   workshopRepos{conf: conf, r: resolver, users: authsvc, az: az},
		Tokens:      sessionTokens{a: authsvc},
		FaceActions: faceActions,
		Checks:      ticketTypes{conf: conf, live: live},
		Log:         log,
	}
	if !boot.Assistant.Enabled {
		return svc, func() {}, nil
	}
	k := boot.K8s
	if k.EngineURL == "" {
		return nil, nil, fmt.Errorf("config: the assistant needs APOTHECARY_K8S_ENGINE_URL (how its pods reach the engine's MCP face), or APOTHECARY_ASSISTANT=off")
	}
	engine, err := url.Parse(k.EngineURL)
	if err != nil || engine.Hostname() == "" {
		return nil, nil, fmt.Errorf("config: APOTHECARY_K8S_ENGINE_URL %q is not a URL", k.EngineURL)
	}
	enginePort := 80
	if p := engine.Port(); p != "" {
		enginePort, _ = strconv.Atoi(p)
	}
	kubeconfig, _, err := kube.Kubeconfig(filepath.Join(boot.RunsDir, "k8s"), k.Kubeconfig)
	if err != nil {
		return nil, nil, fmt.Errorf("assistant kubeconfig: %w", err)
	}
	kc := &kube.Client{Kubeconfig: kubeconfig, Namespace: k.Namespace}
	img := func(set, name string) string {
		if set != "" {
			return set
		}
		return config.DefaultImage(name, version)
	}
	forward := k.Transport == "portforward"
	host := assistantpod.NewK8sHost(assistantpod.Config{
		Kube: kc, Image: img(boot.Assistant.Image, "assistant"), ShimImage: img(k.ShimImage, "shim"),
		ToolsImage: img(k.ToolsImage, "tools"), TunnelImage: img(k.GatewayImage, "egress-gw"), PullPolicy: k.PullPolicy,
		StorageClass: boot.Assistant.StorageClass, VolumeSize: boot.Assistant.VolumeSize,
		EngineNamespace: k.EngineNamespace, EnginePort: enginePort, Forward: forward,
	})
	gateways := egressgw.NewManager(&egressgw.K8sHost{Kube: kc, Image: img(k.GatewayImage, "egress-gw"), PullPolicy: k.PullPolicy,
		ProtectCIDRs: k.ProtectCIDRs, EngineNamespace: k.EngineNamespace, Forward: forward})
	pods := &assistantpod.Manager{
		Host: host, Image: img(boot.Assistant.Image, "assistant"),
		Gateway: assistantpod.EgressGateway{M: gateways, EngineIP: func(ctx context.Context) (string, error) {
			return resolveIP(ctx, engine.Hostname())
		}},
		IdleAfter: boot.Assistant.IdleAfter, VolumeAfter: boot.Assistant.VolumeAfter, Log: log,
	}
	svc.Pods = pods
	face := strings.TrimSuffix(k.EngineURL, "/") + "/mcp"
	svc.FaceURL = func(context.Context) (string, error) { return face, nil }
	sweep, cancel := context.WithCancel(ctx)
	go pods.Run(sweep, time.Minute)
	return svc, cancel, nil
}

// resolveIP is host's IPv4 address: the pod's route to the engine is set up
// before it has any DNS.
func resolveIP(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("resolve the engine's address %q: %v", host, err)
	}
	return ips[0].String(), nil
}

// personHarnesses are a person's own harness integrations of a kind the
// assistant runs.
type personHarnesses struct{ r *integrations.Resolver }

func (p personHarnesses) Integrations(ctx context.Context, person string) ([]assistant.Integration, error) {
	all, err := p.r.PersonHarnesses(ctx, person)
	if err != nil {
		return nil, err
	}
	out := []assistant.Integration{}
	for _, in := range all {
		if assistant.Runs(in.Kind) {
			out = append(out, assistant.Integration{ID: in.ID, Name: in.Name, Kind: in.Kind})
		}
	}
	return out, nil
}

func (p personHarnesses) Open(ctx context.Context, person, id string, env envport.EnvProvisioner) (assistant.Harness, error) {
	ins, err := p.Integrations(ctx, person)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(ins, func(in assistant.Integration) bool { return in.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("harness integration %s is not one of yours the assistant runs on", id)
	}
	h, err := p.r.PersonHarness(ctx, person, id, env)
	if err != nil {
		return nil, err
	}
	return kindHarness{kind: ins[i].Kind, h: h}, nil
}

// kindHarness is a harness kind as the assistant sees it.
type kindHarness struct {
	kind string
	h    kinds.Harness
}

func (k kindHarness) Kind() string { return k.kind }

func (k kindHarness) Models(ctx context.Context) ([]string, error) {
	ms, err := k.h.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out, nil
}

func (k kindHarness) EnvVars() map[string]string { return k.h.EnvVars() }
func (k kindHarness) EgressDomains() []string    { return k.h.EgressDomains() }

func (k kindHarness) SeedAuth(ctx context.Context, env envport.EnvID, register func(string)) error {
	return k.h.SeedAuth(ctx, env, register)
}

func (k kindHarness) SecretValues() []string {
	if sh, ok := k.h.(kinds.SecretHolder); ok {
		return sh.SecretValues()
	}
	return nil
}

// workshopRepos are a workshop's repositories, when the person may read the
// workshop: the primary and the extra ones, each with the workshop's
// repository account's credential (which only the pod's git container
// ever holds).
type workshopRepos struct {
	conf  *confstore.Service
	r     *integrations.Resolver
	users *auth.Service
	az    *authz.Authorizer
}

func (w workshopRepos) Repos(ctx context.Context, person, workshop string) ([]assistantport.FetchItem, error) {
	u, err := w.users.GetUser(ctx, person)
	if err != nil {
		return nil, err
	}
	p, err := w.conf.GetProject(ctx, workshop)
	if err != nil {
		return nil, fmt.Errorf("workshop %s not found", workshop)
	}
	d, err := w.az.Decide(ctx, auth.Principal{User: u}, authz.ProjectRead,
		&authz.Resource{Kind: authz.KindProject, ID: workshop, Owner: p.UserID, ProjectID: workshop})
	if err != nil || !d.Allowed() {
		return nil, fmt.Errorf("workshop %s not found", workshop)
	}
	res, err := w.r.Resolve(ctx, workshop)
	if err != nil {
		return nil, err
	}
	cred := assistantport.Credential{User: res.GitUser, Pass: res.GitPass}
	item := func(name, rawURL string) assistantport.FetchItem {
		return assistantport.FetchItem{Repo: assistantport.Repo{Workshop: workshop, WorkshopName: res.ProjectName, Name: name, URL: rawURL}, Cred: cred}
	}
	out := []assistantport.FetchItem{item(repoName(res.RepoRef.URL), res.RepoRef.URL)}
	for _, x := range res.ExtraRepos {
		out = append(out, item(x.Name, x.URL))
	}
	return out, nil
}

// repoName is a repository's last path segment, without .git.
func repoName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return "repo"
	}
	return strings.TrimSuffix(path.Base(u.Path), ".git")
}

// sessionTokens mints a session's token as the person's own scoped token.
type sessionTokens struct{ a *auth.Service }

func (s sessionTokens) Mint(ctx context.Context, person, name string, actions, workshops []string, ttl time.Duration) (string, string, error) {
	until := time.Now().Add(ttl).UTC()
	secret, tok, err := s.a.NewToken(ctx, person, name, &until, auth.Grant{Actions: actions, ProjectIDs: workshops})
	if err != nil {
		return "", "", err
	}
	return tok.ID, secret, nil
}

func (s sessionTokens) Revoke(ctx context.Context, person, id string) error {
	return s.a.DeleteToken(ctx, person, id)
}

// ticketTypes are a workshop's issue types, from its ticket source.
type ticketTypes struct {
	conf *confstore.Service
	live *tickets.Live
}

func (t ticketTypes) TicketTypes(ctx context.Context, _ string, workshop string) ([]string, error) {
	p, err := t.conf.GetProject(ctx, workshop)
	if err != nil {
		return nil, err
	}
	types, err := t.live.Types(ctx, p.TicketSourceID, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(types))
	for _, ty := range types {
		out = append(out, ty.ID)
	}
	return out, nil
}
