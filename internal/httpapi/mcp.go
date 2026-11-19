package httpapi

// The engine's MCP face: POST /mcp, the Model Context Protocol over its
// Streamable HTTP transport, for an agent that holds a personal token.
//
// A tool is a route of the table that declares one (Route.Tool). Its input
// schema is reflected from the route's path, query and body, as the OpenAPI
// document is, and a call runs the route's own guarded handler in process:
// the same authentication, the same authorizer, the same 404/403 split as
// the HTTP route. The face adds no access rule of its own. tools/list shows
// a token the tools whose action it holds; a call to any other is an
// unknown tool.
//
// Stateless: no session id, no server-sent stream (GET is 405). A browser
// session is refused: the face is for a client that was handed a token.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3gen"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/authz"
)

// Tool is a route's declaration as an MCP tool.
type Tool struct {
	// Name is what a model calls: snake_case, unique across the face.
	Name string
	// Doc is the description a model reads to decide when to call it.
	Doc string
	// Args describes the path parameters, which the route table leaves
	// undocumented (every one must be here), and may override a query
	// parameter's doc for a model.
	Args map[string]string
}

// mcpProtocols are the protocol revisions the face speaks, newest first.
// Nothing it does differs between them.
var mcpProtocols = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// mcpMaxBody bounds one POST: a flow to validate is the largest argument.
const mcpMaxBody = 4 << 20

// mcpMaxResult bounds one tool's text: past it the answer is cut and the
// model told to narrow its question.
const mcpMaxResult = 256 << 10

// mcpInstructions is what an MCP client hands its model about the server.
const mcpInstructions = "Apothecary runs AI agents on repositories, in sandboxed workspaces. " +
	"A workshop is a project: its repositories, flows, tickets and settings. " +
	"A flow is a YAML document that says what a run does, stage by stage; a ticket is a piece of work a run is started on. " +
	"Start from list_workshops: most tools take a workshop id. Every tool here only reads, except validate_flow, which checks a flow and stores nothing."

// JSON-RPC error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

// rpcRequest is any inbound message: a request, a notification, or a
// response to a request of ours (Result or Error set; we send none).
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpToolDef is one entry of tools/list.
type mcpToolDef struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

// mcpContent is a tool result's text block.
type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// mcpTool is a declared tool, its route and its reflected input.
type mcpTool struct {
	route  Route
	def    mcpToolDef
	params []string        // path parameters, in pattern order
	query  map[string]bool // query parameter names
	// bodyFields are the body's top-level properties, flattened into the
	// arguments; wholeBody names the one argument that is the body when
	// it is not an object.
	bodyFields map[string]bool
	wholeBody  string
	// boundSession, set on an assistant session token's fill tool, is the
	// session it fills: the model passes the page's fill alone.
	boundSession string
}

// mcpCatalog is every declared tool of the full table, built once: the
// schemas come from compiled types and cannot change while the binary runs.
var (
	mcpCatalogOnce sync.Once
	mcpCatalogList []*mcpTool
	mcpCatalogErr  error
)

// mcpCatalog is the table's tools, in table order. Handlers are those of
// an empty server: a call runs the serving server's route, never these.
func mcpCatalog() ([]*mcpTool, error) {
	mcpCatalogOnce.Do(func() { mcpCatalogList, mcpCatalogErr = buildMCPCatalog((&Server{}).routes()) })
	return mcpCatalogList, mcpCatalogErr
}

