# GitLab adapter (M4, §12/§13)

- REST v4 with PRIVATE-TOKEN auth; no glab, no GitLab types crossing the
  port. `CheckAccess` requires ≥ Developer (30) on the project (push
  branches + open MRs); `FileExists` uses the files HEAD endpoint, or the
  tree endpoint for trailing-slash directory paths (`.apothecary/agents/`);
  `CreateDraftMR` prefixes `Draft: ` and targets the default branch with
  remove_source_branch; `MRState` maps opened/locked→open.
- The adapter is **bound to the one v1 project at construction**
  (config gitlab.project_id): §4's `MRState(MRRef)` carries no repo.
- **The repo-scoped token deliberately CANNOT merge**: gitlab.com fully
  protects default branches (Maintainer-only merge), so the §4 mergin flow's
  is enforced by GitLab itself — the factory can push branches
  and open draft MRs but never merge them. Discovered via 401 on the merge
  API with the Developer-role project access token; recorded as the
  intended §15 posture, not a limitation.
- **Verdict-contract hardening (found live)**: gpt-5-mini produced a full,
  substantive verdict but printed the JSON as reply text instead of writing
  artifacts/verdict.json → output_contract → abandoned (review has no
  on_fail in the canonical flow). The §3.2 verdict contract line now
  explicitly demands the file-writing tool and states that printing does
  not count. The file requirement stays hard (that is what makes verdicts
  trustworthy); deployments can additionally give review a self-retry
  edge (`on_fail: {to: review, max: 2}`) — flows are data.
