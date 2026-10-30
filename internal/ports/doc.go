// Package ports holds the engine-shaped vendor boundaries (DESIGN.md §4),
// one sub-package per capability: envport (the workspace), agentport (the
// harness), repoport (the repository host), trackerport (the issue
// tracker), egressport (the egress gateway), browserport (the project
// browser) and notifyport (notification channels). Ports are shaped by what
// the engine needs, never by a vendor's API; no vendor types cross this
// boundary. The fakes in internal/adapters/fake are the test substrate for
// every §16 scenario. This package itself declares nothing.
package ports
