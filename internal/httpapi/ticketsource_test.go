package httpapi

import (
	"context"
	"errors"
	"testing"

	"apothecary/internal/engine"
	"apothecary/internal/project/confstore"
)

func TestTrackerHome(t *testing.T) {
	for _, c := range []struct {
		name, kind string
		config     map[string]any
		want       string
	}{
		{"jira site", "jira", map[string]any{"base_url": "https://acme.atlassian.net/"}, "https://acme.atlassian.net"},
		{"jira without a site", "jira", nil, ""},
		{"gitlab host", "gitlab", map[string]any{"base_url": "https://git.acme"}, "https://git.acme"},
		{"gitlab, one project", "gitlab", map[string]any{"projects": "acme/app"}, "https://gitlab.com/acme/app/-/issues"},
		{"gitlab, several projects", "gitlab", map[string]any{"base_url": "https://git.acme", "projects": "a/b, c/d"}, "https://git.acme"},
		{"github.com", "github", map[string]any{"base_url": "https://api.github.com"}, "https://github.com"},
		{"github, one repo", "github", map[string]any{"repos": " acme/app "}, "https://github.com/acme/app/issues"},
		{"github enterprise", "github", map[string]any{"base_url": "https://gh.acme/api/v3", "repos": "a/b"}, "https://gh.acme/a/b/issues"},
		{"a scope that is no string", "github", map[string]any{"repos": []string{"a/b"}}, "https://github.com"},
		{"internal", confstore.KindInternal, nil, ""},
		{"an address that is no web page", "jira", map[string]any{"base_url": "javascript:alert(1)"}, ""},
		{"an address with no host", "jira", map[string]any{"base_url": "https://"}, ""},
	} {
		if got := trackerHome(c.kind, c.config); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// integrationFails fails the integration reads.
type integrationFails struct {
	confstore.DB
	err error
}

func (f integrationFails) GetIntegration(context.Context, string) (confstore.StoredIntegration, error) {
	return confstore.StoredIntegration{}, f.err
}

// A ticket source that is gone is none; one that cannot be read fails the
// workshop's view rather than reading as none.
func TestTicketSourceView(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	seen := newProjectLookups()
	if v, err := x.srv.ticketSourceView(ctx, "gone", seen); v != nil || err != nil {
		t.Errorf("gone = %+v, %v", v, err)
	}
	if _, ok := seen.sources["gone"]; !ok {
		t.Error("a gone source is not remembered for the list")
	}
	x.srv.Conf = confstore.New(integrationFails{x.srv.Conf.DB(), errors.New("db down")}, secretsKey(t))
	if v, err := x.srv.ticketSourceView(ctx, "gone", seen); v != nil || err != nil {
		t.Errorf("remembered = %+v, %v", v, err)
	}
	if _, err := x.srv.ticketSourceView(ctx, "other", seen); err == nil {
		t.Error("an outage read as no source")
	}
}
