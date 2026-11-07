//go:build screenshots

// Command socialcard makes the preview card of the landing page: a picture of
// site/assets/social-card.svg, exactly 1200 by 630 pixels, saved as
// site/assets/social-card.png. Then it writes the SHA-256 of the SVG that it
// used to site/assets/social-card.png.sha256, in the format of sha256sum. A
// test in test/sitecheck fails when the SVG changed and this record did not,
// so a change of the SVG cannot reach git without a new PNG. "make
// social-card" runs it from the root of the repository:
//
//	go run -tags screenshots ./test/socialcard
//
// It finds Chrome as the tests that use Chrome do (chrometest.Find): the
// program that CHROME_PATH names when it is set, or else the first of the
// usual places on Linux and macOS. chromedp starts that Chrome with no window
// and with a new empty profile, and removes the profile at the end, so the
// Chrome profile of the person is not touched.
//
// It has the build tag "screenshots", like the package chrometest, so a normal
// build and a normal "go test" do not need Chrome.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/chrometest"
)

// The size of a preview card, in pixels: the size that og:image:width and
// og:image:height say.
const (
	cardWidth  = 1200
	cardHeight = 630
)

// timeLimit is how long the work may take. A Chrome that hangs then stops.
const timeLimit = time.Minute

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run is main without the process: it returns the exit code and writes a
// problem to out.
func run(args []string, out io.Writer) int {
	flags := flag.NewFlagSet("socialcard", flag.ContinueOnError)
	flags.SetOutput(out)
	svg := flags.String("svg", "site/assets/social-card.svg", "the source of the card")
	card := flags.String("png", "site/assets/social-card.png", "the picture to write")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if err := makeCard(*svg, *card); err != nil {
		_, _ = fmt.Fprintln(out, "socialcard:", err)
		return 1
	}
	return 0
}

// makeCard opens svgPath in Chrome, in a window of the size of a card at one
// pixel for each point, and saves what Chrome shows as a PNG at pngPath.
func makeCard(svgPath, pngPath string) error {
	abs, err := filepath.Abs(svgPath)
	if err != nil {
		return err
	}
	// The record names the bytes that Chrome opens, so they are read first.
	source, err := os.ReadFile(abs) //nolint:gosec // G304: the path is a flag of this tool
	if err != nil {
		return err
	}
	chrome, err := chrometest.Find()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeLimit)
	defer cancel()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome))
	allocator, cancelAllocator := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAllocator()
	tab, cancelTab := chromedp.NewContext(allocator)
	defer cancelTab()

	address := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
	if err := chromedp.Do(tab,
		chromedp.EmulateViewport(cardWidth, cardHeight, chromedp.EmulateScale(1)),
		chromedp.Navigate(address),
	); err != nil {
		return fmt.Errorf("open %s in Chrome: %w", svgPath, err)
	}
	shot, err := chromedp.Run(tab, chromedp.CaptureScreenshot())
	if err != nil {
		return fmt.Errorf("take the picture: %w", err)
	}

	size, err := png.DecodeConfig(bytes.NewReader(shot))
	if err != nil {
		return fmt.Errorf("read the picture: %w", err)
	}
	if size.Width != cardWidth || size.Height != cardHeight {
		return fmt.Errorf("the picture is %d by %d pixels, not %d by %d", size.Width, size.Height, cardWidth, cardHeight)
	}
	if err := os.WriteFile(pngPath, shot, 0o600); err != nil {
		return err
	}
	record := fmt.Sprintf("%x  %s\n", sha256.Sum256(source), filepath.Base(svgPath))
	// G703 flags this path because it comes from a flag. The person who runs
	// this tool chooses the flag, and the file goes next to the PNG.
	return os.WriteFile(pngPath+".sha256", []byte(record), 0o600) //nolint:gosec // G703: see above
}
