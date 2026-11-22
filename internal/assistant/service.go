package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"apothecary/internal/ports/assistantport"
	"apothecary/internal/ports/envport"
	"apothecary/internal/run/event"
	"apothecary/internal/skills"
	"apothecary/internal/workspace/sidecar"
)

// Integration is one of a person's harness integrations the assistant can
// run on.
type Integration struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Harness is a person's harness integration, opened for one session.
type Harness interface {
	Kind() string
	// Models are the model ids the session may pick.
	Models(ctx context.Context) ([]string, error)
	// EnvVars are API-key credentials, for the process's environment.
	EnvVars() map[string]string
	// EgressDomains are the model providers' hosts: the gateway's standing
	// allow list.
	EgressDomains() []string
	// SeedAuth writes login material into the session's home, registering
	// every secret before it writes it.
	SeedAuth(ctx context.Context, env envport.EnvID, register func(secret string)) error
	// SecretValues are the credentials the session's text must never show.
	SecretValues() []string
}

// Harnesses opens a person's harness integrations.
type Harnesses interface {
	// Integrations are the person's own harness integrations of a kind the
	// assistant runs.
	Integrations(ctx context.Context, person string) ([]Integration, error)
	// Open opens one of them, its files written through env.
	Open(ctx context.Context, person, integration string, env envport.EnvProvisioner) (Harness, error)
}

// Workshops is what a session reads of a workshop.
type Workshops interface {
	// Repos are the workshop's repositories with each one's credential,
	// when person may read the workshop; an error otherwise.
	Repos(ctx context.Context, person, workshop string) ([]assistantport.FetchItem, error)
}

// Tokens mints and revokes a session's token on the engine's MCP face.
type Tokens interface {
	Mint(ctx context.Context, person, name string, actions, workshops []string, ttl time.Duration) (id, secret string, err error)
	Revoke(ctx context.Context, person, id string) error
}

// Errors the API maps.
var (
	ErrNotFound   = errors.New("assistant: no such session")
	ErrBusy       = errors.New("assistant: the session is answering")
	ErrNoChoice   = errors.New("assistant: choose the harness the assistant runs on (Settings)")
	ErrNotYours   = errors.New("assistant: this token does not fill this session")
	ErrNotEnabled = errors.New("assistant: not enabled on this instance")
)

// The session token's life: re-minted when less than half is left.
const tokenTTL = time.Hour

// repoFresh is how long a fetch is good for before a turn fetches again.
const repoFresh = 10 * time.Minute

// turnTimeout bounds one answer.
const turnTimeout = 10 * time.Minute

// warmTimeout bounds a warm: a pod started ahead of a question.
const warmTimeout = 2 * time.Minute

// maxQuestion bounds what a person sends.
const maxQuestion = 32 << 10

// FillAction is the action a session's token fills its session with.
const FillAction = "assistant:fill"

// Service runs persons' sessions.
type Service struct {
	Store     Store
	Pods      assistantport.Pods
	Harnesses Harnesses
	Workshops Workshops
	Tokens    Tokens
	Checks    Checks
	// Skills is the person's skill library: nil on a server that keeps
	// none, where a choice names no skillset.
	Skills Skills
	// FaceActions are the read actions a session's token holds on the
	// engine's MCP face (the tools' actions, which the API supplies),
	// beside FillAction.
	FaceActions []string
	// FaceURL is the engine's MCP face as a pod reaches it.
	FaceURL func(ctx context.Context) (string, error)
	Log     *slog.Logger
	// NewID is nil for a ULID.
	NewID func() string

	// saveMu orders every change to a stored session (edit): a model
	// picked mid-answer is not undone by the answer's end.
	saveMu  sync.Mutex
	mu      sync.Mutex
	turns   map[string]*turn      // by session id: the one in flight
	tokens  map[string]liveToken  // by session id
	fetched map[string]time.Time  // by person + workshop
	repos   map[string][]RepoView // by person + workshop: the last fetch
	// deciding are the requests a person's confirm is running, by session
	// and seq: a second click waits its turn.
	deciding map[string]bool
	// warming are the persons whose pod a warm is starting.
	warming map[string]bool
	hub     hub
}

type liveToken struct {
	id, secret string
	until      time.Time
	// installed: a harness session took it (opened or loaded with it). A
	// turn that fails between the mint and the session leaves it unset,
	// and the next turn installs it then.
	installed bool
}

// turn is one answer in flight.
type turn struct {
	cancel context.CancelFunc
	fill   json.RawMessage
	// request is the acts the answer asks the person to confirm.
	request *Request
	// red holds the turn's secrets: its answer is scrubbed of them, and a
	// fill or a request holding one is refused.
	red *redactor
}