func buildMCPCatalog(routes []Route) ([]*mcpTool, error) {
	var out []*mcpTool
	names := map[string]string{}
	for _, rt := range routes {
		if rt.Tool == nil {
			continue
		}
		if prev, dup := names[rt.Tool.Name]; dup {
			return nil, fmt.Errorf("tool %s is declared by both %s and %s", rt.Tool.Name, prev, rt.ID)
		}
		names[rt.Tool.Name] = rt.ID
		t, err := newMCPTool(rt)
		if err != nil {
			return nil, fmt.Errorf("tool %s (%s): %w", rt.Tool.Name, rt.ID, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// newMCPTool reflects a route's arguments into one flat JSON Schema
// object: its path parameters, its query parameters and its body's fields.
func newMCPTool(rt Route) (*mcpTool, error) {
	t := &mcpTool{route: rt, query: map[string]bool{}, bodyFields: map[string]bool{}}
	props := map[string]any{}
	var required []string
	add := func(name string, schema map[string]any, need bool) error {
		if _, dup := props[name]; dup {
			return fmt.Errorf("argument %s is named twice", name)
		}
		props[name] = schema
		if need {
			required = append(required, name)
		}
		return nil
	}
	for _, m := range pathParamRE.FindAllStringSubmatch(rt.Path, -1) {
		doc := rt.Tool.Args[m[1]]
		if doc == "" {
			return nil, fmt.Errorf("path parameter %s has no doc in Tool.Args", m[1])
		}
		t.params = append(t.params, m[1])
		if err := add(m[1], map[string]any{"type": "string", "description": doc}, true); err != nil {
			return nil, err
		}
	}
	if strings.Contains(rt.Path, "...}") {
		return nil, fmt.Errorf("a wildcard path cannot be a tool")
	}
	for _, q := range rt.Query {
		s := map[string]any{"type": "string"}
		if q.Type == "integer" {
			s["type"] = "integer"
		}
		if len(q.Enum) > 0 {
			s["enum"] = slices.Clone(q.Enum)
		}
		doc := q.Doc
		if d := rt.Tool.Args[q.Name]; d != "" {
			doc = d
		}
		if doc != "" {
			s["description"] = doc
		}
		t.query[q.Name] = true
		if err := add(q.Name, s, false); err != nil {
			return nil, err
		}
	}
	if rt.Body != nil {
		body, err := bodySchema(rt.Body)
		if err != nil {
			return nil, err
		}
		if fields, ok := body["properties"].(map[string]any); ok && body["type"] == "object" {
			// Required as the OpenAPI document says (applyRequired): what
			// encoding/json always writes, a field without omitempty.
			need := requiredFields(reflect.TypeOf(rt.Body))
			for _, name := range sortedKeys(fields) {
				t.bodyFields[name] = true
				fs, _ := fields[name].(map[string]any)
				if d := rt.Tool.Args[name]; d != "" && fs != nil {
					fs["description"] = d
				}
				if err := add(name, fs, need[name]); err != nil {
					return nil, err
				}
			}
		} else {
			t.wholeBody = "body"
			if err := add("body", body, true); err != nil {
				return nil, err
			}
		}
	}
	for name := range rt.Tool.Args {
		if _, ok := props[name]; !ok {
			return nil, fmt.Errorf("Tool.Args names %s, which the route does not take", name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	t.def = mcpToolDef{
		Name: rt.Tool.Name, Title: rt.Summary, Description: rt.Tool.Doc, InputSchema: schema,
		Annotations: map[string]any{
			"readOnlyHint":  rt.Method == http.MethodGet || readRoute(rt),
			"openWorldHint": false,
		},
	}
	if t.def.Description == "" {
		return nil, errors.New("a tool needs a Doc")
	}
	return t, nil
}

// requiredFields are a body type's JSON fields without omitempty or
// omitzero, and not pointers: what its decoder needs the caller to send.
func requiredFields(t reflect.Type) map[string]bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]bool{}
	if t.Kind() != reflect.Struct {
		return out
	}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if !f.IsExported() || tag == "-" || f.Type.Kind() == reflect.Pointer {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") {
			out[name] = true
		}
	}
	return out
}

// readRoute reports a route that changes nothing: a GET, or a POST whose
// action only reads (validating a flow).
func readRoute(rt Route) bool {
	if rt.Method == http.MethodGet {
		return true
	}
	_, verb, _ := strings.Cut(string(rt.Action), ":")
	return verb == "read"
}

// bodySchema reflects a request body into a self-contained JSON Schema: the
// OpenAPI schema inlined (no components, a cycle is an error) and its
// nullable rewritten as JSON Schema says it.
func bodySchema(v any) (map[string]any, error) {
	gen := openapi3gen.NewGenerator(
		openapi3gen.UseAllExportedFields(),
		openapi3gen.SchemaCustomizer(customizeSchema),
		openapi3gen.ThrowErrorOnCycle(),
	)
	ref, err := gen.NewSchemaRefForValue(v, nil)
	if err != nil {
		return nil, fmt.Errorf("body schema: %w", err)
	}
	raw, err := json.Marshal(ref.Value)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return jsonSchema(m), nil
}

// jsonSchema rewrites OpenAPI 3.0's nullable into a JSON Schema type list,
// all the way down.
func jsonSchema(m map[string]any) map[string]any {
	if n, _ := m["nullable"].(bool); n {
		if ty, ok := m["type"].(string); ok {
			m["type"] = []any{ty, "null"}
		}
	}
	delete(m, "nullable")
	for k, v := range m {
		switch x := v.(type) {
		case map[string]any:
			if k == "properties" {
				for pk, pv := range x {
					if pm, ok := pv.(map[string]any); ok {
						x[pk] = jsonSchema(pm)
					}
				}
			} else {
				m[k] = jsonSchema(x)
			}
		case []any:
			for i, e := range x {
				if em, ok := e.(map[string]any); ok {
					x[i] = jsonSchema(em)
				}
			}
		}
	}
	return m
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// --- serving -------------------------------------------------------------------

// serveMCP is POST /mcp: one JSON-RPC message, or a batch, answered as
// JSON.
func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "the MCP face answers POST only: it opens no stream")
		return
	}
	// A browser page on another site is refused outright. The token is what
	// protects the face (a rebound hostname passes this check, but holds no
	// token); this only keeps a browser from probing it.
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeErr(w, http.StatusForbidden, "cross-site request refused")
			return
		}
	}
	// Every 401 says how to authenticate: a static bearer token.
	w.Header().Set("WWW-Authenticate", `Bearer realm="apothecary"`)
	if _, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); !ok {
		writeErr(w, http.StatusUnauthorized, "the MCP face takes a personal token, sent as a bearer header")
		return
	}
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	w.Header().Del("WWW-Authenticate")
	if s.Authz == nil {
		writeErr(w, http.StatusServiceUnavailable, "authorization is not wired on this server")
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(mcpProtocols, v) {
		writeErr(w, http.StatusBadRequest, "unsupported MCP protocol version "+v)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "the request is too large")
		return
	}
	ctx := auth.WithPrincipal(r.Context(), p)
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(raw, &batch); err != nil {
			writeJSON(w, http.StatusOK, rpcFailure(nil, rpcParseError, "not JSON: "+err.Error()))
			return
		}
		if len(batch) == 0 {
			writeJSON(w, http.StatusOK, rpcFailure(nil, rpcInvalidRequest, "an empty batch"))
			return
		}
		var out []rpcResponse
		for _, m := range batch {
			if resp, reply := s.mcpMessage(ctx, r, p, m); reply {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	resp, reply := s.mcpMessage(ctx, r, p, raw)
	if !reply {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func rpcFailure(id json.RawMessage, code int, msg string) rpcResponse {
	if id == nil {
		id = json.RawMessage("null")
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// mcpMessage answers one message; reply is false for a notification or a
// response, which are owed nothing.
func (s *Server) mcpMessage(ctx context.Context, outer *http.Request, p auth.Principal, raw json.RawMessage) (rpcResponse, bool) {
	if !json.Valid(raw) {
		return rpcFailure(nil, rpcParseError, "not JSON"), true
	}
	var msg rpcRequest
	if err := json.Unmarshal(raw, &msg); err != nil {
		// Valid JSON, but not an object of the right shape (1, "x", []).
		return rpcFailure(nil, rpcInvalidRequest, "not a JSON-RPC message"), true
	}
	// An id is a string or a number; anything else cannot be echoed back.
	var id json.RawMessage
	switch {
	case len(msg.ID) == 0 || string(msg.ID) == "null":
	case msg.ID[0] == '"' || msg.ID[0] == '-' || (msg.ID[0] >= '0' && msg.ID[0] <= '9'):
		id = msg.ID
	default:
		return rpcFailure(nil, rpcInvalidRequest, "id must be a string or a number"), true
	}
	if msg.JSONRPC != "2.0" {
		return rpcFailure(id, rpcInvalidRequest, `jsonrpc must be "2.0"`), true
	}
	if msg.Method == "" {
		if len(msg.Result) > 0 || len(msg.Error) > 0 {
			return rpcResponse{}, false // a response; we asked nothing
		}
		return rpcFailure(id, rpcInvalidRequest, "a request needs a method"), true
	}
	if id == nil {
		return rpcResponse{}, false // a notification: owed nothing
	}
	ok := func(v any) (rpcResponse, bool) { return rpcResponse{JSONRPC: "2.0", ID: id, Result: v}, true }
	switch msg.Method {
	case "initialize":
		var in struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &in)
		version := mcpProtocols[0]
		if slices.Contains(mcpProtocols, in.ProtocolVersion) {
			version = in.ProtocolVersion
		}
		serverVersion := s.Version
		if serverVersion == "" {
			serverVersion = SpecVersion
		}
		return ok(map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "apothecary", "title": "Apothecary", "version": serverVersion},
			"instructions":    mcpInstructions,
		})
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		tools, err := s.mcpTools(ctx, p)
		if err != nil {
			return rpcFailure(id, rpcInvalidRequest, err.Error()), true
		}
		defs := make([]mcpToolDef, 0, len(tools))
		for _, t := range tools {
			defs = append(defs, t.def)
		}
		return ok(map[string]any{"tools": defs})
	case "tools/call":
		var in struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &in); err != nil {
			return rpcFailure(id, rpcInvalidParams, "params: "+err.Error()), true
		}
		tools, err := s.mcpTools(ctx, p)
		if err != nil {
			return rpcFailure(id, rpcInvalidRequest, err.Error()), true
		}
		i := slices.IndexFunc(tools, func(t *mcpTool) bool { return t.def.Name == in.Name })
		if i < 0 {
			// Not declared, or not this token's: the same answer, so a
			// token cannot map what it was not given.
			return rpcFailure(id, rpcInvalidParams, "unknown tool "+in.Name), true
		}
		return ok(s.callTool(ctx, outer, tools[i], in.Arguments))
	}
	return rpcFailure(id, rpcMethodNotFound, "method "+msg.Method+" not found"), true
}

// mcpTools is what p may call here: the declared tools this server serves
// whose action p holds.
func (s *Server) mcpTools(ctx context.Context, p auth.Principal) ([]*mcpTool, error) {
	all, err := mcpCatalog()
	if err != nil {
		return nil, err
	}
	served := map[string]Route{}
	for _, rt := range s.routes() {
		if rt.Tool != nil && (rt.Enabled == nil || rt.Enabled(s)) {
			served[rt.ID] = rt
		}
	}
	var out []*mcpTool
	for _, t := range all {
		rt, ok := served[t.route.ID]
		if !ok {
			continue
		}
		if t.route.ID == fillRouteID {
			// The fill tool is an assistant session's alone, bound to it
			// and shaped as its page's fill; no other token lists it.
			bound, ok := s.boundFill(ctx, p, t, rt)
			if ok {
				out = append(out, bound)
			}
			continue
		}
		d, err := s.Authz.Decide(ctx, p, t.route.Action, nil)
		if err != nil {
			return nil, err
		}
		if !d.Allowed() {
			continue
		}
		bound := *t
		bound.route = rt
		out = append(out, &bound)
	}
	return out, nil
}

// fillRouteID is the assistant's fill route: a tool only for the session
// whose token calls it.
const fillRouteID = "fillAssistantSession"

// boundFill is the fill tool as an assistant session's token sees it: its
// page's fill as the input, its session bound. ok false for any other
// principal.
func (s *Server) boundFill(ctx context.Context, p auth.Principal, t *mcpTool, rt Route) (*mcpTool, bool) {
	if s.Assistant == nil || p.Token == nil {
		return nil, false
	}
	d, err := s.Authz.Decide(ctx, p, rt.Action, nil)
	if err != nil || !d.Allowed() {
		return nil, false
	}
	sid, schema, ok := s.Assistant.FillTool(ctx, p.Token.ID)
	if !ok {
		return nil, false
	}
	bound := *t
	bound.route = rt
	bound.boundSession = sid
	bound.def.InputSchema = schema
	bound.def.Annotations = map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	return &bound, true
}

// callTool runs the tool's route as the caller: a request built from the
// arguments, through the route's own guard.
func (s *Server) callTool(ctx context.Context, outer *http.Request, t *mcpTool, args map[string]json.RawMessage) mcpCallResult {
	if t.boundSession != "" {
		// The model passed the page's fill; the session is the token's.
		fill, err := json.Marshal(args)
		if err != nil {
			return mcpCallResult{Content: []mcpContent{{Type: "text", Text: err.Error()}}, IsError: true}
		}
		id, _ := json.Marshal(t.boundSession)
		args = map[string]json.RawMessage{"id": id, "fill": fill}
	}
	fail := func(format string, a ...any) mcpCallResult {
		return mcpCallResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf(format, a...)}}, IsError: true}
	}
	path := t.route.Path
	values := map[string]string{}
	for _, name := range t.params {
		v, ok := stringArg(args[name])
		if !ok || v == "" {
			return fail("argument %s is required, as a string", name)
		}
		values[name] = v
		path = strings.Replace(path, "{"+name+"}", url.PathEscape(v), 1)
	}
	q := url.Values{}
	body := map[string]json.RawMessage{}
	var whole json.RawMessage
	for _, name := range sortedKeys(rawKeys(args)) {
		raw := args[name]
		switch {
		case slices.Contains(t.params, name):
		case t.query[name]:
			v, ok := stringArg(raw)
			if !ok {
				return fail("argument %s must be a string or a number", name)
			}
			if v != "" {
				q.Set(name, v)
			}
		case t.bodyFields[name]:
			body[name] = raw
		case name == t.wholeBody && t.wholeBody != "":
			whole = raw
		default:
			return fail("unknown argument %s", name)
		}
	}
	var rd io.Reader
	if t.route.Body != nil {
		var data []byte
		var err error
		if t.wholeBody != "" {
			data = whole
		} else {
			data, err = json.Marshal(body)
		}
		if err != nil || len(data) == 0 {
			return fail("the arguments do not make a request body")
		}
		rd = bytes.NewReader(data)
	}
	target := path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, t.route.Method, target, rd)
	if err != nil {
		return fail("building the request: %v", err)
	}
	req.Host = outer.Host
	req.RemoteAddr = outer.RemoteAddr
	req.Header.Set("Authorization", outer.Header.Get("Authorization"))
	req.Header.Set("User-Agent", outer.Header.Get("User-Agent"))
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, v := range values {
		req.SetPathValue(name, v)
	}
	rec := newToolRecorder()
	s.guard(t.route)(rec, req)
	return toolResult(rec)
}

