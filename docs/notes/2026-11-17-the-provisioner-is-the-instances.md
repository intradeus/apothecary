# The provisioner is the instance's

Why: Kubernetes is the only sandbox host (2026-11-11 note), and every
account got its own copy of the same Kubernetes provisioner integration at
sign-up (`EnsureProvisioner` through the `NewAccount` hook), filled from the
deployment's `APOTHECARY_K8S_*`. The copies were identical, a workshop form
asked to pick one, and System → Integrations showed a provisioner group no
one needed to touch. The owner asked for the provisioner to leave the
integrations and become one setting, edited by an admin, on its own System
page beside the limits, the reaper and storage; and to keep
`projects.provisioner_id`, so a different kind (VM-based, say) could come
back as a choice later.

What now:

- **One provisioner, owned by no one.** It is an `integrations` row with
  `user_id = ''` (`confstore.InstanceOwner`): the descriptor form, sealed
  secrets, the resolver's cache and `Test` all work unchanged. No account
  has the id `''`, so the per-person routes (`/api/integrations/{id}`, the
  list) never reach it. The daemon ensures it at boot
  (`EnsureProvisioner(ctx, kind, name)`): `kubernetes` when
  `APOTHECARY_K8S_NAMESPACE` is set, `fake` under `-dev`. The per-account
  hook (`Server.NewAccount`) had no other use and is gone.
- **`/api/provisioner`**: GET for anyone signed in (secrets masked); PUT
  (integration write rules: config replaces, secrets merge) and `POST
  /api/provisioner/test` for an admin. `POST /api/integrations` refuses
  port `provisioner` (400) and points at it.
- **Workshops keep `provisioner_id`.** A create that names none gets the
  instance's (400 when there is none yet); an edit that names none keeps
  its own. Validation accepts the instance's provisioner or one the
  workshop's owner owns, so a person's own provisioner kind can be added
  without a schema change. The workshop form, chips and overview no longer
  show or pick it.
- **Migration 0008** keeps one provisioner: the ownerless one if there is
  one (a `-dev` seed), else the oldest. Every workshop moves onto it, the
  other copies are deleted, and the one kept loses its owner. Where people
  had tuned their copies differently, only the kept copy's settings
  survive; an admin re-applies anything else on System → Provisioner.
- **UI.** System → Provisioner (`lib/system/ProvisionerPage`) holds three
  blocks, each saved on its own: the cluster (`IntegrationCard` in
  `instance` mode: tests and saves through `/api/provisioner`, no Delete,
  Test and Edit for admins only), the limits (an admin's, instance-wide)
  and the reaper with retention (one's own settings, one draft). The
  Limits, Reaper and Storage entries left the nav. Integrations gained a
  second level in the System nav: a page per kind (repositories, ticket
  sources, harnesses), each with its own Add button.

Tests that hold it: `confstore` `TestEnsureProvisioner`,
`TestProjectProvisioner`, `TestValidateProject`; `pgstore`
`TestInstanceProvisionerMigration`; `httpapi`
`TestInstanceProvisionerRoutes`, `TestInstanceProvisionerMissing`,
`TestProjectCannotBorrowAnIntegration`; `cmd/apothecaryd`
`TestSeedDevProject`, `TestDevBootRunsAFlowAndRecoversIt`; CPUI
`ProvisionerCluster`, `ProvisionerPage`, `PortIntegrations` and `SideNav`
tests.
