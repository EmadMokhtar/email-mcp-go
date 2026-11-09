package timelimit

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// recorder is a testing.TB that keeps the message of Fatalf instead of
// stopping the test.
type recorder struct {
	testing.TB
	failed string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatalf(format string, args ...any) {
	r.failed = fmt.Sprintf(format, args...)
}

// work returns a function that counts its calls in calls.
func work(calls *int) func() {
	return func() { *calls++ }
}

func TestAFastFirstTryIsNotTimedAgain(t *testing.T) {
	r := &recorder{TB: t}
	calls := 0

	check(r, "the work", 10*time.Millisecond, time.Second, work(&calls))

	require.Empty(t, r.failed)
	require.Zero(t, calls)
}

// The first try was slow, as when the machine paused the test. The work
// itself is fast, so the second try passes.
func TestASlowFirstTryIsTimedAgain(t *testing.T) {
	r := &recorder{TB: t}
	calls := 0

	check(r, "the work", 2*time.Second, time.Second, work(&calls))

	require.Empty(t, r.failed)
	require.Equal(t, 1, calls)
}

func TestWorkThatIsSlowOnEveryTryFails(t *testing.T) {
	r := &recorder{TB: t}
	calls := 0
	slow := func() {
		calls++
		time.Sleep(20 * time.Millisecond)
	}

	check(r, "the work", time.Second, 10*time.Millisecond, slow)

	require.Equal(t, tries-1, calls)
	require.Contains(t, r.failed, "the work took ")
	require.Contains(t, r.failed, fmt.Sprintf("in the fastest of %d tries", tries))
	require.Contains(t, r.failed, "the limit is 10ms")
}
