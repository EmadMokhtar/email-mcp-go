//go:build race

package mime

// raceEnabled is true when the tests run with the race detector. The heavy
// tests (skipUnderRace) and the time tests skip themselves then, because the
// detector makes the code much slower.
const raceEnabled = true
