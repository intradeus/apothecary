# The chart can make the kata RuntimeClass

## Why

A cluster with Kata on only some of its nodes (EKS with a node group of
`*.metal` or nested-virtualisation instances) needs the `kata` RuntimeClass
to carry `scheduling`. Its `nodeSelector` and `tolerations` place every pod
that names the class on the Kata nodes, so the engine's pod builder needs
no node settings of its own. Until now, `docs/kubernetes.md` only said
"add a `scheduling` block", and only `install.sh --kata` made the class.

## What now

- `workspaces.kataRuntimeClass`: `create` (default false), `name` (`kata`),
  `handler` (`kata`), `overhead` (250m and 320Mi, what the rig measured),
  `nodeSelector` and `tolerations`. These render
  `templates/runtimeclass-kata.yaml`, with `scheduling` only when either of
  the last two is set.
- **Off by default.** A RuntimeClass is cluster-wide: two releases, or a
  release on a box where `install.sh --kata` already made the class, would
  fight over one object. Installing Kata stays the cluster's job. The chart
  only names it.
- **The name must be the one sandboxes use.** When Kata is the default
  runtime and `workspaces.runtimeClassName` is set, the template fails
  unless the class's `name` is the same. A Kata class of another name, with
  Kata not the default, is the provisioner's `kata=<class>`. The chart
  cannot check that one: it is set in the UI.
- `ci/kata-values.yaml` is the worked example: sysbox by default, Kata
  approved, labelled and tainted Kata nodes.

## Tests that hold it

`task chart:lint`:

- it renders `ci/kata-values.yaml`;
- the schema refuses a handler that is not a DNS label;
- the template refuses a name that differs from `runtimeClassName`.
