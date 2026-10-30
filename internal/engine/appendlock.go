package engine

import "sync"

// appendLock serializes appends to one run's log. The run's goroutine is
// its writer, but a person acting on a service from the run page appends
// while it runs (a service's history is not the run loop's to sequence), and
// the store assigns seqs per call.
func (e *Engine) appendLock(runID string) func() {
	m, _ := e.appendLocks.LoadOrStore(runID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
