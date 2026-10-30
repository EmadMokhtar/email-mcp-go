// Package inflight counts work that runs, so that a program that stops can
// refuse new work and wait for the work that has started.
package inflight

import "sync"

// Tracker counts the work that runs. After Stop it refuses new work. The zero
// value is ready to use. A Tracker is safe for use by many goroutines.
type Tracker struct {
	mu      sync.Mutex
	n       int
	stopped bool
	// idle is made by Stop. It is closed when no work runs after Stop.
	idle chan struct{}
}

// Begin counts a new piece of work and returns true, or returns false after
// Stop. Each true needs one End.
func (t *Tracker) Begin() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return false
	}
	t.n++
	return true
}

// End ends a piece of work that Begin counted.
func (t *Tracker) End() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n--
	if t.stopped && t.n == 0 {
		// After Stop, Begin counts nothing more, so the count reaches zero once.
		close(t.idle)
	}
}

// Stop refuses new work and returns a channel that is closed when no work runs.
// It does not wait. A second call returns the same channel.
func (t *Tracker) Stop() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.stopped {
		t.stopped = true
		t.idle = make(chan struct{})
		if t.n == 0 {
			close(t.idle)
		}
	}
	return t.idle
}
