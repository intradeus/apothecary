# Parameters declare their type; lists, and labels on the merge request

What a parameter holds is declared, never read off its default (2026-10-26).

- **`type:`** — `bool | int | float | string | duration` — is required on a
  flow's own parameter (the six the engine reads keep their bare default and
  the engine's type; another type, or a list, is refused). The default reads
  *as* the type, so YAML quoting stops deciding it; a missing type is refused
  with the one the old inference would have given. `duration` is a real type
  now: `timeout` (known) is one, and `timeout`/`quiet_for` sites take one
  (they took a string that parsed as a duration). `DocParamValue.Kind` became
  `Hint` — what the YAML wrote, used only for the bare known defaults and
  that error message.
- **`list: true`** — any number of values of the type; the default is a YAML
  list, a bound value a JSON array, canonicalised typed (`EncodeParamList`).
  An enum constrains each item. `paramSite.list` keeps lists out of scalar
  sites and the reverse. Bindings stay `map[string]string`: a list is its
  JSON text, so no store changed.
- **The first reader: `create_mr`'s `labels`** (`actionspec.KindList`). A
  `with:` value may now be a YAML list (`IOPair.Items`); a list setting takes
  that or a list `$parameter`, each item non-blank and no `$` inside.
  `RepoHost.AddMRLabels` (GitHub: `POST issues/{n}/labels`; GitLab:
  `add_labels` on the merge request — both create a missing label) runs
  after `CreateDraftMR`, best effort: a refusal is logged and the merge
  request stands. `builder` labels its merge request from `mr_labels`,
  default `[apothecary]`.
- Editor: the parameter card gets Type and List controls (the default field
  follows the type; changing type keeps what still parses); the Configure
  sheet and the start form draw a list as checkboxes (enum) or a
  comma-separated field, sending a JSON array; timeouts offer duration
  parameters, a list setting a list parameter or items written in place.
