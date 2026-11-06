//go:build screenshots

package chrometest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindChromeNamesTheFix(t *testing.T) {
	t.Setenv("CHROME_PATH", "/no/such/chrome")
	_, err := Find()
	require.ErrorContains(t, err, "make screenshots needs Chrome")
	require.ErrorContains(t, err, "CHROME_PATH")
}

func TestFindChromeUsesChromePath(t *testing.T) {
	// CHROME_PATH needs a program that exists, and the test binary always does.
	program, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("CHROME_PATH", program)

	found, err := Find()
	require.NoError(t, err)
	require.Equal(t, program, found)
}
