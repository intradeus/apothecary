// Package authz decides what a request may do: one Cedar policy set
// (policies.cedar) over entities built per request from the stores, so the
// rules are reviewable text rather than checks spread over handlers.
//
// The model: an account has an instance role (admin, member, guest); a
// workshop (project) is its owner's and may be shared with other accounts
// as viewer, operator or maintainer; a personal token is the account
// itself, or a scoped token that holds only some actions and, optionally,
// only some workshops — evaluated as the intersection with what the account
// may do now.
//
// Point decisions are Decide's (the HTTP guard, the SSH door). Lists stay
// SQL: Visibility is the set of workshops a principal may read a kind in,
// which every list query filters on.
package authz

import (
	"context"
	_ "embed"
	"fmt"
	"slices"

	"github.com/cedar-policy/cedar-go"

	"apothecary/internal/account/auth"
)

//go:embed policies.cedar
var policyText []byte

//go:embed schema.cedarschema
var schemaText []byte

// ProjectRole is an account's role on a workshop. The owner is implicit
// (projects.user_id) and above the three a member can hold.
type ProjectRole string

const (
	RoleViewer     ProjectRole = "viewer"
	RoleOperator   ProjectRole = "operator"
	RoleMaintainer ProjectRole = "maintainer"
	RoleOwner      ProjectRole = "owner"
)

// MemberRoles are the roles a member can hold, lowest first.
var MemberRoles = []ProjectRole{RoleViewer, RoleOperator, RoleMaintainer}

// Rank orders roles: 0 for none, then viewer < operator < maintainer < owner.
func (r ProjectRole) Rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleMaintainer:
		return 3
	case RoleOwner:
		return 4
	}
	return 0
}

// ValidMember reports a role a member may hold.
func (r ProjectRole) ValidMember() bool { return slices.Contains(MemberRoles, r) }

// Resource is what a request acts on: nil for the routes with no resource in
// their path (lists, creates, one's own account). Owner is whose it is;
// ProjectID the workshop it lives in, empty for personal configuration.
type Resource struct {
	Kind      Kind
	ID        string
	Owner     string
	ProjectID string
}

// Membership is an account's place in a workshop.
type Membership struct {
	ProjectID string
	Owner     string
	Role      ProjectRole
}

// Facts is what decisions read from the stores (confstore, through the
// daemon's wiring).
type Facts interface {
	// Workshop is a project's owner and userID's role on it: RoleOwner for
	// the owner, "" for an account that is not a member. Owner "" means the
	// project does not exist.
	Workshop(ctx context.Context, projectID, userID string) (owner string, role ProjectRole, err error)
	// Workshops is every project userID owns or is a member of.
	Workshops(ctx context.Context, userID string) ([]Membership, error)
}

// Outcome is a decision's answer.
type Outcome int

const (
	// Allow: go ahead.
	Allow Outcome = iota
	// Hide: the principal may not even know the resource is there (404).
	Hide
	// Refuse: it may see the resource (or the route) but not do this (403).
	Refuse
)

// Decision is an outcome and, for Refuse, the action that was missing.
type Decision struct {
	Outcome Outcome
	Action  Action
}

// Allowed reports an Allow.
func (d Decision) Allowed() bool { return d.Outcome == Allow }

// Authorizer decides requests against the embedded policies.
type Authorizer struct {
	policies *cedar.PolicySet
	facts    Facts
	static   cedar.EntityMap
}

// New parses the embedded policies; a parse error is a build defect and
// fails loudly here rather than as a refusal on every request.
func New(facts Facts) (*Authorizer, error) {
	ps, err := cedar.NewPolicySetFromBytes("policies.cedar", policyText)
	if err != nil {
		return nil, fmt.Errorf("authz: policies: %w", err)
	}
	if facts == nil {
		return nil, fmt.Errorf("authz: no facts")
	}
	return &Authorizer{policies: ps, facts: facts, static: staticEntities()}, nil
}

