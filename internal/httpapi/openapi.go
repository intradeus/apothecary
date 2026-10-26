package httpapi

// The OpenAPI document, generated from the route table.
//
// Nothing here is hand-maintained per endpoint: Spec() walks routes() and
// reflects the Go request/response types into schemas. Add a route to the
// table and it is in the document, the served /docs page and the generated
// frontend client, in that order, with no second place to remember.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"apothecary/internal/flow"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3gen"
	"gopkg.in/yaml.v3"
)

// SpecVersion is the documented API version. Bump it when the surface changes
// in a way a client must notice.
const SpecVersion = "1.1.0"

var pathParamRE = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// Spec builds the OpenAPI document for the whole API — every route, whether or
// not this particular server has the dependencies wired to serve it.
func Spec() (*openapi3.T, error) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info: &openapi3.Info{
			Title:   "Apothecary engine API",
			Version: SpecVersion,
			Description: "The engine's whole surface. The control-plane UI talks only to this API, " +
				"and its client is generated from this document.",
		},
		Servers: openapi3.Servers{{URL: "/", Description: "the engine, same origin as the UI"}},
		Paths:   &openapi3.Paths{},
		Components: &openapi3.Components{
			Schemas:         openapi3.Schemas{},
			SecuritySchemes: openapi3.SecuritySchemes{},
		},
	}
	doc.Components.SecuritySchemes["runToken"] = &openapi3.SecuritySchemeRef{
		Value: openapi3.NewSecurityScheme().
			WithType("http").WithScheme("bearer").
			WithDescription("Per-run token, issued to the in-workspace shim. Not a user credential."),
	}

	// Every named Go type becomes a named component, referenced by $ref. That
	// is what makes the generated client's types nameable — and therefore
	// mappable to the frontend's own domain types — instead of a wall of
	// anonymous inline shapes.
	names := &typeNamer{seen: map[string]reflect.Type{}}
	gen := openapi3gen.NewGenerator(
		openapi3gen.UseAllExportedFields(),
		openapi3gen.SchemaCustomizer(customizeSchema),
		openapi3gen.CreateTypeNameGenerator(names.name),
		openapi3gen.CreateComponentSchemas(openapi3gen.ExportComponentSchemasOptions{
			ExportComponentSchemas: true,
			ExportTopLevelSchema:   true,
		}),
	)
	schemaFor := func(v any) (*openapi3.SchemaRef, error) {
		if v == nil {
			return nil, nil
		}
		return gen.NewSchemaRefForValue(v, doc.Components.Schemas)
	}

	tags := map[string]bool{}
	for _, rt := range (&Server{}).routes() {
		if err := addRoute(doc, rt, schemaFor); err != nil {
			return nil, fmt.Errorf("%s %s: %w", rt.Method, rt.Path, err)
		}
		tags[rt.Tag] = true
	}
	if err := names.conflicts(); err != nil {
		return nil, err
	}
	// Reflection knows nothing about which fields are always present, so every
	// property would reach the client as optional and every mapper would have
	// to defend against a field that is in fact guaranteed. Go's own
	// marshalling already answers the question: a field without `omitempty` is
	// always written.
	applyRequired(doc, names)
	for name := range tags {
		doc.Tags = append(doc.Tags, &openapi3.Tag{Name: name})
	}
	sort.Slice(doc.Tags, func(i, j int) bool { return doc.Tags[i].Name < doc.Tags[j].Name })

	// Round-trip through the loader so every $ref carries its resolved value.
	// The generator emits refs by name; nothing resolves them in place, and an
	// unresolved ref is invalid — which Validate would only tell us about
	// after a broken client had already been generated from it.
	data, err := doc.MarshalJSON()
	if err != nil {
		return nil, err
	}
	loader := openapi3.NewLoader()
	resolved, err := loader.LoadFromData(data)
	if err != nil {
		return nil, fmt.Errorf("reloading the generated document: %w", err)
	}
	return resolved, nil
}

