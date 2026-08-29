# The API boundary

Three layers, and one rule.

```
api/openapi.yaml          generated from the engine's route table (task api:spec)
        │
        ▼
src/lib/api/generated/    machine-written client — NEVER edited, NEVER imported
        │                 outside this directory
        ▼
src/lib/api/              the anti-corruption layer:
   client.ts                transport (one place turns a result into a value
                            or an Error carrying the engine's message)
   mappers.ts               wire → domain, one function per type
   index.ts                 what the app calls: domain in, domain out
        │
        ▼
src/lib/domain/           OUR types — named and shaped for this UI
        │
        ▼
components                import { api } from '$lib/api'
                          import type { Run } from '$lib/domain'
```

**The rule:** nothing outside `src/lib/api/` imports from `src/lib/api/generated/`.
`task api:check` fails the build if something does.

Why: the generated types are the engine's, expressed in its vocabulary
(`run_id`, `ticket_title`, `state: string`). Letting them into components means
an engine rename becomes a thirty-file change, and every component learns to
defend against fields that are in fact guaranteed. The mappers absorb that:
they narrow open strings into the unions the UI switches on, turn absent into
`[]`, and give each field the name this app thinks in.

## Changing the API

1. Edit the route table in `internal/httpapi/routes.go` (and the handler).
2. `task api` — regenerates `api/openapi.yaml` and this client.
3. Fix what the compiler flags. It will be a mapper, which is the point.
