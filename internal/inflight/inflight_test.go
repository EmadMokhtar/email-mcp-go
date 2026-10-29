package inflight

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// closedWithin reports whether ch is closed within d.
func closedWithin(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func TestTrackerWaitsForRunningWorkAndRefusesNewWork(t *testing.T) {
	var tr Tracker
	require.True(t, tr.Begin())
	require.True(t, tr.Begin())

	idle := tr.Stop()
	require.False(t, tr.Begin(), "no new work after Stop")
	require.False(t, closedWithin(idle, 50*time.Millisecond), "two pieces of work still run")

	tr.End()
	require.False(t, closedWithin(idle, 50*time.Millisecond), "one piece of work still runs")
	tr.End()
	require.True(t, closedWithin(idle, time.Second))

	require.Equal(t, idle, tr.Stop(), "a second Stop returns the same channel")
	require.False(t, tr.Begin())
}

func TestTrackerStopWithNothingRunning(t *testing.T) {
	var tr Tracker
	require.True(t, tr.Begin())
	tr.End()

	require.True(t, closedWithin(tr.Stop(), time.Second))
}
