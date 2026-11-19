package confstore

import (
	"context"
	"slices"
	"time"

	"apothecary/internal/account/authz"
)

// Member roles: what a workshop's owner shares it as (authz's, which
// decides what each holds). The owner is projects.user_id and never a
// member row.
const (
	MemberViewer     = string(authz.RoleViewer)
	MemberOperator   = string(authz.RoleOperator)
	MemberMaintainer = string(authz.RoleMaintainer)
	// RoleOwner is what Reach says for a project the user owns.
	RoleOwner = string(authz.RoleOwner)
)

// MemberRoles are the roles a member can hold, lowest first.
var MemberRoles = []string{MemberViewer, MemberOperator, MemberMaintainer}

// Member is one account a workshop is shared with.
type Member struct {
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// Reach is a project an account reaches: its own (Role RoleOwner) or one
// shared with it.
type Reach struct {
	ProjectID string
	OwnerID   string
	Role      string
}

// PutMember shares a workshop with an account, or changes its role there,
// when its role there is still from ("": not yet a member) — ErrConflict
// when it changed meanwhile. The owner is not a member.
func (s *Service) PutMember(ctx context.Context, m Member, from string) (Member, error) {
	if !slices.Contains(MemberRoles, m.Role) {
		return Member{}, invalidf("role must be viewer, operator or maintainer")
	}
	p, err := s.db.GetProject(ctx, m.ProjectID)
	if err != nil {
		return Member{}, err
	}
	if p.UserID == m.UserID {
		return Member{}, invalidf("the owner of a workshop is not one of its members")
	}
	m.CreatedAt = s.now().UTC() // a change keeps the stored one
	if err := s.db.PutMember(ctx, m, from); err != nil {
		return Member{}, err
	}
	return s.db.GetMember(ctx, m.ProjectID, m.UserID)
}

func (s *Service) GetMember(ctx context.Context, projectID, userID string) (Member, error) {
	return s.db.GetMember(ctx, projectID, userID)
}

func (s *Service) ListMembers(ctx context.Context, projectID string) ([]Member, error) {
	return s.db.ListMembers(ctx, projectID)
}

// DeleteMember stops sharing a workshop with an account whose role there is
// still from: ErrConflict when it changed meanwhile, ErrNotFound when it
// is no member.
func (s *Service) DeleteMember(ctx context.Context, projectID, userID, from string) error {
	return s.db.DeleteMember(ctx, projectID, userID, from)
}

// ProjectAccess is a project's owner and userID's role on it: RoleOwner
// for its owner, "" for anyone else who is not a member. A missing project
// is ErrNotFound.
func (s *Service) ProjectAccess(ctx context.Context, projectID, userID string) (owner, role string, err error) {
	return s.db.ProjectAccess(ctx, projectID, userID)
}

// ProjectsOf is every project userID owns or is a member of.
func (s *Service) ProjectsOf(ctx context.Context, userID string) ([]Reach, error) {
	return s.db.ProjectsOf(ctx, userID)
}

// ListVisibleProjects is the projects a list may show: userID's own ("":
// none of them by owner) and those in projectIDs, oldest first.
func (s *Service) ListVisibleProjects(ctx context.Context, userID string, projectIDs []string) ([]Project, error) {
	return s.db.ListVisibleProjects(ctx, userID, projectIDs)
}
