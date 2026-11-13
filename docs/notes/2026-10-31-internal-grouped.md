# internal/ grouped (2026-10-31)

Fifty-eight flat folders under `internal/` became twelve, grouped by what a
package is about; the README has the map. Moves only: every folder name still
matches its package, so no identifier, OpenAPI name, event or golden changed.
Nothing merged, because a merge can create an import cycle and a move cannot.

`pgstore`, `browser`, `egressgw` and `kube` joined `adapters/`, so the rule on
who may import an adapter covers them. `platform/` may import only itself, and
`flow/` only itself. Earlier notes and `docs/clean-architecture-plan.md` keep
the paths of their day; so do the comments in the frozen SQL migrations.
