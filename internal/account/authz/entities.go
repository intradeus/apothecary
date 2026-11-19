package authz

import (
	"github.com/cedar-policy/cedar-go"

	"apothecary/internal/account/auth"
)

// The entity types of schema.cedarschema.
const (
	typeRole     = cedar.EntityType("Role")
	typeGroup    = cedar.EntityType("Group")
	typeUser     = cedar.EntityType("User")
	typeProject  = cedar.EntityType("Project")
	typeInstance = cedar.EntityType("Instance")
	typeResource = cedar.EntityType("Resource")
	typeAction   = cedar.EntityType("Action")
)

// instance is the one Instance entity: what the routes with no resource in
// their path act on.
var instance = cedar.NewEntityUID(typeInstance, "apothecary")

// workshop is the one project a request is about, as the stores say it is:
// its owner and the principal's role there ("" when not a member).
type workshop struct {
	id, owner string
	role      ProjectRole
}

// grant is the request's token as the policies see it.
type grant struct {
	scoped   bool
	actions  []string
	limited  bool
	projects []string
}

// grantOf reads a principal's token: a session or a full token is the
// account itself.
func grantOf(p auth.Principal) grant {
	if p.Token == nil {
		return grant{}
	}
	return grant{
		scoped: p.Token.Actions != nil, actions: p.Token.Actions,
		limited: p.Token.ProjectIDs != nil, projects: p.Token.ProjectIDs,
	}
}

func actionUID(a string) cedar.EntityUID { return cedar.NewEntityUID(typeAction, cedar.String(a)) }
func userUID(id string) cedar.EntityUID  { return cedar.NewEntityUID(typeUser, cedar.String(id)) }
func projectUID(id string) cedar.EntityUID {
	return cedar.NewEntityUID(typeProject, cedar.String(id))
}
func groupUID(project string, r ProjectRole) cedar.EntityUID {
	return cedar.NewEntityUID(typeGroup, cedar.String(project+"/"+string(r)))
}
func roleUID(r auth.Role) cedar.EntityUID { return cedar.NewEntityUID(typeRole, cedar.String(r)) }
func resourceUID(r *Resource) cedar.EntityUID {
	return cedar.NewEntityUID(typeResource, cedar.String(string(r.Kind)+"/"+r.ID))
}

// staticEntities are the actions, the groups they are in and the instance
// roles: the same for every request, built once.
func staticEntities() cedar.EntityMap {
	m := cedar.EntityMap{}
	admin, member, guest := roleUID(auth.RoleAdmin), roleUID(auth.RoleMember), roleUID(auth.RoleGuest)
	m[admin] = cedar.Entity{UID: admin, Parents: cedar.NewEntityUIDSet(member)}
	m[member] = cedar.Entity{UID: member}
	m[guest] = cedar.Entity{UID: guest}
	m[instance] = cedar.Entity{UID: instance}
	for _, g := range []string{groupAdmin, groupBrowse, groupPersonal, groupOwned, groupMine, groupViewer, groupOperator, groupMaintainer, groupManageSelf} {
		m[actionUID(g)] = cedar.Entity{UID: actionUID(g)}
	}
	for _, s := range vocabulary {
		parents := make([]cedar.EntityUID, 0, len(s.groups))
		for _, g := range s.groups {
			parents = append(parents, actionUID(g))
		}
		m[actionUID(string(s.Action))] = cedar.Entity{UID: actionUID(string(s.Action)), Parents: cedar.NewEntityUIDSet(parents...)}
	}
	return m
}

// layered is a request's entities over the static ones, so no request
// copies the action table.
type layered struct{ static, req cedar.EntityMap }

func (l layered) Get(uid cedar.EntityUID) (cedar.Entity, bool) {
	if e, ok := l.req[uid]; ok {
		return e, true
	}
	return l.static.Get(uid)
}

// request builds one question's own entities (the principal, its workshop,
// the resource) and the request.
func request(p auth.Principal, act Action, res *Resource, w *workshop, g grant) (cedar.EntityMap, cedar.Request) {
	m := make(cedar.EntityMap, 7)

	var userParents []cedar.EntityUID
	if p.User.Role.Valid() {
		userParents = append(userParents, roleUID(p.User.Role))
	}
	if w != nil {
		viewers, operators, maintainers := groupUID(w.id, RoleViewer), groupUID(w.id, RoleOperator), groupUID(w.id, RoleMaintainer)
		m[viewers] = cedar.Entity{UID: viewers}
		m[operators] = cedar.Entity{UID: operators, Parents: cedar.NewEntityUIDSet(viewers)}
		m[maintainers] = cedar.Entity{UID: maintainers, Parents: cedar.NewEntityUIDSet(operators)}
		m[projectUID(w.id)] = cedar.Entity{UID: projectUID(w.id), Attributes: cedar.NewRecord(cedar.RecordMap{
			"owner": userUID(w.owner), "viewers": viewers, "operators": operators, "maintainers": maintainers,
		})}
		if w.role.ValidMember() && w.owner != p.User.ID {
			userParents = append(userParents, groupUID(w.id, w.role))
		}
	}
	principal := userUID(p.User.ID)
	m[principal] = cedar.Entity{UID: principal, Parents: cedar.NewEntityUIDSet(userParents...)}

	resource := instance
	if res != nil {
		resource = resourceUID(res)
		attrs := cedar.RecordMap{"owner": userUID(res.Owner), "workshopKind": cedar.Boolean(res.Kind.InWorkshops())}
		if w != nil {
			attrs["project"] = projectUID(w.id)
		}
		m[resource] = cedar.Entity{UID: resource, Attributes: cedar.NewRecord(attrs)}
	}

	actions := make([]cedar.Value, 0, len(g.actions))
	for _, x := range g.actions {
		actions = append(actions, actionUID(x))
	}
	projects := make([]cedar.Value, 0, len(g.projects))
	for _, x := range g.projects {
		projects = append(projects, projectUID(x))
	}
	ctx := cedar.NewRecord(cedar.RecordMap{"token": cedar.NewRecord(cedar.RecordMap{
		"scoped": cedar.Boolean(g.scoped), "actions": cedar.NewSet(actions...),
		"limited": cedar.Boolean(g.limited), "projects": cedar.NewSet(projects...),
	})})
	return m, cedar.Request{Principal: principal, Action: actionUID(string(act)), Resource: resource, Context: ctx}
}
