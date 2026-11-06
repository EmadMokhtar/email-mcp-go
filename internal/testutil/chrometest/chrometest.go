//go:build screenshots

// Package chrometest starts a headless Chrome (Chrome with no window) for
// tests, through chromedp. Only tests with the build tag "screenshots" use it,
// so a normal "go test" needs no Chrome.
package chrometest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// timeLimit is how long one Chrome may run. A page that never loads then fails
// the test, and the build does not wait for ever.
const timeLimit = 2 * time.Minute

// programs are the places to look for Chrome, in this order: the names on Linux,
// and the application on macOS, which is not in the PATH. The first three were
// here first, and they stay first, so a computer that has more than one Chrome
// finds the same one as before. The rest are other names that some systems
// use: google-chrome-stable and chromium-browser on Linux, and Chromium, which
// is a separate application on macOS.
var programs = []string{
	"google-chrome",
	"chromium",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"google-chrome-stable",
	"chromium-browser",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
}

// Find returns the path of Chrome. When CHROME_PATH is set, it is the only
// choice: a person who sets it wants that program, and a silent fallback would
// hide a wrong path.
func Find() (string, error) {
	if path := os.Getenv("CHROME_PATH"); path != "" {
		found, err := exec.LookPath(path)
		if err != nil {
			return "", fmt.Errorf("make screenshots needs Chrome, and CHROME_PATH is not a program: %w", err)
		}
		return found, nil
	}
	if found, ok := firstProgram(programs); ok {
		return found, nil
	}
	return "", errors.New("make screenshots needs Chrome: install Google Chrome or Chromium, or set CHROME_PATH to the program")
}

// firstProgram returns the path of the first of candidates that is a program.
// A name with no slash is looked up in the PATH.
func firstProgram(candidates []string) (string, bool) {
	for _, candidate := range candidates {
		if found, err := exec.LookPath(candidate); err == nil {
			return found, true
		}
	}
	return "", false
}

// Start prepares a headless Chrome and returns the context of its first tab.
// Chrome itself starts at the first chromedp action on that context. The cancel
// func closes Chrome and waits until it has stopped.
func Start(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	path, err := Find()
	require.NoError(t, err)

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(path))
	if os.Getenv("CI") != "" {
		// The Linux runners of CI do not allow the sandbox of Chrome. On a
		// computer of a person the sandbox stays on.
		opts = append(opts, chromedp.NoSandbox)
	}

	limited, cancelLimit := context.WithTimeout(t.Context(), timeLimit)
	allocator, cancelAllocator := chromedp.NewExecAllocator(limited, opts...)
	tab, cancelTab := chromedp.NewContext(allocator)
	return tab, func() {
		cancelTab()
		cancelAllocator()
		cancelLimit()
	}
}
