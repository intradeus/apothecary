# Commands declare structures too; a schema narrows a type

Same day, the user: "is there a reason why command stages can't declare a
schema? … why can't the 2 share the same schema in output → input?" There
was none — the schema lived on agent decls only because commands had no decl.

- **Long form** `name: {path, schema}` on a command's `input:`/`output:`
  (`IOPair.Schema`, `IOBinding.Schema`); both keys required, nothing else
  allowed, refused on any other stage type with the reason. The document's
  JSON keeps it as `{path, schema}`; the editor splits it on load into
  `input_schema`/`output_schema` beside the plain `StageIO` (every consumer
  of bindings stays `name → path`) and `docToYaml` joins it back.
  `BindingRows` carries the schema on the row, so renaming a binding keeps it.
- **Agreement is by writer, whoever it is**: agent outputs (decl), command
  bindings (long form), action outputs (opaque files) all count. A command
  writing for an action's *typed* input is exempt — it cannot declare a
  built-in type, and the action parses the file anyway (a flow test feeds
  `post_review` from a script).
- **Runtime**: a command's structured inputs are checked after the existence
  check, its outputs after its exit-0 existence check (`checkFiles`, shared
  with agents).
- **`schema` beside `type`** (was mutually exclusive): the file holds both;
  `Flow.OutputSchemas` lists the type's built-in schema then the flow's, both
  rendered into the contract; the by-hand type checks (verdict, review_*) run
  first. Agreement: a writer satisfies a reader when it has every structure
  the reader names (`shape.satisfies`).
