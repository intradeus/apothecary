# Structured agent outputs: built-in types and flow schemas

2026-10-26, TODO #33, the user: "make sure the agent respected whatever was
asked of it … enforce things like a schema output on an expected agent
output". The rule they set: *if a built-in stage requires a structured input,
it deserves a built-in type; if the stage is the user's, the user defines the
structure.*

- **Built-in types** stay the four something built in reads: `verdict` (the
  router), `review_threads` (`post_review`), `review_replies`
  (`reply_review_comments`), and the new `commit_message` (`commit`). An
  action's file input now names its type (`actionspec.InputSig.Type`), and the
  parser refuses an agent output of another shape bound to it — it was only a
  description before.
- **Flow schemas** (`schemas:` — `{name, description, json_schema}`, JSON
  Schema 2020-12 in YAML), named by an agent input's or output's `schema:`.
  Compiled at parse with every loader removed (`jsonschema.SchemeURLLoader{}`):
  a `$ref` to `file://` or `https://` is a parse error, never a read. Static:
  every agent output writing a structured input's path must declare the same
  structure. Runtime: outputs checked at stage end (`output_invalid`, the
  violations as `$FAILURE`), schema'd agent inputs before the stage starts
  (`input_missing` / `input_invalid`, before outputs are cleared — as a
  command's). The schema is rendered verbatim into the OUTPUT CONTRACT.
  `commit_message`'s shape is a built-in schema through the same machinery
  (`flow.TypeSchema`), so it is checked and shown the same way.
- **`DocJSON`** keeps the YAML node, not a decoded map, so the editor gets
  the author's key order and re-emits it (Go maps would sort `properties`
  before `type`). Unexported field: an exported `yaml.Node` leaked into the
  OpenAPI components.
- **`commit` action** replaces the builder template's `git commit -F` command:
  the message is piped to `git commit -F -` (shell-quoted, no temp file),
  every checkout or the subject's, a clean tree left alone (idempotent
  re-entry). Considered first: jq in the dind image (wrong level — commands
  run in the repo's own image), jq via the harness feature (installs into the
  user's image), a shim wrapper. The shim got a real `jq` anyway
  (`apothecary-shim jq`, gojq's own CLI, pinned in the builder Dockerfile and
  held to go.mod by a test; `/opt/apothecary` appended to every Exec'd
  script's PATH, never prepended).
- A verdict that fails its shape now feeds the error to the retry as
  `$FAILURE` (it fed nothing; the review types already did).
- Not done: "unstaged code exists" (TODO #33's other half) — an agent that
  must leave changes is not a file, so it fits neither a type nor a schema.
