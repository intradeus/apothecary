// Package workspace embeds the factory-owned workspace sources (DESIGN2
// §8.1, §8.4) — single copies, consumed at runtime: the engine builds the
// base image from BaseContext and pushes Template to the provisioner.
// Everything here is invariant material (shim, firewall, agent wiring);
// user-owned toolchain lines live in project config, never in files.
package workspace

import "embed"

// BaseContext is the base-image build context: the Dockerfile plus the two
// files it COPYs.
//
//go:embed Dockerfile ask-human.mjs init-firewall.sh
var BaseContext embed.FS

// Template is the engine-pushed Coder template (§8.4).
//
//go:embed template/main.tf
var Template embed.FS
