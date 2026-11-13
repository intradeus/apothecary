# The YAML tab is a code editor, checked against the flow's schema

2026-10-26, the user: "update the yaml editor to be with colors … and could we
enforce a yaml schema on the editor directly?"

- **The schema** (`flow.Schema`, `GET /api/flows/schema`): JSON Schema
  2020-12 of the document as written — shorthand and all — built in Go from
  the registries (stage types, observed signals, actions and, per action
  through `if`/`then`, its inputs and their enums; start policies; output
  types; the six engine-read parameters with their defaults). Tests: every
  template and example validates; typos (an unknown key, a stage type, a
  signal, a start policy, an action input, `then`, the version) do not; and
  the schema names exactly the YAML keys the parser's structs read.
- **The editor** (`YamlEditor.svelte`, lazy-loaded with the tab): CodeMirror 6
  with the YAML mode, highlighted from the theme's tokens, `$param` and
  `$SENTINEL` marked; `codemirror-json-schema` for key completion and hover
  docs; one linter running the schema check and then the parser
  (`/api/flows/validate`, debounced), the parser's error dropped on a line the
  schema already marks; `yamlValues.ts` completes every enum value — empty or
  started, block or flow style — which the library did only on a started
  value and a schema's first branch. The editor chunk is ~270 KB gzipped,
  fetched only by the YAML tab.
- **The parser's unknown key** reads `unknown key "mian" in the document — it
  takes name, version, …` (was Go's `field mian not found in type
  flow.Doc`), for every decoded part: a stage, a watcher or an outcome, `on:`,
  a route, a parameter, an agent and its inputs and outputs.
