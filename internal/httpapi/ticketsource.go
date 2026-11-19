package httpapi

// A workshop's ticket source as its readers see it. The source is an
// integration of the workshop's owner — a person's credentials, listed to
// no one else — yet whoever may write the workshop's tickets needs to know
// where they are written: here (the internal tracker) or in the tracker
// itself, and where that is.

import (
	"cmp"
	"context"
	"errors"
	"net/url"
	"strings"

	"apothecary/internal/project/confstore"
)

// projectLookups caches what workshop views share across a list: owners'
// handles and ticket sources, by id.
type projectLookups struct {
	handles map[string]string
	sources map[string]*TicketSourceView
}

func newProjectLookups() projectLookups {
	return projectLookups{handles: map[string]string{}, sources: map[string]*TicketSourceView{}}
}

// ticketSourceView is the ticket source id names, nil when there is none
// or it is gone. Its secrets are never opened.
func (s *Server) ticketSourceView(ctx context.Context, id string, seen projectLookups) (*TicketSourceView, error) {
	if id == "" {
		return nil, nil
	}
	if v, ok := seen.sources[id]; ok {
		return v, nil
	}
	in, err := s.Conf.GetIntegrationSealed(ctx, id)
	var v *TicketSourceView
	switch {
	case err == nil:
		v = &TicketSourceView{Kind: in.Kind, Name: in.Name, Home: trackerHome(in.Kind, in.Config)}
	case !errors.Is(err, confstore.ErrNotFound):
		return nil, err
	}
	seen.sources[id] = v
	return v, nil
}

// trackerHome is where a remote tracker's issues are browsed, from its
// config: the site for Jira, the one project's or repository's issues on
// GitLab and GitHub (the host alone when the scope names several). "" when
// there is no telling, or the address is no http(s) one. The address is
// the configured one stripped of whatever may carry a credential — its
// user info, query and fragment — since everyone the workshop is shared
// with reads it.
func trackerHome(kind string, config map[string]any) string {
	str := func(key string) string {
		v, _ := config[key].(string)
		return strings.TrimRight(strings.TrimSpace(v), "/")
	}
	// only is the single entry of a comma-separated scope list, "" when
	// there are none or several (no one place to land then).
	only := func(key string) string {
		var one string
		n := 0
		for _, s := range strings.Split(str(key), ",") {
			if s = strings.TrimSpace(s); s != "" {
				one, n = s, n+1
			}
		}
		if n != 1 {
			return ""
		}
		return one
	}
	base := str("base_url")
	var scope []string
	switch kind {
	case "jira":
	case "gitlab":
		base = cmp.Or(base, "https://gitlab.com")
		if p := only("projects"); p != "" {
			scope = []string{p, "-", "issues"}
		}
	case "github":
		base = cmp.Or(base, "https://api.github.com")
		if r := only("repos"); r != "" {
			scope = []string{r, "issues"}
		}
	default:
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	home := url.URL{Scheme: u.Scheme, Host: u.Host, Path: strings.TrimRight(u.Path, "/")}
	if kind == "github" {
		// base_url is the API: api.github.com, or <host>/api/v3 on Enterprise.
		if home.Host == "api.github.com" {
			home = url.URL{Scheme: "https", Host: "github.com"}
		}
		home.Path = strings.TrimSuffix(home.Path, "/api/v3")
	}
	if scope != nil {
		home = *home.JoinPath(scope...)
	}
	return home.String()
}