func addRoute(doc *openapi3.T, rt Route, schemaFor func(any) (*openapi3.SchemaRef, error)) error {
	op := &openapi3.Operation{
		OperationID: rt.ID,
		Summary:     rt.Summary,
		Description: rt.Doc,
		Tags:        []string{rt.Tag},
		Responses:   openapi3.NewResponses(),
	}
	// Path parameters come from the pattern itself: one truth, so a renamed
	// segment cannot leave a stale parameter behind.
	for _, m := range pathParamRE.FindAllStringSubmatch(rt.Path, -1) {
		op.AddParameter(&openapi3.Parameter{
			Name: m[1], In: openapi3.ParameterInPath, Required: true,
			Schema: openapi3.NewStringSchema().NewRef(),
		})
	}
	for _, q := range rt.Query {
		schema := openapi3.NewStringSchema()
		if q.Type == "integer" {
			schema = openapi3.NewIntegerSchema()
		}
		for _, e := range q.Enum {
			schema.Enum = append(schema.Enum, e)
		}
		op.AddParameter(&openapi3.Parameter{
			Name: q.Name, In: openapi3.ParameterInQuery,
			Description: q.Doc, Schema: schema.NewRef(),
		})
	}
	if rt.RunToken {
		op.Security = openapi3.NewSecurityRequirements().With(openapi3.NewSecurityRequirement().Authenticate("runToken"))
	}

	// Request body.
	switch {
	case rt.Accepts == "multipart/form-data":
		// A stream of files under one field; the shape is the transport's, not
		// a Go type's.
		schema := openapi3.NewObjectSchema()
		schema.Properties = openapi3.Schemas{
			"files": {Value: openapi3.NewArraySchema().WithItems(openapi3.NewStringSchema().WithFormat("binary"))},
		}
		op.RequestBody = &openapi3.RequestBodyRef{Value: openapi3.NewRequestBody().
			WithRequired(true).
			WithContent(openapi3.Content{"multipart/form-data": openapi3.NewMediaType().WithSchema(schema)})}
	case rt.Body != nil:
		ref, err := schemaFor(rt.Body)
		if err != nil {
			return fmt.Errorf("request schema: %w", err)
		}
		op.RequestBody = &openapi3.RequestBodyRef{Value: openapi3.NewRequestBody().
			WithRequired(true).
			WithContent(openapi3.Content{"application/json": {Schema: ref}})}
	}

	// Success response.
	status := rt.Status
	if status == 0 {
		status = http.StatusOK
		if rt.Returns == nil {
			status = http.StatusNoContent
		}
	}
	resp := openapi3.NewResponse().WithDescription(statusText(status))
	switch {
	case rt.Produces == "text/event-stream":
		resp = resp.WithContent(openapi3.Content{"text/event-stream": openapi3.NewMediaType().
			WithSchema(openapi3.NewStringSchema())})
	case rt.Produces == "text/plain":
		resp = resp.WithContent(openapi3.Content{"text/plain": openapi3.NewMediaType().
			WithSchema(openapi3.NewStringSchema())})
	case rt.Produces == "application/octet-stream":
		resp = resp.WithContent(openapi3.Content{"application/octet-stream": openapi3.NewMediaType().
			WithSchema(openapi3.NewStringSchema().WithFormat("binary"))})
	case rt.Returns != nil:
		ref, err := schemaFor(rt.Returns)
		if err != nil {
			return fmt.Errorf("response schema: %w", err)
		}
		resp = resp.WithContent(openapi3.Content{"application/json": {Schema: ref}})
	}
	op.AddResponse(status, resp)

	// A ping hold answers in-band or hands the turn back; both are success.
	if rt.ID == "raisePing" || rt.ID == "repollPing" {
		op.AddResponse(http.StatusNoContent, openapi3.NewResponse().
			WithDescription("nobody answered inside the hold; re-poll with the X-Ping-ID header"))
		op.AddResponse(http.StatusGone, openapi3.NewResponse().
			WithDescription("the run parked on this question; stop waiting — the answer arrives by resuming the session"))
	}

	// Errors: one shape, everywhere, so a client has one thing to handle.
	errSchema := openapi3.NewSchemaRef("#/components/schemas/ErrorResponse", nil)
	if _, ok := doc.Components.Schemas["ErrorResponse"]; !ok {
		doc.Components.Schemas["ErrorResponse"] = openapi3.NewObjectSchema().
			WithProperty("error", openapi3.NewStringSchema()).NewRef()
	}
	errResp := openapi3.NewResponse().WithDescription("error").
		WithContent(openapi3.Content{"application/json": openapi3.NewMediaType().WithSchema(errSchema.Value)})
	for _, code := range errorCodes(rt) {
		op.AddResponse(code, errResp)
	}

	doc.AddOperation(rt.Path, rt.Method, op)
	return nil
}

