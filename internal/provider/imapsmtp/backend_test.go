package imapsmtp

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/signing"
)

// New is called while the program is wired together, not for a user request.
// A missing scope or signer is a bug in that code, so New panics at once and
// does not let the backend make tokens that another account could open.
func TestNewPanicsWhenWiringIsIncomplete(t *testing.T) {
	signer := testSigner()

	t.Run("empty token scope", func(t *testing.T) {
		require.PanicsWithValue(t, panicNoScope, func() {
			New(Config{TokenScope: ""}, signer)
		})
	})
	t.Run("nil signer", func(t *testing.T) {
		require.PanicsWithValue(t, panicNoSigner, func() {
			New(Config{TokenScope: testScope}, (*signing.Signer)(nil))
		})
	})
	t.Run("both missing", func(t *testing.T) {
		require.Panics(t, func() { New(Config{}, nil) })
	})
	t.Run("scope and signer given", func(t *testing.T) {
		var b *Backend
		require.NotPanics(t, func() { b = New(Config{TokenScope: testScope}, signer) })
		require.NoError(t, b.Close())
	})
}