// RepoView is one repository's state in the panel.
type RepoView struct {
	Workshop string    `json:"workshop"`
	Name     string    `json:"name"`
	OK       bool      `json:"ok"`
	Head     string    `json:"head,omitempty"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

func (svc *Service) log() *slog.Logger {
	if svc.Log != nil {
		return svc.Log
	}
	return slog.Default()
}

func (svc *Service) newID() string {
	if svc.NewID != nil {
		return svc.NewID()
	}
	return ulid.Make().String()
}

func (svc *Service) init() {
	if svc.turns == nil {
		svc.turns = map[string]*turn{}
		svc.tokens = map[string]liveToken{}
		svc.fetched = map[string]time.Time{}
		svc.repos = map[string][]RepoView{}
		svc.deciding = map[string]bool{}
		svc.warming = map[string]bool{}
	}
}

// Status is the panel's view of a person's assistant.
type Status struct {
	Enabled      bool                 `json:"enabled"`
	Reason       string               `json:"reason,omitempty"`
	Choice       Choice               `json:"choice"`
	Integrations []Integration        `json:"integrations"`
	Models       []string             `json:"models"`
	Pod          assistantport.Status `json:"pod"`
	Repos        []RepoView           `json:"repos"`
	Pages        map[Kind]PageView    `json:"pages"`
}

// PageView is what the panel knows of a page: its presets.
type PageView struct {
	Presets []string `json:"presets"`
	Repos   bool     `json:"repos"`
}

// Status reports the person's assistant: whether it can run, on what, and
// the state of their pod and of a workshop's repositories ("" for none).
func (svc *Service) Status(ctx context.Context, person, workshop string) (Status, error) {
	st := Status{Pages: map[Kind]PageView{}, Integrations: []Integration{}, Models: []string{}, Repos: []RepoView{}}
	for _, k := range Kinds {
		p := pages[k]
		st.Pages[k] = PageView{Presets: p.Presets, Repos: p.Repos}
	}
	if svc.Pods == nil {
		st.Reason = ErrNotEnabled.Error()
		return st, nil
	}
	choice, err := svc.Store.AssistantChoice(ctx, person)
	if err != nil {
		return st, err
	}
	st.Choice = choice
	if st.Integrations, err = svc.Harnesses.Integrations(ctx, person); err != nil {
		return st, err
	}
	if choice.Integration == "" || !slices.ContainsFunc(st.Integrations, func(i Integration) bool { return i.ID == choice.Integration }) {
		st.Reason = ErrNoChoice.Error()
	} else if h, err := svc.Harnesses.Open(ctx, person, choice.Integration, nil); err == nil {
		if models, err := h.Models(ctx); err == nil {
			st.Models = models
		}
	}
	if st.Pod, err = svc.Pods.Status(ctx, person); err != nil {
		return st, err
	}
	if st.Pod.Error != "" && st.Reason == "" {
		st.Reason = st.Pod.Error
	}
	st.Enabled = st.Reason == ""
	svc.mu.Lock()
	svc.init()
	st.Repos = append(st.Repos, svc.repos[person+"\x00"+workshop]...)
	svc.mu.Unlock()
	return st, nil
}

// SetChoice records the harness integration (one of the person's), the
// model their sessions start with and the skillsets (their own) a new
// session installs.
func (svc *Service) SetChoice(ctx context.Context, person string, c Choice) error {
	c.UserID = person
	if c.Integration != "" {
		ins, err := svc.Harnesses.Integrations(ctx, person)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(ins, func(i Integration) bool { return i.ID == c.Integration }) {
			return errors.Join(ErrInvalid, errors.New("not one of your harness integrations the assistant runs on"))
		}
	}
	var sets []skills.Ref
	for _, r := range c.Skillsets {
		if !slices.ContainsFunc(sets, func(x skills.Ref) bool { return x.ID == r.ID }) {
			sets = append(sets, r)
		}
	}
	c.Skillsets = sets
	if len(sets) > 0 {
		if svc.Skills == nil {
			return errors.Join(ErrInvalid, errors.New("this server keeps no skills"))
		}
		if _, err := svc.Skills.ResolveSkillsets(ctx, person, sets); err != nil {
			return errors.Join(ErrInvalid, err)
		}
	}
	return svc.Store.SaveAssistantChoice(ctx, c)
}

// pins are a choice's skillsets as a new session installs them: each ref
// resolved on its own, one that no longer resolves (deleted since) left
// out, so the assistant keeps working. The library failing to answer is
// an error: a session opened without the pin would never install it.
func (svc *Service) pins(ctx context.Context, person string, refs []skills.Ref) ([]event.SkillsetPin, error) {
	if svc.Skills == nil {
		return nil, nil
	}
	var out []event.SkillsetPin
	for _, r := range refs {
		p, err := svc.Skills.ResolveSkillsets(ctx, person, []skills.Ref{r})
		if errors.Is(err, ErrSkillsetGone) {
			svc.log().Warn("assistant: a chosen skillset is gone; skipping", "person", person, "skillset", r.ID, "err", err)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("your skills: %w", err)
		}
		out = append(out, p...)
	}
	return out, nil
}

// Open is a person's session about a subject: their latest, or a new one;
// always a new one when fresh.
func (svc *Service) Open(ctx context.Context, person string, kind Kind, workshop, subject string, fresh bool) (Session, []Message, error) {
	if !kind.Valid() || strings.TrimSpace(subject) == "" {
		return Session{}, nil, errors.Join(ErrInvalid, errors.New("a session needs a kind and a subject"))
	}
	if (kind == KindFlow || kind == KindTicket || kind == KindRun) && workshop == "" {
		return Session{}, nil, errors.Join(ErrInvalid, errors.New("a flow's, a ticket's or a run's session needs its workshop"))
	}
	if workshop != "" {
		// Reading the workshop is the person's to have: the same check its
		// repositories go through.
		if _, err := svc.Workshops.Repos(ctx, person, workshop); err != nil {
			return Session{}, nil, err
		}
	}
	var s Session
	ok := false
	if !fresh {
		var err error
		if s, ok, err = svc.Store.FindAssistantSession(ctx, person, kind, workshop, subject); err != nil {
			return Session{}, nil, err
		}
	}
	if !ok {
		choice, err := svc.Store.AssistantChoice(ctx, person)
		if err != nil {
			return Session{}, nil, err
		}
		pins, err := svc.pins(ctx, person, choice.Skillsets)
		if err != nil {
			return Session{}, nil, err
		}
		s = Session{ID: strings.ToLower(svc.newID()), UserID: person, Kind: kind, Workshop: workshop, Subject: subject,
			Integration: choice.Integration, Model: choice.Model, Skillsets: pins}
		if err := svc.Store.SaveAssistantSession(ctx, s); err != nil {
			return Session{}, nil, err
		}
		if s, _, err = svc.Store.AssistantSession(ctx, s.ID); err != nil {
			return Session{}, nil, err
		}
	}
	msgs, err := svc.Store.AssistantMessages(ctx, s.ID, 0)
	return s, msgs, err
}

// edit changes a stored session: read it again, change it, save it, under
// saveMu, so two changes never undo each other.
func (svc *Service) edit(ctx context.Context, id string, change func(*Session)) (Session, error) {
	svc.saveMu.Lock()
	defer svc.saveMu.Unlock()
	s, ok, err := svc.Store.AssistantSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrNotFound
	}
	change(&s)
	if err := svc.Store.SaveAssistantSession(ctx, s); err != nil {
		return Session{}, err
	}
	return s, nil
}

func (svc *Service) session(ctx context.Context, person, id string) (Session, error) {
	s, ok, err := svc.Store.AssistantSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if !ok || s.UserID != person {
		return Session{}, ErrNotFound
	}
	return s, nil
}

// Reopen is one of a person's sessions as it stands, by its id, and its
// messages: the session a page holds, never another about the same
// subject opened since.
func (svc *Service) Reopen(ctx context.Context, person, id string) (Session, []Message, error) {
	s, err := svc.session(ctx, person, id)
	if err != nil {
		return Session{}, nil, err
	}
	msgs, err := svc.Store.AssistantMessages(ctx, s.ID, 0)
	return s, msgs, err
}

// Messages are a session's, after a sequence.
func (svc *Service) Messages(ctx context.Context, person, id string, after int64) ([]Message, error) {
	if _, err := svc.session(ctx, person, id); err != nil {
		return nil, err
	}
	return svc.Store.AssistantMessages(ctx, id, after)
}

// SetModel picks the model a session answers with from its next turn.
func (svc *Service) SetModel(ctx context.Context, person, id, model string) (Session, error) {
	s, err := svc.session(ctx, person, id)
	if err != nil {
		return Session{}, err
	}
	model = strings.TrimSpace(model)
	return svc.edit(ctx, s.ID, func(x *Session) { x.Model = model })
}

// Send records the person's words and starts the answer; its text, tools
// and fill arrive on the person's stream. pageContext is the page's state.
func (svc *Service) Send(ctx context.Context, person, id, text string, pageContext json.RawMessage) (Message, error) {
	if svc.Pods == nil {
		return Message{}, ErrNotEnabled
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxQuestion {
		return Message{}, errors.Join(ErrInvalid, fmt.Errorf("a question is 1 to %d bytes", maxQuestion))
	}
	s, err := svc.session(ctx, person, id)
	if err != nil {
		return Message{}, err
	}
	if s.Integration == "" {
		choice, err := svc.Store.AssistantChoice(ctx, person)
		if err != nil {
			return Message{}, err
		}
		if choice.Integration == "" {
			return Message{}, ErrNoChoice
		}
		s.Integration = choice.Integration
		if s.Model == "" {
			s.Model = choice.Model
		}
	}
	svc.mu.Lock()
	svc.init()
	if svc.turns[id] != nil {
		svc.mu.Unlock()
		return Message{}, ErrBusy
	}
	tctx, cancel := context.WithTimeout(context.Background(), turnTimeout)
	t := &turn{cancel: cancel, red: &redactor{}}
	svc.turns[id] = t
	svc.mu.Unlock()
	q, err := svc.Store.AppendAssistantMessage(ctx, Message{SessionID: id, Role: RolePerson, Text: text})
	if err != nil {
		svc.endTurn(id, t)
		return Message{}, err
	}
	svc.hub.publish(person, Frame{Kind: "message", Session: id, Message: &q})
	svc.hub.publish(person, Frame{Kind: "state", Session: id, Busy: true})
	first := q.Seq == 1
	go svc.answer(tctx, t, s, text, pageContext, first)
	return q, nil
}

// Cancel stops a session's answer in flight.
func (svc *Service) Cancel(ctx context.Context, person, id string) error {
	if _, err := svc.session(ctx, person, id); err != nil {
		return err
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if t := svc.turns[id]; t != nil {
		t.cancel()
	}
	return nil
}

func (svc *Service) endTurn(id string, t *turn) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.turns[id] == t {
		delete(svc.turns, id)
	}
	t.cancel()
}

// answer runs one turn and records its answer, whatever happens.
func (svc *Service) answer(ctx context.Context, t *turn, s Session, text string, pageContext json.RawMessage, first bool) {
	person := s.UserID
	red := t.red
	said, cost, err := svc.run(ctx, t, &s, red, text, pageContext, first)
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The session's token stays with its harness session (token): its
	// process, idle in the pod, reads through it on the next turn.
	// What the turn learnt, onto the session as it is now: a model changed
	// while it answered stays.
	if _, serr := svc.edit(sctx, s.ID, func(x *Session) {
		x.HarnessSession = s.HarnessSession
		if x.Integration == "" {
			x.Integration = s.Integration
		}
		if x.Model == "" {
			x.Model = s.Model
		}
	}); serr != nil {
		svc.log().Warn("assistant: save session", "session", s.ID, "err", serr)
	}
	svc.mu.Lock()
	fill := t.fill
	svc.mu.Unlock()
	svc.mu.Lock()
	request := t.request
	svc.mu.Unlock()
	m := Message{SessionID: s.ID, Role: RoleAssistant, Text: red.scrub(strings.TrimSpace(said)), Fill: fill, Request: request, CostUSD: cost}
	if err != nil {
		m.Error = red.scrub(err.Error())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			m.Error = fmt.Sprintf("no answer within %s", turnTimeout)
		} else if errors.Is(ctx.Err(), context.Canceled) {
			m.Error = "stopped"
		}
	}
	rec, rerr := svc.Store.AppendAssistantMessage(sctx, m)
	if rerr != nil {
		svc.log().Error("assistant: record an answer", "session", s.ID, "err", rerr)
		rec = m
	}
	svc.endTurn(s.ID, t)
	svc.hub.publish(person, Frame{Kind: "message", Session: s.ID, Message: &rec})
	svc.hub.publish(person, Frame{Kind: "state", Session: s.ID, Busy: false})
}

// run is the turn: the pod, the repositories, the harness, the prompt.
func (svc *Service) run(ctx context.Context, t *turn, s *Session, red *redactor, text string, pageContext json.RawMessage, first bool) (string, float64, error) {
	person := s.UserID
	page, ok := pages[s.Kind]
	if !ok {
		return "", 0, fmt.Errorf("no page %s", s.Kind)
	}
	env := &sessionEnv{pods: svc.Pods, person: person, home: svc.Pods.Home(s.ID)}
	h, err := svc.Harnesses.Open(ctx, person, s.Integration, env)
	if err != nil {
		return "", 0, fmt.Errorf("your harness integration: %w", err)
	}
	prof, ok := profiles[h.Kind()]
	if !ok {
		return "", 0, fmt.Errorf("the assistant does not run on %s", h.Kind())
	}
	for _, v := range h.SecretValues() {
		red.add(v)
	}
	for _, v := range h.EnvVars() {
		red.add(v)
	}
	svc.status(person, s.ID, "starting your assistant")
	if err := svc.Pods.Ensure(ctx, person, h.EgressDomains()); err != nil {
		return "", 0, err
	}
	if err := svc.installSkills(ctx, s, prof.skills(env.home)); err != nil {
		return "", 0, err
	}
	var repos []assistantport.Repo
	if page.Repos && s.Workshop != "" {
		if repos, err = svc.freshRepos(ctx, person, s.Workshop, false); err != nil {
			return "", 0, err
		}
	}
	tok, fresh, err := svc.token(ctx, s)
	if err != nil {
		return "", 0, err
	}
	red.add(tok)
	face, err := svc.FaceURL(ctx)
	if err != nil {
		return "", 0, err
	}
	// Files first: the harness's own settings, then its logins.
	for _, f := range prof.files(env.home) {
		if err := svc.Pods.WriteFile(ctx, person, f.path, []byte(f.content), 0o600); err != nil {
			return "", 0, err
		}
	}
	if err := h.SeedAuth(ctx, envport.EnvID(s.ID), red.add); err != nil {
		return "", 0, fmt.Errorf("sign the harness in: %w", err)
	}
	procEnv := prof.env()
	for k, v := range h.EnvVars() {
		procEnv[k] = v
	}
	start, err := svc.Pods.StartRequest(person, assistantport.Session{ID: s.ID, Repos: repos, Argv: prof.argv, Exec: prof.exec, Env: procEnv})
	if err != nil {
		return "", 0, err
	}
	ah, err := svc.Pods.AgentHost(ctx, person)
	if err != nil {
		return "", 0, err
	}
	cl := sidecar.NewWithHeader(ah.Dialer, assistantport.AgentHostTokenHeader, ah.Token)
	svc.status(person, s.ID, "starting the harness")
	startReq := sidecar.StartRequest{Argv: start.Argv, Env: start.Env, Cwd: start.Cwd, Confine: &sidecar.Confine{
		Read: start.Confine.Read, Write: start.Confine.Write, Exec: start.Confine.Exec, Devices: start.Confine.Devices}}
	st, err := cl.Start(ctx, startReq)
	if err != nil {
		return "", 0, fmt.Errorf("start the harness: %w", err)
	}
	gitCmd, gitArgs := svc.gitServer(s.ID, repos)
	servers := []sidecar.MCPServer{
		{Name: "apothecary", URL: face, Headers: []sidecar.EnvVar{{Name: "Authorization", Value: "Bearer " + tok}}},
	}
	if len(repos) > 0 {
		servers = append(servers, sidecar.MCPServer{Name: "git", Command: gitCmd, Args: gitArgs})
	}
	req := sidecar.SessionRequest{SessionID: s.HarnessSession, Proc: st.Proc, Cwd: start.Cwd, Mode: prof.mode, Model: s.Model, MCPServers: servers}
	sess, err := cl.Session(ctx, req)
	if err == nil && fresh && req.SessionID != "" && sess.SessionID == req.SessionID && !sess.Loaded {
		// The harness session is live in its process, which holds the MCP
		// servers it was opened with: a token since replaced (this turn or
		// a failed one before it). A session takes its servers only when
		// opened, so the process starts again and the session is loaded
		// with the new token, its context kept.
		svc.status(person, s.ID, "restarting the harness")
		if err := cl.StopProc(ctx, st.Proc); err != nil {
			return "", 0, fmt.Errorf("restart the harness: %w", err)
		}
		if st, err = cl.Start(ctx, startReq); err != nil {
			return "", 0, fmt.Errorf("start the harness: %w", err)
		}
		req.Proc = st.Proc
		sess, err = cl.Session(ctx, req)
	}
	if err != nil && req.SessionID != "" {
		// The harness lost the session (its state went with a volume, or a
		// version moved on): a new one, the instructions given again.
		svc.log().Info("assistant: the harness session is gone; a new one", "session", s.ID, "err", red.scrub(err.Error()))
		req.SessionID, first = "", true
		sess, err = cl.Session(ctx, req)
	}
	if err != nil {
		return "", 0, fmt.Errorf("open the harness session: %w", err)
	}
	// The session was opened or loaded with tok, or is live holding it.
	svc.installed(s.ID, s.TokenID)
	if sess.SessionID != s.HarnessSession {
		first = first || !sess.Loaded
	}
	s.HarnessSession = sess.SessionID
	if s.Model == "" {
		s.Model = sess.Model
	}
	outcomes, told := svc.outcomes(ctx, s.ID)
	prompt := svc.prompt(page, s, repos, text, pageContext, first, outcomes)
	evs, errs, err := cl.Events(ctx, "", 0)
	if err != nil {
		return "", 0, err
	}
	turnID := strings.ToLower(svc.newID())
	if err := cl.Prompt(ctx, sidecar.PromptRequest{SessionID: sess.SessionID, Turn: turnID, Text: prompt}); err != nil {
		return "", 0, fmt.Errorf("ask the harness: %w", err)
	}
	svc.reported(ctx, s.ID, told)
	svc.status(person, s.ID, "")
	var said strings.Builder
	var cost float64
	last := time.Time{}
	for {
		select {
		case <-ctx.Done():
			_ = cl.Cancel(context.Background(), sess.SessionID)
			return said.String(), cost, ctx.Err()
		case err := <-errs:
			if err != nil {
				return said.String(), cost, err
			}
		case ev, ok := <-evs:
			if !ok {
				return said.String(), cost, errors.New("the agent host's stream ended")
			}
			if ev.Type == "agent_exit" && ev.Proc == st.Proc {
				return said.String(), cost, fmt.Errorf("the harness exited: %s", red.scrub(ev.Error))
			}
			if ev.Turn != turnID {
				continue
			}
			switch ev.Type {
			case "message":
				said.WriteString(ev.Text)
				if time.Since(last) >= 120*time.Millisecond {
					last = time.Now()
					svc.hub.publish(person, Frame{Kind: "delta", Session: s.ID, Text: red.scrub(said.String())})
				}
			case "tool_call", "tool_update":
				if ev.Tool != nil && ev.Tool.Title != "" && ev.Tool.Status != "completed" && ev.Tool.Status != "failed" {
					svc.hub.publish(person, Frame{Kind: "delta", Session: s.ID, Text: red.scrub(said.String()), Tool: red.scrub(ev.Tool.Title)})
				}
			case "turn_end":
				if ev.CostUSD != nil {
					cost = *ev.CostUSD
				}
				if ev.Error != "" {
					return said.String(), cost, errors.New(red.scrub(ev.Error))
				}
				return said.String(), cost, nil
			}
		}
	}
}

// skillsMarker is the file in a session's skills directory that names the
// pins installed there: a turn finding it as it would write it skips the
// install.
const skillsMarker = ".apothecary-skillsets"

// installSkills writes the session's pinned skills into the harness's
// skills directory in the session's home, once: the pins are fixed per
// session, so nothing is ever removed.
func (svc *Service) installSkills(ctx context.Context, s *Session, dir string) error {
	if len(s.Skillsets) == 0 || svc.Skills == nil {
		return nil
	}
	var mark strings.Builder
	for _, p := range s.Skillsets {
		fmt.Fprintf(&mark, "%s v%d %s\n", p.ID, p.Seq, p.Hash)
	}
	marker := path.Join(dir, skillsMarker)
	if got, err := svc.Pods.ReadFile(ctx, s.UserID, marker); err == nil && string(got) == mark.String() {
		return nil
	}
	svc.status(s.UserID, s.ID, "loading your skills")
	trees, err := svc.Skills.SkillTrees(ctx, s.Skillsets)
	if err != nil {
		return fmt.Errorf("your skills: %w", err)
	}
	for _, t := range trees {
		for _, f := range t.Files {
			mode := uint32(f.Mode.Perm())
			if mode == 0 {
				mode = 0o644
			}
			// Rooted, then joined: a name or a path never leaves dir.
			p := path.Join(dir, path.Clean("/"+t.Name), path.Clean("/"+f.Path))
			if err := svc.Pods.WriteFile(ctx, s.UserID, p, f.Data, mode); err != nil {
				return fmt.Errorf("install skill %s: %w", t.Name, err)
			}
		}
	}
	return svc.Pods.WriteFile(ctx, s.UserID, marker, []byte(mark.String()), 0o600)
}

func (svc *Service) gitServer(sessionID string, repos []assistantport.Repo) (string, []string) {
	args := []string{"git-mcp", "--state", svc.Pods.Home(sessionID)}
	for _, r := range repos {
		args = append(args, "--repo", RepoName(r)+"="+svc.Pods.Folder(r))
	}
	return assistantport.ShimBin, args
}

// RepoName is a repository's name for the git tool.
func RepoName(r assistantport.Repo) string {
	name := strings.ToLower(r.Name)
	var b strings.Builder
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// prompt is the turn's text: the page's instructions on a session's first
// turn, the page as it stands, the repositories, the person's words.
func (svc *Service) prompt(page Page, s *Session, repos []assistantport.Repo, text string, pageContext json.RawMessage, first bool, outcomes string) string {
	var b strings.Builder
	if first {
		b.WriteString(page.Instructions)
		b.WriteString("\n\n")
	}
	if len(repos) > 0 {
		b.WriteString("The workshop's repositories, in your working directory (git tool names in brackets):")
		for _, r := range repos {
			fmt.Fprintf(&b, " %s [%s]", svc.Pods.Folder(r), RepoName(r))
		}
		b.WriteString("\n")
	}
	if c := page.Context(pageContext); c != "" {
		b.WriteString(c)
	}
	b.WriteString(outcomes)
	b.WriteString("\nThe person asks:\n")
	b.WriteString(text)
	return b.String()
}

// Token is a person's session's live token on the MCP face, minted when it
// has none: what its harness session is opened with.
func (svc *Service) Token(ctx context.Context, person, id string) (string, error) {
	s, err := svc.session(ctx, person, id)
	if err != nil {
		return "", err
	}
	tok, _, err := svc.token(ctx, &s)
	return tok, err
}

// token is the session's token on the MCP face, minted when it has none (or
// too little life left, or the engine restarted, the secret being held in
// memory alone): the face's read actions in its workshop, and its
// session's fill. It lives with the harness session it is handed to, across
// turns, until replaced; fresh reports one no harness session has taken
// yet (installed), which a harness session still live does not hold.
func (svc *Service) token(ctx context.Context, s *Session) (secret string, fresh bool, err error) {
	svc.mu.Lock()
	svc.init()
	lt, ok := svc.tokens[s.ID]
	svc.mu.Unlock()
	if ok && lt.id == s.TokenID && time.Until(lt.until) > tokenTTL/2 {
		return lt.secret, !lt.installed, nil
	}
	actions := slices.Clone(svc.FaceActions)
	if s.Kind == KindSkill && s.Workshop == "" {
		actions = []string{"skill:read"}
	}
	actions = append(actions, FillAction)
	var workshops []string
	if s.Workshop != "" {
		workshops = []string{s.Workshop}
	}
	id, secret, err := svc.Tokens.Mint(ctx, s.UserID, "Assistant session "+s.ID, actions, workshops, tokenTTL)
	if err != nil {
		return "", false, fmt.Errorf("the session's token: %w", err)
	}
	if s.TokenID != "" {
		if err := svc.Tokens.Revoke(ctx, s.UserID, s.TokenID); err != nil {
			svc.log().Warn("assistant: revoke a session's old token", "session", s.ID, "err", err)
		}
	}
	s.TokenID = id
	svc.mu.Lock()
	svc.tokens[s.ID] = liveToken{id: id, secret: secret, until: time.Now().Add(tokenTTL)}
	svc.mu.Unlock()
	_, err = svc.edit(ctx, s.ID, func(x *Session) { x.TokenID = id })
	return secret, true, err
}

// installed records that the session's harness session took its token id.
func (svc *Service) installed(sessionID, tokenID string) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if lt, ok := svc.tokens[sessionID]; ok && lt.id == tokenID {
		lt.installed = true
		svc.tokens[sessionID] = lt
	}
}

// FillTool is what the MCP face shows a session's token for its fill and
// request tools: the session it is bound to, the page's fill schema (nil
// for none) and whether the page acts.
func (svc *Service) FillTool(ctx context.Context, tokenID string) (sessionID string, schema map[string]any, acts, ok bool) {
	svc.mu.Lock()
	svc.init()
	var sid string
	for id, lt := range svc.tokens {
		if lt.id == tokenID {
			sid = id
		}
	}
	svc.mu.Unlock()
	if sid == "" {
		return "", nil, false, false
	}
	s, found, err := svc.Store.AssistantSession(ctx, sid)
	if err != nil || !found || s.TokenID != tokenID {
		return "", nil, false, false
	}
	p := pages[s.Kind]
	return sid, p.FillSchema, p.Acts, true
}

// Fill is the fill tool: checked against its page, then sent to the
// person's page, once. Only the session's own token fills it, while its
// question is being answered. The errors are the agent's to correct;
// nothing is stored.
func (svc *Service) Fill(ctx context.Context, tokenID, person, sessionID string, raw json.RawMessage) ([]string, error) {
	s, err := svc.session(ctx, person, sessionID)
	if err != nil {
		return nil, err
	}
	if tokenID == "" || s.TokenID != tokenID {
		return nil, ErrNotYours
	}
	// The token outlives the turn (token); the person's page takes a fill
	// only from the answer they wait for.
	svc.mu.Lock()
	svc.init()
	t := svc.turns[sessionID]
	svc.mu.Unlock()
	if t == nil {
		return []string{"no question is being answered: fill only while answering one"}, nil
	}
	out, errs := pages[s.Kind].Check(ctx, svc.Checks, person, s.Workshop, raw)
	if len(errs) > 0 {
		return errs, nil
	}
	// The turn's secrets stay out of a page.
	if t.red.holds(string(raw)) || t.red.holds(string(out)) {
		return []string{"the fill holds a credential of this session (a key, a token, a login): never put one in a page"}, nil
	}
	svc.mu.Lock()
	t.fill = out
	svc.mu.Unlock()
	svc.hub.publish(person, Frame{Kind: "fill", Session: sessionID, Fill: out, Subject: s.Subject, Page: s.Kind})
	return nil, nil
}

// Warm starts the person's pod ahead of their first question (they began
// typing one), with their harness's model hosts, and returns at once. One
// warm runs at a time per person: another while it runs is nothing.
// Ensure is idempotent, so a turn that starts meanwhile waits for the pod
// rather than racing it.
func (svc *Service) Warm(ctx context.Context, person string) error {
	if svc.Pods == nil {
		return ErrNotEnabled
	}
	choice, err := svc.Store.AssistantChoice(ctx, person)
	if err != nil {
		return err
	}
	if choice.Integration == "" {
		return ErrNoChoice
	}
	h, err := svc.Harnesses.Open(ctx, person, choice.Integration, nil)
	if err != nil {
		return fmt.Errorf("your harness integration: %w", err)
	}
	hosts := h.EgressDomains()
	svc.mu.Lock()
	svc.init()
	if svc.warming[person] {
		svc.mu.Unlock()
		return nil
	}
	svc.warming[person] = true
	svc.mu.Unlock()
	go func() {
		wctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
		defer cancel()
		if err := svc.Pods.Ensure(wctx, person, hosts); err != nil {
			svc.log().Warn("assistant: warm a pod", "person", person, "err", err)
		}
		svc.mu.Lock()
		delete(svc.warming, person)
		svc.mu.Unlock()
	}()
	return nil
}

// Refresh fetches a workshop's repositories now.
func (svc *Service) Refresh(ctx context.Context, person, workshop string) ([]RepoView, error) {
	if svc.Pods == nil {
		return nil, ErrNotEnabled
	}
	choice, err := svc.Store.AssistantChoice(ctx, person)
	if err != nil {
		return nil, err
	}
	var hosts []string
	if choice.Integration != "" {
		if h, err := svc.Harnesses.Open(ctx, person, choice.Integration, nil); err == nil {
			hosts = h.EgressDomains()
		}
	}
	if err := svc.Pods.Ensure(ctx, person, hosts); err != nil {
		return nil, err
	}
	if _, err := svc.freshRepos(ctx, person, workshop, true); err != nil {
		return nil, err
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return slices.Clone(svc.repos[person+"\x00"+workshop]), nil
}

// freshRepos are a workshop's repositories, fetched when the last fetch is
// old (or force): the ones that came, for the session's rules.
func (svc *Service) freshRepos(ctx context.Context, person, workshop string, force bool) ([]assistantport.Repo, error) {
	items, err := svc.Workshops.Repos(ctx, person, workshop)
	if err != nil {
		return nil, err
	}
	key := person + "\x00" + workshop
	svc.mu.Lock()
	svc.init()
	at := svc.fetched[key]
	svc.mu.Unlock()
	if force || time.Since(at) > repoFresh {
		svc.hub.publish(person, Frame{Kind: "repos", Workshop: workshop, Text: "fetching"})
		states, err := svc.Pods.Fetch(ctx, person, items)
		if err != nil {
			return nil, err
		}
		views := make([]RepoView, 0, len(states))
		for _, st := range states {
			views = append(views, RepoView{Workshop: workshop, Name: st.Name, OK: st.OK, Head: st.Head, Error: st.Error, At: st.At})
		}
		svc.mu.Lock()
		svc.fetched[key] = time.Now()
		svc.repos[key] = views
		svc.mu.Unlock()
		svc.hub.publish(person, Frame{Kind: "repos", Workshop: workshop, Repos: views})
	}
	svc.mu.Lock()
	views := svc.repos[key]
	svc.mu.Unlock()
	var out []assistantport.Repo
	for _, it := range items {
		if slices.ContainsFunc(views, func(v RepoView) bool { return v.Name == it.Repo.Name && v.OK }) {
			out = append(out, it.Repo)
		}
	}
	return out, nil
}

func (svc *Service) status(person, session, text string) {
	svc.hub.publish(person, Frame{Kind: "status", Session: session, Text: text})
}

// --- the stream ------------------------------------------------------------

// Frame is one event on a person's stream.
type Frame struct {
	Seq      int64           `json:"seq"`
	Kind     string          `json:"kind"` // message|delta|state|status|fill|request|repos
	Session  string          `json:"session,omitempty"`
	Message  *Message        `json:"message,omitempty"`
	Text     string          `json:"text,omitempty"`
	Tool     string          `json:"tool,omitempty"`
	Busy     bool            `json:"busy,omitempty"`
	Fill     json.RawMessage `json:"fill,omitempty"`
	Page     Kind            `json:"page,omitempty"`
	Subject  string          `json:"subject,omitempty"`
	Workshop string          `json:"workshop,omitempty"`
	Repos    []RepoView      `json:"repos,omitempty"`
}

// ringSize is how many frames a person's stream keeps for a reconnect.
const ringSize = 512

type hub struct {
	mu      sync.Mutex
	seq     int64
	ring    map[string][]Frame
	subs    map[string]map[chan Frame]struct{}
	dropped map[string]int64 // by person: the last seq no longer kept
}

func (h *hub) publish(person string, f Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ring == nil {
		h.ring, h.subs, h.dropped = map[string][]Frame{}, map[string]map[chan Frame]struct{}{}, map[string]int64{}
	}
	h.seq++
	f.Seq = h.seq
	r := append(h.ring[person], f)
	if len(r) > ringSize {
		h.dropped[person] = r[len(r)-ringSize-1].Seq
		r = r[len(r)-ringSize:]
	}
	h.ring[person] = r
	for ch := range h.subs[person] {
		select {
		case ch <- f:
		default: // a reader too slow loses the frame; it resumes by seq
		}
	}
}

// Head is the stream's last sequence.
func (svc *Service) Head() int64 {
	svc.hub.mu.Lock()
	defer svc.hub.mu.Unlock()
	return svc.hub.seq
}

// Busy reports a session answering now.
func (svc *Service) Busy(id string) bool {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return svc.turns[id] != nil
}

// Watch is a person's stream from after: the frames kept past it, then
// live. after < 0 is from now, with head the sequence it starts after.
// gap reports frames past after that are no longer kept, or an after from
// before the engine restarted (the panel reloads its sessions).
func (svc *Service) Watch(person string, after int64) (backlog []Frame, gap bool, live <-chan Frame, stop func()) {
	h := &svc.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ring == nil {
		h.ring, h.subs, h.dropped = map[string][]Frame{}, map[string]map[chan Frame]struct{}{}, map[string]int64{}
	}
	switch {
	case after < 0:
		// From now: nothing kept is replayed.
		after = h.seq
	case after > h.seq:
		// A sequence from before the engine restarted: everything since is
		// new to the reader, and what it missed is gone.
		gap, after = true, 0
	case after > 0 && after < h.dropped[person]:
		gap = true
	}
	for _, f := range h.ring[person] {
		if f.Seq > after {
			backlog = append(backlog, f)
		}
	}
	ch := make(chan Frame, 64)
	if h.subs[person] == nil {
		h.subs[person] = map[chan Frame]struct{}{}
	}
	h.subs[person][ch] = struct{}{}
	return backlog, gap, ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs[person], ch)
	}
}

// --- secrets ---------------------------------------------------------------

// redactor scrubs a turn's secrets from what reaches the person.
type redactor struct {
	mu      sync.Mutex
	secrets []string
}

func (r *redactor) add(s string) {
	if len(s) < 6 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.secrets, s) {
		r.secrets = append(r.secrets, s)
	}
}

// holds reports whether s contains one of the secrets.
func (r *redactor) holds(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.secrets {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func (r *redactor) scrub(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.secrets {
		s = strings.ReplaceAll(s, x, "***")
	}
	return s
}

// --- the session as an environment ------------------------------------------

// sessionEnv is a session's home as the harness kinds see an environment:
// they probe $HOME and write their files there. Nothing else runs: the pod
// has no shell.
type sessionEnv struct {
	pods   assistantport.Pods
	person string
	home   string
}

var errNoShell = errors.New("the assistant pod runs no command")

func (e *sessionEnv) Provision(context.Context, envport.EnvSpec) (envport.EnvID, error) {
	return "", errNoShell
}

func (e *sessionEnv) Exec(_ context.Context, _ envport.EnvID, cmd envport.Command) (envport.ExecResult, error) {
	// The one script the kinds run here: the home probe (wshome).
	if strings.TrimSpace(cmd.Script) == `printf %s "$HOME"` {
		return envport.ExecResult{Output: e.home}, nil
	}
	return envport.ExecResult{ExitCode: 127}, errNoShell
}

func (e *sessionEnv) ReadFile(ctx context.Context, _ envport.EnvID, p string) ([]byte, error) {
	return e.pods.ReadFile(ctx, e.person, e.abs(p))
}

func (e *sessionEnv) WriteFile(ctx context.Context, _ envport.EnvID, p string, data []byte, mode fs.FileMode) error {
	return e.pods.WriteFile(ctx, e.person, e.abs(p), data, uint32(mode))
}

func (e *sessionEnv) WriteTree(context.Context, envport.EnvID, string, []envport.TreeFile) error {
	return errNoShell
}

func (e *sessionEnv) ListFiles(context.Context, envport.EnvID, string) ([]envport.FileEntry, error) {
	return nil, errNoShell
}

func (e *sessionEnv) Status(context.Context, envport.EnvID) (envport.EnvStatus, error) {
	return "", errNoShell
}

func (e *sessionEnv) Destroy(context.Context, envport.EnvID) error { return nil }

func (e *sessionEnv) abs(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return e.home + "/" + p
}

var _ envport.EnvProvisioner = (*sessionEnv)(nil)

// --- requests ---------------------------------------------------------------

// maxActs bounds a request: a handful of acts a person can read and confirm.
const maxActs = 10

// Propose is the request tool: acts the answer asks the person to confirm,
// already checked against their tools (the API's). Only the session's own
// token proposes, while its question is being answered; a second request
// in one answer replaces the first. Nothing runs until the person confirms.
func (svc *Service) Propose(ctx context.Context, tokenID, person, sessionID string, r Request) ([]string, error) {
	s, err := svc.session(ctx, person, sessionID)
	if err != nil {
		return nil, err
	}
	if tokenID == "" || s.TokenID != tokenID {
		return nil, ErrNotYours
	}
	if !pages[s.Kind].Acts {
		return []string{"this session fills its page and requests nothing: acting on the platform is the global assistant's, behind the assistant button at the top right of every page"}, nil
	}
	if len(r.Acts) == 0 || len(r.Acts) > maxActs || strings.TrimSpace(r.Why) == "" {
		return []string{fmt.Sprintf("a request is why (one line for the person) and 1 to %d acts", maxActs)}, nil
	}
	svc.mu.Lock()
	svc.init()
	t := svc.turns[sessionID]
	svc.mu.Unlock()
	if t == nil {
		return []string{"no question is being answered: request only while answering one"}, nil
	}
	raw, _ := json.Marshal(r)
	if t.red.holds(string(raw)) {
		return []string{"the request holds a credential of this session (a key, a token, a login): never put one in an act"}, nil
	}
	r.State, r.Reported = RequestPending, false
	for i := range r.Acts {
		r.Acts[i].Status, r.Acts[i].Result = 0, ""
	}
	if err := CheckRequest(r); err != nil {
		return []string{err.Error()}, nil
	}
	svc.mu.Lock()
	t.request = &r
	svc.mu.Unlock()
	svc.hub.publish(person, Frame{Kind: "request", Session: sessionID, Message: &Message{SessionID: sessionID, Role: RoleAssistant, Request: &r}})
	return nil, nil
}

// maxResult bounds what an act's outcome keeps of its answer.
const maxResult = 2 << 10

// Decide is the person's answer to a request: confirmed, each act runs in
// order through run (the API's route, as the person) until one fails; the
// rest are not run. Declined, none is. The outcome reaches the assistant
// in the session's next prompt.
func (svc *Service) Decide(ctx context.Context, person, sessionID string, seq int64, confirm bool,
	run func(ctx context.Context, a Act) (int, string)) (Message, error) {
	if _, err := svc.session(ctx, person, sessionID); err != nil {
		return Message{}, err
	}
	key := fmt.Sprintf("%s/%d", sessionID, seq)
	svc.mu.Lock()
	svc.init()
	if svc.deciding[key] {
		svc.mu.Unlock()
		return Message{}, errors.Join(ErrStale, errors.New("this request is being decided"))
	}
	svc.deciding[key] = true
	svc.mu.Unlock()
	defer func() {
		svc.mu.Lock()
		delete(svc.deciding, key)
		svc.mu.Unlock()
	}()
	msgs, err := svc.Store.AssistantMessages(ctx, sessionID, seq-1)
	if err != nil {
		return Message{}, err
	}
	if len(msgs) == 0 || msgs[0].Seq != seq || msgs[0].Request == nil {
		return Message{}, errors.Join(ErrNotFound, errors.New("no request there"))
	}
	m := msgs[0]
	r := *m.Request
	r.Acts = slices.Clone(r.Acts)
	if r.State != RequestPending {
		return Message{}, errors.Join(ErrStale, fmt.Errorf("this request is %s already", r.State))
	}
	// The outcome is kept whatever the caller does now: the acts ran.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if !confirm {
		r.State = RequestDeclined
		if err := svc.Store.SetAssistantRequest(sctx, sessionID, seq, RequestPending, r); err != nil {
			return Message{}, err
		}
	} else {
		// Claimed before anything runs: a second click, another tab or an
		// engine that stops mid-way never runs these acts again.
		r.State = RequestRunning
		if err := svc.Store.SetAssistantRequest(sctx, sessionID, seq, RequestPending, r); err != nil {
			return Message{}, err
		}
		running := r
		svc.hub.publish(person, Frame{Kind: "request", Session: sessionID, Message: &Message{SessionID: sessionID, Seq: seq, Role: m.Role, Text: m.Text, Fill: m.Fill, Request: &running, At: m.At}})
		r.Acts = slices.Clone(r.Acts)
		for i := range r.Acts {
			status, result := run(ctx, r.Acts[i])
			if len(result) > maxResult {
				cut := maxResult
				for cut > 0 && !utf8.RuneStart(result[cut]) {
					cut--
				}
				result = result[:cut] + " [cut]"
			}
			r.Acts[i].Status, r.Acts[i].Result = status, result
			if status >= 400 || status == 0 {
				break
			}
		}
		r.State = RequestDone
		if err := svc.Store.SetAssistantRequest(sctx, sessionID, seq, RequestRunning, r); err != nil {
			return Message{}, err
		}
	}
	m.Request = &r
	svc.hub.publish(person, Frame{Kind: "request", Session: sessionID, Message: &m})
	return m, nil
}

// outcomes are what the assistant has not been told of its requests: the
// prompt's lines, and the messages they come from.
func (svc *Service) outcomes(ctx context.Context, sessionID string) (string, []Message) {
	msgs, err := svc.Store.AssistantMessages(ctx, sessionID, 0)
	if err != nil {
		return "", nil
	}
	var b strings.Builder
	var told []Message
	for _, m := range msgs {
		r := m.Request
		if r == nil || r.Reported {
			continue
		}
		if r.State == RequestRunning {
			svc.mu.Lock()
			now := svc.deciding[fmt.Sprintf("%s/%d", sessionID, m.Seq)]
			svc.mu.Unlock()
			if now {
				continue // running as we speak: its outcome comes next time
			}
		}
		if b.Len() == 0 {
			b.WriteString("\nWhat came of your requests to the person:\n")
		}
		switch r.State {
		case RequestPending:
			fmt.Fprintf(&b, "- %q: not decided yet (the person has not confirmed it).\n", r.Why)
			continue
		case RequestDeclined:
			fmt.Fprintf(&b, "- %q: declined; nothing was done.\n", r.Why)
		case RequestRunning:
			fmt.Fprintf(&b, "- %q: confirmed, but its acts were interrupted: what they did is unknown; read the platform before asking again.\n", r.Why)
		default:
			fmt.Fprintf(&b, "- %q: confirmed.\n", r.Why)
			for _, a := range r.Acts {
				switch {
				case a.Status == 0:
					fmt.Fprintf(&b, "  - %s: not run (an earlier act failed).\n", a.Tool)
				case a.Status >= 400:
					fmt.Fprintf(&b, "  - %s: failed, %d: %s\n", a.Tool, a.Status, a.Result)
				default:
					fmt.Fprintf(&b, "  - %s: done, %d: %s\n", a.Tool, a.Status, a.Result)
				}
			}
		}
		told = append(told, m)
	}
	return b.String(), told
}

// reported marks outcomes as told.
func (svc *Service) reported(ctx context.Context, sessionID string, told []Message) {
	for _, m := range told {
		r := *m.Request
		r.Reported = true
		if err := svc.Store.SetAssistantRequest(ctx, sessionID, m.Seq, m.Request.State, r); err != nil {
			svc.log().Warn("assistant: mark a request reported", "session", sessionID, "seq", m.Seq, "err", err)
		}
	}
}
