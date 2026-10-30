// Package ports holds the engine-shaped vendor boundaries (DESIGN.md §4).
// Ports are shaped by what the engine needs, never by a vendor's API; no
// vendor types cross this boundary. The fakes in internal/adapters/fake are
// the test substrate for every §16 scenario.
package ports

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"sort"
	"strings"
	"time"
)

type EnvID string

// ProjectHostKey is the ONE token host-side, per-project resources are named
// by: the egress gateway and browser containers, their volumes and folders
// (<RunsDir>/gw/<key>, <RunsDir>/browser/<key>) and the build-cache registry's
// repository path. It is the project's ID (projects.id, a ULID) lowercased —
// docker names and registry paths want lowercase, and a ULID is uppercase
// Crockford base32 — with anything else mapped to '-' for safety; an empty id
// (direct and test callers with no project) reads as "default". The project's
// NAME keys nothing: two users' "api" projects must never share a gateway, and
// renaming a project must not move it off its caches. The name survives only
// as the apothecary.project.name docker label, for `docker ps`.
func ProjectHostKey(projectID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(projectID) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	return out
}

// EnvSpec carries the §11 provisioning inputs. Everything the adapter may
// expose to the vendor (template parameters, labels, generated configs)
// must be non-secret; GitUser/GitPass are the one engine-held exception,
// provided for adapters that must materialize the source BEFORE the
// environment exists (clone-first models like devcontainer) — they run the
// clone engine-side and must never let the credential reach the vendor
// surface or the workspace (in-workspace git auth is bootstrap's job, §11
// step 3).
type EnvSpec struct {
	RunID   string
	RepoURL string
	Branch  string
	// Name is the human-friendly workspace/container name the engine derives
	// from the branch (wsname.For: "apo-" + slugified branch + a
	// short run-id tail for uniqueness, ≤32 chars). Adapters use it verbatim
	// as the container / pod name and the env identity. Empty ⇒ the adapter falls back to WorkspaceName(RunID),
	// the legacy run-id scheme, so direct and test callers keep working.
	Name string
	// Project is the run's project ID (projects.id): what every host-side
	// per-project resource is keyed by (ProjectHostKey — the egress gateway,
	// the build cache, the browser) and the apothecary.project docker label
	// containers are grouped by (`docker ps --filter
	// label=apothecary.project=<id>`). Empty ⇒ no label, the "default" key.
	Project string
	// ProjectName is the project's human name, stamped as the
	// apothecary.project.name label where a container is created with --label,
	// so `docker ps` reads well. It keys NOTHING: a rename changes the label on
	// the next create and nothing else. Empty ⇒ omitted.
	ProjectName string
	// NoCache builds the workspace image from scratch, ignoring the project's
	// build cache. The cache is keyed by what the build actually reads, so a
	// changed Dockerfile or context file already rebuilds on its own — this is
	// for the staleness a cache key cannot see: a floating base tag that moved,
	// an `apt-get update` layer from three months ago. The fresh layers still
	// REPLACE the cached ones, so one no-cache build fixes the next ten.
	NoCache bool
	// EgressDomains is the fully derived firewall allowlist (deny-all + allowlist
	// mode): repo host ∪ adapter contributions ∪ registry defaults ∪ project
	// extras. Non-secret.
	EgressDomains []string
	// EgressMode selects the gateway posture: "allow" (deny-all + EgressDomains,
	// the default) or "deny" (allow-all + EgressDenylist). Empty ⇒ allow.
	EgressMode string
	// EgressDenylist is the blocked-host list used in "deny" mode.
	EgressDenylist []string
	// EgressBlocked is the force-DENY override (wins in both modes) — the run
	// page's live "block domain" toggle, persisted per project.
	EgressBlocked []string
	// EgressAllowedCIDRs/EgressAllowedPorts are the "allow" mode escape hatch for
	// legitimate raw (non-HTTP/TLS) external egress that the default backstop
	// would otherwise drop (e.g. an external managed database).
	EgressAllowedCIDRs []string
	EgressAllowedPorts []int
	// GitUser/GitPass are the repository host git credential (see type
	// comment). Empty for public repos.
	GitUser string
	GitPass string
}

type Command struct {
	Script string // executed `bash -eo pipefail` by the adapter
	// Dir is the working directory relative to the workspace home
	// ("" = home, "repo" = the checkout). Command stages run in the repo
	// root (§3); bootstrap's clone step runs in home.
	Dir     string
	Timeout time.Duration
	// Marker tags the command's process tree (as APOTHECARY_CMD in its
	// environment) and, before it starts, kills whatever earlier command with
	// the same marker is still running in the environment — TERM, then KILL.
	// Command stages set it per stage: an attempt's exec client dies with its
	// timeout or with the engine process, but the tree inside the container
	// does not, so without this a retry or a recovery re-entry would race
	// the orphan. Empty: no tagging, no sweep.
	Marker string
}

type ExecResult struct {
	ExitCode int
	Output   string // combined stdout+stderr; adapters also stream to the stage log
}

type EnvStatus string

const (
	EnvStarting EnvStatus = "starting"
	EnvRunning  EnvStatus = "running"
	EnvStopped  EnvStatus = "stopped"
	EnvAbsent   EnvStatus = "absent"
	// EnvUnknown is NOT returned by any adapter's Status — a provisioner
	// either knows a workspace's state or errors. The engine synthesizes it
	// when a status probe fails (provisioner unreachable, expired session,
	// deleted project) so the UI can show "can't tell" instead of a stale
	// last-known value dressed up as live.
	EnvUnknown EnvStatus = "unknown"
)

type EnvProvisioner interface {
	// Provision blocks until the environment is ready; 1 internal retry.
	Provision(ctx context.Context, spec EnvSpec) (EnvID, error)
	Exec(ctx context.Context, env EnvID, cmd Command) (ExecResult, error)
	// ReadFile/WriteFile paths: relative = repo-root-relative
	// (artifacts/plan.md), absolute = absolute (/apothecary/run.env).
	ReadFile(ctx context.Context, env EnvID, path string) ([]byte, error)
	WriteFile(ctx context.Context, env EnvID, path string, data []byte, mode fs.FileMode) error
	// WriteTree replaces the ABSOLUTE directory dir with exactly files, in
	// one round trip: whatever was there is removed and the tree is laid
	// down as the remote user. Engine-owned locations only
	// (/apothecary/...); a relative dir is refused, and so is any file path
	// that is not a clean relative path (see TreeFile).
	WriteTree(ctx context.Context, env EnvID, dir string, files []TreeFile) error
	// ListFiles lists the regular files under dir, recursively, sorted by
	// path — what a files port holds. Symlinks are never followed; a dir
	// that is not there is fs.ErrNotExist. At most ListFilesLimit entries.
	ListFiles(ctx context.Context, env EnvID, dir string) ([]FileEntry, error)
	Status(ctx context.Context, env EnvID) (EnvStatus, error)
	Destroy(ctx context.Context, env EnvID) error
}

