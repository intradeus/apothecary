package confstore

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// egressMu serialises the egress-rule saves: the read-modify-write of a
// project's two host lists would otherwise drop a host when a person rules
// on one while the engine saves what a provisioning reached.
var egressMu sync.Mutex

// SaveEgressRule records hosts' rule on the project, so every later run
// starts with it: allow puts them on the allowlist (extra_domains), block
// on blocked_domains, and a host is only ever on one of the two. One update
// whatever the number of hosts; nothing to change writes nothing.
func (s *Service) SaveEgressRule(ctx context.Context, projectID string, allow bool, hosts ...string) error {
	egressMu.Lock()
	defer egressMu.Unlock()
	p, err := s.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	// Copies: an in-memory store may hand out its own slices.
	p.Workspace.ExtraDomains = slices.Clone(p.Workspace.ExtraDomains)
	p.Workspace.BlockedDomains = slices.Clone(p.Workspace.BlockedDomains)
	on, off := &p.Workspace.ExtraDomains, &p.Workspace.BlockedDomains
	if !allow {
		on, off = off, on
	}
	changed := false
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if i := slices.Index(*off, h); i >= 0 {
			*off = slices.Delete(*off, i, i+1)
			changed = true
		}
		if !slices.Contains(*on, h) {
			*on = append(*on, h)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	_, err = s.UpdateProject(ctx, projectID, p)
	return err
}
