package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// codexForm is what codex-acp sends for request_user_input: one question
// picked from options with codex's own "None of the above" and a note field,
// one free-text, one secret.
const codexForm = `{
  "sessionId": "s1", "toolCallId": "call_1", "mode": "form",
  "message": "Codex needs your input to continue.",
  "requestedSchema": {
    "type": "object",
    "properties": {
      "db": {"type": "string", "title": "Which database?", "description": "Storage",
             "oneOf": [{"const": "Postgres", "title": "Postgres", "description": "the big one"},
                       {"const": "None of the above", "title": "None of the above"}],
             "_meta": {"codex": {"isOther": true, "isSecret": false}}},
      "db_note": {"type": "string", "title": "Additional answer or note",
                  "_meta": {"codex": {"questionId": "db", "role": "user_note"}}},
      "token": {"type": "string", "title": "API token", "_meta": {"codex": {"isSecret": true}}},
      "name": {"type": "string", "title": "Service name", "minLength": 2}
    },
    "required": ["db", "token", "name"]
  }
}`

// The schema's properties map onto ping fields in the order written; a
// secret is never asked.
func TestElicitFields(t *testing.T) {
	var r struct {
		RequestedSchema struct {
			Properties json.RawMessage `json:"properties"`
			Required   []string        `json:"required"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal([]byte(codexForm), &r); err != nil {
		t.Fatal(err)
	}
	fields, secrets, err := elicitFields(r.RequestedSchema.Properties, r.RequestedSchema.Required)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, f := range fields {
		ids = append(ids, f.ID)
	}
	if strings.Join(ids, ",") != "db,db_note,name" || len(secrets) != 1 || secrets[0] != "token" {
		t.Fatalf("ids = %v, secrets = %v", ids, secrets)
	}
	db := fields[0]
	if !db.Required || db.Title != "Which database?" || len(db.Options) != 2 || db.Options[0] != (pingOption{ID: "Postgres", Label: "Postgres", Detail: "the big one"}) {
		t.Errorf("db = %+v", db)
	}
	if fields[1].Required || fields[2].MinLength == nil || *fields[2].MinLength != 2 {
		t.Errorf("fields = %+v", fields)
	}

	// MCP's other spellings: a bare enum with enumNames, a multi-select's
	// items, numbers and booleans.
	props := `{"color": {"type": "string", "enum": ["r", "g"], "enumNames": ["Red", "Green"]},
	           "tags": {"type": "array", "items": {"anyOf": [{"const": "a", "title": "A"}]}, "maxItems": 1},
	           "plain": {"type": "array", "items": {"type": "string", "enum": ["x", "y"]}},
	           "n": {"type": "integer", "minimum": 1, "maximum": 9, "default": 3},
	           "ok": {"type": "boolean"}}`
	fields, _, err = elicitFields(json.RawMessage(props), nil)
	if err != nil {
		t.Fatal(err)
	}
	if fields[0].Options[1] != (pingOption{ID: "g", Label: "Green"}) || fields[1].Options[0].Label != "A" || *fields[1].MaxItems != 1 ||
		len(fields[2].Options) != 2 || *fields[3].Maximum != 9 || fields[3].Default != 3.0 || fields[4].Type != "boolean" {
		t.Errorf("fields = %+v", fields)
	}
}

// fakeEngine answers a raised question with the given answer, recording
// what was raised.
type fakeEngine struct {
	mu     sync.Mutex
	raised []map[string]any
	answer string // the 200 body; "" = 204 then 410 (parked)
}

func (f *fakeEngine) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.raised = append(f.raised, body)
			f.mu.Unlock()
			w.Header().Set("X-Ping-Id", "p1")
			if f.answer == "" {
				w.WriteHeader(204)
				return
			}
			_, _ = io.WriteString(w, f.answer)
			return
		}
		w.WriteHeader(http.StatusGone)
	}))
	t.Cleanup(srv.Close)
	writeTestEnv(t, srv.URL, "t")
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.raised)
}

func elicit(t *testing.T, params string) map[string]any {
	t.Helper()
	res, rerr := (&agentHost{}).onElicitation(json.RawMessage(params), nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	return res.(map[string]any)
}

// A harness's question becomes a ping like ask_human's, and the human's
// form answers it — a secret field answered with the notice, never asked.
func TestElicitationAccepted(t *testing.T) {
	fastProtocol(t)
	eng := &fakeEngine{answer: `{"text":"x","answer":"accept","content":{"db":"Postgres","name":"api"}}`}
	eng.serve(t)
	res := elicit(t, codexForm)
	content, _ := res["content"].(map[string]any)
	if res["action"] != "accept" || content["db"] != "Postgres" || content["name"] != "api" || content["token"] != secretNotice {
		t.Fatalf("result = %v", res)
	}
	raised := eng.raised[0]
	if raised["type"] != "clarification" || raised["question"] != "Codex needs your input to continue." {
		t.Errorf("raised = %v", raised)
	}
	fields := raised["fields"].([]any)
	if len(fields) != 3 || fields[0].(map[string]any)["id"] != "db" || fields[0].(map[string]any)["required"] != true {
		t.Errorf("fields = %v", fields)
	}
}

func TestElicitationDeclinedParkedAndRefused(t *testing.T) {
	fastProtocol(t)
	eng := &fakeEngine{answer: `{"text":"declined — x","answer":"decline"}`}
	eng.serve(t)
	if res := elicit(t, codexForm); res["action"] != "decline" || res["content"] != nil {
		t.Errorf("declined = %v", res)
	}

	// Parked before an answer: the request is cancelled; the session hears
	// the answer when it resumes.
	eng = &fakeEngine{}
	eng.serve(t)
	if res := elicit(t, codexForm); res["action"] != "cancel" {
		t.Errorf("parked = %v", res)
	}

	// A url elicitation is declined without asking anyone.
	eng = &fakeEngine{answer: `{}`}
	eng.serve(t)
	if res := elicit(t, `{"mode":"url","url":"https://x","elicitationId":"e","message":"sign in"}`); res["action"] != "decline" || eng.count() != 0 {
		t.Errorf("url = %v (raised %d)", res, eng.count())
	}

	// Only secrets: nothing to ask the human.
	res := elicit(t, `{"mode":"form","message":"key?","requestedSchema":{"properties":{"k":{"type":"string","_meta":{"codex":{"isSecret":true}}}}}}`)
	if c, _ := res["content"].(map[string]any); res["action"] != "accept" || c["k"] != secretNotice || eng.count() != 0 {
		t.Errorf("secret only = %v (raised %d)", res, eng.count())
	}
}

// A form with no fields asks to go ahead: yes accepts, no declines.
func TestElicitationMessageOnly(t *testing.T) {
	fastProtocol(t)
	for _, c := range []struct{ proceed, want string }{{"true", "accept"}, {"false", "decline"}} {
		eng := &fakeEngine{answer: `{"text":"x","answer":"accept","content":{"proceed":` + c.proceed + `}}`}
		eng.serve(t)
		res := elicit(t, `{"mode":"form","message":"Run the migration?","requestedSchema":{"type":"object","properties":{}}}`)
		if res["action"] != c.want {
			t.Errorf("proceed %s = %v", c.proceed, res)
		}
		f := eng.raised[0]["fields"].([]any)[0].(map[string]any)
		if f["id"] != proceedField || f["type"] != "boolean" {
			t.Errorf("field = %v", f)
		}
	}
}

// The agent cancelling its request ($/cancel_request) stops the handler
// waiting on it, and the request is still answered.
func TestAgentRequestCancelled(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := newRPCConn(outW)
	started := make(chan struct{})
	c.onRequest = func(method string, _ json.RawMessage, done <-chan struct{}) (any, *rpcError) {
		close(started)
		<-done
		return map[string]any{"action": "cancel"}, nil
	}
	go c.readLoop(inR)
	go func() {
		_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{}}`+"\n")
		<-started
		_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":7}}`+"\n")
	}()
	reply := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(outR).ReadString('\n')
		reply <- line
	}()
	select {
	case line := <-reply:
		if !strings.Contains(line, `"id":7`) || !strings.Contains(line, `"cancel"`) {
			t.Errorf("reply = %s", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled request was never answered")
	}
	inW.Close()
}

// A form the client cannot read is the agent's error, not a question.
func TestElicitationMalformed(t *testing.T) {
	for _, params := range []string{
		`{"mode":`,
		`{"mode":"form","requestedSchema":{"properties":[1]}}`,
		`{"mode":"form","requestedSchema":{"properties":{"a":5}}}`,
	} {
		if res, rerr := (&agentHost{}).onElicitation(json.RawMessage(params), nil); rerr == nil || rerr.Code != -32602 {
			t.Errorf("%s = %v, %v", params, res, rerr)
		}
	}
}

// A form with no message still reaches the human as a question.
func TestElicitationDefaultQuestion(t *testing.T) {
	fastProtocol(t)
	eng := &fakeEngine{answer: `{"text":"x","answer":"accept","content":{"n":"v"}}`}
	eng.serve(t)
	res := elicit(t, `{"mode":"form","message":"  ","requestedSchema":{"properties":{"n":{"type":"string"}}}}`)
	if res["action"] != "accept" || eng.raised[0]["question"] != "The agent needs your input to continue." {
		t.Errorf("result = %v, raised %v", res, eng.raised)
	}
}

// A question that could not be put, or was given up on, is cancelled.
func TestElicitationCancelled(t *testing.T) {
	fastProtocol(t)
	writeTestEnv(t, "http://engine", "t")
	withEngine(t, func(*http.Request) (*http.Response, error) { return engineReply(500, "down"), nil })
	form := `{"mode":"form","message":"m","requestedSchema":{"properties":{"n":{"type":"string"}}}}`
	if res := elicit(t, form); res["action"] != "cancel" {
		t.Errorf("engine failure = %v", res)
	}
	done := make(chan struct{})
	close(done)
	res, rerr := (&agentHost{}).onElicitation(json.RawMessage(form), done)
	if rerr != nil || res.(map[string]any)["action"] != "cancel" {
		t.Errorf("given up on = %v, %v", res, rerr)
	}
}

func TestOrderedObject(t *testing.T) {
	keys, vals, err := orderedObject(json.RawMessage(`{"b":1,"a":2,"b":3}`))
	if err != nil || len(keys) != 2 || keys[0] != "b" || string(vals["b"]) != "3" {
		t.Errorf("keys %v, vals %v, %v", keys, vals, err)
	}
	for _, empty := range []string{"", "  ", "null"} {
		if keys, _, err := orderedObject(json.RawMessage(empty)); err != nil || keys != nil {
			t.Errorf("%q = %v, %v", empty, keys, err)
		}
	}
	for _, bad := range []string{`[]`, `{`, `{1:2}`, `{"a":`, `x`} {
		if _, _, err := orderedObject(json.RawMessage(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// Fields with nothing to pick from stay plain.
func TestElicitFieldsWithoutChoices(t *testing.T) {
	fields, _, err := elicitFields(json.RawMessage(`{"tags":{"type":"array"},"s":{"type":"string","_meta":{"codex":"unreadable"}}}`), nil)
	if err != nil || len(fields) != 2 || fields[0].Options != nil || fields[1].Options != nil {
		t.Errorf("fields = %+v, %v", fields, err)
	}
}