// errorCodes is the honest minimum every endpoint of a shape can return. It is
// deliberately coarse: a client branches on 4xx-vs-5xx and the message, and
// enumerating every conflict a handler can reach would be a second source of
// truth that drifts the moment a handler grows a case.
func errorCodes(rt Route) []int {
	codes := []int{http.StatusBadRequest}
	if strings.Contains(rt.Path, "{") {
		codes = append(codes, http.StatusNotFound)
	}
	if rt.Method != http.MethodGet {
		codes = append(codes, http.StatusConflict)
	}
	if rt.RunToken {
		codes = append(codes, http.StatusUnauthorized)
	}
	// Any endpoint can be served by an engine that lacks the dependency it
	// needs (no file store, no relay, no config plane): the handler says so
	// honestly with a 503, so the document does too.
	codes = append(codes, http.StatusServiceUnavailable)
	return codes
}

func statusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return t
	}
	return fmt.Sprintf("status %d", code)
}

// typeNamer names components. A bare Go type name reads best in the generated
// client, so that is the default; the overrides disambiguate the handful of
// types whose bare names would be meaningless out of their package. Two
// distinct types landing on one name is a generation-time error, not a silently
// merged schema.
type typeNamer struct {
	seen map[string]reflect.Type
	dups []string
}

var typeNameOverrides = map[string]string{
	"run.View":                 "RunView",
	"run.PingView":             "PingView",
	"event.Option":             "PingOption",
	"event.Event":              "Event",
	"flow.Doc":                 "FlowDoc",
	"channels.Entry":           "ChannelEntry",
	"kinds.Descriptor":         "KindDescriptor",
	"kinds.RepoInfo":           "RepoInfo",
	"kinds.ModelInfo":          "ModelInfo",
	"portrelay.Exposure":       "PortExposure",
	"engine.CheckRun":          "EnvCheck",
	"engine.PreflightStep":     "PreflightStep",
	"httpapi.flowResp":         "Flow",
	"httpapi.projectBody":      "ProjectRequest",
	"httpapi.ticketBody":       "TicketRequest",
	"httpapi.ticketResp":       "TicketDetail",
	"httpapi.integrationJSON":  "Integration",
	"httpapi.sourceTicketJSON": "SourceTicket",
	"sidecar.TreeEntry":        "WorkspaceEntry",
	"sidecar.File":             "WorkspaceFile",
	"sidecar.Changes":          "WorkspaceChanges",
	"sidecar.ChangedFile":      "WorkspaceChangedFile",
	"sidecar.Commit":           "WorkspaceCommit",
	"sidecar.FileDiff":         "WorkspaceDiff",
	"event.Checkout":           "RunCheckout",
	"run.MRView":               "RunMergeRequest",
	"confstore.ExtraRepo":      "ExtraRepo",
}