// Decide is one request: may p do a on res (nil: a route with no resource)?
// A resource the principal may not read is Hide whatever the action; one it
// may read but not act on is Refuse.
func (a *Authorizer) Decide(ctx context.Context, p auth.Principal, act Action, res *Resource) (Decision, error) {
	if _, ok := byAction[act]; !ok {
		return Decision{Outcome: Refuse, Action: act}, fmt.Errorf("authz: unknown action %q", act)
	}
	if res == nil {
		if a.allows(p, act, nil, nil, grantOf(p)) {
			return Decision{Outcome: Allow}, nil
		}
		return Decision{Outcome: Refuse, Action: act}, nil
	}
	var w *workshop
	if res.ProjectID != "" {
		owner, role, err := a.facts.Workshop(ctx, res.ProjectID, p.User.ID)
		if err != nil {
			return Decision{Outcome: Hide}, err
		}
		if owner != "" {
			w = &workshop{id: res.ProjectID, owner: owner, role: role}
		}
	}
	g := grantOf(p)
	// Whether it is there to be seen is the account's reach and the
	// token's workshops, never the token's actions: a token that may abort
	// but not read learns the run exists from the 403, as its account would.
	seen := g
	seen.scoped, seen.actions = false, nil
	if !a.allows(p, res.Kind.Read(), res, w, seen) {
		return Decision{Outcome: Hide}, nil
	}
	if !a.allows(p, act, res, w, g) {
		return Decision{Outcome: Refuse, Action: act}, nil
	}
	return Decision{Outcome: Allow}, nil
}

// Visibility is what a principal's lists of a kind may hold: its own rows
// (UserID; empty when a token limits it to workshops, or for a guest, which
// owns nothing) and the rows of the workshops it may read the kind in.
// Empty both: nothing — never everything.
type Visibility struct {
	UserID     string
	ProjectIDs []string
}

// Sees reports a row the lists may hold.
func (v Visibility) Sees(userID, projectID string) bool {
	return (v.UserID != "" && userID == v.UserID) || (projectID != "" && slices.Contains(v.ProjectIDs, projectID))
}

// Visibility computes a principal's lists of kind through the same policies
// as Decide, one workshop at a time: what a list holds is exactly what the
// guard would let the principal read.
func (a *Authorizer) Visibility(ctx context.Context, p auth.Principal, kind Kind) (Visibility, error) {
	g := grantOf(p)
	g.scoped, g.actions = false, nil
	v := Visibility{}
	// Its own rows, as the guard would read one of them in no workshop.
	if a.allows(p, kind.Read(), &Resource{Kind: kind, ID: "*", Owner: p.User.ID}, nil, g) {
		v.UserID = p.User.ID
	}
	ms, err := a.facts.Workshops(ctx, p.User.ID)
	if err != nil {
		return Visibility{}, err
	}
	v.ProjectIDs = []string{}
	for _, m := range ms {
		res := &Resource{Kind: kind, ID: "*", Owner: m.Owner, ProjectID: m.ProjectID}
		if a.allows(p, kind.Read(), res, &workshop{id: m.ProjectID, owner: m.Owner, role: m.Role}, g) {
			v.ProjectIDs = append(v.ProjectIDs, m.ProjectID)
		}
	}
	slices.Sort(v.ProjectIDs)
	return v, nil
}

// Access is what a principal may do on one workshop: its role there and
// the actions it holds on the workshop's resources (the UI disables what is
// not in the list).
func (a *Authorizer) Access(ctx context.Context, p auth.Principal, projectID string) (ProjectRole, []Action, error) {
	owner, role, err := a.facts.Workshop(ctx, projectID, p.User.ID)
	if err != nil || owner == "" {
		return "", nil, err
	}
	w := &workshop{id: projectID, owner: owner, role: role}
	g := grantOf(p)
	var out []Action
	for _, s := range vocabulary {
		switch s.Kind {
		case KindProject, KindRun, KindTicket:
		default:
			continue
		}
		res := &Resource{Kind: s.Kind, ID: "*", Owner: owner, ProjectID: projectID}
		if a.allows(p, s.Action, res, w, g) {
			out = append(out, s.Action)
		}
	}
	return role, out, nil
}

func (a *Authorizer) allows(p auth.Principal, act Action, res *Resource, w *workshop, g grant) bool {
	own, req := request(p, act, res, w, g)
	d, _ := cedar.Authorize(a.policies, layered{static: a.static, req: own}, req)
	return d == cedar.Allow
}