func rawKeys(m map[string]json.RawMessage) map[string]any {
	out := make(map[string]any, len(m))
	for k := range m {
		out[k] = nil
	}
	return out
}

// stringArg reads a scalar argument as the text a URL carries.
func stringArg(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&n) == nil {
		return n.String(), true
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return strconv.FormatBool(b), true
	}
	return "", false
}

// toolResult turns the route's answer into a tool result: its JSON as
// text, or its error as an error result the model reads and acts on.
func toolResult(rec *toolRecorder) mcpCallResult {
	body := rec.body.Bytes()
	if rec.status >= 400 {
		msg := strings.TrimSpace(string(body))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if after := rec.header.Get("Retry-After"); after != "" {
			msg += " (retry after " + after + " s)"
		}
		return mcpCallResult{Content: []mcpContent{{Type: "text",
			Text: fmt.Sprintf("%d %s: %s", rec.status, http.StatusText(rec.status), msg)}}, IsError: true}
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		text = "done"
	}
	if len(text) > mcpMaxResult {
		cut := mcpMaxResult
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + fmt.Sprintf("\n[cut: the answer was %d bytes; narrow it with this tool's arguments]", len(body))
	}
	return mcpCallResult{Content: []mcpContent{{Type: "text", Text: text}}}
}

// toolRecorder is the ResponseWriter a tool call's route writes into.
type toolRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newToolRecorder() *toolRecorder { return &toolRecorder{header: http.Header{}} }

func (r *toolRecorder) Header() http.Header { return r.header }

func (r *toolRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

func (r *toolRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}

// MCPActions are the actions the face's read tools need: what an MCP
// client's grant holds, and what an assistant session's token reads with.
func MCPActions() []authz.Action { return mcpActions() }

// mcpActions is every action a declared tool needs: what a read-only grant
// for an MCP client holds (the token form's preset).
func mcpActions() []authz.Action {
	all, err := mcpCatalog()
	if err != nil {
		return nil
	}
	var out []authz.Action
	for _, t := range all {
		if t.route.ID == fillRouteID {
			continue // an assistant session's, never a client's grant
		}
		if !slices.Contains(out, t.route.Action) {
			out = append(out, t.route.Action)
		}
	}
	slices.Sort(out)
	return out
}

// The path parameters most tools share, described once for a model.
const (
	argWorkshop = "The workshop's id, from list_workshops."
	argTicket   = "The ticket's key, from list_tickets."
	argFlow     = "The flow's name, from list_flows."
)
