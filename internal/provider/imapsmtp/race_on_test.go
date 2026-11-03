//go:build race

package imapsmtp

// raceEnabled is true when the tests run with the race detector. Time checks
// skip themselves then, because the detector makes the code much slower.
const raceEnabled = true
