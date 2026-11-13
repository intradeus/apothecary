# One route table: the OpenAPI document, /docs, and the CPUI's client

The API surface used to be declared three times — a mux in `Handler()`, a
hand-written TypeScript client in `cpui/src/lib/api.ts`, and nothing at all for
anyone outside those two. Now it is declared once.

`internal/httpapi/routes.go` holds a table: method, path, operation id, tags,
summary, the handler, and the Go types of the request and response. `Handler()`
builds the mux from it and `openapi.go` reflects over the same table, so the
document cannot describe an endpoint the server lacks, or miss one it has —
two tests assert exactly that, both directions.

```
internal/httpapi/routes.go        one declaration per endpoint
   │  task api:spec
   ▼
api/openapi.yaml                  generated, committed, freshness-tested
   │  task api:client                     └─ served at /api/openapi.{json,yaml}
   ▼                                         and rendered at /docs
cpui/src/lib/api/generated/       machine-written, quarantined
   │
   ▼
cpui/src/lib/api/                 the anti-corruption layer
   │                                client.ts  transport + error unwrapping
   ▼                                mappers.ts wire → domain
cpui/src/lib/domain/              the frontend's own types
   │
   ▼
components                        never see a wire type
```

### What the generator had to be taught

Reflection alone produces a document that is subtly wrong, and each of these
was found by a test rather than by reading:

- **Required fields.** Nothing in a Go type says which fields are always
  present, so every property arrived optional and every mapper would have had
  to defend against fields that are in fact guaranteed. `encoding/json` already
  answers it: a field without `omitempty` (or `omitzero`) is always written.
- **Nullable collections.** Go marshals a nil slice or map as `null`, not `[]`.
  A client generated from "array, not nullable" breaks on the first empty one —
  which is what the conformance test hit on `/api/projects` (`commands: null`).
- **Custom marshallers.** `flow.DocIO` has a `MarshalJSON` that emits an
  ordered name → value mapping; reflection saw its Go fields and described an
  array of `{Name, Value}` pairs that the server never sends.
- **Named components.** Without `CreateComponentSchemas`, every schema inlines
  and the generated client is a wall of anonymous shapes with nothing to map
  from. Names collide across packages, so `typeNameOverrides` disambiguates and
  a collision is a generation-time error rather than two types quietly sharing
  one schema.

### The test that earned its place

`TestResponsesMatchTheSpec` drives the real handlers and validates the real
bytes against the generated schema (`openapi3filter`). The route table is
hand-written — it is the one place a human still says what an endpoint
returns — and a wrong declaration there is invisible: the code compiles, the
spec validates, and the client is confidently wrong. It caught
`/api/projects/{id}/branches` being declared `[]string` when it returns
`[]kinds.BranchInfo`, a bug that had already reached the mapper.

### The anti-corruption layer

The generated types are the engine's, in the engine's vocabulary (`run_id`,
`ticket_title`, `state: string`). Letting them into components means a rename
on the Go side becomes a thirty-file change, and every component learns to
handle `undefined` for fields that are guaranteed. So they stop at
`src/lib/api/`: the mappers rename, narrow open strings into the unions the UI
switches on (`RunState`, `PingType`, `SnapshotKind` — with an unrecognised
value falling back visibly instead of crashing a page), and turn absent into
`[]`. `cpui/scripts/check-api-boundary.mjs` fails the build if anything outside
that directory imports the generated client; `task api:check` runs it beside
the spec-freshness check, and `task verify` runs both.

Two deliberate exceptions live in the ACL and are commented there: the SSE
stream (`EventSource` is the browser's; OpenAPI describes the endpoint but
cannot hand us a stream) and the direct asset URLs (`/api/attachments/{id}`),
which the browser fetches itself from an `img src` or a download link.

### Swagger UI

`/docs`, from assets vendored into `internal/httpapi/swaggerui/` and embedded
with `go:embed` — not a CDN. An engine that runs behind its own egress firewall,
or on a machine with no internet, must still be able to show its own API.
`task api:swagger-ui` refreshes them.
