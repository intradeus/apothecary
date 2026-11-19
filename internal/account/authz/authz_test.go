package authz

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/cedar-policy/cedar-go"
	xast "github.com/cedar-policy/cedar-go/x/exp/ast"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
	"github.com/cedar-policy/cedar-go/x/exp/schema/validate"

	"apothecary/internal/account/auth"
)

// memFacts is the stores, in memory: project → owner, and project → user →
// role.
type memFacts struct {
	owners  map[string]string
	members map[string]map[string]ProjectRole
	err     error
}

func newFacts() *memFacts {
	return &memFacts{owners: map[string]string{}, members: map[string]map[string]ProjectRole{}}
}

func (f *memFacts) project(id, owner string) *memFacts {
	f.owners[id] = owner
	return f
}

func (f *memFacts) share(project, user string, r ProjectRole) *memFacts {
	if f.members[project] == nil {
		f.members[project] = map[string]ProjectRole{}
	}
	f.members[project][user] = r
	return f
}

func (f *memFacts) Workshop(_ context.Context, projectID, userID string) (string, ProjectRole, error) {
	if f.err != nil {
		return "", "", f.err
	}
	owner, ok := f.owners[projectID]
	if !ok {
		return "", "", nil
	}
	if owner == userID {
		return owner, RoleOwner, nil
	}
	return owner, f.members[projectID][userID], nil
}

func (f *memFacts) Workshops(_ context.Context, userID string) ([]Membership, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []Membership
	for p, owner := range f.owners {
		switch {
		case owner == userID:
			out = append(out, Membership{ProjectID: p, Owner: owner, Role: RoleOwner})
		case f.members[p][userID] != "":
			out = append(out, Membership{ProjectID: p, Owner: owner, Role: f.members[p][userID]})
		}
	}
	return out, nil
}

