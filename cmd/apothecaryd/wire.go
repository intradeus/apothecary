package main

import (
	"apothecary/internal/automation"
	"apothecary/internal/engine"
	"apothecary/internal/envcheck"
	"apothecary/internal/mrs"
)

// The services extracted from the engine declare what they consume; these
// assertions check, at the composition root, that what main hands them
// satisfies it.
var (
	_ automation.Runs = (*engine.Engine)(nil)
	_ automation.MRs  = (*mrs.Service)(nil)
	_ envcheck.Flows  = (*engine.Engine)(nil)
)
