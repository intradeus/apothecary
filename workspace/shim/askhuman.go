package main

// ask_human MCP stdio server (DESIGN.md §10).
//
// Exposes one tool: ask_human{type, question, context?, fields?,
// artifacts?}. Its fields are ACP's elicitation form — the same kinds of
// question a harness's own question tool asks over ACP (elicitation.go), so
// both reach the human as one kind of ping. The tool call BLOCKS until the human answers: the agent runs
// as a long-lived ACP session the engine follows, so nothing needs the turn
// handed back any more, and the model never sees a "posted, stop now" notice
// it might ignore.
//
// Protocol with the engine (per-run bearer token from /apothecary/run.env,
// re-read on 401 because the engine rotates tokens on restart):
//
//	POST /api/runs/{run}/pings      raise it (idempotency key: a retry after a
//	                                lost response never asks twice); holds
//	                                briefly, 200 = answered already, 204 + X-Ping-Id
//	                                = still open
//	GET  /api/runs/{run}/pings/{id} long-poll until answered (200), still open
//	                                (204, poll again) or parked (410)
//
// While it waits it sends MCP progress notifications, which harnesses use to
// keep a slow tool call alive (opencode resets its timeout on progress). A
// question nobody answers for a long time is the ENGINE's call: it parks the
// run by cancelling the turn, which cancels this call (notifications/cancelled)
// — and the answer is then delivered when the session resumes. Transport
// errors (an engine restart) are retried with backoff.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Protocol knobs — vars, not consts, so tests can compress time and point
// the env file at a fixture.
var (
	runEnvPath = "/apothecary/run.env"
	// graceHold is how long the raising POST asks the engine to hold for an
	// answer that is already coming; the rest of the wait is long-polls.
	graceHold        = 20 * time.Second
	pollInterval     = 5 * time.Second
	progressInterval = 10 * time.Second
	unreachableLimit = 10 * time.Minute
	backoffBase      = time.Second
	backoffCap       = 60 * time.Second
)

func readRunEnv() map[string]string {
	env := map[string]string{}
	data, err := os.ReadFile(runEnvPath)
	if err != nil {
		return env
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if i := strings.Index(line, "="); i > 0 {
			env[line[:i]] = line[i+1:]
		}
	}
	return env
}

// httpClient's timeout must exceed the engine's 55 s long-poll hold.
var httpClient = &http.Client{Timeout: 70 * time.Second}

func callEngine(method, path string, body any, env map[string]string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, env["APOTHECARY_ENGINE_URL"]+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+env["APOTHECARY_RUN_TOKEN"])
	req.Header.Set("Content-Type", "application/json")
	return httpClient.Do(req)
}

type askArgs struct {
	Type     string `json:"type"`
	Question string `json:"question"`
	Context  string `json:"context"`
	// Fields are the form, passed to the engine as given — it validates
	// them (internal/run/event.Field is the shape).
	Fields    []json.RawMessage `json:"fields"`
	Artifact  string            `json:"artifact"`  // legacy single ref
	Artifacts []string          `json:"artifacts"` // multi refs
}

// humanAnswer is what came back: Text is the engine-composed answer the
// agent reads; Answer (accept|decline) and Content are the raw form answer
// an elicitation is answered with. Parked: the run was parked before an
// answer came, which the session receives when it resumes.
type humanAnswer struct {
	Text    string
	Answer  string
	Content map[string]any
	Parked  bool
}

// errCancelled: the client cancelled the call (the engine parked the run and
// cancelled the turn). No reply is due for a cancelled request.
var errCancelled = errors.New("cancelled")

func newKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// askHuman drives the protocol until the human answers, the call is
// cancelled (done closes), or the engine stays unreachable too long. tick is
// called while waiting (MCP progress).
func askHuman(args askArgs, done <-chan struct{}, tick func()) (humanAnswer, error) {
	env := readRunEnv()
	refs := args.Artifacts
	if len(refs) == 0 && args.Artifact != "" {
		refs = []string{args.Artifact}
	}
	key := newKey()
	pingID := ""
	var downSince time.Time
	backoff := backoffBase

	// Progress keeps flowing for the whole wait, including long-polls and
	// retries, so the harness never decides the tool hung.
	stopTick := make(chan struct{})
	defer close(stopTick)
	if tick != nil {
		go func() {
			t := time.NewTicker(progressInterval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					tick()
				case <-stopTick:
					return
				}
			}
		}()
	}
	sleep := func(d time.Duration) bool {
		select {
		case <-time.After(d):
			return true
		case <-done:
			return false
		}
	}

	for {
		select {
		case <-done:
			return humanAnswer{}, errCancelled
		default:
		}
		var res *http.Response
		var err error
		if pingID == "" {
			res, err = callEngine("POST", "/api/runs/"+env["APOTHECARY_RUN_ID"]+"/pings", map[string]any{
				"type":      args.Type,
				"question":  args.Question,
				"context":   args.Context,
				"fields":    append([]json.RawMessage{}, args.Fields...),
				"artifacts": append([]string{}, refs...),
				"hold_ms":   graceHold.Milliseconds(),
				"key":       key,
			}, env)
		} else {
			res, err = callEngine("GET", "/api/runs/"+env["APOTHECARY_RUN_ID"]+"/pings/"+pingID, nil, env)
		}
		if err != nil {
			if downSince.IsZero() {
				downSince = time.Now()
			}
			if time.Since(downSince) > unreachableLimit {
				return humanAnswer{}, fmt.Errorf("ask_human unavailable: %v", err)
			}
			if !sleep(backoff) {
				return humanAnswer{}, errCancelled
			}
			backoff = min(backoff*2, backoffCap)
			continue
		}
		downSince = time.Time{}
		backoff = backoffBase

		switch res.StatusCode {
		case http.StatusUnauthorized:
			// Engine restarted and rotated the run token; pick up the fresh one.
			drain(res)
			if !sleep(pollInterval) {
				return humanAnswer{}, errCancelled
			}
			env = readRunEnv()
		case http.StatusOK:
			return decodeAnswer(res)
		case http.StatusNoContent:
			if id := res.Header.Get("X-Ping-Id"); id != "" {
				pingID = id
			}
			drain(res)
			if pingID == "" {
				return humanAnswer{}, errors.New("ask_human failed: engine did not return the question's id")
			}
		case http.StatusGone:
			drain(res)
			// Parked: the engine is ending this turn and will deliver the
			// answer when the session resumes.
			return humanAnswer{Text: parkedNotice(pingID), Parked: true}, nil
		default:
			text, _ := io.ReadAll(io.LimitReader(res.Body, 300))
			res.Body.Close()
			return humanAnswer{}, fmt.Errorf("ask_human failed: engine returned %d: %s", res.StatusCode, text)
		}
	}
}

func decodeAnswer(res *http.Response) (humanAnswer, error) {
	var body struct {
		Text    string         `json:"text"` // engine-composed answer
		Answer  string         `json:"answer"`
		Content map[string]any `json:"content"`
	}
	err := json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if err != nil {
		return humanAnswer{}, fmt.Errorf("ask_human failed: bad engine response: %v", err)
	}
	return humanAnswer{Text: body.Text, Answer: body.Answer, Content: body.Content}, nil
}

// parkedNotice is returned in the rare race where the call outlives the park
// (the engine cancels the turn, so normally nobody reads this).
func parkedNotice(pingID string) string {
	id := ""
	if pingID != "" {
		id = " (id " + pingID + ")"
	}
	return "Your question" + id + " is still waiting for the human, and this run has been parked " +
		"until they answer. Stop here without guessing; their answer will arrive as your next " +
		"message in this same session."
}

func drain(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
}

// --- minimal MCP over stdio (JSON-RPC 2.0, one message per line) ------------