func newAuthorizer(t *testing.T, f Facts) *Authorizer {
	t.Helper()
	a, err := New(f)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func person(id string, r auth.Role) auth.Principal {
	return auth.Principal{User: auth.User{ID: id, Role: r}}
}

func withToken(p auth.Principal, actions, projects []string) auth.Principal {
	p.Token = &auth.Token{ID: "tok", UserID: p.User.ID, Actions: actions, ProjectIDs: projects}
	return p
}

func decide(t *testing.T, a *Authorizer, p auth.Principal, act Action, res *Resource) Outcome {
	t.Helper()
	d, err := a.Decide(context.Background(), p, act, res)
	if err != nil {
		t.Fatalf("%s: %v", act, err)
	}
	if d.Outcome == Refuse && d.Action != act {
		t.Errorf("%s refused naming %s", act, d.Action)
	}
	return d.Outcome
}

const (
	A = Allow
	H = Hide
	R = Refuse
)

// relations are a principal's place in the workshop a resource lives in.
var relations = []string{"owner", "maintainer", "operator", "viewer", "none"}

// workshopMatrix is the spec of a shared workshop, one column per relation
// above: what each holds on the workshop's own resources, for every
// instance role alike — the instance role adds nothing inside a workshop,
// but takes ownership away: a guest owns nothing (ownerAsGuest).
var workshopMatrix = map[Action][5]Outcome{
	ProjectRead:    {A, A, A, A, H},
	ProjectWrite:   {A, A, R, R, H},
	ProjectMembers: {A, A, R, R, H},
	// Leaving is a member's: the owner deletes its workshop instead.
	ProjectLeave:  {R, A, A, A, H},
	ProjectDelete: {A, R, R, R, H},
	RunRead:       {A, A, A, A, H},
	RunOperate:    {A, A, A, R, H},
	RunShell:      {A, A, R, R, H},
	TicketRead:    {A, A, A, A, H},
	TicketWrite:   {A, A, A, R, H},
}

// instanceMatrix is the spec of the routes with no resource in their path:
// admin, member, guest.
var instanceMatrix = map[Action][3]Outcome{
	InstanceAdmin:    {A, R, R},
	AccountRead:      {A, A, A},
	AccountManage:    {A, A, A},
	ProjectCreate:    {A, A, R},
	ProjectRead:      {A, A, A},
	ProjectWrite:     {A, A, A},
	ProjectMembers:   {A, A, A},
	ProjectLeave:     {R, R, R},
	ProjectDelete:    {R, R, R},
	RunRead:          {A, A, A},
	RunOperate:       {A, A, A},
	RunShell:         {A, A, A},
	TicketRead:       {A, A, A},
	TicketWrite:      {A, A, A},
	IntegrationRead:  {A, A, A},
	IntegrationWrite: {A, A, R},
	CredentialRead:   {A, A, A},
	CredentialWrite:  {A, A, R},
	ChannelRead:      {A, A, A},
	ChannelWrite:     {A, A, R},
	SkillRead:        {A, A, A},
	SkillWrite:       {A, A, R},
	AnalyticsRead:    {A, A, A},
	AnalyticsWrite:   {A, A, A},
	NoticeRead:       {A, A, A},
	NoticeWrite:      {A, A, A},
	SettingsRead:     {A, A, A},
	SettingsWrite:    {A, A, A},
	// The assistant is a person's own, guests' too: it reads what they may.
	AssistantRead:  {A, A, A},
	AssistantWrite: {A, A, A},
	AssistantFill:  {A, A, A},
}

// ownerAsGuest is the owner column for an account demoted to guest: its
// workshop is not there for it.
const ownerAsGuest = H

// personalKinds are never shared: their owner holds them, nobody else
// knows they are there.
var personalKinds = []Kind{KindIntegration, KindCredential, KindChannel, KindSkill, KindAnalytics, KindNotice, KindSettings, KindAssistant}

// frozenForGuests are the personal kinds whose changes are a member's: a
// guest still sees its own, as it still sees its notices, settings and
// dashboards and changes them.
var frozenForGuests = []Kind{KindIntegration, KindCredential, KindChannel, KindSkill}

// sharedWorld is one workshop P owned by "owner" and shared with one
// account at each role, plus "none", who is not in it.
func sharedWorld() *memFacts {
	return newFacts().project("P", "owner").
		share("P", "maintainer", RoleMaintainer).
		share("P", "operator", RoleOperator).
		share("P", "viewer", RoleViewer)
}

func TestPolicyMatrix(t *testing.T) {
	a := newAuthorizer(t, sharedWorld())
	if len(instanceMatrix) != len(vocabulary) {
		t.Fatalf("the instance matrix covers %d actions of %d", len(instanceMatrix), len(vocabulary))
	}

	t.Run("instance", func(t *testing.T) {
		for act, want := range instanceMatrix {
			for i, r := range auth.Roles {
				if got := decide(t, a, person("someone", r), act, nil); got != want[i] {
					t.Errorf("%s %s on the instance = %v, want %v", r, act, got, want[i])
				}
			}
		}
		// No role at all (a row the store could not read) holds no more than a guest.
		for act, want := range instanceMatrix {
			if got := decide(t, a, person("someone", ""), act, nil); want[2] == R && got != R {
				t.Errorf("no role %s = %v", act, got)
			}
		}
	})

	t.Run("workshop", func(t *testing.T) {
		for _, kind := range []Kind{KindProject, KindRun, KindTicket} {
			res := &Resource{Kind: kind, ID: "x", Owner: "owner", ProjectID: "P"}
			for act, want := range workshopMatrix {
				if kind != KindProject && act.Kind() != kind {
					continue
				}
				for i, rel := range relations {
					for _, r := range auth.Roles {
						got := decide(t, a, person(rel, r), act, res)
						// What hides the resource is reading its own kind.
						exp := want[i]
						if read := workshopMatrix[kind.Read()][i]; read == H {
							exp = H
						}
						if rel == "owner" && r == auth.RoleGuest {
							exp = ownerAsGuest
						}
						if got != exp {
							t.Errorf("%s (%s) %s on a %s = %v, want %v", rel, r, act, kind, got, exp)
						}
					}
				}
			}
		}
	})

	t.Run("a workshop's owner holds what others made in it", func(t *testing.T) {
		upload := &Resource{Kind: KindRun, ID: "att", Owner: "operator", ProjectID: "P"}
		for _, rel := range []string{"owner", "operator"} {
			if got := decide(t, a, person(rel, auth.RoleMember), RunRead, upload); got != A {
				t.Errorf("%s reading an operator's upload = %v", rel, got)
			}
		}
		if got := decide(t, a, person("none", auth.RoleMember), RunRead, upload); got != H {
			t.Errorf("a stranger reading it = %v", got)
		}
	})

	t.Run("personal configuration is never shared", func(t *testing.T) {
		for _, kind := range personalKinds {
			res := &Resource{Kind: kind, ID: "x", Owner: "owner"}
			for _, act := range []Action{kind.Read(), Action(string(kind) + ":write")} {
				for _, rel := range relations {
					for _, r := range auth.Roles {
						want := H
						switch {
						case rel != "owner":
						case r == auth.RoleGuest && act != kind.Read() && slices.Contains(frozenForGuests, kind):
							want = R
						default:
							want = A
						}
						if got := decide(t, a, person(rel, r), act, res); got != want {
							t.Errorf("%s (%s) %s on %s = %v, want %v", rel, r, act, kind, got, want)
						}
					}
				}
			}
		}
	})

	t.Run("a workshop that is gone is its owner's resource only", func(t *testing.T) {
		res := &Resource{Kind: KindRun, ID: "x", Owner: "owner", ProjectID: "gone"}
		if got := decide(t, a, person("owner", auth.RoleMember), RunOperate, res); got != A {
			t.Errorf("owner = %v", got)
		}
		if got := decide(t, a, person("maintainer", auth.RoleMember), RunRead, res); got != H {
			t.Errorf("maintainer elsewhere = %v", got)
		}
	})
}

// An admin manages accounts. It grants no view into anyone's work.
func TestAdminSeesNoOneElsesWork(t *testing.T) {
	a := newAuthorizer(t, sharedWorld())
	root := person("root", auth.RoleAdmin)
	for _, kind := range []Kind{KindProject, KindRun, KindTicket} {
		res := &Resource{Kind: kind, ID: "x", Owner: "owner", ProjectID: "P"}
		if got := decide(t, a, root, kind.Read(), res); got != H {
			t.Errorf("an admin reading another's %s = %v", kind, got)
		}
	}
	for _, kind := range personalKinds {
		if got := decide(t, a, root, kind.Read(), &Resource{Kind: kind, ID: "x", Owner: "owner"}); got != H {
			t.Errorf("an admin reading another's %s = %v", kind, got)
		}
	}
	v, err := a.Visibility(context.Background(), root, KindRun)
	if err != nil || len(v.ProjectIDs) != 0 {
		t.Errorf("an admin's run visibility = %+v, %v", v, err)
	}
}

func TestTokenIsAnIntersection(t *testing.T) {
	f := sharedWorld().project("Q", "viewer")
	a := newAuthorizer(t, f)
	inP := &Resource{Kind: KindRun, ID: "r1", Owner: "owner", ProjectID: "P"}
	inQ := &Resource{Kind: KindRun, ID: "r2", Owner: "viewer", ProjectID: "Q"}

	t.Run("never more than its account", func(t *testing.T) {
		tok := withToken(person("viewer", auth.RoleMember), []string{"run:read", "run:operate"}, nil)
		if got := decide(t, a, tok, RunOperate, inP); got != R {
			t.Errorf("a viewer's token aborting = %v", got)
		}
		if got := decide(t, a, tok, RunRead, inP); got != A {
			t.Errorf("a viewer's token reading = %v", got)
		}
		admin := withToken(person("viewer", auth.RoleMember), []string{"instance:admin"}, nil)
		if got := decide(t, a, admin, InstanceAdmin, nil); got != R {
			t.Errorf("a member's token holding instance:admin = %v", got)
		}
	})

	t.Run("only what it was given", func(t *testing.T) {
		tok := withToken(person("viewer", auth.RoleMember), []string{"run:read"}, nil)
		if got := decide(t, a, tok, RunOperate, inQ); got != R {
			t.Errorf("operating its own run without run:operate = %v", got)
		}
		if got := decide(t, a, tok, ProjectRead, &Resource{Kind: KindProject, ID: "Q", Owner: "viewer", ProjectID: "Q"}); got != R {
			t.Errorf("reading its workshop without project:read = %v", got)
		}
		if got := decide(t, a, tok, IntegrationRead, nil); got != R {
			t.Errorf("listing integrations = %v", got)
		}
		// Not holding the read does not hide what the account can see.
		op := withToken(person("viewer", auth.RoleMember), []string{"run:operate"}, nil)
		if got := decide(t, a, op, RunRead, inQ); got != R {
			t.Errorf("reading without run:read = %v, want a 403", got)
		}
		if got := decide(t, a, op, RunOperate, inQ); got != A {
			t.Errorf("operating with run:operate = %v", got)
		}
	})

	t.Run("outside its workshops is not there", func(t *testing.T) {
		tok := withToken(person("viewer", auth.RoleMember), []string{"run:read", "run:operate"}, []string{"P"})
		if got := decide(t, a, tok, RunRead, inQ); got != H {
			t.Errorf("its own run outside the token's workshops = %v", got)
		}
		if got := decide(t, a, tok, RunRead, inP); got != A {
			t.Errorf("inside = %v", got)
		}
		// A run in no workshop (from before workshops) is outside every limit.
		if got := decide(t, a, tok, RunRead, &Resource{Kind: KindRun, ID: "old", Owner: "viewer"}); got != H {
			t.Errorf("a run in no workshop = %v", got)
		}
		if got := decide(t, a, withToken(person("viewer", auth.RoleMember), []string{"run:read"}, nil), RunRead, &Resource{Kind: KindRun, ID: "old", Owner: "viewer"}); got != A {
			t.Errorf("a run in no workshop, no limit = %v", got)
		}
		// Personal configuration is not in a workshop: the limit leaves it be.
		cfg := withToken(person("viewer", auth.RoleMember), []string{"integration:read"}, []string{"P"})
		if got := decide(t, a, cfg, IntegrationRead, &Resource{Kind: KindIntegration, ID: "i", Owner: "viewer"}); got != A {
			t.Errorf("its integration = %v", got)
		}
	})

	t.Run("never account:manage, never project:leave", func(t *testing.T) {
		// Past the mint's validation, the policies still refuse them.
		tok := withToken(person("viewer", auth.RoleAdmin), []string{"account:manage", "account:read", "project:leave", "project:read"}, nil)
		if got := decide(t, a, tok, AccountManage, nil); got != R {
			t.Errorf("a scoped token managing its account = %v", got)
		}
		if got := decide(t, a, tok, AccountRead, nil); got != A {
			t.Errorf("reading it = %v", got)
		}
		shared := &Resource{Kind: KindProject, ID: "P", Owner: "owner", ProjectID: "P"}
		if got := decide(t, a, tok, ProjectLeave, shared); got != R {
			t.Errorf("a scoped token leaving a workshop = %v", got)
		}
		if got := decide(t, a, tok, ProjectRead, shared); got != A {
			t.Errorf("seeing it = %v", got)
		}
		for _, act := range []string{"account:manage", "project:leave"} {
			if err := ValidateGrant([]string{act}); err == nil {
				t.Errorf("%s was grantable", act)
			}
		}
	})

	t.Run("a full token is the account", func(t *testing.T) {
		resources := []*Resource{nil, inP, inQ, {Kind: KindProject, ID: "P", Owner: "owner", ProjectID: "P"}, {Kind: KindNotice, ID: "n", Owner: "viewer"}}
		for _, rel := range relations {
			for _, r := range auth.Roles {
				bare := person(rel, r)
				full := withToken(bare, nil, nil)
				for _, res := range resources {
					for _, s := range vocabulary {
						if got, want := decide(t, a, full, s.Action, res), decide(t, a, bare, s.Action, res); got != want {
							t.Errorf("%s (%s) %s on %+v: full token %v, account %v", rel, r, s.Action, res, got, want)
						}
					}
				}
			}
		}
	})
}

func TestVisibility(t *testing.T) {
	f := sharedWorld().project("Q", "viewer").project("S", "stranger")
	a := newAuthorizer(t, f)
	ctx := context.Background()

	v, err := a.Visibility(ctx, person("viewer", auth.RoleMember), KindRun)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Visibility{UserID: "viewer", ProjectIDs: []string{"P", "Q"}}); !reflect.DeepEqual(v, want) {
		t.Errorf("viewer = %+v, want %+v", v, want)
	}
	if !v.Sees("viewer", "") || !v.Sees("owner", "P") || v.Sees("stranger", "S") || v.Sees("", "") {
		t.Errorf("Sees disagrees with %+v", v)
	}

	// Demoted to guest, the workshop it made is gone from its lists, and so
	// are its own rows: it keeps only what is shared with it.
	v, err = a.Visibility(ctx, person("viewer", auth.RoleGuest), KindRun)
	if want := (Visibility{ProjectIDs: []string{"P"}}); err != nil || !reflect.DeepEqual(v, want) {
		t.Errorf("viewer demoted to guest = %+v, %v; want %+v", v, err, want)
	}
	if v.Sees("viewer", "") || v.Sees("viewer", "Q") || !v.Sees("owner", "P") {
		t.Errorf("Sees disagrees with %+v", v)
	}

	tok := withToken(person("viewer", auth.RoleMember), []string{"run:read"}, []string{"P", "S"})
	v, err = a.Visibility(ctx, tok, KindRun)
	if err != nil || v.UserID != "" || !reflect.DeepEqual(v.ProjectIDs, []string{"P"}) {
		t.Errorf("limited token = %+v, %v", v, err)
	}

	none := withToken(person("viewer", auth.RoleMember), []string{"run:read"}, []string{"S"})
	v, err = a.Visibility(ctx, none, KindRun)
	if err != nil || v.UserID != "" || v.ProjectIDs == nil || len(v.ProjectIDs) != 0 {
		t.Errorf("empty intersection = %#v, %v", v, err)
	}
	if v.Sees("viewer", "Q") || v.Sees("", "") || v.Sees("stranger", "S") {
		t.Error("the empty intersection sees something")
	}

	f.err = errors.New("down")
	if _, err := a.Visibility(ctx, person("viewer", auth.RoleMember), KindRun); err == nil {
		t.Error("a facts error was swallowed")
	}
	if _, err := a.Decide(ctx, person("viewer", auth.RoleMember), RunRead, &Resource{Kind: KindRun, ID: "r", ProjectID: "P"}); err == nil {
		t.Error("a facts error was swallowed by Decide")
	}
	if _, _, err := a.Access(ctx, person("viewer", auth.RoleMember), "P"); err == nil {
		t.Error("a facts error was swallowed by Access")
	}
}

