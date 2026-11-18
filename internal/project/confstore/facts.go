package confstore

import (
	"context"
	"errors"

	"apothecary/internal/account/authz"
)

// The Service is what authz reads its facts from: a workshop's owner and
// members.
var _ authz.Facts = (*Service)(nil)

// Workshop is a project's owner and userID's role on it ("" owner: no such
// project).
func (s *Service) Workshop(ctx context.Context, projectID, userID string) (string, authz.ProjectRole, error) {
	owner, role, err := s.db.ProjectAccess(ctx, projectID, userID)
	if errors.Is(err, ErrNotFound) {
		return "", "", nil
	}
	return owner, authz.ProjectRole(role), err
}

// Workshops is every project userID owns or is a member of.
func (s *Service) Workshops(ctx context.Context, userID string) ([]authz.Membership, error) {
	rs, err := s.db.ProjectsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]authz.Membership, 0, len(rs))
	for _, r := range rs {
		out = append(out, authz.Membership{ProjectID: r.ProjectID, Owner: r.OwnerID, Role: authz.ProjectRole(r.Role)})
	}
	return out, nil
}
