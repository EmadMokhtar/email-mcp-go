//go:build screenshots

package e2e

// This file checks the layout of pages in a real Chrome, where a test of the
// HTML alone cannot see the problem. "make screenshots" runs it with the shots,
// because it needs Chrome too.

import (
	"context"
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/chrometest"
)

// actionsView is what the browser measures on the Connections page.
type actionsView struct {
	// Actions are the texts of the buttons in the tables (Revoke, Sign out), and
	// InView the texts of the ones that show whole inside the visible part of
	// their table.
	Actions []string `json:"actions"`
	InView  []string `json:"inView"`
	// PageWidth and WindowWidth are the width of the page and of the window. A
	// wider page scrolls sideways as a whole.
	PageWidth   float64 `json:"pageWidth"`
	WindowWidth float64 `json:"windowWidth"`
}

// measureScript measures the buttons of every table that can scroll sideways.
// It only compares the places of the boxes, so the fonts of the computer do not
// change the result.
const measureScript = `(() => {
  const actions = [], inView = [];
  for (const column of document.querySelectorAll('.table-scroll')) {
    const edge = column.getBoundingClientRect();
    for (const b of column.querySelectorAll('button')) {
      const r = b.getBoundingClientRect();
      const text = b.textContent.trim();
      actions.push(text);
      if (r.left >= edge.left && r.right <= edge.right) inView.push(text);
    }
  }
  return {actions, inView, pageWidth: document.documentElement.scrollWidth, windowWidth: window.innerWidth};
})()`

// TestConnectionsTableFits checks that the action of each row of the tables on
// the Connections page (Revoke, Sign out) is in view without scrolling
// sideways: at the width of the shots (1280 pixels), in a narrower window, and
// on a phone. The tables may scroll sideways in their column, but the page
// itself does not.
func TestConnectionsTableFits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// The widest rows: every permission, a long name, a token that was used, and
	// a revoked token, whose status is a date.
	all := policy.Scopes(policy.KnownScopes())
	used, err := h.app.Tokens.Create(ctx, store.LocalTenantID, "Claude Desktop on the work laptop", all,
		service.TokenOptions{InBandConfirm: true})
	require.NoError(t, err)
	_, err = h.app.Tokens.Verify(ctx, used)
	require.NoError(t, err)
	_, err = h.app.Tokens.Create(ctx, store.LocalTenantID, "old laptop", all, service.TokenOptions{})
	require.NoError(t, err)
	list, err := h.app.Tokens.List(ctx, store.LocalTenantID)
	require.NoError(t, err)
	for _, info := range list {
		if info.Name == "old laptop" {
			require.NoError(t, h.app.Tokens.Revoke(ctx, store.LocalTenantID, info.ID))
		}
	}

	setupURL, err := h.app.Web.NewSetupURL(ctx)
	require.NoError(t, err)
	browserCtx, cancel := chrometest.Start(t)
	defer cancel()
	require.NoError(t, chromedp.Do(browserCtx, chromedp.EmulateViewport(shotWidth, shotHeight)))
	s := &shooter{t: t, ctx: browserCtx, forbidden: []string{setupURL}}
	s.open(setupURL, "Sign out")
	s.open(h.webBase()+"/connections", "Revoke", "Revoked on")

	for _, size := range []struct{ width, height int64 }{
		{shotWidth, shotHeight}, // the shots
		{1100, 800},             // a narrow laptop window
		{375, 812},              // a phone
	} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			require.NoError(t, chromedp.Do(browserCtx, chromedp.EmulateViewport(size.width, size.height), chromedp.Reload()))
			got, err := chromedp.Run(browserCtx, chromedp.Evaluate[actionsView](measureScript))
			require.NoError(t, err)
			require.Equal(t, []string{"Revoke", "Sign out"}, got.Actions)
			require.Equal(t, got.Actions, got.InView, "an action is out of view")
			require.LessOrEqual(t, got.PageWidth, got.WindowWidth, "the page scrolls sideways")
		})
	}
}
