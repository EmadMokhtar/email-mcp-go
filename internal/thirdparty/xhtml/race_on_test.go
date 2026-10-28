//go:build race

package xhtml // email-mcp: tests of the node limit (not from upstream)

// raceEnabled is true when the tests run with the race detector, which makes the
// code much slower.
const raceEnabled = true