var toolDef = map[string]any{
	"name": "ask_human",
	"description": "Ask the human operator a question and wait for their answer. Use when " +
		"blocked: unclear requirements (type=clarification), an obstacle you " +
		"cannot resolve (type=blocker), or an action needing approval " +
		"(type=permission). The call waits until the human answers and returns " +
		"their answer as text, so you never have to guess — do not work around " +
		"a slow answer. Without fields the human answers in free text. With " +
		"fields you get a form, answered field by field: a string field with " +
		"options is one pick (radio buttons — branch on the returned option id), " +
		"an array field with options any number of picks (checkboxes; the " +
		"result reads 'selected: id, id' or 'none selected'), and string, " +
		"number, integer and boolean fields are typed inputs. One field returns " +
		"its value; several return one 'id: value' line each. The human may " +
		"always decline instead: the result then starts with 'declined —' " +
		"followed by their reason — handle that rather than assuming an answer. " +
		"Never add an 'other' option yourself. To have the human review " +
		"documents you wrote, pass artifacts with repo-relative paths (e.g. " +
		"[\"artifacts/comparison.md\"]): they see them rendered with inline commenting.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":     map[string]any{"type": "string", "enum": []string{"clarification", "blocker", "permission"}},
			"question": map[string]any{"type": "string", "description": "The question for the human"},
			"context":  map[string]any{"type": "string", "description": "Optional supporting context (markdown renders)"},
			"fields": map[string]any{
				"type":        "array",
				"description": "Optional form (at most 20 fields), in the order the human reads them; omit for one free-text answer",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"id", "type"},
					"properties": map[string]any{
						"id":          map[string]any{"type": "string", "description": "what the answer is keyed by; one line"},
						"type":        map[string]any{"type": "string", "enum": []string{"string", "number", "integer", "boolean", "array"}},
						"title":       map[string]any{"type": "string", "description": "the question this field asks (defaults to the id)"},
						"description": map[string]any{"type": "string", "description": "an optional second line"},
						"required":    map[string]any{"type": "boolean"},
						"options": map[string]any{
							"type":        "array",
							"description": "string: pick one; array: pick any (required for array). At most 50.",
							"items": map[string]any{"anyOf": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "object", "required": []string{"id"}, "properties": map[string]any{
									"id":     map[string]any{"type": "string", "description": "what the answer names; one line"},
									"label":  map[string]any{"type": "string", "description": "what the human reads (defaults to the id)"},
									"detail": map[string]any{"type": "string", "description": "an optional second line"},
								}},
							}},
						},
						"format":     map[string]any{"type": "string", "enum": []string{"email", "uri", "date", "date-time"}},
						"pattern":    map[string]any{"type": "string", "description": "string: a regular expression the answer must match"},
						"min_length": map[string]any{"type": "integer"},
						"max_length": map[string]any{"type": "integer"},
						"minimum":    map[string]any{"type": "number"},
						"maximum":    map[string]any{"type": "number"},
						"min_items":  map[string]any{"type": "integer"},
						"max_items":  map[string]any{"type": "integer"},
						"default":    map[string]any{"description": "pre-fills the field: a value of its type, or (array) a list of option ids"},
					},
				},
			},
			"artifacts": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional repo-relative files for the human to review (e.g. [\"artifacts/plan.md\"]); the first shows in the rich review pane, the rest as attachments",
			},
		},
		"required": []string{"type", "question"},
	},
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

var (
	stdoutMu sync.Mutex
	// mcpOut is swapped by tests.
	mcpOut io.Writer = os.Stdout
)

func send(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	mcpOut.Write(append(data, '\n'))
}

