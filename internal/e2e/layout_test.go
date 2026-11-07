//go:build screenshots

package e2e

// This file checks the layout of pages in a real Chrome, where a test of the
// HTML alone cannot see the problem. "make screenshots" runs it with the shots,
// because it needs Chrome too.

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/chrometest"
)

// tableFit is what the browser measures on the Connections page.
type tableFit struct {
	// TableWidth and ColumnWidth are the full width of the token table and the
	// width that its column shows. A wider table scrolls sideways.
	TableWidth  float64 `json:"tableWidth"`
	ColumnWidth float64 `json:"columnWidth"`
	// Revokes is the number of Revoke buttons, and RevokesInView the number of
	// them that show inside the column of the table.
	Revokes       int `json:"revokes"`
	RevokesInView int `json:"revokesInView"`
	// IDLines is the most lines that a token id takes.
	IDLines int `json:"idLines"`
	// PageWidth and WindowWidth are the width of the page and of the window. A
	// wider page scrolls sideways as a whole.
	PageWidth   float64 `json:"pageWidth"`
	WindowWidth float64 `json:"windowWidth"`
}

// measureScript measures the first table of the Connections page (the tokens).
const measureScript = `(() => {
  const column = document.querySelector('.table-scroll');
  const right = column.getBoundingClientRect().right;
  const revokes = [...column.querySelectorAll('button')].filter((b) => b.textContent.trim() === 'Revoke');
  const ids = [...column.querySelectorAll('tbody code')];
  return {
    tableWidth: column.scrollWidth,
    columnWidth: column.clientWidth,
    revokes: revokes.length,
    revokesInView: revokes.filter((b) => b.getBoundingClientRect().right <= right).length,
    idLines: Math.max(...ids.map((c) => c.getClientRects().length)),
    pageWidth: document.documentElement.scrollWidth,
    windowWidth: window.innerWidth,
  };
})()`

// TestConnectionsTableFits checks the token table at the width of the shots
// (1280 pixels): the whole table shows, with its Revoke buttons, and no token id
// is cut into many lines. On a phone the table may scroll sideways in its
// column, but the page itself does not.
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

	measure := func() tableFit {
		t.Helper()
		got, err := chromedp.Run(browserCtx, chromedp.Evaluate[tableFit](measureScript))
		require.NoError(t, err)
		return got
	}

	got := measure()
	require.LessOrEqual(t, got.TableWidth, got.ColumnWidth, "the token table scrolls sideways at %d pixels", shotWidth)
	require.Equal(t, 1, got.Revokes)
	require.Equal(t, got.Revokes, got.RevokesInView, "a Revoke button is out of view")
	require.Equal(t, 1, got.IDLines, "a token id takes more than one line")
	require.LessOrEqual(t, got.PageWidth, got.WindowWidth)

	// A phone: only the table may scroll sideways.
	require.NoError(t, chromedp.Do(browserCtx, chromedp.EmulateViewport(375, 812), chromedp.Reload()))
	got = measure()
	require.LessOrEqual(t, got.PageWidth, got.WindowWidth, "the page scrolls sideways on a phone")
}
