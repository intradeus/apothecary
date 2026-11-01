package jira

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/ports/trackerport"
)

// writeStub serves the issue endpoints the write side touches and records
// every write body by "METHOD path".
func writeStub(t *testing.T, apiVersion string) (*httptest.Server, map[string][]map[string]any) {
	t.Helper()
	writes := map[string][]map[string]any{}
	base := "/rest/api/" + apiVersion
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request, status int, resp string) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		writes[r.Method+" "+r.URL.Path] = append(writes[r.Method+" "+r.URL.Path], body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}
	mux.HandleFunc("GET "+base+"/issue/PROJ-7", func(w http.ResponseWriter, r *http.Request) {
		fields := r.URL.Query().Get("fields")
		switch {
		case fields == "status":
			_, _ = w.Write([]byte(`{"key":"PROJ-7","fields":{"status":{"name":"To Do"}}}`))
		default:
			// The append-mode read: a text field, a labels field, a select and a paragraph.
			_, _ = w.Write([]byte(`{"key":"PROJ-7","fields":{"customfield_10001":"ff_a, ff_b","labels":["x","y"],
				"customfield_10003":{"value":"Platform"},"customfield_10002":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"old notes"}]}]},
				"customfield_10009":null}}`))
		}
	})
	mux.HandleFunc("GET "+base+"/issue/PROJ-7/transitions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"transitions":[
			{"id":"31","name":"Start work","to":{"name":"In Progress"}},
			{"id":"41","name":"Close","to":{"name":"Done"}},
			{"id":"42","name":"Close as duplicate","to":{"name":"Done"}}]}`))
	})
	mux.HandleFunc("POST "+base+"/issue/PROJ-7/transitions", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, 204, "")
	})
	mux.HandleFunc("POST "+base+"/issue/PROJ-7/comment", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, 201, `{"id":"1"}`)
	})
	mux.HandleFunc("GET "+base+"/issue/PROJ-7/comment", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("startAt") != "0" {
			_, _ = w.Write([]byte(`{"startAt":2,"maxResults":100,"total":2,"comments":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"startAt":0,"maxResults":100,"total":2,"comments":[
			{"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]},{"type":"paragraph","content":[{"type":"text","text":"— apothecary · run r1 · stage post"}]}]}},
			{"body":"plain v2 text"}]}`))
	})
	mux.HandleFunc("POST "+base+"/issue/PROJ-7/remotelink", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, 201, `{"id":9}`)
	})
	mux.HandleFunc("PUT "+base+"/issue/PROJ-7", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, 204, "")
	})
	mux.HandleFunc("GET "+base+"/field", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string","system":"summary"}},
			{"id":"labels","name":"Labels","custom":false,"schema":{"type":"array","items":"string","system":"labels"}},
			{"id":"assignee","name":"Assignee","custom":false,"schema":{"type":"user","system":"assignee"}},
			{"id":"customfield_10001","name":"Feature flags","custom":true,"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textfield"}},
			{"id":"customfield_10002","name":"Notes","custom":true,"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textarea"}},
			{"id":"customfield_10003","name":"Team","custom":true,"schema":{"type":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:select"}},
			{"id":"customfield_10004","name":"Tags","custom":true,"schema":{"type":"array","items":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:multiselect"}},
			{"id":"customfield_10005","name":"Estimate","custom":true,"schema":{"type":"number","custom":"com.atlassian.jira.plugin.system.customfieldtypes:float"}},
			{"id":"customfield_10006","name":"Twin","custom":true,"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textfield"}},
			{"id":"customfield_10007","name":"Twin","custom":true,"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textfield"}}
		]`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, writes
}

func TestTransitionMatchesReachableStatus(t *testing.T) {
	srv, writes := writeStub(t, "3")
	c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok"})
	ctx := context.Background()

	// Status name, case-insensitive.
	if err := c.Transition(ctx, "PROJ-7", "in progress"); err != nil {
		t.Fatal(err)
	}
	posts := writes["POST /rest/api/3/issue/PROJ-7/transitions"]
	if len(posts) != 1 || posts[0]["transition"].(map[string]any)["id"] != "31" {
		t.Fatalf("transition posts = %+v, want id 31", posts)
	}
	// The transition's own name works too; the first match wins among twins.
	if err := c.Transition(ctx, "PROJ-7", "Close as duplicate"); err != nil {
		t.Fatal(err)
	}
	if posts = writes["POST /rest/api/3/issue/PROJ-7/transitions"]; len(posts) != 2 || posts[1]["transition"].(map[string]any)["id"] != "42" {
		t.Fatalf("transition posts = %+v, want id 42 second", posts)
	}
	// Already there: no call.
	if err := c.Transition(ctx, "PROJ-7", "to do"); err != nil {
		t.Fatal(err)
	}
	if len(writes["POST /rest/api/3/issue/PROJ-7/transitions"]) != 2 {
		t.Error("a no-op transition posted")
	}
	// Unreachable: the typed error lists what is, deduplicated.
	err := c.Transition(ctx, "PROJ-7", "Blocked")
	var unreachable *trackerport.ErrStatusUnreachable
	if !errors.As(err, &unreachable) {
		t.Fatalf("err = %v, want ErrStatusUnreachable", err)
	}
	if unreachable.From != "To Do" || strings.Join(unreachable.Reachable, ",") != "In Progress,Done" {
		t.Errorf("unreachable = %+v", unreachable)
	}
	if !strings.Contains(err.Error(), `reachable: In Progress, Done`) {
		t.Errorf("message = %q", err)
	}
}

