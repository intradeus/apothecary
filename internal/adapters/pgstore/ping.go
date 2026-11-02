package pgstore

import "context"

// Ping is the readiness probe's question: can the engine reach its
// database right now. The pool dials (or reuses) one connection and
// round-trips it, so a stopped Postgres reads as not ready.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
