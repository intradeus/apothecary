package authz

import (
	"fmt"
	"slices"
	"strings"
)

// Action is one thing a request does, "kind:verb". Every user route
// declares exactly one; the list below is the whole vocabulary.
type Action string

// Kind is the sort of resource an action acts on.
type Kind string

const (
	KindInstance    Kind = "instance"
	KindAccount     Kind = "account"
	KindProject     Kind = "project"
	KindRun         Kind = "run"
	KindTicket      Kind = "ticket"
	KindIntegration Kind = "integration"
	KindCredential  Kind = "credential"
	KindChannel     Kind = "channel"
	KindSkill       Kind = "skill"
	KindAnalytics   Kind = "analytics"
	KindNotice      Kind = "notice"
	KindSettings    Kind = "settings"
	KindAssistant   Kind = "assistant"
)

const (
	InstanceAdmin Action = "instance:admin"

	AccountRead   Action = "account:read"
	AccountManage Action = "account:manage"

	ProjectCreate  Action = "project:create"
	ProjectRead    Action = "project:read"
	ProjectWrite   Action = "project:write"
	ProjectMembers Action = "project:members"
	ProjectLeave   Action = "project:leave"
	ProjectDelete  Action = "project:delete"

	RunRead    Action = "run:read"
	RunOperate Action = "run:operate"
	RunShell   Action = "run:shell"

	TicketRead  Action = "ticket:read"
	TicketWrite Action = "ticket:write"

	IntegrationRead  Action = "integration:read"
	IntegrationWrite Action = "integration:write"
	CredentialRead   Action = "credential:read"
	CredentialWrite  Action = "credential:write"
	ChannelRead      Action = "channel:read"
	ChannelWrite     Action = "channel:write"
	SkillRead        Action = "skill:read"
	SkillWrite       Action = "skill:write"
	AnalyticsRead    Action = "analytics:read"
	AnalyticsWrite   Action = "analytics:write"
	NoticeRead       Action = "notice:read"
	NoticeWrite      Action = "notice:write"
	SettingsRead     Action = "settings:read"
	SettingsWrite    Action = "settings:write"

	AssistantRead  Action = "assistant:read"
	AssistantWrite Action = "assistant:write"
	AssistantFill  Action = "assistant:fill"
)

// The action groups policies.cedar grants (an action entity's parents).
const (
	groupAdmin      = "admin"      // the instance admin's
	groupBrowse     = "browse"     // on a route with no resource: anyone signed in
	groupPersonal   = "personal"   // on a route with no resource: members and admins
	groupOwned      = "owned"      // on a resource: its owner, and its workshop's — a member or an admin
	groupMine       = "mine"       // on one's own personal resource: its owner, whatever its role
	groupViewer     = "viewer"     // on a workshop's resource: a viewer and up
	groupOperator   = "operator"   // ... an operator and up
	groupMaintainer = "maintainer" // ... a maintainer
	groupManageSelf = "manageself" // what a scoped token can never do
)

// Spec is one action of the vocabulary.
type Spec struct {
	Action Action
	Kind   Kind
	// Grantable: a scoped token may hold it. account:manage and
	// project:leave are not: a token never mints a wider one, changes the
	// password, or takes its account off a workshop.
	Grantable bool
	Doc       string

	groups []string
}

const b, p, own, mine = groupBrowse, groupPersonal, groupOwned, groupMine

