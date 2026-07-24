// Package workspace embeds the apothecary-owned workspace sources (DESIGN2
// §8.1, §8.4) — single copies, consumed at runtime: the engine builds the
// base image from BaseContext and pushes Template to the provisioner.
// Everything here is invariant material (shim, firewall, agent wiring);
// user-owned toolchain lines live in project config, never in files.
package workspace

import "embed"

// BaseContext is the base-image build context: the Dockerfile, the files
// it COPYs, and the shim source tree its multi-stage build compiles (the
// same tree wsimage.EnsureShim builds standalone for the devcontainer
// provisioner).
//
//go:embed Dockerfile init-firewall.sh shim
var BaseContext embed.FS

// Feature is the injected devcontainer feature that brings the §11 egress
// firewall to repo-authored dev containers (the devcontainer provisioner):
// capAdd NET_ADMIN + a container-start entrypoint. init-firewall.sh is
// NOT here — it is single-sourced from BaseContext and copied into the
// materialized feature dir by the adapter.
//
//go:embed feature
var Feature embed.FS

// HarnessFeature is the injected devcontainer feature that installs the
// project's coding harness into repo-authored dev containers, which run the
// REPO's image and so never get the §8 pipeline image that bakes it in. The
// per-project install commands are NOT here — the adapter generates them
// into the materialized feature dir (harness-binaries + harness-install.sh).
//
//go:embed harness-feature
var HarnessFeature embed.FS
