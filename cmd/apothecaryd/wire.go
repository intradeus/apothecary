package main

import (
	"apothecary/internal/ask"
	"apothecary/internal/automation"
	"apothecary/internal/egress"
	"apothecary/internal/engine"
	"apothecary/internal/envcheck"
	"apothecary/internal/mrs"
	"apothecary/internal/pgstore"
	"apothecary/internal/runports"
)

// The services extracted from the engine declare what they consume; these
// assertions check, at the composition root, that what main hands them
// satisfies it.
var (
	_ automation.Runs    = (*engine.Engine)(nil)
	_ automation.MRs     = (*mrs.Service)(nil)
	_ envcheck.Flows     = (*engine.Engine)(nil)
	_ engine.EnvTeardown = (*egress.Service)(nil)
	_ engine.EnvTeardown = (*runports.Service)(nil)
	_ engine.RunPorts    = (*runports.Service)(nil)
	_ egress.AuditStore  = (*pgstore.Store)(nil)
	_ ask.Engine         = (*engine.Engine)(nil)
	_ ask.Store          = (*pgstore.Store)(nil)
)
