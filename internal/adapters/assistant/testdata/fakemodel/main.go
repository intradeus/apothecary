// The live test's model (live_k8s_test.go): a fake Anthropic Messages API,
// run in the cluster beside the assistant pod. QUESTION_READ calls the
// harness's read tool on $FILE, QUESTION_HISTORY its git tool on $REPO,
// QUESTION_SIBLING the read tool on $SIBLING; a tool result is echoed back
// as the answer. Every request is logged.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// calls numbers the tool calls: a conversation must not hold two with one id.
var calls atomic.Int64

func main() {
	http.HandleFunc("/", serve)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	log.Printf("%s %s %d bytes", r.Method, r.URL.Path, len(raw))
	if !strings.HasSuffix(r.URL.Path, "/messages") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Tools []struct {
			Name   string `json:"name"`
			Schema struct {
				Props map[string]any `json:"properties"`
			} `json:"input_schema"`
		} `json:"tools"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(raw, &body)
	// The latest message that is a question or a tool result: a harness may
	// add a message of its own after the person's (claude-code's first turn).
	last := ""
	for i := len(body.Messages) - 1; i >= 0; i-- {
		m := string(body.Messages[i].Content)
		if strings.Contains(m, "tool_result") || strings.Contains(m, "QUESTION_") {
			last = m
			break
		}
	}
	read := func(target string) [][2]string {
		for _, tl := range body.Tools {
			if strings.EqualFold(tl.Name, "read") {
				for p := range tl.Schema.Props {
					if strings.Contains(strings.ToLower(p), "path") {
						in, _ := json.Marshal(map[string]string{p: target})
						return toolUse(tl.Name, string(in))
					}
				}
			}
		}
		return text("no read tool")
	}
	turn := text("pong")
	switch {
	case strings.Contains(last, "tool_result"):
		turn = text("ANSWER: " + last)
	case strings.Contains(last, "QUESTION_READ"):
		turn = read(os.Getenv("FILE"))
	case strings.Contains(last, "QUESTION_SIBLING"):
		turn = read(os.Getenv("SIBLING"))
	case strings.Contains(last, "QUESTION_HISTORY"):
		for _, tl := range body.Tools {
			if strings.HasSuffix(tl.Name, "git") {
				in, _ := json.Marshal(map[string]any{"repo": os.Getenv("REPO"), "args": []string{"log", "--format=%s", "-n", "5", "--", "README"}})
				turn = toolUse(tl.Name, string(in))
			}
		}
	}
	log.Printf("  -> %s", turn[1][1])
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range turn {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev[0], ev[1])
	}
}

func text(s string) [][2]string {
	return [][2]string{
		{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"acme-1","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, s)},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
}

func toolUse(name, in string) [][2]string {
	id := fmt.Sprintf("toolu_%d", calls.Add(1))
	return [][2]string{
		{"message_start", `{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"acme-1","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`},
		{"content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, id, name)},
		{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, in)},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":1}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
}
