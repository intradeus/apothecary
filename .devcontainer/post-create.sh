#!/usr/bin/env bash
# devcontainer.json postCreateCommand: once per container, as the remote
# user, in the workspace folder. Idempotent, so it also serves as the
# apothecary project's optional `setup` command. Under apothecary it runs
# during provisioning, before the run's bootstrap, on the default-branch
# checkout — with egress (the module proxy, npm) but no git credentials.
set -euo pipefail
go mod download
# Its postinstall registers lefthook's commit-msg hook (.git is present).
(cd cpui && npm ci)
