// Package timelimit checks in tests that a piece of work stays under a time
// limit, in a way that a busy CI machine does not break.
//
// The limit tests of internal/mime and internal/thirdparty/xhtml check that a
// hostile document is refused quickly. A limit catches work that grows too
// fast with the size of the input. Such work is slow on every try. A shared CI
// machine can also pause a test for a moment, and then one try is slow for a
// reason outside the code. So a slow first try is timed again, and the fastest
// try counts.
package timelimit

import (
	"testing"
	"time"
)

// tries is the most times that Less times the work.
const tries = 3

// Less fails the test unless the work took less than limit. what names the
// work in the message of a failure. elapsed is the time of the first try,
// which the test already measured. When elapsed is not under the limit, Less
// calls again, which must do the same work, and times it, up to tries times in
// all. The fastest try counts.
//
// With the race detector, Less checks nothing: the detector makes the code
// many times slower, so the time says nothing about the code. "make test" runs
// the packages with limit tests a second time without -race.
func Less(t testing.TB, what string, elapsed, limit time.Duration, again func()) {
	t.Helper()
	if raceEnabled {
		return
	}
	check(t, what, elapsed, limit, again)
}

// check is Less without the test for the race detector.
func check(t testing.TB, what string, elapsed, limit time.Duration, again func()) {
	t.Helper()
	fastest := elapsed
	tried := 1
	for ; tried < tries && fastest >= limit; tried++ {
		started := time.Now()
		again()
		fastest = min(fastest, time.Since(started))
	}
	if fastest >= limit {
		t.Fatalf("%s took %v in the fastest of %d tries; the limit is %v", what, fastest, tried, limit)
	}
}
