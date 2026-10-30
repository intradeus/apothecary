package egressport

import (
	"testing"
)

func TestEgressPolicyStatus(t *testing.T) {
	allow := EgressPolicy{Mode: "allow", Domains: []string{"github.com", "*.npmjs.org"}, BlockedDomains: []string{"evil.io", "*.ads.net"}}
	deny := EgressPolicy{Mode: "deny", Domains: []string{"pastebin.com"}, BlockedDomains: []string{"evil.io"}}
	for _, c := range []struct {
		pol  EgressPolicy
		host string
		want EgressStatus
	}{
		{allow, "github.com", EgressAllowed},
		{allow, "GitHub.com.", EgressAllowed},
		{allow, "registry.npmjs.org", EgressAllowed},
		{allow, "npmjs.org", EgressHeld}, // a wildcard never covers its apex
		{allow, "example.com", EgressHeld},
		{allow, "evil.io", EgressBlocked},
		{allow, "x.ads.net", EgressBlocked},
		{deny, "example.com", EgressAllowed},
		{deny, "pastebin.com", EgressBlocked}, // a denylist entry is a written rule
		{deny, "evil.io", EgressBlocked},
		{EgressPolicy{}, "example.com", EgressHeld}, // empty mode = allowlist
	} {
		if got := c.pol.Status(c.host); got != c.want {
			t.Errorf("%s under %s mode: %s, want %s", c.host, c.pol.Mode, got, c.want)
		}
	}
}