func TestCommentIsADFOnV3AndTextOnV2(t *testing.T) {
	srv, writes := writeStub(t, "3")
	c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok"})
	if err := c.Comment(context.Background(), "PROJ-7", "Hello **world**"); err != nil {
		t.Fatal(err)
	}
	body := writes["POST /rest/api/3/issue/PROJ-7/comment"][0]["body"].(map[string]any)
	if body["type"] != "doc" {
		t.Errorf("v3 comment body = %v, want an ADF doc", body)
	}

	srv2, writes2 := writeStub(t, "2")
	c2 := New(Config{BaseURL: srv2.URL, Token: "pat", APIVersion: "2"})
	if err := c2.Comment(context.Background(), "PROJ-7", "Hello **world**"); err != nil {
		t.Fatal(err)
	}
	if body := writes2["POST /rest/api/2/issue/PROJ-7/comment"][0]["body"]; body != "Hello **world**" {
		t.Errorf("v2 comment body = %v, want the text", body)
	}
}

func TestListCommentsRenderBothShapes(t *testing.T) {
	srv, _ := writeStub(t, "3")
	c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok"})
	got, err := c.ListComments(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.Contains(got[0].Body, "— apothecary · run r1 · stage post") || got[1].Body != "plain v2 text" {
		t.Errorf("comments = %+v", got)
	}
}

func TestSetFieldsCoercesBySchema(t *testing.T) {
	srv, writes := writeStub(t, "3")
	c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok"})
	ctx := context.Background()
	err := c.SetFields(ctx, "PROJ-7", map[string]string{
		"feature flags":     "ff_a, ff_b",   // display name, case-insensitive → text
		"Notes":             "para **one**", // textarea → ADF
		"labels":            "x, y",         // array of string
		"Team":              "Platform",     // select → {value}
		"Tags":              "a, b",         // multiselect → [{value}]
		"Estimate":          "3.5",          // number
		"customfield_10001": "by id",        // raw id wins over its name
	})
	if err != nil {
		t.Fatal(err)
	}
	puts := writes["PUT /rest/api/3/issue/PROJ-7"]
	if len(puts) != 1 {
		t.Fatalf("puts = %d, want one", len(puts))
	}
	fields := puts[0]["fields"].(map[string]any)
	// The raw id and the display name address the same field; the later
	// (sorted) name wins — what matters is that both resolve to the id.
	if fields["customfield_10001"] != "ff_a, ff_b" {
		t.Errorf("text field = %v", fields["customfield_10001"])
	}
	if doc, ok := fields["customfield_10002"].(map[string]any); !ok || doc["type"] != "doc" {
		t.Errorf("textarea = %v, want ADF", fields["customfield_10002"])
	}
	if ls, _ := fields["labels"].([]any); len(ls) != 2 || ls[0] != "x" {
		t.Errorf("labels = %v", fields["labels"])
	}
	if sel, _ := fields["customfield_10003"].(map[string]any); sel["value"] != "Platform" {
		t.Errorf("select = %v", fields["customfield_10003"])
	}
	if multi, _ := fields["customfield_10004"].([]any); len(multi) != 2 || multi[1].(map[string]any)["value"] != "b" {
		t.Errorf("multiselect = %v", fields["customfield_10004"])
	}
	if fields["customfield_10005"] != 3.5 {
		t.Errorf("number = %v", fields["customfield_10005"])
	}

	// Refusals are typed, so the engine can tell "fix the flow" from "tracker down".
	var unsupported *trackerport.ErrFieldUnsupported
	if err := c.SetFields(ctx, "PROJ-7", map[string]string{"Assignee": "lu"}); !errors.As(err, &unsupported) || unsupported.Type != "user" {
		t.Errorf("user field: err = %v", err)
	}
	if err := c.SetFields(ctx, "PROJ-7", map[string]string{"Estimate": "lots"}); !errors.As(err, &unsupported) {
		t.Errorf("non-numeric number: err = %v", err)
	}
	var unknown *trackerport.ErrFieldUnknown
	if err := c.SetFields(ctx, "PROJ-7", map[string]string{"Feature": "x"}); !errors.As(err, &unknown) || len(unknown.Candidates) != 1 || unknown.Candidates[0] != "Feature flags" {
		t.Errorf("unknown field: err = %v", err)
	}
	if err := c.SetFields(ctx, "PROJ-7", map[string]string{"Twin": "x"}); !errors.As(err, &unknown) || len(unknown.Candidates) != 2 {
		t.Errorf("ambiguous name: err = %v", err)
	}
	if len(writes["PUT /rest/api/3/issue/PROJ-7"]) != 1 {
		t.Error("a refused field still produced a PUT")
	}
}

func TestFieldsReadsCurrentValuesAsText(t *testing.T) {
	srv, _ := writeStub(t, "3")
	c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok"})
	got, err := c.Fields(context.Background(), "PROJ-7", []string{"Feature flags", "labels", "Team", "Notes"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Feature flags": "ff_a, ff_b", "labels": "x, y", "Team": "Platform", "Notes": "old notes"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestAddLinkUpsertsOnGlobalID(t *testing.T) {
	srv, writes := writeStub(t, "3")
	c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok"})
	link := trackerport.TicketLink{Title: "Merge request: feat/x", URL: "https://gitlab.local/mr/1"}
	if err := c.AddLink(context.Background(), "PROJ-7", link); err != nil {
		t.Fatal(err)
	}
	posts := writes["POST /rest/api/3/issue/PROJ-7/remotelink"]
	if len(posts) != 1 || posts[0]["globalId"] != "apothecary:https://gitlab.local/mr/1" {
		t.Fatalf("remotelink posts = %+v", posts)
	}
	if obj := posts[0]["object"].(map[string]any); obj["url"] != link.URL || obj["title"] != link.Title {
		t.Errorf("object = %v", obj)
	}
}

func TestFromMarkdownRoundTrips(t *testing.T) {
	md := strings.Join([]string{
		"# Title",
		"",
		"A **bold** and *em* and `code` and [link](https://x.io) and ~~gone~~.",
		"Second line of the paragraph.",
		"",
		"- one",
		"- two",
		"",
		"1. first",
		"2. second",
		"",
		"```go",
		"fmt.Println()",
		"```",
		"",
		"> quoted",
		"",
		"---",
		"",
		"— apothecary · run r1 · stage post",
	}, "\n")
	raw, err := json.Marshal(FromMarkdown(md))
	if err != nil {
		t.Fatal(err)
	}
	if got := ToMarkdown(raw); got != md {
		t.Errorf("round trip changed the text:\n--- want\n%s\n--- got\n%s", md, got)
	}
	// Unmatched delimiters stay literal rather than eating the line.
	raw, _ = json.Marshal(FromMarkdown("a * b ** c ` d [e"))
	if got := ToMarkdown(raw); got != "a * b ** c ` d [e" {
		t.Errorf("unmatched delimiters: %q", got)
	}
	// Empty input is still a valid (empty) document.
	if doc := FromMarkdown(""); len(doc["content"].([]map[string]any)) != 0 {
		t.Errorf("empty doc = %v", doc)
	}
}

// Attach posts the file as multipart with the XSRF opt-out and links it by
// its content URL — on v2 in wiki markup, the image embedded by name.
func TestAttach(t *testing.T) {
	for _, tc := range []struct{ version, wantMD string }{
		{"3", "[home.png](CONTENT)"},
		{"2", "!home.png!"},
	} {
		t.Run("v"+tc.version, func(t *testing.T) {
			var name, xsrf string
			mux := http.NewServeMux()
			srv := httptest.NewServer(mux)
			defer srv.Close()
			content := srv.URL + "/rest/api/" + tc.version + "/attachment/content/10001"
			mux.HandleFunc("POST /rest/api/"+tc.version+"/issue/PROJ-7/attachments", func(w http.ResponseWriter, r *http.Request) {
				xsrf = r.Header.Get("X-Atlassian-Token")
				if _, h, err := r.FormFile("file"); err == nil {
					name = h.Filename
				}
				_, _ = w.Write([]byte(`[{"id":"10001","filename":"home.png","content":"` + content + `"}]`))
			})
			c := New(Config{BaseURL: srv.URL, Email: "you@acme.com", Token: "tok", APIVersion: tc.version})
			att, err := c.Attach(context.Background(), "PROJ-7", "home.png", "image/png", []byte("\x89PNG"))
			if err != nil {
				t.Fatal(err)
			}
			if name != "home.png" || xsrf != "no-check" {
				t.Errorf("sent %q with X-Atlassian-Token %q", name, xsrf)
			}
			if want := strings.ReplaceAll(tc.wantMD, "CONTENT", content); att.URL != content || att.Markdown != want {
				t.Errorf("attachment = %+v, want markdown %q", att, want)
			}
		})
	}
}
