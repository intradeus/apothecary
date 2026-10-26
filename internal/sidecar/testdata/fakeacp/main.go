// fakeacp is a scripted ACP agent for integration tests: the same wire
// protocol as `opencode acp` / `codex-acp`, with behaviour chosen by words in
// the prompt.
//
//	WAIT      block until session/cancel, end with stopReason "cancelled"
//	SLOW      say something, then end after 400ms (or on cancel)
//	FAIL      answer the prompt with a JSON-RPC error
//	MCP       say the mcpServers the session was opened (or loaded) with
//	(else)    echo the prompt back, with a tool call and a plan
//
// Every completed prompt adds $0.10 to the session's cost (usage_update).
// It takes remote (http) MCP servers unless FAKEACP_NO_HTTP_MCP is set.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	var omu sync.Mutex
	write := func(v any) {
		data, _ := json.Marshal(v)
		omu.Lock()
		out.Write(append(data, '\n'))
		out.Flush()
		omu.Unlock()
	}
	update := func(sid string, u map[string]any) {
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update",
			"params": map[string]any{"sessionId": sid, "update": u}})
	}
	var mu sync.Mutex
	cancels := map[string]chan struct{}{}
	cost := map[string]float64{}
	mcps := map[string]json.RawMessage{}
	nsess := 0
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &f) != nil || f.Method == "" {
			continue
		}
		var p struct {
			SessionID  string          `json:"sessionId"`
			ModeID     string          `json:"modeId"`
			MCPServers json.RawMessage `json:"mcpServers"`
			Prompt     []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(f.Params, &p)
		id := f.ID
		res := func(v any) { write(map[string]any{"jsonrpc": "2.0", "id": id, "result": v}) }
		fail := func(msg string) {
			write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000, "message": msg}})
		}
		switch f.Method {
		case "initialize":
			res(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true,
				"mcpCapabilities": map[string]any{"http": os.Getenv("FAKEACP_NO_HTTP_MCP") == ""}}})
		case "session/new":
			mu.Lock()
			nsess++
			sid := fmt.Sprintf("s%d", nsess)
			mcps[sid] = p.MCPServers
			mu.Unlock()
			res(map[string]any{"sessionId": sid,
				"modes": map[string]any{"availableModes": []any{map[string]any{"id": "builder"}, map[string]any{"id": "planner"}}}})
		case "session/load":
			mu.Lock()
			mcps[p.SessionID] = p.MCPServers
			mu.Unlock()
			res(map[string]any{})
		case "session/set_mode", "session/set_model":
			res(map[string]any{})
		case "session/cancel":
			mu.Lock()
			if ch := cancels[p.SessionID]; ch != nil {
				close(ch)
				delete(cancels, p.SessionID)
			}
			mu.Unlock()
		case "session/prompt":
			text := ""
			if len(p.Prompt) > 0 {
				text = p.Prompt[0].Text
			}
			sid := p.SessionID
			cancel := make(chan struct{})
			mu.Lock()
			cancels[sid] = cancel
			mu.Unlock()
			go func() {
				finish := func(stop string) {
					mu.Lock()
					cost[sid] += 0.10
					c := cost[sid]
					delete(cancels, sid)
					mu.Unlock()
					update(sid, map[string]any{"sessionUpdate": "usage_update", "used": 10, "size": 100,
						"cost": map[string]any{"amount": c, "currency": "USD"}})
					res(map[string]any{"stopReason": stop, "usage": map[string]any{"inputTokens": 7, "outputTokens": 3}})
				}
				switch {
				case strings.Contains(text, "FAIL"):
					fail("model exploded")
				case strings.Contains(text, "MCP"):
					mu.Lock()
					servers := string(mcps[sid])
					mu.Unlock()
					update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "mcp: " + servers}})
					finish("end_turn")
				case strings.Contains(text, "WAIT"):
					update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "waiting"}})
					<-cancel
					finish("cancelled")
				case strings.Contains(text, "SLOW"):
					update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "working slowly"}})
					select {
					case <-time.After(400 * time.Millisecond):
						finish("end_turn")
					case <-cancel:
						finish("cancelled")
					}
				default:
					update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "echo: " + text}})
					update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Read go.mod", "kind": "read", "status": "pending"})
					update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed",
						"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "module x"}}}})
					update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": " done"}})
					update(sid, map[string]any{"sessionUpdate": "plan", "entries": []any{map[string]any{"content": "step", "status": "completed"}}})
					finish("end_turn")
				}
			}()
		default:
			fail("method not found")
		}
	}
}