func (n *typeNamer) name(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	pkg := t.PkgPath()
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		pkg = pkg[i+1:]
	}
	qualified := pkg + "." + t.Name()
	name := t.Name()
	if over, ok := typeNameOverrides[qualified]; ok {
		name = over
	}
	if name == "" {
		return t.String()
	}
	if prev, ok := n.seen[name]; ok && prev != t {
		// Recorded and reported by conflicts(); the generator keeps going so
		// the error can name every clash at once rather than one per run.
		n.dups = append(n.dups, fmt.Sprintf("%q wanted by both %s and %s", name, prev, t))
	}
	n.seen[name] = t
	return name
}

func (n *typeNamer) conflicts() error {
	if len(n.dups) == 0 {
		return nil
	}
	sort.Strings(n.dups)
	return fmt.Errorf("schema name collision (add an override in typeNameOverrides): %s",
		strings.Join(slices.Compact(n.dups), "; "))
}

// applyRequired marks each component's non-omitempty fields required, matching
// what encoding/json actually emits.
func applyRequired(doc *openapi3.T, names *typeNamer) {
	for name, ref := range doc.Components.Schemas {
		t, ok := names.seen[name]
		if !ok || ref.Value == nil || t.Kind() != reflect.Struct {
			continue
		}
		var required []string
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			jsonName, opts, _ := strings.Cut(tag, ",")
			if jsonName == "" {
				jsonName = f.Name
			}
			// omitempty and omitzero both mean "may not be there at all".
			if strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero") ||
				f.Type.Kind() == reflect.Ptr {
				continue
			}
			if _, present := ref.Value.Properties[jsonName]; present {
				required = append(required, jsonName)
			}
		}
		sort.Strings(required)
		ref.Value.Required = required
	}
}

// customizeSchema fixes what reflection alone gets wrong: a time.Time is a
// date-time string on the wire, not the struct Go sees, and json tags decide
// which fields exist and which are optional.
func customizeSchema(name string, t reflect.Type, tag reflect.StructTag, schema *openapi3.Schema) error {
	if t == reflect.TypeOf(time.Time{}) {
		*schema = *openapi3.NewStringSchema().WithFormat("date-time")
		return nil
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		// Opaque by design (the flow editor's layout): any JSON.
		*schema = openapi3.Schema{}
		return nil
	}
	// Go marshals a nil slice or map as null, not as [] or {}. Without
	// omitempty that null really does go over the wire, so the schema has to
	// admit it — a client generated from "array, not nullable" breaks on the
	// first empty one.
	if json := tag.Get("json"); (t.Kind() == reflect.Slice || t.Kind() == reflect.Map) &&
		!strings.Contains(json, "omitempty") && !strings.Contains(json, "omitzero") {
		schema.Nullable = true
	}
	// A type with its own MarshalJSON does not look like its Go fields, and
	// reflection cannot see through it. Each one has to say what it really
	// emits, or the generated client gets a shape the server never sends.
	if t == reflect.TypeOf(flow.DocIO{}) {
		// An ordered name → literal mapping (marshalOrderedPairs): an action
		// stage's `with:`.
		*schema = *openapi3.NewObjectSchema().WithAdditionalProperties(openapi3.NewStringSchema())
		return nil
	}
	if t == reflect.TypeOf(flow.DocPorts{}) {
		// An ordered name → port mapping: {from|to, type, schema,
		// description, optional}.
		port := openapi3.NewObjectSchema().WithProperties(map[string]*openapi3.Schema{
			"from":        openapi3.NewStringSchema(),
			"to":          openapi3.NewStringSchema(),
			"type":        openapi3.NewStringSchema(),
			"schema":      openapi3.NewStringSchema(),
			"description": openapi3.NewStringSchema(),
			"optional":    openapi3.NewBoolSchema(),
		})
		*schema = *openapi3.NewObjectSchema().WithAdditionalProperties(port)
		return nil
	}
	if t == reflect.TypeOf(flow.DocJSON{}) {
		// A JSON value written in YAML (a schema's json_schema): any JSON,
		// keys in document order. The empty Properties exports it as a
		// component (see paramValueSchema).
		*schema = openapi3.Schema{Properties: openapi3.Schemas{}}
		return nil
	}
	if t == reflect.TypeOf(flow.DocParamValue{}) {
		// A parameter's default, typed: true, 10, 1.5 or "text" — or a list.
		*schema = *paramDefaultSchema()
		return nil
	}
	if t == reflect.TypeOf(flow.DocParams{}) {
		// An ordered name → declaration mapping, always in the long form.
		types := make([]any, len(flow.ParamTypes))
		for i, pt := range flow.ParamTypes {
			types[i] = string(pt)
		}
		decl := openapi3.NewObjectSchema().WithProperties(map[string]*openapi3.Schema{
			"type":        openapi3.NewStringSchema().WithEnum(types...),
			"list":        openapi3.NewBoolSchema(),
			"default":     paramDefaultSchema(),
			"description": openapi3.NewStringSchema(),
			"enum":        openapi3.NewArraySchema().WithItems(openapi3.NewStringSchema()),
		})
		decl.Required = []string{"default"}
		*schema = *openapi3.NewObjectSchema().WithAdditionalProperties(decl)
		return nil
	}
	if t == reflect.TypeOf(flow.DocInt{}) {
		// An integer, or the "$name" of the parameter the field reads.
		*schema = openapi3.Schema{OneOf: openapi3.SchemaRefs{
			openapi3.NewIntegerSchema().NewRef(), openapi3.NewStringSchema().NewRef(),
		}, Properties: openapi3.Schemas{}}
		return nil
	}
	return nil
}

