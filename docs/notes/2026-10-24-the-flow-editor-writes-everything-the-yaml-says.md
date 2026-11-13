# The flow editor writes everything the YAML says

2026-10-24, the user: "users should be able to 100% fully recreate whatever is
in the yaml directly in the UI" — "make sure that all the options are
represented". What the editor could not write before, and now can:

- **Agents** — the flow carries them (FLOWLANG §1, v3) and a stage's agent
  must be one, yet none could be declared or edited. The flow panel lists
  them (+ agent); an agent panel edits name (renaming every stage that runs
  it), description, read-only, allow shell, the prompt, inputs (name,
  description, optional) and outputs (name, path, type — file, verdict,
  review_threads, review_replies — description), renaming a port renaming
  the bindings under it. An agent stage links to its agent, or declares the
  one it names.
- **Sequences** — the flow panel lists them (+ sequence); a sequence panel
  edits id, description, its stages (reordered ↑ ↓: the first is the entry
  and a stage saying nothing goes on to the next), adds a stage of each kind
  and lists what runs it; it deletes once nothing does. Its stages are
  edited in place: a selection carries the sequence it is in
  (`Selection.sequence`), the inspector works on that sequence's list — ids,
  routes and neighbours its own, `fail` among the edge targets, "next" / "out"
  for a success that says nothing — and the pane beside the flow stays open
  on it, re-laid out on every change.
- **Chains** — each panel adds a stage of each kind to its own chain, wired
  after the last (`appendStage`, `appendSequenceStage`); a watcher's or an
  outcome's **Runs** picks its own stages or a sequence, `main` included
  (`sequence: X`); + main chain gives a flow that only watches its main
  chain back. Quiet for applies to any debounced signal and reads a string
  `$param`; a command takes a timeout; an action's enum input offers the
  enum `$params` that fit; the six engine-read parameters take a description
  and allowed values like any other.
- Two defaults changed with it: the first watcher or outcome turns a main
  chain's closing `done` into `end` (`armMain` — with `done` the run would
  complete before anything could watch), and `docToYaml` writes the six
  engine-read parameters first, in their order (the new-flow skeleton too).
- A refused rename (not a slug, or taken) puts the field back.

Checked by rebuilding the built-in `conflict-resolver` from the New flow
skeleton through the editor only — parameters, its agent and prompt, the
`settle` sequence with its routes, `main`, the `again` watcher, both outcomes —
and comparing what the saved document compiles to with the template's: equal,
line numbers aside. On the way, a bug: a stage changed after it joined the
reactive document kept its old id (the proxy had already read it), so
`setChainSequence` now gives `appendStage` its fields up front (`init`).