func TestAccess(t *testing.T) {
	a := newAuthorizer(t, sharedWorld())
	ctx := context.Background()
	for _, tc := range []struct {
		who  string
		role ProjectRole
		want []Action
	}{
		{"owner", RoleOwner, []Action{ProjectRead, ProjectWrite, ProjectMembers, ProjectDelete, RunRead, RunOperate, RunShell, TicketRead, TicketWrite}},
		{"maintainer", RoleMaintainer, []Action{ProjectRead, ProjectWrite, ProjectMembers, ProjectLeave, RunRead, RunOperate, RunShell, TicketRead, TicketWrite}},
		{"operator", RoleOperator, []Action{ProjectRead, ProjectLeave, RunRead, RunOperate, TicketRead, TicketWrite}},
		{"viewer", RoleViewer, []Action{ProjectRead, ProjectLeave, RunRead, TicketRead}},
		{"none", "", nil},
	} {
		role, acts, err := a.Access(ctx, person(tc.who, auth.RoleMember), "P")
		if err != nil || role != tc.role || !reflect.DeepEqual(acts, tc.want) {
			t.Errorf("%s = %s %v, %v; want %s %v", tc.who, role, acts, err, tc.role, tc.want)
		}
	}
	tok := withToken(person("maintainer", auth.RoleMember), []string{"run:read", "run:shell"}, nil)
	if _, acts, _ := a.Access(ctx, tok, "P"); !reflect.DeepEqual(acts, []Action{RunRead, RunShell}) {
		t.Errorf("a token's access = %v", acts)
	}
	if _, acts, err := a.Access(ctx, person("owner", auth.RoleGuest), "P"); acts != nil || err != nil {
		t.Errorf("an owner demoted to guest holds %v, %v", acts, err)
	}
	if role, acts, err := a.Access(ctx, person("owner", auth.RoleMember), "gone"); role != "" || acts != nil || err != nil {
		t.Errorf("a missing workshop = %s %v %v", role, acts, err)
	}
}

