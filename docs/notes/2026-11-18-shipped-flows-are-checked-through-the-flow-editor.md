# Shipped flows are checked through the flow editor

Why: opening the built-in `builder` in the flow editor and validating it
answered `flow:515: stage "in_progress": with "to": a value is required
(parameter "in_progress_status")`. The YAML in `internal/flow/builtin/flows`
was valid, and every test was green. The flow breaks on the way through the
editor:

1. the engine's document of a flow (`flow.DocParam.MarshalJSON`) says
   `"default": ""` for a parameter that has no default;
2. the editor writes an empty default back as no default only where the
   parameter may wait for the workshop's binding — and its rule for that
   (`flowdoc.ts`) knew models and harnesses, not the ticket statuses that
   `ticket_transition`'s `to` reads since 2026-11-15
   (`actionspec.SettingSig.Unbound`);
3. so `in_progress_status: { type: string, default: '' }` went back to the
   engine, which refuses a blank status.

`builder`, `hands-on` and `examples/flows/v5.yaml` were refused this way: a
person could not validate, save or copy them from the editor.

No test saw it because the trip crosses two languages. The Go tests parse
the files as written. The CPUI's round-trip test (`flowdoc.test.ts`, "loses
nothing of …") reads the same files with a YAML parser, where a missing
default is `undefined`, never the engine's `""`.

What now:

- **The editor's rule.** `readWhereItMayWait` (was `readAsModelOrHarness`)
  also counts any action setting reading the parameter. Two options: mirror
  the engine's list of settings that may wait (today one,
  `ticket_transition.to`), or count every `with:` value. The second was
  chosen: it cannot fall behind the registry, and it loses nothing — at a
  setting that may not wait the engine refuses an empty default too (an
  enum's value must be one of its values, a text must say something), and
  then says the clearer thing: the parameter has no value. The same
  reasoning the rule already made for an enum harness option.
- **The engine's document is unchanged.** `"default": ""` for no default
  stays: the editor's form holds an empty field either way, and has to
  write it as no default at these sites whatever the document says.
- **`task flows:check`** (`cmd/flowcheck`, `cpui/scripts/flows-through-editor.mjs`)
  runs the trip on every built-in flow and on the reference flows
  (`examples/flows`, `internal/flow/testdata`): the engine writes each flow
  and its document; Node runs the editor's own `normalizeDoc` and
  `docToYaml` on the document; the engine parses the result and holds it to
  no error, no warning, and the same meaning as the flow it came from. The
  meaning is the parsed flow less what a rewrite may change: lines, the
  hash, and the order of stages (each chain's first stage is kept),
  parameters and references. What each step wrote stays in `build/flows/`.
- **Where it runs.** It needs Go and Node, so it is neither side's test: a
  task of its own, in `task test`, `task check` and `task ci`, and CI's
  `flows-check` job, which runs when the go side or the ui side changed.
  Golden files were the alternative (the document checked in for the CPUI's
  tests, the editor's YAML checked in for Go's): each side would stay in its
  own job, but two generated files per flow would change with every edit of
  a flow and need regenerating; a live check has nothing to keep current.

Held by: `cmd/flowcheck`'s tests (`TestVerifyFails` has the refused
default, a changed value, a lost stage, a moved entry),
`flowdoc.test.ts` ("writes a blank default on a ticket status parameter as
none", "knows whether … reads a parameter where it may wait"),
`ParamsEditor.test.ts` ("explains an empty one an action reads as its ticket
status"), and `task flows:check` itself.