// TreeFile is one file of a directory tree WriteTree lays down: a clean
// relative path (forward slashes, no empty, "." or ".." segments, no leading
// "/"), its permission bits (0 = 0644) and its bytes.
type TreeFile struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

// FileEntry is one file ListFiles found: its path as ReadFile takes it (the
// directory's path joined with the file's own) and its size in bytes.
type FileEntry struct {
	Path string
	Size int64
}

// ListFilesLimit bounds one ListFiles: a directory holding more lists its
// first ListFilesLimit files. A files port is screenshots and reports, not a
// tree to walk.
const ListFilesLimit = 1000

type SessionID string

// AgentSpec is what a harness needs to invoke one agent stage. The agent
// name resolves inside the workspace: harness-native config is generated at
// bootstrap from the flow document's own agent declarations (FLOWLANG v3 —
// a flow carries its agents; there is no repo-side agents directory).
type AgentSpec struct {
	Agent    string
	Harness  string
	Model    string // provider/model
	MaxTurns int
}

type AgentInput struct {
	Prompt string // assembled per §3.2 (Run), or the reply to deliver (Resume)
	// Events receives the invocation's transcript AS IT HAPPENS — message
	// text, tool calls, plans, turn boundaries. nil = nobody is listening.
	// Called from the runner's goroutine; implementations must not block for
	// long (the engine buffers).
	Events func(AgentEvent)
	// MCPServers are the MCP servers the stage's agent may call (its `mcp:`
	// list, FLOWLANG §1), resolved for this run: secrets filled, the builtin
	// browser turned into its workspace command. A runner registers them on
	// the session it opens or loads — so they are the agent's, not the
	// workspace's — and must never log or persist them.
	MCPServers []MCPServer
	// Inbox holds the human's messages for this invocation. A runner that can
	// deliver them mid-invocation takes them at the end of each turn (or
	// interrupts the turn, when a message asks to); one that cannot simply
	// never takes them, and the engine carries them into the next prompt.
	// nil = no messages.
	Inbox Inbox
	// ReadOnly: the session must not change the workspace (the Ask pane's
	// session, beside a stage's). A runner that cannot promise it says so
	// (ReadOnlyCapable) and is never asked.
	ReadOnly bool
	// Model, on a Resume, switches the session to this model before the
	// prompt; "" keeps the session's.
	Model string
}

// ReadOnlyCapable is an OPTIONAL AgentRunner capability: whether it can run
// a session that cannot change the workspace (AgentInput.ReadOnly).
type ReadOnlyCapable interface {
	CanReadOnly() bool
}

// MCPServer is one MCP server an agent session can call: a local command
// speaking MCP on stdio in the workspace (Command), or a remote server over
// streamable HTTP (URL). Values are final — a harness passes them through.
type MCPServer struct {
	Name    string
	Command []string
	URL     string
	Env     map[string]string
	Headers map[string]string
}

// UserMessage is one message a human sent to a running agent.
type UserMessage struct {
	ID   string
	Text string
	// Interrupt asks for the turn in flight to be cancelled so the message
	// is read now, rather than queued until the agent finishes the turn.
	Interrupt bool
	// Verbatim marks text the engine composed (the handback prompt that
	// ends a take-over): delivered as it is, not framed as a human's words.
	Verbatim bool
}

// Inbox is the engine-owned queue of human messages for one invocation.
type Inbox interface {
	// Wake is signalled (buffered, coalescing) whenever a message is queued
	// — and when a hold ends.
	Wake() <-chan struct{}
	// Take removes and returns every queued message: taking IS delivering,
	// so a runner takes only what it is about to send.
	Take() []UserMessage
	// Interrupting reports whether a queued message asked to interrupt.
	Interrupting() bool
	// Held reports whether a human holds the session (a take-over). While
	// held, a turn that ends with nothing queued is not the invocation's
	// end: the runner waits for the holder's next message (Wake) and keeps
	// going in the same session. The hold ends with a Wake and Held false.
	Held() bool
}

// AgentEvent kinds.
const (
	AgentEvMessage     = "message"      // Text: a chunk of the agent's reply
	AgentEvThought     = "thought"      // Text: a chunk of visible reasoning
	AgentEvTool        = "tool"         // Tool: a tool call, created or updated
	AgentEvPlan        = "plan"         // Plan: the agent's current todo list
	AgentEvPermission  = "permission"   // Tool + Text: a permission request and its outcome
	AgentEvTurnStart   = "turn_start"   // Turn
	AgentEvTurnEnd     = "turn_end"     // Turn, StopReason, Error
	AgentEvUserMessage = "user_message" // Messages: what was delivered to the agent, Text: as sent
	AgentEvNotice      = "notice"       // Text: something about the agent process itself (it exited, a gap)
	AgentEvSession     = "session"      // Text: the session id, as soon as it exists
)

// AgentEvent is one normalized fact about a running agent. Cursor is an
// opaque position in the runner's own stream: handed back to Reattach after
// an engine restart, it resumes the stream right after this event.
type AgentEvent struct {
	Kind       string
	Turn       string
	Text       string
	Tool       *AgentTool
	Plan       []AgentPlanEntry
	StopReason string
	Error      string
	Messages   []UserMessage
	Cursor     string
	// Usage, on a turn_end, is what that one turn spent — so the engine can
	// account for the turns a take-over prompted.
	Usage *Usage
}

type AgentTool struct {
	ID        string
	Title     string
	Kind      string // read|edit|delete|move|search|execute|think|fetch|other
	Status    string // pending|in_progress|completed|failed
	Input     string // raw input, JSON (truncated)
	Output    string // text output (truncated)
	Locations []string
	Diffs     []AgentDiff
}

type AgentDiff struct {
	Path    string
	OldText string
	NewText string
}

type AgentPlanEntry struct {
	Content  string
	Status   string // pending|in_progress|completed
	Priority string
}

// Verdict is a judgement on the work: approve or reject, a summary, and
// comments — each a remark on the whole, or on a file (and its lines).
type Verdict struct {
	Approve  bool
	Summary  string
	Comments []VerdictComment
}