// vocabulary is every action, in the order the UI lists them.
var vocabulary = []Spec{
	{InstanceAdmin, KindInstance, true, "Administer the instance: accounts, limits, the provisioner", []string{groupAdmin}},

	{AccountRead, KindAccount, true, "Read one's own account and the instance's catalogues", []string{b}},
	{AccountManage, KindAccount, false, "Change one's own profile, password, tokens and SSH keys", []string{b, groupManageSelf}},

	{ProjectCreate, KindProject, true, "Create workshops", []string{p}},
	{ProjectRead, KindProject, true, "See workshops: settings, flows, automations, members", []string{b, own, groupViewer}},
	{ProjectWrite, KindProject, true, "Change a workshop's settings, flows, automations and secrets", []string{b, own, groupMaintainer}},
	{ProjectMembers, KindProject, true, "Share a workshop: add, change and remove members", []string{b, own, groupMaintainer}},
	{ProjectLeave, KindProject, false, "Leave a workshop shared with one", []string{groupViewer, groupManageSelf}},
	{ProjectDelete, KindProject, true, "Delete a workshop", []string{own}},

	{RunRead, KindRun, true, "See runs: logs, checkout files, pings", []string{b, own, groupViewer}},
	{RunOperate, KindRun, true, "Start, abort, retry and archive runs; answer pings; ask and take over", []string{b, own, groupOperator}},
	{RunShell, KindRun, true, "Open a run's terminal, editor, ports, SSH and files outside its checkouts", []string{b, own, groupMaintainer}},

	{TicketRead, KindTicket, true, "See tickets", []string{b, own, groupViewer}},
	{TicketWrite, KindTicket, true, "Create and change tickets", []string{b, own, groupOperator}},

	{IntegrationRead, KindIntegration, true, "See one's integrations", []string{b, mine}},
	{IntegrationWrite, KindIntegration, true, "Create and change one's integrations", []string{p, own}},
	{CredentialRead, KindCredential, true, "See one's model credentials", []string{b, mine}},
	{CredentialWrite, KindCredential, true, "Create and change one's model credentials", []string{p, own}},
	{ChannelRead, KindChannel, true, "See one's notification channels", []string{b, mine}},
	{ChannelWrite, KindChannel, true, "Add and remove one's notification channels", []string{p, own}},
	{SkillRead, KindSkill, true, "See one's skills and skillsets", []string{b, mine}},
	{SkillWrite, KindSkill, true, "Create and change one's skills and skillsets", []string{p, own}},
	{AnalyticsRead, KindAnalytics, true, "Query run analytics; see one's dashboards", []string{b, mine}},
	{AnalyticsWrite, KindAnalytics, true, "Change one's dashboards and scorecards", []string{b, mine}},
	{NoticeRead, KindNotice, true, "See one's notices", []string{b, mine}},
	{NoticeWrite, KindNotice, true, "Dismiss one's notices", []string{b, mine}},
	{SettingsRead, KindSettings, true, "See one's settings", []string{b, mine}},
	{SettingsWrite, KindSettings, true, "Change one's settings", []string{b, mine}},
	{AssistantRead, KindAssistant, true, "See one's assistant: its sessions and its state", []string{b, mine}},
	{AssistantWrite, KindAssistant, true, "Talk to one's assistant: ask, pick its harness and model, refresh its repositories", []string{b, mine}},
	{AssistantFill, KindAssistant, true, "Fill a page from an assistant session (the session's own token only)", []string{b, mine}},
}

var byAction = func() map[Action]Spec {
	m := make(map[Action]Spec, len(vocabulary))
	for _, s := range vocabulary {
		if _, dup := m[s.Action]; dup || !strings.HasPrefix(string(s.Action), string(s.Kind)+":") {
			panic("authz: bad vocabulary entry " + string(s.Action))
		}
		m[s.Action] = s
	}
	return m
}()

// Actions is the vocabulary, in order.
func Actions() []Spec { return slices.Clone(vocabulary) }

// Lookup is one action's spec.
func Lookup(a Action) (Spec, bool) {
	s, ok := byAction[a]
	return s, ok
}

// Kind is the kind an action acts on.
func (a Action) Kind() Kind { return byAction[a].Kind }

// InWorkshops reports a kind that lives in workshops: a workshop, its runs
// (and their pings, snapshots, uploads), its tickets.
func (k Kind) InWorkshops() bool { return k == KindProject || k == KindRun || k == KindTicket }

// Read is the action that reads a kind: what a principal must hold on a
// resource before it may know the resource is there.
func (k Kind) Read() Action { return Action(string(k) + ":read") }

// ValidateGrant checks a scoped token's action list: known, grantable,
// no repeats, not empty.
func ValidateGrant(actions []string) error {
	if len(actions) == 0 {
		return fmt.Errorf("a limited token needs at least one action")
	}
	seen := map[string]bool{}
	for _, a := range actions {
		s, ok := byAction[Action(a)]
		switch {
		case !ok:
			return fmt.Errorf("unknown action %q", a)
		case !s.Grantable:
			return fmt.Errorf("a limited token cannot hold %s", a)
		case seen[a]:
			return fmt.Errorf("action %s is listed twice", a)
		}
		seen[a] = true
	}
	return nil
}
