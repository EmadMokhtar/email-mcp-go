package mime

import "testing"

// skipUnderRace skips a heavy test when the tests run with the race detector.
// A heavy test converts large or hostile HTML in one goroutine. Under -race
// each of these tests took from 3 s to 27 s, about 10 times longer, and together
// they were most of the time of the whole test run. The detector cannot find a
// race in work that one goroutine does. The smaller tests of the same code
// still run with -race.
//
// "make test" runs this package a second time without -race (NORACE_PKGS in
// the Makefile), so the heavy tests still run, in CI too.
func skipUnderRace(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("a heavy test: make test runs it without -race")
	}
}