// paramValueSchema is a flow parameter's default: one of the three scalar
// kinds a parameter may be. The empty Properties map is what makes the
// generator export a named type as a component (it keeps only schemas with
// one); it is omitted from the document.
func paramValueSchema() *openapi3.Schema {
	return &openapi3.Schema{OneOf: openapi3.SchemaRefs{
		openapi3.NewBoolSchema().NewRef(), openapi3.NewFloat64Schema().NewRef(), openapi3.NewStringSchema().NewRef(),
	}, Properties: openapi3.Schemas{}}
}

// paramDefaultSchema is a parameter's default: one value, or a list's.
func paramDefaultSchema() *openapi3.Schema {
	return &openapi3.Schema{OneOf: openapi3.SchemaRefs{
		openapi3.NewBoolSchema().NewRef(), openapi3.NewFloat64Schema().NewRef(), openapi3.NewStringSchema().NewRef(),
		openapi3.NewArraySchema().WithItems(paramValueSchema()).NewRef(),
	}, Properties: openapi3.Schemas{}}
}

// --- serving ----------------------------------------------------------------

// specJSON renders the document once per process; it is derived from compiled
// code and cannot change while the binary runs.
var (
	specDoc  *openapi3.T
	specErr  error
	specOnce = func() func() {
		done := false
		return func() {
			if done {
				return
			}
			done = true
			specDoc, specErr = Spec()
		}
	}()
)

func (s *Server) getOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	specOnce()
	if specErr != nil {
		writeErr(w, http.StatusInternalServerError, specErr.Error())
		return
	}
	data, err := specDoc.MarshalJSON()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *Server) getOpenAPIYAML(w http.ResponseWriter, r *http.Request) {
	specOnce()
	if specErr != nil {
		writeErr(w, http.StatusInternalServerError, specErr.Error())
		return
	}
	data, err := SpecYAML(specDoc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(data)
}

// SpecYAML renders a document as YAML — what the committed api/openapi.yaml
// holds and what the client generator reads.
func SpecYAML(doc *openapi3.T) ([]byte, error) {
	data, err := doc.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte("# Generated by cmd/openapigen from internal/httpapi/routes.go. DO NOT EDIT.\n" + b.String()), nil
}
