package main

// ask_human MCP stdio server (DESIGN.md §10) — the Go port of the retired
// ask-human.mjs, protocol-identical:
//
// Exposes one tool: ask_human{type, question, context?, options?,
// artifact?}. Protocol with the engine: POST the ping (per-run bearer
// token); on 204 re-poll every 5 s (the server holds 55 s) indefinitely —
// the stage timeout is suspended while a ping is pending. On network error
// retry with backoff capped at 60 s; if the engine is unreachable > 10
// min, return a tool error string and let the agent proceed. Credentials
// come from /apothecary/run.env and are re-read on 401 (the engine
// regenerates tokens on restart).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Protocol knobs — vars, not consts, so tests can compress time and point
// the env file at a fixture.
var (
	runEnvPath       = "/apothecary/run.env"
	pollInterval     = 5 * time.Second
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
	Type      string   `json:"type"`
	Question  string   `json:"question"`
	Context   string   `json:"context"`
	Options   []string `json:"options"`
	Artifact  string   `json:"artifact"`  // legacy single ref
	Artifacts []string `json:"artifacts"` // multi refs
}

// askHuman drives the full §10 protocol; returns the answer text or an
// error (surfaced as a tool-level error string, not a protocol error).
func askHuman(args askArgs) (string, error) {
	env := readRunEnv()
	refs := args.Artifacts
	if len(refs) == 0 && args.Artifact != "" {
		refs = []string{args.Artifact}
	}
	pingID := ""
	var downSince time.Time
	backoff := backoffBase

	for {
		var res *http.Response
		var err error
		if pingID == "" {
			res, err = callEngine("POST", "/api/runs/"+env["APOTHECARY_RUN_ID"]+"/pings", map[string]any{
				"type":      args.Type,
				"question":  args.Question,
				"context":   args.Context,
				"options":   append([]string{}, args.Options...),
				"artifacts": append([]string{}, refs...),
			}, env)
		} else {
			res, err = callEngine("GET", "/api/runs/"+env["APOTHECARY_RUN_ID"]+"/pings/"+pingID, nil, env)
		}
		if err != nil {
			if downSince.IsZero() {
				downSince = time.Now()
			}
			if time.Since(downSince) > unreachableLimit {
				return "", fmt.Errorf("ask_human unavailable: %v", err)
			}
			time.Sleep(backoff)
			backoff = min(backoff*2, backoffCap)
			continue
		}
		downSince = time.Time{}
		backoff = backoffBase

		switch res.StatusCode {
		case http.StatusUnauthorized:
			// Engine restarted and rotated the run token; pick up the fresh one.
			drain(res)
			time.Sleep(pollInterval)
			env = readRunEnv()
		case http.StatusOK:
			var body struct {
				Answer    string   `json:"answer"`
				Feedback  string   `json:"feedback"`
				Artifacts []string `json:"artifacts"` // workspace paths of the human's attachments
			}
			err := json.NewDecoder(res.Body).Decode(&body)
			res.Body.Close()
			if err != nil {
				return "", fmt.Errorf("ask_human failed: bad engine response: %v", err)
			}
			// Free-text answers arrive as answer:"text" + the text in
			// feedback; option answers keep the chosen option in answer with
			// an optional note in feedback (PLAN4 M16). "other" is the
			// hardcoded escape hatch: the human rejected every offered option
			// and the note IS their answer — render it unambiguously so the
			// agent can't misread it as a chosen option.
			var answer string
			switch {
			case body.Answer == "text":
				answer = body.Feedback
			case body.Answer == "other":
				answer = "other — none of the offered options fit; the human answered: " + body.Feedback
			case body.Feedback != "":
				answer = body.Answer + "\n(note: " + body.Feedback + ")"
			default:
				answer = body.Answer
			}
			if len(body.Artifacts) > 0 {
				answer += "\n\nThe human attached these files — read them with your tools:\n- " + strings.Join(body.Artifacts, "\n- ")
			}
			return answer, nil
		case http.StatusNoContent:
			if id := res.Header.Get("X-Ping-Id"); id != "" {
				pingID = id
			}
			drain(res)
			time.Sleep(pollInterval)
		default:
			text, _ := io.ReadAll(io.LimitReader(res.Body, 300))
			res.Body.Close()
			return "", fmt.Errorf("ask_human failed: engine returned %d: %s", res.StatusCode, text)
		}
	}
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
		"(type=permission). The answer is returned as text. For closed " +
		"questions, pass options — the human picks exactly one (buttons in " +
		"their UI) and you can branch on the returned option deterministically. " +
		"The human ALWAYS also has an automatic 'other' escape hatch: if none " +
		"of your options fit, the result starts with 'other —' followed by " +
		"their free-text answer — handle that case rather than assuming one of " +
		"your options. To have the human review documents you wrote, pass " +
		"artifacts with repo-relative paths (e.g. [\"artifacts/comparison.md\"]): " +
		"they see them rendered with inline commenting.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":     map[string]any{"type": "string", "enum": []string{"clarification", "blocker", "permission"}},
			"question": map[string]any{"type": "string", "description": "The question for the human"},
			"context":  map[string]any{"type": "string", "description": "Optional supporting context (markdown renders)"},
			"options": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Optional closed answer set (max 8); the answer will be one of these, or 'other' " +
					"with the human's own text (added automatically — never declare 'other' yourself)",
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

func runAskHuman() { serveMCP(os.Stdin) }

// serveMCP handles line-delimited JSON-RPC from in until EOF; replies go
// through send() (mcpOut).
func serveMCP(in io.Reader) {
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
			continue // notification
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
			reply(msg.ID, map[string]any{"tools": []any{toolDef}})
		case "tools/call":
			var p struct {
				Name      string  `json:"name"`
				Arguments askArgs `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Name != "ask_human" {
				replyErr(msg.ID, -32602, "unknown tool "+p.Name)
				continue
			}
			// Concurrent calls allowed (parity with the async mjs handler);
			// send() serializes the stdout frames.
			go func(id json.RawMessage, args askArgs) {
				answer, err := askHuman(args)
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
					"content": []any{map[string]any{"type": "text", "text": answer}},
				})
			}(msg.ID, p.Arguments)
		case "ping":
			reply(msg.ID, map[string]any{})
		default:
			replyErr(msg.ID, -32601, "method "+msg.Method+" not found")
		}
	}
}
