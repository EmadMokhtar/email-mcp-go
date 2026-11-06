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

// programs are the places to look for Chrome: the names on Linux, and the
// application on macOS, which is not in the PATH.
var programs = []string{
	"google-chrome",
	"chromium",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
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
	for _, program := range programs {
		if found, err := exec.LookPath(program); err == nil {
			return found, nil
		}
	}
	return "", errors.New("make screenshots needs Chrome: install Google Chrome or Chromium, or set CHROME_PATH to the program")
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