func reply(id json.RawMessage, result any) {
	send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func replyErr(id json.RawMessage, code int, message string) {
	send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func runAskHuman() { serveMCP(os.Stdin, underAgent(os.Getenv, os.Getppid())) }

// agentEnv marks the agent processes the sidecar starts (acp.go). ask_human
// is the agent's line to the human through the run; the harness configs that
// register it (codex's config.toml, opencode.json) are the user's too, and
// the human driving codex or opencode themselves — the editor extension, the
// terminal — must not reach the run with it.
const agentEnv = "APOTHECARY_AGENT"

// procDir is procfs, a var for tests.
var procDir = "/proc"

// underAgent reports whether this server runs below an agent process: the
// marker in its own environment, or in an ancestor's — codex hands its MCP
// servers an allowlisted environment, so the marker stops at codex itself.
// An ancestor whose environ cannot be read is passed over, not trusted.
func underAgent(getenv func(string) string, ppid int) bool {
	if getenv(agentEnv) != "" {
		return true
	}
	pid := ppid
	for range 64 {
		if pid <= 1 {
			return false
		}
		if env, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "environ")); err == nil {
			for kv := range bytes.SplitSeq(env, []byte{0}) {
				if bytes.HasPrefix(kv, []byte(agentEnv+"=")) && len(kv) > len(agentEnv)+1 {
					return true
				}
			}
		}
		stat, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
		if err != nil {
			return false
		}
		// pid (comm) state ppid …: comm may hold spaces and parens, so
		// the fields start after its LAST ')'.
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			return false
		}
		f := strings.Fields(string(stat[i+1:]))
		if len(f) < 2 {
			return false
		}
		if pid, err = strconv.Atoi(f[1]); err != nil {
			return false
		}
	}
	return false
}

// inflight tracks running tool calls by JSON-RPC id, so a cancellation can
// stop the matching wait.
type inflight struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

var calls = &inflight{m: map[string]chan struct{}{}}

func (f *inflight) add(id string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{})
	f.m[id] = ch
	return ch
}

func (f *inflight) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, id)
}

func (f *inflight) cancel(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.m[id]; ok {
		close(ch)
		delete(f.m, id)
	}
}

// serveMCP handles line-delimited JSON-RPC from in until EOF; replies go
// through send() (mcpOut). offered=false (not below an agent, underAgent)
// serves a toolless server: the human's harness starts it without error and
// sees nothing to call.
func serveMCP(in io.Reader, offered bool) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20) // agent prompts can be large
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg rpcMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			if msg.Method == "notifications/cancelled" {
				var c struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				_ = json.Unmarshal(msg.Params, &c)
				calls.cancel(string(c.RequestID))
			}
			continue // other notifications need no action
		}
		switch msg.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2024-11-05"
			}
			reply(msg.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "apothecary-ask-human", "version": "2.0.0"},
			})
		case "tools/list":
			tools := []any{}
			if offered {
				tools = append(tools, toolDef)
			}
			reply(msg.ID, map[string]any{"tools": tools})
		case "tools/call":
			var p struct {
				Name      string  `json:"name"`
				Arguments askArgs `json:"arguments"`
				Meta      struct {
					ProgressToken json.RawMessage `json:"progressToken"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Name != "ask_human" || !offered {
				replyErr(msg.ID, -32602, "unknown tool "+p.Name)
				continue
			}
			// Concurrent calls allowed; send() serializes the stdout frames.
			done := calls.add(string(msg.ID))
			go func(id json.RawMessage, args askArgs, token json.RawMessage) {
				defer calls.remove(string(id))
				var tick func()
				if len(token) > 0 && string(token) != "null" {
					n := 0
					tick = func() {
						n++
						send(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress",
							"params": map[string]any{"progressToken": token, "progress": n,
								"message": "waiting for the human to answer"}})
					}
				}
				ans, err := askHuman(args, done, tick)
				if errors.Is(err, errCancelled) {
					return // the client gave up on this call; nothing is owed
				}
				if err != nil {
					// Tool-level error string (§10), not a protocol error:
					// the agent sees it and proceeds.
					reply(id, map[string]any{
						"content": []any{map[string]any{"type": "text", "text": err.Error()}},
						"isError": true,
					})
					return
				}
				reply(id, map[string]any{
					"content": []any{map[string]any{"type": "text", "text": ans.Text}},
				})
			}(msg.ID, p.Arguments, p.Meta.ProgressToken)
		case "ping":
			reply(msg.ID, map[string]any{})
		default:
			replyErr(msg.ID, -32601, "method "+msg.Method+" not found")
		}
	}
}