func TestDecideRefusesAnUnknownAction(t *testing.T) {
	a := newAuthorizer(t, sharedWorld())
	d, err := a.Decide(context.Background(), person("owner", auth.RoleAdmin), "run:fly", nil)
	if err == nil || d.Allowed() {
		t.Errorf("unknown action = %+v, %v", d, err)
	}
	if _, err := New(nil); err == nil {
		t.Error("New without facts")
	}
}

func TestRoles(t *testing.T) {
	ranks := []int{}
	for _, r := range []ProjectRole{"", RoleViewer, RoleOperator, RoleMaintainer, RoleOwner} {
		ranks = append(ranks, r.Rank())
	}
	if !slices.IsSorted(ranks) || slices.Compact(slices.Clone(ranks))[4] != 4 {
		t.Errorf("ranks = %v", ranks)
	}
	if RoleOwner.ValidMember() || !RoleOperator.ValidMember() || ProjectRole("").ValidMember() {
		t.Error("ValidMember")
	}
}

func TestValidateGrant(t *testing.T) {
	for _, tc := range []struct {
		in []string
		ok bool
	}{
		{[]string{"run:read", "instance:admin"}, true},
		{nil, false},
		{[]string{}, false},
		{[]string{"run:read", "run:read"}, false},
		{[]string{"run:fly"}, false},
		{[]string{"account:manage"}, false},
	} {
		if err := ValidateGrant(tc.in); (err == nil) != tc.ok {
			t.Errorf("ValidateGrant(%v) = %v", tc.in, err)
		}
	}
	if s, ok := Lookup(RunShell); !ok || s.Kind != KindRun || !s.Grantable {
		t.Errorf("Lookup = %+v %v", s, ok)
	}
	if got := Actions(); len(got) != len(vocabulary) || &got[0] == &vocabulary[0] {
		t.Error("Actions is not a copy of the vocabulary")
	}
}