// VerdictComment is one comment of a verdict: Path "" is a remark on the
// whole change; Line 0 is on the whole file.
type VerdictComment struct {
	Path    string
	Line    int
	EndLine int
	Quote   string
	Body    string
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// AgentResult is what a harness reports about one agent invocation. Note what
// is absent: a verdict. A verdict is a declared output file (FLOWLANG §4), and
// the engine reads it from the path the flow bound — one source of truth, so a
// harness cannot report APPROVE over a file that says REJECT.
type AgentResult struct {
	Session   SessionID
	Output    string
	TurnsUsed int
	Usage     Usage
}

type AgentRunner interface {
	Run(ctx context.Context, env EnvID, spec AgentSpec, in AgentInput) (AgentResult, error)
	// Resume continues session s with in.Prompt as the next message.
	Resume(ctx context.Context, env EnvID, s SessionID, in AgentInput) (AgentResult, error)
}

// AgentReattacher is an OPTIONAL AgentRunner capability: after an engine
// restart, pick up a turn that is STILL RUNNING in session s (the agent
// process outlived the engine) instead of resuming it with a new message.
// cursor is the last AgentEvent.Cursor the engine recorded ("" = none). ok is
// false when nothing is in flight for s — the caller then resumes normally.
type AgentReattacher interface {
	// Reattachable reports, quickly, whether Reattach has something to
	// follow: a turn of s still running, or one that finished after cursor
	// while nobody was listening.
	Reattachable(ctx context.Context, env EnvID, s SessionID, cursor string) bool
	Reattach(ctx context.Context, env EnvID, s SessionID, cursor string, in AgentInput) (res AgentResult, ok bool, err error)
}

// AgentModelLister is an OPTIONAL AgentRunner capability: ask the agent IN
// the environment which models it will accept — the authoritative list, as
// opposed to what the provider serves (the installed harness version may not
// know a model the provider still offers). Ids are the agent's own spelling.
type AgentModelLister interface {
	AgentModels(ctx context.Context, env EnvID) ([]string, error)
}

// AgentStopper is an OPTIONAL AgentRunner capability: stop whatever agent
// process the runner keeps alive in the environment (a run that ended gives
// up its credentials, and a live agent holds them in memory).
type AgentStopper interface {
	StopAgent(ctx context.Context, env EnvID) error
}

// RepoRef identifies a project repo at its repository host.
type RepoRef struct {
	URL           string
	DefaultBranch string
	// Locator is the host-specific repo identifier (GitLab, GitHub) (DESIGN2 §5):
	// GitLab's numeric project id as a string, GitHub's "owner/repo".
	// Filled by the repo picker; adapters parse their own locator.
	Locator string
}

type MRRef struct {
	IID int
	URL string
}

type MRState string

const (
	MROpen   MRState = "open"
	MRMerged MRState = "merged"
	MRClosed MRState = "closed"
)

// Mergeable is what the repository host says about merging a merge request
// into its target. Unknown is "still computing" (GitHub's mergeable:null,
// GitLab's checking/unchecked) and must never be read as a conflict.
type Mergeable string

const (
	MergeClean     Mergeable = "clean"
	MergeConflicts Mergeable = "conflicts"
	MergeUnknown   Mergeable = "unknown"
)

// MRStatus is one poll of a merge request.
type MRStatus struct {
	State     MRState
	Mergeable Mergeable
	HeadSHA   string
	Target    string
	// TargetSHA is the target branch's tip, filled when Mergeable is
	// MergeConflicts: a conflict is one (target tip, head) pair, so a fix a
	// human declined is offered again only once either side moves.
	TargetSHA string
	// MergeableNow is the repository host's own verdict that it would merge
	// the merge request right now: no conflict, its required approvals and
	// checks in, not a draft (GitHub's mergeable_state clean, GitLab's
	// detailed merge status mergeable). Draft is whether it is still marked a
	// draft.
	MergeableNow bool
	Draft        bool
}

// CheckState is a CI check's state on a commit, or the aggregate over all of
// a commit's checks.
type CheckState string

const (
	// ChecksNone: nothing reported for the commit (aggregate only).
	ChecksNone   CheckState = ""
	CheckPending CheckState = "pending"
	CheckSuccess CheckState = "success"
	CheckFailure CheckState = "failure"
	// CheckNeutral is a check that neither passes nor fails the commit:
	// skipped, cancelled, neutral, waiting for a person to start it.
	CheckNeutral CheckState = "neutral"
)

// Check is one CI check reported on a commit: a GitHub check run or commit
// status, a GitLab pipeline or one of its failed jobs.
type Check struct {
	Name  string     `json:"name"`
	State CheckState `json:"state"`
	URL   string     `json:"url,omitempty"`
}

// CIFailure is one failed CI check, said so that what failed can be found and
// run again locally: the CI file that declares it, its job and stage, the
// steps that failed. A check another app reports (not the repository host's
// own CI) has only its name, link and summary.
type CIFailure struct {
	Name    string   `json:"name"`
	URL     string   `json:"url,omitempty"`
	File    string   `json:"file,omitempty"`  // .github/workflows/ci.yml, .gitlab-ci.yml
	Job     string   `json:"job,omitempty"`   // the job's name in that file
	Stage   string   `json:"stage,omitempty"` // GitLab: the job's stage
	Steps   []string `json:"steps,omitempty"` // GitHub Actions: the failed steps, in order
	Summary string   `json:"summary,omitempty"`
}

// Checks are a merge request head's CI checks and what they add up to.
type Checks struct {
	Head  string     `json:"head"`
	State CheckState `json:"state"`
	Items []Check    `json:"items"`
}

// AggregateChecks adds checks up: failure if any failed, else pending if
// any is still running, else success if any passed, else none.
func AggregateChecks(items []Check) CheckState {
	var pending, success bool
	for _, c := range items {
		switch c.State {
		case CheckFailure:
			return CheckFailure
		case CheckPending:
			pending = true
		case CheckSuccess:
			success = true
		}
	}
	switch {
	case pending:
		return CheckPending
	case success:
		return CheckSuccess
	}
	return ChecksNone
}

// Failed lists the failed checks' names, sorted: what one failure of a head
// is (a check failing later on the same head is a new one).
func (c Checks) Failed() []string {
	var out []string
	for _, it := range c.Items {
		if it.State == CheckFailure {
			out = append(out, it.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Approval is whether a merge request is approved, and by whom.
type Approval struct {
	Approved bool     `json:"approved"`
	By       []string `json:"by,omitempty"`
}

// MRInfo is a merge request as the repository host lists it: what a run
// created on it needs to check it out and say whose it is.
type MRInfo struct {
	Ref    MRRef   `json:"ref"`
	Title  string  `json:"title"`
	Body   string  `json:"body,omitempty"`
	Author string  `json:"author"`
	State  MRState `json:"state"`
	Draft  bool    `json:"draft,omitempty"`
	// Source and Target are its branches; Head its head commit.
	Source string `json:"source"`
	Target string `json:"target"`
	Head   string `json:"head"`
	// HeadRef is where its head can be fetched from in this repository, a
	// fork's included: refs/pull/<n>/head, refs/merge-requests/<iid>/head.
	HeadRef string `json:"head_ref"`
	// Fork: its branch lives in another repository — readable through
	// HeadRef, not a branch here anyone can push to.
	Fork bool `json:"fork,omitempty"`
}

// MRBase is the open merge request another is stacked on: the one whose
// branch it targets. Merging the stacked one lands it in that branch, not in
// the branch the base itself goes to.
type MRBase struct {
	IID    int    `json:"iid"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Branch string `json:"branch"`
}

// StackedOn finds, among a repository's open merge requests, the one a merge
// request targeting target is stacked on — nil when target is no open merge
// request's branch. A fork's branch lives in another repository, so a fork
// is never a base.
func StackedOn(target string, open []MRInfo) *MRBase {
	for _, m := range open {
		if !m.Fork && m.State != MRMerged && m.State != MRClosed && m.Source == target {
			return &MRBase{IID: m.Ref.IID, URL: m.Ref.URL, Title: m.Title, Branch: m.Source}
		}
	}
	return nil
}

// Merge methods: a merge commit, one squashed commit, or the branch's
// commits rebased onto the target.
const (
	MergeMethodMerge  = "merge"
	MergeMethodSquash = "squash"
	MergeMethodRebase = "rebase"
)

// MergeOptions is how MergeMR merges.
type MergeOptions struct {
	Method string
	// Head pins the merge: the repository host merges only while the head is
	// still this commit ("" = whatever it is), so nothing pushed after the
	// decision is merged unseen.
	Head string
	// DeleteBranch removes the merge request's branch once merged.
	DeleteBranch bool
}

// ErrMergeRefused is a merge the repository host declined — not mergeable now,
// still a draft, the head moved, a method it does not offer. An outcome a flow
// routes (on_fail), unlike a repository host it cannot reach.
type ErrMergeRefused struct{ Reason string }

func (e *ErrMergeRefused) Error() string { return "the repository refused the merge: " + e.Reason }

// ReviewNote is one comment of a review, on a line of the change: Path is
// the file, Line its line in the new version, Body the text as posted (its
// kind already its leading tag), Kind the note's kind for the marker — so a
// later poll knows what kind of thread the factory opened without reading
// the body.
type ReviewNote struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
	Kind string `json:"kind,omitempty"`
}

// NoteMarker is the marker of a review's i-th note: the review's, with the
// note's kind as an attribute. Adapters post it and look for it with the
// same call, so an attribute never breaks a retry's reconciliation.
func NoteMarker(reviewID string, i int, kind string) string {
	if kind == "" {
		return ReviewMarker(reviewID, fmt.Sprintf("note-%d", i))
	}
	return ReviewMarker(reviewID, fmt.Sprintf("note-%d", i), "kind="+kind)
}

// Review is what PostReview posts: a summary and notes on lines of the
// merge request's diff. It never approves nor requests changes — that
// decision stays a person's. ID names it in the marker every part carries,
// so a retry after a crash finds what is posted and posts only the rest.
type Review struct {
	// Head is the commit the review read ("" = the merge request's head).
	Head    string
	Summary string
	Notes   []ReviewNote
	ID      string
}

// ReviewPosted says what PostReview did: notes posted on their line, notes
// whose line is not in the diff (folded into the summary, quoted with their
// place), and whether everything was already there.
type ReviewPosted struct {
	Inline  int  `json:"inline"`
	Folded  int  `json:"folded"`
	Already bool `json:"already,omitempty"`
}

// ReviewMarker is embedded (an HTML comment, invisible when rendered) in
// every part of a review the factory posts — the summary, each note — so
// the part is recognisable as the factory's and a retry posts nothing twice.
// attrs are further "key=value" words (a note's kind); a value holds no
// space.
func ReviewMarker(reviewID, part string, attrs ...string) string {
	return marker(append([]string{"review=" + reviewID, "part=" + part}, attrs...))
}

// marker renders the factory's HTML-comment marker from its words.
func marker(words []string) string {
	return "<!-- apothecary " + strings.Join(words, " ") + " -->"
}

// markerWords reads the first factory marker of a body as its key=value
// words; nil when there is none.
func markerWords(body string) map[string]string {
	_, rest, ok := strings.Cut(body, "<!-- apothecary ")
	if !ok {
		return nil
	}
	inner, _, ok := strings.Cut(rest, "-->")
	if !ok {
		return nil
	}
	words := map[string]string{}
	for _, w := range strings.Fields(inner) {
		if k, v, ok := strings.Cut(w, "="); ok {
			words[k] = v
		}
	}
	return words
}

// MarkerAttr is one word of a body's factory marker ("" when absent): the
// review id, its part, a note's kind, a reply's round, thread or did.
func MarkerAttr(body, key string) string { return markerWords(body)[key] }

// ReviewMarkerOf reads the review id and part a factory review part's
// marker names; ok=false when the body is not one.
func ReviewMarkerOf(body string) (reviewID, part string, ok bool) {
	w := markerWords(body)
	return w["review"], w["part"], w["review"] != ""
}

// MarkerRound is the round a factory reply's marker names, or "".
func MarkerRound(body string) string { return MarkerAttr(body, "run") }

// DiffLines reads one file's unified diff and returns the lines a note may
// anchor to, on the new side: new line → old line (0 for an added line, the
// old number for an unchanged one shown as context).
func DiffLines(patch string) map[int]int {
	out := map[int]int{}
	oldN, newN := 0, 0
	for _, l := range strings.Split(patch, "\n") {
		if strings.HasPrefix(l, "@@") {
			var a, b, c, d int
			if n, _ := fmt.Sscanf(l, "@@ -%d,%d +%d,%d @@", &a, &b, &c, &d); n == 4 {
				oldN, newN = a, c
			} else if n, _ := fmt.Sscanf(l, "@@ -%d +%d,%d @@", &a, &c, &d); n == 3 {
				oldN, newN = a, c
			} else if n, _ := fmt.Sscanf(l, "@@ -%d,%d +%d @@", &a, &b, &c); n == 3 {
				oldN, newN = a, c
			} else if n, _ := fmt.Sscanf(l, "@@ -%d +%d @@", &a, &c); n == 2 {
				oldN, newN = a, c
			}
			continue
		}
		if newN == 0 || l == "" {
			continue
		}
		switch l[0] {
		case '+':
			out[newN] = 0
			newN++
		case '-':
			oldN++
		case ' ':
			out[newN] = oldN
			oldN++
			newN++
		}
	}
	return out
}

// FoldedNotes renders notes that could not go on their line as a list the
// summary carries, each quoted with its place.
func FoldedNotes(notes []ReviewNote) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("**On lines outside this change:**\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "\n- `%s:%d` — %s", n.Path, n.Line, strings.ReplaceAll(strings.TrimSpace(n.Body), "\n", "\n  "))
	}
	return b.String()
}

// ReviewThread is one review conversation on a merge request: an inline
// discussion on a line, or a review's top-level body.
type ReviewThread struct {
	ID       string
	Path     string // "" for a thread not anchored to a file
	Line     int
	DiffHunk string
	Resolved bool
	Comments []ReviewComment // oldest first
}

type ReviewComment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt time.Time
}

// ReplyMarker is embedded (as an HTML comment, invisible when rendered) in
// every reply the factory posts; runID may carry a round suffix. It is how a
// reply is recognised as the factory's — independent of which account the
// token belongs to — and how a reply posted before a crash is found instead
// of posted twice. attrs are further "key=value" words (what the reply did).
func ReplyMarker(runID, threadID string, attrs ...string) string {
	return marker(append([]string{"run=" + runID, "thread=" + threadID}, attrs...))
}

// IsFactoryReply reports whether a comment body carries a ReplyMarker or a
// ReviewMarker: the factory wrote it, so it is never a reviewer's word.
func IsFactoryReply(body string) bool {
	return strings.Contains(body, "<!-- apothecary run=") || strings.Contains(body, "<!-- apothecary review=")
}

// PendingThreads are the review threads still waiting on the factory: not
// resolved, and whose latest comment is not a factory reply (so a reviewer
// spoke after our last reply, or we never replied). Derived from the
// repository host alone — no cursor is stored, so nothing can drift from what
// the reviewer sees.
func PendingThreads(threads []ReviewThread) []ReviewThread {
	var out []ReviewThread
	for _, t := range threads {
		if t.Resolved || len(t.Comments) == 0 {
			continue
		}
		if IsFactoryReply(t.Comments[len(t.Comments)-1].Body) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// NewestPending is the latest reviewer comment across pending threads: its
// time drives the quiet period, its id is the trigger key.
func NewestPending(pending []ReviewThread) (ReviewComment, bool) {
	var newest ReviewComment
	found := false
	for _, t := range pending {
		for _, c := range t.Comments {
			if IsFactoryReply(c.Body) {
				continue
			}
			if !found || c.CreatedAt.After(newest.CreatedAt) {
				newest, found = c, true
			}
		}
	}
	return newest, found
}

// IsReplyTo reports whether a body is the factory's reply in thread
// threadID from round roundID: how a retry tells a reply already posted
// from one still to post.
func IsReplyTo(body, roundID, threadID string) bool {
	return MarkerRound(body) == roundID && MarkerThread(body) == threadID
}

// MarkerThread returns the thread id a factory reply's marker names, or "".
func MarkerThread(body string) string { return MarkerAttr(body, "thread") }

// WorkspaceReattacher is an OPTIONAL EnvProvisioner capability: after an
// engine restart, re-establish any engine-side resources for a still-
// running workspace that died with the previous process — notably the
// reverse tunnel (workspace → engine/proxy loopback). Idempotent; called
// on the recovery/resume reuse path. Adapters with no such state (fake)
// don't implement it.
type WorkspaceReattacher interface {
	Reattach(ctx context.Context, env EnvID) error
}

// EgressDenial is one blocked host as the run page consumes it (the gateway
// tracks these per run; the engine surfaces them at GET /runs/{id}/egress-denials).
type EgressDenial struct {
	Host  string    `json:"host"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
	// Status is the host's rule now (EgressPolicy.Status): held until someone
	// allows or blocks it — only held hosts ask for review.
	Status EgressStatus `json:"status"`
}

// EgressStatus is a host's current rule, as a reviewer sees it.
type EgressStatus string

const (
	EgressAllowed EgressStatus = "allowed"
	// EgressHeld is denied by default (allowlist miss) and not yet reviewed:
	// refused for now, still asking for a verdict.
	EgressHeld EgressStatus = "held"
	// EgressBlocked is denied by a rule someone wrote (force-block, or a
	// denylist entry): reviewed, never asks again.
	EgressBlocked EgressStatus = "blocked"
)

// EgressAuditRecord is one aggregated egress fact for a run (a (host, port,
// verdict) tuple) with counts, byte totals and first/last timestamps.
type EgressAuditRecord struct {
	Host     string    `json:"host"`
	DstIP    string    `json:"dst_ip"`
	Port     int       `json:"port"`
	Proto    string    `json:"proto"`
	Verdict  string    `json:"verdict"`
	Count    int64     `json:"count"`
	BytesOut int64     `json:"bytes_out"`
	BytesIn  int64     `json:"bytes_in"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

// EgressAuditRow is a durable audit record (an EgressAuditRecord tagged with its
// project + engine run id) as stored and queried in the audit trail.
type EgressAuditRow struct {
	Project  string    `json:"project"`
	Run      string    `json:"run"`
	Host     string    `json:"host"`
	DstIP    string    `json:"dst_ip"`
	Port     int       `json:"port"`
	Proto    string    `json:"proto"`
	Verdict  string    `json:"verdict"`
	Count    int64     `json:"count"`
	BytesOut int64     `json:"bytes_out"`
	BytesIn  int64     `json:"bytes_in"`
	First    time.Time `json:"first_seen"`
	Last     time.Time `json:"last_seen"`
}

// EgressAuditFilter narrows an audit query; empty fields are ignored.
type EgressAuditFilter struct {
	Run     string
	Project string
	Verdict string
	Host    string
	Limit   int
}

// EgressAuditor is an OPTIONAL EnvProvisioner capability: the aggregated egress
// audit trail for an environment (every host the workspace reached or was
// blocked from, with counts + bytes + timestamps), read from the per-project
// gateway. The engine ingests these into durable storage so the trail survives
// the ephemeral gateway.
type EgressAuditor interface {
	EgressAudit(ctx context.Context, env EnvID) []EgressAuditRecord
}

// EgressPolicy is the firewall policy the gateway enforces RIGHT NOW (read
// back from the hot-reloaded policy file). The UI renders a host's current
// rule from it — as opposed to the audit trail, whose verdicts are records
// of past traffic and never change when the rule does.
type EgressPolicy struct {
	Mode           string   // "allow" (deny-all + allowlist) or "deny" (allow-all + denylist)
	Domains        []string // allowlist (mode=allow) or denylist (mode=deny); exact or "*.suffix"
	BlockedDomains []string // force-deny overrides; win in both modes
}

// Status classifies host under the policy, mirroring the gateway's verdict
// (workspace/gateway policy.verdict): a force-block wins in both modes, then
// mode + list membership decide. The only denial nobody wrote a rule for is an
// allowlist miss — that one is held.
func (p EgressPolicy) Status(host string) EgressStatus {
	if egressMatches(host, p.BlockedDomains) {
		return EgressBlocked
	}
	m := egressMatches(host, p.Domains)
	switch {
	case p.Mode == "deny" && m:
		return EgressBlocked
	case p.Mode == "deny" || m:
		return EgressAllowed
	}
	return EgressHeld
}

// egressMatches mirrors the gateway's hostMatches: an exact entry, or a
// "*.suffix" wildcard matched by suffix (never the bare apex itself).
func egressMatches(host string, list []string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range list {
		d = strings.ToLower(strings.TrimSpace(d))
		if rest, ok := strings.CutPrefix(d, "*."); ok {
			if strings.HasSuffix(h, "."+rest) {
				return true
			}
		} else if h == d {
			return true
		}
	}
	return false
}

// EgressController is an OPTIONAL EnvProvisioner capability: inspect and live-
// edit a run's egress policy, backed by the per-project egress gateway
// (internal/egressgw). AllowEgress is the "add to egress" fast path — it rewrites
// the project policy file and the gateway hot-reloads it, so the next request
// succeeds with no restart. Because the policy is per-project, an allow applies
// to every run of the project; denials stay per-run. Provisioners without a
// gateway simply don't implement it and the engine degrades honestly.
type EgressController interface {
	EgressDenials(ctx context.Context, env EnvID) []EgressDenial
	AllowEgress(ctx context.Context, env EnvID, host string) error
	// SetEgressRule live-toggles a host's rule: allow=true permits it, allow=false
	// force-blocks it (overrides base allowlist defaults). Project-wide, no restart.
	SetEgressRule(ctx context.Context, env EnvID, host string, allow bool) error
	// EgressPolicy reads back the policy currently enforced for the env's
	// project — the source of truth for "what happens to this host now".
	EgressPolicy(ctx context.Context, env EnvID) (EgressPolicy, error)
}

// BuildCacheInfo is what a project's devcontainer build cache holds right now.
// Available is false when the provisioner has a cache but cannot be reached
// for it (no gateway up yet) — distinct from an empty cache, which is a real
// answer.
type BuildCacheInfo struct {
	Available bool  `json:"available"`
	Bytes     int64 `json:"bytes"`
}

// BuildCacheController is an OPTIONAL EnvProvisioner capability: report and
// wipe the project's devcontainer build cache.
//
// The cache is per PROJECT and outlives every run, which is the point — a run's
// daemon is thrown away, so without a cache that outlives it each run rebuilds
// the image from scratch. It is also the one piece of derived state a human
// occasionally has to delete: a base image that moved under a floating tag, a
// feature install that cached a bad layer. Provisioners without one simply
// don't implement this and the engine says so honestly. project is the
// project's id — the same value EnvSpec.Project carried at Provision.
type BuildCacheController interface {
	BuildCache(ctx context.Context, project string) (BuildCacheInfo, error)
	ClearBuildCache(ctx context.Context, project string) error
}

// PortConn is one exposed workspace port as the engine consumes it: a
// dialer for per-browser-connection streams plus teardown. The ENGINE owns
// the handle (provisioner adapter instances are refcount-closed on config
// edits; a subprocess or listener parented there would die with them).
type PortConn interface {
	Dial(ctx context.Context) (net.Conn, error)
	Close() error
}

// WorkspacePortExposer is an OPTIONAL EnvProvisioner capability: reach a
// TCP port inside the environment so the engine can publish it to the
// human (the "browse the app under development" path). Adapters implement
// whatever transport reaches their workspaces (devcontainer: a reverse
// relay over the bind-mounted unix sockets).
type WorkspacePortExposer interface {
	// ExposePort prepares access to the port AS SERVED BY the environment and
	// returns the dial handle. Which container actually serves it is the
	// adapter's business: a compose devcontainer's port may belong to a
	// sibling service rather than the workspace's own loopback. Idempotency
	// across repeated exposures of the same port is the caller's job (the
	// engine's port registry).
	ExposePort(ctx context.Context, env EnvID, port int) (PortConn, error)
	// AppPorts lists ports the environment declares it will serve (e.g.
	// devcontainer forwardPorts) — auto-exposed at env_ready. Best-effort;
	// empty is normal.
	AppPorts(ctx context.Context, env EnvID) []int
}

// Browser is the project browser (internal/browser): one container per
// project, a Chromium per agent session, driven through Playwright's MCP
// server — what a flow's builtin `browser` MCP server is. The browser has no
// network of its own: it reaches the run's services at localhost, through
// Dial, and nothing else.
type Browser interface {
	// Session serves one MCP session on stdio (newline-delimited JSON-RPC,
	// as an MCP client speaks to a stdio server) until either side closes,
	// bringing the project's browser up first. One session is one Chromium.
	Session(ctx context.Context, s BrowserSession, stdio io.ReadWriter) error
	// Warm brings the project's browser up (building its image on first
	// use) in the background, so a session does not wait on it. project is
	// the project's id (what names the container, via ProjectHostKey);
	// projectName is a docker label only.
	Warm(project, projectName string)
	// Status is the project's browser as the settings page shows it, by
	// project id.
	Status(ctx context.Context, project string) (BrowserStatus, error)
}

// BrowserSession is who a browser session is for.
type BrowserSession struct {
	// Project is the project's id: what names its container (ProjectHostKey).
	Project string
	// ProjectName is the human name, a docker label on the container only.
	ProjectName string
	Run         string
	// Dial reaches a TCP port the run's workspace serves: where the
	// browser's http://localhost:<port> goes.
	Dial func(ctx context.Context, port int) (net.Conn, error)
}

// BrowserStatus is a project browser's state.
type BrowserStatus struct {
	Container string `json:"container"`
	// State is the container's: absent (never started, or removed),
	// running, or stopped (idle — the next session starts it again).
	State string `json:"state"`
	// Image is what the container runs; Current what this build would run
	// — they differ until the browser is next idle and gets recreated.
	Image   string `json:"image,omitempty"`
	Current string `json:"current,omitempty"`
	// Sessions are the agent sessions using it now, each its own Chromium.
	Sessions int `json:"sessions"`
}

// CodeServerPort is the fixed workspace-loopback port code-server binds
// (avoids the engine 8080 / egress-proxy 8079 loopback ports the bridge uses).
const CodeServerPort = 8471

// EditorSettings are neutral, editor-agnostic UI preferences and extensions
// applied to the in-workspace editor when it starts — written as the editor's
// user settings and installed into its per-run extensions dir, so changing
// them needs NO image rebuild. The adapter maps these to its editor's native
// config (code-server → VS Code settings.json keys, Open VSX ids); a future
// editor adapter maps the same neutral fields to its own. Zero value ⇒ leave
// the editor's own defaults untouched.
type EditorSettings struct {
	// Extensions are the VS Code extension ids the project declares plus its
	// harnesses' own; the adapter unions them with the ones the workspace
	// definition itself declares
	// (a devcontainer's customizations.vscode.extensions) and installs the
	// union when the editor first opens in a run. Non-secret.
	Extensions []string
	// Theme is the color theme name (code-server: workbench.colorTheme); must be
	// a built-in theme or one carried by an installed extension.
	Theme string
	// FontSize / TabSize apply only when > 0.
	FontSize int
	TabSize  int
	// FormatOnSave / WordWrap toggle the matching editor prefs when true.
	FormatOnSave bool
	WordWrap     bool
	// TrustWorkspace, when true, disables VS Code's Workspace Trust feature
	// (the @tag:workspaceTrust settings) so the editor never prompts to trust
	// the folder — the workspace is an ephemeral sandbox where the agent already
	// runs arbitrary code, so the prompts are pure friction.
	TrustWorkspace bool
}

// EditorStarter is an OPTIONAL EnvProvisioner capability: start an in-workspace
// code-server (VS Code in the browser) listening on the given loopback port, so
// the engine can expose it (the "Open editor" path). Idempotent — a no-op when
// code-server is already listening; returns when the port accepts connections.
// The binary comes from the tools bundle mounted into every workspace (glibc
// images only). settings are the project's editor UI defaults and extensions,
// (re)applied on each start.
type EditorStarter interface {
	StartEditor(ctx context.Context, env EnvID, port int, settings EditorSettings) error
	// EditorExtensions is the list StartEditor installs for env: the
	// definition's own ids (a devcontainer's customizations.vscode.extensions)
	// ∪ settings.Extensions, deduplicated and sorted. The run page hands it
	// to a desktop editor connecting over SSH, so the same extensions can
	// follow a person there. Best effort: what the definition cannot say is
	// left out, never an error.
	EditorExtensions(ctx context.Context, env EnvID, settings EditorSettings) []string
}

// WorkspaceRebuilder is an OPTIONAL EnvProvisioner capability: recreate the
// environment's container(s) IN PLACE, keeping the run and the engine-side
// checkout (agent commits survive). Distinct from a fresh Provision (same
// env identity) and from the engine's re-run/resume actions. The engine
// re-runs bootstrap afterwards (the fresh container has no /apothecary state).
type WorkspaceRebuilder interface {
	Rebuild(ctx context.Context, spec EnvSpec) error
}

// WorkspaceDirtyChecker is an OPTIONAL EnvProvisioner capability: whether the
// environment's build inputs (its devcontainer configuration) have changed
// since the container was built, so the UI can suggest a rebuild — a
// "dirty" workspace. External state like EnvStatus: polled by the engine's
// reconciler, kept in memory, never in the event log. False when the adapter
// cannot tell (an environment built before it recorded the inputs).
type WorkspaceDirtyChecker interface {
	ConfigDirty(ctx context.Context, env EnvID) (bool, error)
}

// ObservedPort is a listening TCP port discovered at runtime inside an
// environment (as opposed to declared in devcontainer.json). Process is a
// best-effort name; Service names the compose sibling that serves it (empty
// for ports on the workspace container itself).
type ObservedPort struct {
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
	Service string `json:"service,omitempty"`
}

// SiblingPortScanner is an OPTIONAL EnvProvisioner capability: discover
// listening ports in the environment's SIBLING containers (compose
// services), which the in-workspace scanner cannot see (separate network +
// pid namespaces). The workspace's own ports are discovered in-container and
// reported directly to the engine; this covers the rest. Implementations
// also prime their expose routing so a discovered sibling port can be
// reached by ExposePort. Best-effort; empty is normal (non-compose, or a
// sibling image without a shell/procfs).
type SiblingPortScanner interface {
	ScanSiblingPorts(ctx context.Context, env EnvID) []ObservedPort
}

// ResourceSample is one point-in-time reading of an environment's resource
// usage — for the devcontainer adapter, the whole DinD sandbox (inner dockerd
// + workspace + compose siblings), which is exactly "what this run costs the
// host". At is stamped by the caller (the engine's clock), not the adapter.
// Net counters are cumulative since container start; the UI derives rates
// from deltas.
type ResourceSample struct {
	At       time.Time `json:"at"`
	CPUPerc  float64   `json:"cpu_perc"`  // % of one core; ceiling is CPUs×100
	CPUs     int       `json:"cpus"`      // host logical cores (the % denominator); 0 = unknown
	MemUsed  int64     `json:"mem_used"`  // bytes
	MemLimit int64     `json:"mem_limit"` // bytes (host total when uncapped)
	NetRx    int64     `json:"net_rx"`    // cumulative bytes received
	NetTx    int64     `json:"net_tx"`    // cumulative bytes sent
	PIDs     int       `json:"pids"`
}

// ResourceSampler is an OPTIONAL EnvProvisioner capability: one resource
// reading of a live environment (the run-page cpu/ram graphs). Samples
// are external state like EnvStatus — the engine keeps a rolling in-memory
// window, never the event log. An error means "no sample" (a gap in the
// series), which callers must prefer over carrying a stale value forward.
type ResourceSampler interface {
	SampleResources(ctx context.Context, env EnvID) (ResourceSample, error)
}

// ExecLogTailer is an OPTIONAL EnvProvisioner capability: a live tail of
// an environment's combined exec output (bootstrap, commands, agent
// invocations) for run-page feedback. How output is captured is the
// adapter's business; implementations that cannot stream simply don't
// implement the interface and the API degrades honestly.
type ExecLogTailer interface {
	TailExecLog(ctx context.Context, env EnvID, n int) ([]byte, error)
}

// Sidecar is an OPTIONAL EnvProvisioner capability: the per-workspace
// runtime the engine drives over HTTP — the long-lived coding agent (ACP),
// the workspace's files and git state, and terminals. Sidecar starts it if it
// is not already serving (idempotent) and returns a dialer for its socket;
// the HTTP API is internal/sidecar's. Provisioners whose workspaces cannot
// host it don't implement this, and the features it backs degrade honestly.
type Sidecar interface {
	Sidecar(ctx context.Context, env EnvID) (PortConn, error)
}

// WorkspaceSSH is an OPTIONAL EnvProvisioner capability: a dialer for the
// sidecar's SSH server (ssh.sock beside sidecar.sock in the shim dir), the
// far end of the engine's SSH door — a person's ssh or editor session into
// the workspace. Starting the sidecar first is the adapter's business, as
// for Sidecar.
type WorkspaceSSH interface {
	SSH(ctx context.Context, env EnvID) (PortConn, error)
}

// EngineURLProvider is an OPTIONAL EnvProvisioner capability: the URL this
// env's workspace reaches the engine at (written into run.env as
// APOTHECARY_ENGINE_URL). A provisioner whose workspaces are not on the
// engine's machine supplies its own — a Kubernetes pod reaches the engine's
// Service, not a loopback relay. Absent ⇒ the engine's configured URL.
type EngineURLProvider interface {
	EngineURL(env EnvID) string
}

type RepoHost interface {
	// CheckAccess verifies token validity, project reachability and scopes (§1 preflight).
	CheckAccess(ctx context.Context, repo RepoRef) error
	FileExists(ctx context.Context, repo RepoRef, path string) (bool, error)
	// CreateDraftMR opens a draft merge request from branch onto target
	// (empty target = the repo's default branch). A stacked change is MRs
	// whose targets chain: each layer onto the one below.
	CreateDraftMR(ctx context.Context, repo RepoRef, branch, target, title, body string) (MRRef, error)
	// AddMRLabels adds labels to a merge request, keeping the ones it has;
	// a label the repository does not have yet is created. Idempotent.
	AddMRLabels(ctx context.Context, mr MRRef, labels []string) error
	// FindMR locates an already-open merge request from branch onto target.
	// The engine reconciles with it after a crash between creating an MR on
	// the repository host and recording it (PLAN5 M20): the branch is unique
	// per run, so it is a natural idempotency key and needs no stored token.
	// ok=false means "no such MR", which is distinct from an error.
	FindMR(ctx context.Context, repo RepoRef, branch, target string) (ref MRRef, ok bool, err error)
	// BranchURL is the branch's browser page on the repository host, read
	// from the host itself — so it is only ever a link that resolves.
	// ok=false means the branch is not there (not pushed yet), which is
	// distinct from an error.
	BranchURL(ctx context.Context, repo RepoRef, branch string) (url string, ok bool, err error)
	// MRStatus is the merge request's state plus what the repository host
	// knows about merging it: whether it conflicts with its target, its head
	// commit and its target branch. One call on every repository host.
	MRStatus(ctx context.Context, mr MRRef) (MRStatus, error)
	// MRChecks lists the CI checks reported on head (the merge request's
	// head commit, as MRStatus returned it) with their aggregate state.
	MRChecks(ctx context.Context, mr MRRef, head string) (Checks, error)
	// CIFailures says what failed on head, so it can be found and re-run:
	// each failed check's CI file, job, stage and failed steps — never its
	// log. More calls than MRChecks: read once, when a chain needs it.
	CIFailures(ctx context.Context, mr MRRef, head string) ([]CIFailure, error)
	// MRApproval says whether the merge request is approved: by at least one
	// reviewer with no request for changes outstanding (GitHub), or with its
	// approval rules met by at least one approval (GitLab).
	MRApproval(ctx context.Context, mr MRRef) (Approval, error)
	// MRThreads lists the merge request's review threads: inline discussions
	// and review comments, system notes dropped. Resolved threads included
	// (Resolved=true) — the caller decides what is pending.
	MRThreads(ctx context.Context, mr MRRef) ([]ReviewThread, error)
	// ReplyThread posts body as a reply in a thread (the body carries
	// ReplyMarker so the reply is recognisable as the factory's).
	ReplyThread(ctx context.Context, mr MRRef, threadID, body string) error
	// ResolveThread marks a thread resolved.
	ResolveThread(ctx context.Context, mr MRRef, threadID string) error
	// GetMR reads one merge request of the bound repository; ListMRs its
	// open ones, newest first (bounded: a few hundred).
	GetMR(ctx context.Context, iid int) (MRInfo, error)
	ListMRs(ctx context.Context) ([]MRInfo, error)
	// Whoami is the account the integration acts as: its username, so the
	// factory can tell its own merge requests from others'.
	Whoami(ctx context.Context) (string, error)
	// MergeMR merges the merge request. A merge the repository host declines
	// is an
	// *ErrMergeRefused; any other error is the repository host not answering.
	MergeMR(ctx context.Context, mr MRRef, opts MergeOptions) error
	// PostReview posts a review: its notes on their lines of the diff — one
	// whose line is not in the diff joins the summary (FoldedNotes) — and the
	// summary. Every part carries ReviewMarker(rev.ID, …); parts already on
	// the merge request are not posted again.
	PostReview(ctx context.Context, mr MRRef, rev Review) (ReviewPosted, error)
	// UploadAttachment hosts a file on the repository host for a merge
	// request's body to show (a screenshot): uploaded, not committed, so it
	// never lands on the branch.
	UploadAttachment(ctx context.Context, repo RepoRef, name, contentType string, data []byte) (Attachment, error)
}

// Attachment is a file a repository host or a tracker now hosts, and how a
// body written there shows it.
type Attachment struct {
	// URL is the file's absolute address.
	URL string
	// Link is what a markdown link or image in a body written there points
	// at: URL, or the host's own shorter form its renderer resolves
	// (GitLab's project-relative /uploads/…). "" means URL.
	Link string
	// Markdown shows it: an image for an image where the host renders one,
	// a link otherwise. "" means the caller builds it from Link.
	Markdown string
}

// Target is Link, else URL.
func (a Attachment) Target() string {
	if a.Link != "" {
		return a.Link
	}
	return a.URL
}

// Notification is the one-way fan-out payload; the CPUI inbox is canonical,
// channels are best-effort mirrors.
type Notification struct {
	// UserID is whose channels the notification goes to: the run's owner.
	// Empty is an instance-wide notice (every channel).
	UserID string
	RunID  string
	PingID string
	Type   string // ping type
	Title  string
	Body   string
	URL    string // deep link into the CPUI
}

type NotificationChannel interface {
	// Send errors are logged, never fail the run.
	Send(ctx context.Context, n Notification) error
}
