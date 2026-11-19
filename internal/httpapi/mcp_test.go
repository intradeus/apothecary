package httpapi

// The engine's MCP face: its catalog is the route table's declared tools,
// each with a valid input schema and nothing but reads; a token lists the
// tools its grant allows and no other; a call answers exactly as the route
// would, the guard's 404 included; and a browser session gets nowhere.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/authz"
	"apothecary/internal/engine"
	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
)

// mcpReply is a JSON-RPC answer, its result left raw.
type mcpReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// mcpCall sends one JSON-RPC request to /mcp as hdr (nil: the test user's
// full token) and returns the HTTP status and the reply.
func (x *env) mcpCall(t *testing.T, hdr map[string]string, method string, params any) (int, mcpReply) {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		msg["params"] = params
	}
	res, data := x.do(t, "POST", "/mcp", msg, hdr)
	var r mcpReply
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatalf("%s: reply %s: %v", method, data, err)
		}
	}
	return res.StatusCode, r
}

// mcpRaw posts body to /mcp verbatim, as the test user.
func (x *env) mcpRaw(t *testing.T, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest("POST", x.ts.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(x.authed(req))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, bytes.TrimSpace(data)
}

// mcpToolNames lists the tools hdr sees.
func (x *env) mcpToolNames(t *testing.T, hdr map[string]string) []string {
	t.Helper()
	status, r := x.mcpCall(t, hdr, "tools/list", nil)
	if status != http.StatusOK || r.Error != nil {
		t.Fatalf("tools/list = %d %+v", status, r.Error)
	}
	var out struct {
		Tools []mcpToolDef `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &out); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(out.Tools))
	for _, tl := range out.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// mcpTool calls a tool and returns its result.
func (x *env) mcpTool(t *testing.T, hdr map[string]string, name string, args map[string]any) mcpCallResult {
	t.Helper()
	status, r := x.mcpCall(t, hdr, "tools/call", map[string]any{"name": name, "arguments": args})
	if status != http.StatusOK || r.Error != nil {
		t.Fatalf("tools/call %s = %d %+v", name, status, r.Error)
	}
	var out mcpCallResult
	if err := json.Unmarshal(r.Result, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" {
		t.Fatalf("tools/call %s content = %+v", name, out.Content)
	}
	return out
}

var toolName = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

// Every declared tool is a read of a live route, under a unique name, with
// an input schema a client can compile.
func TestMCPCatalog(t *testing.T) {
	tools, err := mcpCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) < 20 {
		t.Fatalf("only %d tools declared", len(tools))
	}
	byID := map[string]Route{}
	for _, rt := range (&Server{}).routes() {
		byID[rt.ID] = rt
	}
	seen := map[string]bool{}
	for _, tl := range tools {
		name := tl.def.Name
		if !toolName.MatchString(name) || seen[name] {
			t.Errorf("tool name %q is not snake_case or is not unique", name)
		}
		seen[name] = true
		rt, ok := byID[tl.route.ID]
		if !ok {
			t.Errorf("%s names route %s, which the table does not have", name, tl.route.ID)
			continue
		}
		// This face reads: a write is never a tool, but the assistant's
		// fill, which stores nothing and is its session's token's alone.
		if rt.ID == fillRouteID {
			if rt.Action != authz.AssistantFill {
				t.Errorf("fill needs %s", rt.Action)
			}
			continue
		}
		if !readRoute(rt) || tl.def.Annotations["readOnlyHint"] != true {
			t.Errorf("%s is %s %s (%s): not a read", name, rt.Method, rt.Path, rt.Action)
		}
		if _, verb, _ := strings.Cut(string(rt.Action), ":"); verb != "read" {
			t.Errorf("%s needs %s, not a read action", name, rt.Action)
		}
		if rt.Auth != AccessUser || rt.Produces != "" || rt.Accepts != "" {
			t.Errorf("%s: a tool is a user route answering JSON (auth %d, produces %q, accepts %q)", name, rt.Auth, rt.Produces, rt.Accepts)
		}
		if len(tl.def.Description) < 30 {
			t.Errorf("%s: a model needs more than %q", name, tl.def.Description)
		}
		raw, err := json.Marshal(tl.def.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(name+".json", doc); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Compile(name + ".json"); err != nil {
			t.Errorf("%s: input schema does not compile: %v\n%s", name, err, raw)
		}
		props, _ := tl.def.InputSchema["properties"].(map[string]any)
		req, _ := tl.def.InputSchema["required"].([]string)
		for _, r := range req {
			if _, ok := props[r]; !ok {
				t.Errorf("%s requires %s, which it does not describe", name, r)
			}
		}
		for _, p := range pathParamRE.FindAllStringSubmatch(rt.Path, -1) {
			if !slices.Contains(req, p[1]) {
				t.Errorf("%s: path parameter %s is not required", name, p[1])
			}
		}
	}
	for _, a := range mcpActions() {
		if _, verb, _ := strings.Cut(string(a), ":"); verb != "read" {
			t.Errorf("an MCP client's grant would hold %s", a)
		}
	}
}

// A declaration the face could not serve fails the catalog, not a call.
func TestMCPCatalogRefusesBadDeclarations(t *testing.T) {
	ok := Route{Method: "GET", Path: "/api/x/{id}", ID: "x", Action: authz.RunRead,
		Tool: &Tool{Name: "get_x", Doc: "Read an x.", Args: map[string]string{"id": "the x"}}}
	if _, err := buildMCPCatalog([]Route{ok}); err != nil {
		t.Fatalf("a good declaration: %v", err)
	}
	with := func(f func(*Route)) []Route {
		r := ok
		tl := *ok.Tool
		r.Tool = &tl
		f(&r)
		return []Route{r}
	}
	for name, routes := range map[string][]Route{
		"undocumented path parameter": with(func(r *Route) { r.Tool.Args = nil }),
		"doc for an argument it lacks": with(func(r *Route) {
			r.Tool.Args = map[string]string{"id": "the x", "other": "nothing"}
		}),
		"no doc":            with(func(r *Route) { r.Tool.Doc = "" }),
		"wildcard path":     with(func(r *Route) { r.Path = "/api/x/{id}/{rest...}" }),
		"duplicate name":    append(with(func(*Route) {}), with(func(r *Route) { r.ID = "y" })...),
		"query beside path": with(func(r *Route) { r.Query = []Param{{Name: "id"}} }),
		"body beside query": with(func(r *Route) {
			r.Method, r.Query, r.Body = "POST", []Param{{Name: "yaml"}}, ValidateFlowRequest{}
		}),
	} {
		if _, err := buildMCPCatalog(routes); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The handshake, and what the transport refuses.
func TestMCPProtocol(t *testing.T) {
	x := newEnv(t, engine.Config{})

	status, r := x.mcpCall(t, nil, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"},
	})
	var init struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      map[string]any `json:"serverInfo"`
		Instructions    string         `json:"instructions"`
	}
	if status != 200 || r.Error != nil || json.Unmarshal(r.Result, &init) != nil {
		t.Fatalf("initialize = %d %+v %s", status, r.Error, r.Result)
	}
	if init.ProtocolVersion != "2025-06-18" || init.Capabilities["tools"] == nil || init.ServerInfo["name"] != "apothecary" || init.Instructions == "" {
		t.Errorf("initialize = %+v", init)
	}
	_, r = x.mcpCall(t, nil, "initialize", map[string]any{"protocolVersion": "1999-01-01"})
	if json.Unmarshal(r.Result, &init) != nil || init.ProtocolVersion != mcpProtocols[0] {
		t.Errorf("an unknown version is answered with %q, want the newest", init.ProtocolVersion)
	}

	if _, r = x.mcpCall(t, nil, "ping", nil); r.Error != nil || string(r.Result) != "{}" {
		t.Errorf("ping = %+v %s", r.Error, r.Result)
	}
	if _, r = x.mcpCall(t, nil, "resources/list", nil); r.Error == nil || r.Error.Code != rpcMethodNotFound {
		t.Errorf("an unknown method = %+v", r.Error)
	}
	if _, r = x.mcpCall(t, nil, "tools/call", map[string]any{"name": "create_ticket"}); r.Error == nil || r.Error.Code != rpcInvalidParams {
		t.Errorf("a tool that is not declared = %+v", r.Error)
	}

	// A notification is owed nothing.
	res, data := x.do(t, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil)
	if res.StatusCode != http.StatusAccepted || len(data) != 0 {
		t.Errorf("notification = %d %s", res.StatusCode, data)
	}
	// A batch is answered in one array, its notifications skipped.
	res, data = x.do(t, "POST", "/mcp", []any{
		map[string]any{"jsonrpc": "2.0", "id": "a", "method": "ping"},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": "b", "method": "nope"},
	}, nil)
	var batch []mcpReply
	if res.StatusCode != 200 || json.Unmarshal(data, &batch) != nil || len(batch) != 2 ||
		string(batch[0].ID) != `"a"` || batch[1].Error == nil {
		t.Errorf("batch = %d %s", res.StatusCode, data)
	}
	res, data = x.do(t, "POST", "/mcp", map[string]any{"id": 1, "method": "ping"}, nil)
	if res.StatusCode != 200 || !strings.Contains(string(data), `"code":-32600`) {
		t.Errorf("no jsonrpc member = %d %s", res.StatusCode, data)
	}
	// JSON-RPC's own edges, each owed exactly one answer of the right code.
	for name, tc := range map[string]struct{ body, want string }{
		"not JSON":              {`{"jsonrpc":`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700`},
		"not an object":         {`"x"`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600`},
		"an empty batch":        {`[]`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600`},
		"a batch of junk":       {`[1]`, `[{"jsonrpc":"2.0","id":null,"error":{"code":-32600`},
		"no method":             {`{"jsonrpc":"2.0","id":1}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32600`},
		"an object id":          {`{"jsonrpc":"2.0","id":{"x":1},"method":"ping"}`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600`},
		"jsonrpc 1.0":           {`{"jsonrpc":"1.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600`},
		"a numeric string id":   {`{"jsonrpc":"2.0","id":"7","method":"ping"}`, `{"jsonrpc":"2.0","id":"7","result":{}}`},
		"a negative numeric id": {`{"jsonrpc":"2.0","id":-3,"method":"ping"}`, `{"jsonrpc":"2.0","id":-3,"result":{}}`},
	} {
		status, data := x.mcpRaw(t, tc.body)
		if status != 200 || !strings.HasPrefix(string(data), tc.want) {
			t.Errorf("%s = %d %s, want %s…", name, status, data, tc.want)
		}
	}
	// A response to a request of ours is owed nothing.
	if status, data := x.mcpRaw(t, `{"jsonrpc":"2.0","id":1,"result":{}}`); status != http.StatusAccepted || len(data) != 0 {
		t.Errorf("a response = %d %s", status, data)
	}
	// An MCP client refused a token looks for OAuth metadata: there is none,
	// and it is told so in JSON, not handed the UI.
	res, data = x.do(t, "GET", "/.well-known/oauth-protected-resource", nil, nil)
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(data), "bearer") {
		t.Errorf("oauth discovery = %d %s", res.StatusCode, data)
	}

	for _, method := range []string{"GET", "DELETE"} {
		if res, _ := x.do(t, method, "/mcp", nil, nil); res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "POST" {
			t.Errorf("%s /mcp = %d, Allow %q", method, res.StatusCode, res.Header.Get("Allow"))
		}
	}
	res, _ = x.do(t, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"},
		map[string]string{"MCP-Protocol-Version": "1999-01-01"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unsupported protocol header = %d", res.StatusCode)
	}
	res, _ = x.do(t, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"},
		map[string]string{"Origin": "https://evil.example"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("another site's origin = %d", res.StatusCode)
	}
}

// The face is for a client that was handed a token: no cookie, no
// signed-out caller, no disabled account.
func TestMCPWantsAToken(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.addPasswordUser(t, "carol", false)
	_, cookie := x.login(t, "carol", goodPassword)
	cookie["Authorization"] = "none"
	cookie["Sec-Fetch-Site"] = "same-origin"
	res, _ := x.do(t, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, cookie)
	if res.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("a session cookie = %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	res, _ = x.do(t, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, bearer("apo_not-a-token"))
	if res.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("a bad token = %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	if res, _ := x.do(t, "POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, nil); res.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("an accepted token is told how to authenticate: %q", res.Header.Get("WWW-Authenticate"))
	}
	dave := x.addUser(t, "dave", false)
	if _, err := x.srv.Auth.SetDisabled(context.Background(), "dave", true); err != nil {
		t.Fatal(err)
	}
	if status, _ := x.mcpCall(t, dave, "ping", nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Errorf("a disabled account = %d", status)
	}
}

// tools/list is exactly the grant: a full token sees every tool this
// server serves, a scoped one only those whose action it holds, and a call
// to a tool outside its list is an unknown tool.
func TestMCPListEqualsTheGrant(t *testing.T) {
	x := newWorld(t)
	all, err := mcpCatalog()
	if err != nil {
		t.Fatal(err)
	}
	// Every tool but fill, which only an assistant session's token lists.
	full := x.mcpToolNames(t, nil)
	if len(full) != len(all)-1 || slices.Contains(full, "fill") {
		t.Errorf("a full token lists %d of %d tools: %v", len(full), len(all), full)
	}

	ctx := context.Background()
	scoped := func(actions ...string) map[string]string {
		secret, _, err := x.srv.Auth.NewToken(ctx, testUser, "mcp", nil, auth.Grant{Actions: actions})
		if err != nil {
			t.Fatal(err)
		}
		return bearer(secret)
	}
	tickets := scoped("ticket:read")
	got := x.mcpToolNames(t, tickets)
	var want []string
	for _, tl := range all {
		if tl.route.Action == authz.TicketRead {
			want = append(want, tl.def.Name)
		}
	}
	if !slices.Equal(got, want) || len(want) == 0 {
		t.Errorf("ticket:read lists %v, want %v", got, want)
	}
	if res := x.mcpTool(t, tickets, "list_tickets", map[string]any{"id": testProject}); res.IsError {
		t.Errorf("list_tickets with ticket:read = %+v", res)
	}
	if _, r := x.mcpCall(t, tickets, "tools/call", map[string]any{"name": "list_workshops"}); r.Error == nil || r.Error.Code != rpcInvalidParams {
		t.Errorf("a tool outside the grant is not unknown: %+v", r.Error)
	}
	if got := x.mcpToolNames(t, scoped("notice:read")); len(got) != 0 {
		t.Errorf("a grant no tool needs lists %v", got)
	}

	// A server without a skill library does not list what it cannot serve.
	x.srv.Skills = nil
	for _, n := range x.mcpToolNames(t, nil) {
		if strings.Contains(n, "skill") {
			t.Errorf("a server without skills lists %s", n)
		}
	}
}

// A call is the route: its answer, its errors, its guard.
func TestMCPCallRunsTheRoute(t *testing.T) {
	x := newEnv(t, engine.Config{})
	conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: "prj-bob", UserID: "bob", Name: "bobs"})
	bob := x.addUser(t, "bob", false)
	key := x.newTicket(t, "fix the login page", "")

	res := x.mcpTool(t, nil, "list_workshops", nil)
	if res.IsError || !strings.Contains(res.Content[0].Text, testProject) || strings.Contains(res.Content[0].Text, "prj-bob") {
		t.Errorf("list_workshops = %+v", res)
	}
	res = x.mcpTool(t, nil, "get_ticket", map[string]any{"id": testProject, "key": key})
	if res.IsError || !strings.Contains(res.Content[0].Text, "fix the login page") {
		t.Errorf("get_ticket = %+v", res)
	}
	res = x.mcpTool(t, nil, "list_tickets", map[string]any{"id": testProject, "done": "any", "limit": 5})
	if res.IsError || !strings.Contains(res.Content[0].Text, key) {
		t.Errorf("list_tickets with query arguments = %+v", res)
	}
	res = x.mcpTool(t, nil, "get_flow", map[string]any{"id": testProject, "name": "builder"})
	if res.IsError {
		t.Errorf("get_flow on a template = %+v", res)
	}

	// The guard answers as it would over HTTP: another's workshop is not
	// there at all.
	res = x.mcpTool(t, nil, "list_tickets", map[string]any{"id": "prj-bob"})
	if !res.IsError || !strings.HasPrefix(res.Content[0].Text, "404 ") {
		t.Errorf("another's workshop = %+v", res)
	}
	res = x.mcpTool(t, bob, "get_workshop", map[string]any{"id": testProject})
	if !res.IsError || !strings.HasPrefix(res.Content[0].Text, "404 ") {
		t.Errorf("bob reading my workshop = %+v", res)
	}
	res = x.mcpTool(t, bob, "get_workshop", map[string]any{"id": "prj-bob"})
	if res.IsError {
		t.Errorf("bob reading his own = %+v", res)
	}
	// A token limited to one workshop reaches no other.
	secret, _, err := x.srv.Auth.NewToken(context.Background(), testUser, "one", nil,
		auth.Grant{Actions: []string{"project:read"}, ProjectIDs: []string{"prj-none"}})
	if err != nil {
		t.Fatal(err)
	}
	res = x.mcpTool(t, bearer(secret), "get_workshop", map[string]any{"id": testProject})
	if !res.IsError || !strings.HasPrefix(res.Content[0].Text, "404 ") {
		t.Errorf("a token limited elsewhere = %+v", res)
	}

	// A token limited to one workshop lists only that workshop's things.
	conftest.SeedProject(t, x.srv.Conf, confstore.Project{ID: "prj-two", UserID: testUser, Name: "two"})
	run := x.runOnProject(t, "prj-two", "elsewhere")
	x.e.Wait(run)
	secret, _, err = x.srv.Auth.NewToken(context.Background(), testUser, "two", nil,
		auth.Grant{Actions: []string{"project:read", "run:read"}, ProjectIDs: []string{"prj-two"}})
	if err != nil {
		t.Fatal(err)
	}
	res = x.mcpTool(t, bearer(secret), "list_workshops", nil)
	if res.IsError || !strings.Contains(res.Content[0].Text, "prj-two") || strings.Contains(res.Content[0].Text, `"`+testProject+`"`) {
		t.Errorf("list_workshops, limited to prj-two = %+v", res)
	}
	res = x.mcpTool(t, bearer(secret), "list_runs", nil)
	if res.IsError || !strings.Contains(res.Content[0].Text, run) {
		t.Errorf("list_runs, limited to prj-two = %+v", res)
	}
	var runs []map[string]any
	if json.Unmarshal([]byte(res.Content[0].Text), &runs) == nil {
		for _, r := range runs {
			if r["project_id"] != "prj-two" {
				t.Errorf("list_runs, limited to prj-two, holds a run of %v", r["project_id"])
			}
		}
	}

	// validate_flow takes the body's fields as its arguments.
	res = x.mcpTool(t, nil, "validate_flow", map[string]any{"yaml": loopFlowDoc})
	var v FlowValidation
	if res.IsError || json.Unmarshal([]byte(res.Content[0].Text), &v) != nil || !v.OK || v.Name != "loop" {
		t.Errorf("validate_flow = %+v", res)
	}
	res = x.mcpTool(t, nil, "validate_flow", map[string]any{"yaml": "name: [", "params": map[string]string{"harness": "fake"}})
	if res.IsError || json.Unmarshal([]byte(res.Content[0].Text), &v) != nil || v.OK || len(v.Errors) == 0 {
		t.Errorf("validate_flow on a broken flow = %+v", res)
	}

	// Arguments the route does not take, or lack, are the model's to fix.
	for name, args := range map[string]map[string]any{
		"missing path argument": {"id": testProject},
		"unknown argument":      {"id": testProject, "key": key, "sql": "drop"},
		"object as a key":       {"id": testProject, "key": map[string]any{"a": 1}},
	} {
		res = x.mcpTool(t, nil, "get_ticket", args)
		if !res.IsError {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

// validate_flow's schema says what the call needs: the YAML, described.
func TestMCPBodyFieldsAreRequiredAndDescribed(t *testing.T) {
	tools, err := mcpCatalog()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(tools, func(tl *mcpTool) bool { return tl.def.Name == "validate_flow" })
	if i < 0 {
		t.Fatal("no validate_flow")
	}
	schema := tools[i].def.InputSchema
	props := schema["properties"].(map[string]any)
	if req, _ := schema["required"].([]string); !slices.Equal(req, []string{"yaml"}) {
		t.Errorf("validate_flow requires %v, want [yaml]", req)
	}
	for _, f := range []string{"yaml", "params"} {
		if d, _ := props[f].(map[string]any)["description"].(string); d == "" {
			t.Errorf("validate_flow's %s has no description", f)
		}
	}
}

// A large answer is cut, and the model told why.
func TestMCPCutsALargeAnswer(t *testing.T) {
	rec := newToolRecorder()
	rec.WriteHeader(http.StatusOK)
	_, _ = rec.Write([]byte(strings.Repeat("x", mcpMaxResult+10)))
	res := toolResult(rec)
	if res.IsError || len(res.Content[0].Text) > mcpMaxResult+200 || !strings.Contains(res.Content[0].Text, "narrow it") {
		t.Errorf("cut answer: %d bytes, %q", len(res.Content[0].Text), res.Content[0].Text[len(res.Content[0].Text)-80:])
	}
	// Never inside a character: the text stays valid UTF-8.
	rec = newToolRecorder()
	_, _ = rec.Write([]byte(strings.Repeat("x", mcpMaxResult-1) + "é" + "tail"))
	if res := toolResult(rec); !utf8.ValidString(res.Content[0].Text) || !strings.HasPrefix(res.Content[0].Text[mcpMaxResult-1:], "\n[cut") {
		t.Errorf("a cut inside a character: valid %v", utf8.ValidString(res.Content[0].Text))
	}
	// A refusal says when to come back.
	rec = newToolRecorder()
	rec.Header().Set("Retry-After", "30")
	rec.WriteHeader(http.StatusServiceUnavailable)
	_, _ = rec.Write([]byte(`{"error":"the tracker is holding off"}`))
	if res := toolResult(rec); !res.IsError || !strings.HasSuffix(res.Content[0].Text, "(retry after 30 s)") {
		t.Errorf("a 503 with Retry-After = %+v", res)
	}
	rec = newToolRecorder()
	rec.WriteHeader(http.StatusNoContent)
	if res := toolResult(rec); res.IsError || res.Content[0].Text != "done" {
		t.Errorf("an empty answer = %+v", res)
	}
	rec = newToolRecorder()
	rec.WriteHeader(http.StatusBadGateway)
	_, _ = rec.Write([]byte("upstream down"))
	if res := toolResult(rec); !res.IsError || res.Content[0].Text != "502 Bad Gateway: upstream down" {
		t.Errorf("a non-JSON error = %+v", res)
	}
}

// The token form's MCP grant is the actions the tools need, from the engine.
func TestPermissionsMarkTheMCPGrant(t *testing.T) {
	x := newEnv(t, engine.Config{})
	perms := decode[[]Permission](t, x.expect(t, "me", nil, "GET", "/api/permissions", nil, 200))
	var marked []string
	for _, p := range perms {
		if p.MCP {
			marked = append(marked, p.Action)
		}
	}
	var want []string
	for _, a := range mcpActions() {
		want = append(want, string(a))
	}
	slices.Sort(marked)
	if !slices.Equal(marked, want) || !slices.Contains(marked, "ticket:read") || slices.Contains(marked, "run:operate") {
		t.Errorf("mcp-marked actions = %v, want %v", marked, want)
	}
}

func TestStringArg(t *testing.T) {
	for raw, want := range map[string]string{`"a b"`: "a b", `12`: "12", `1.5`: "1.5", `true`: "true", `null`: ""} {
		if got, ok := stringArg(json.RawMessage(raw)); !ok || got != want {
			t.Errorf("stringArg(%s) = %q %v", raw, got, ok)
		}
	}
	for _, raw := range []string{`{}`, `[1]`} {
		if _, ok := stringArg(json.RawMessage(raw)); ok {
			t.Errorf("stringArg(%s) accepted", raw)
		}
	}
}