// The vocabulary is closed and agrees with itself: every action's kind is
// a kind, every kind a resource can be has a read, and the schema declares
// the actions and the groups actions.go puts them in. The policies parse
// and type-check against the schema, and so does every entity and request
// the authorizer builds.
func TestVocabularyIsClosed(t *testing.T) {
	kinds := []Kind{KindInstance, KindAccount, KindProject, KindRun, KindTicket, KindIntegration, KindCredential,
		KindChannel, KindSkill, KindAnalytics, KindNotice, KindSettings, KindAssistant}
	for _, s := range vocabulary {
		if !slices.Contains(kinds, s.Kind) || s.Doc == "" {
			t.Errorf("%s: kind %q, doc %q", s.Action, s.Kind, s.Doc)
		}
	}
	for _, k := range kinds[2:] {
		if _, ok := Lookup(k.Read()); !ok {
			t.Errorf("kind %s has no read", k)
		}
	}

	var sch schema.Schema
	if err := sch.UnmarshalCedar(schemaText); err != nil {
		t.Fatal(err)
	}
	resolved, err := sch.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	static := staticEntities()
	for uid, act := range resolved.Actions {
		e, ok := static[uid]
		if !ok {
			t.Errorf("the schema declares %s, the vocabulary does not", uid)
			continue
		}
		if !e.Parents.Equal(act.Entity.Parents) {
			t.Errorf("%s: groups %v in actions.go, %v in the schema", uid, slices.Collect(e.Parents.All()), slices.Collect(act.Entity.Parents.All()))
		}
	}
	for uid := range static {
		if _, ok := resolved.Actions[uid]; uid.Type == typeAction && !ok {
			t.Errorf("the vocabulary has %s, the schema does not", uid)
		}
	}

	v := validate.New(resolved)
	ps, err := cedar.NewPolicySetFromBytes("policies.cedar", policyText)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for id, p := range ps.All() {
		n++
		if err := v.Policy(string(id), (*xast.Policy)(p.AST())); err != nil {
			t.Errorf("policy %s: %v", id, err)
		}
	}
	if n < 10 {
		t.Errorf("only %d policies", n)
	}

	w := &workshop{id: "P", owner: "owner", role: RoleOperator}
	tok := withToken(person("operator", auth.RoleGuest), []string{"run:read"}, []string{"P"})
	for i, tc := range []struct {
		p   auth.Principal
		res *Resource
		w   *workshop
	}{
		{person("root", auth.RoleAdmin), nil, nil},
		{tok, &Resource{Kind: KindRun, ID: "r", Owner: "owner", ProjectID: "P"}, w},
		{person("x", auth.RoleMember), &Resource{Kind: KindSkill, ID: "s", Owner: "x"}, nil},
	} {
		own, req := request(tc.p, RunRead, tc.res, tc.w, grantOf(tc.p))
		all := cedar.EntityMap{}
		for k, e := range static {
			all[k] = e
		}
		for k, e := range own {
			all[k] = e
		}
		if err := v.Entities(all); err != nil {
			t.Errorf("case %d entities: %v", i, err)
		}
		if err := v.Request(req); err != nil {
			t.Errorf("case %d request: %v", i, err)
		}
	}
}

func Example() {
	a, _ := New(sharedWorld())
	d, _ := a.Decide(context.Background(), person("operator", auth.RoleMember), RunShell,
		&Resource{Kind: KindRun, ID: "r1", Owner: "owner", ProjectID: "P"})
	fmt.Println(d.Outcome == Refuse, d.Action)
	// Output: true run:shell
}
