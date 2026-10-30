package mail

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAlias(t *testing.T) {
	for _, alias := range []string{
		AliasInbox, AliasArchive, AliasSent, AliasDrafts, AliasTrash, AliasJunk,
	} {
		require.True(t, IsAlias(alias), alias)
	}

	require.False(t, IsAlias("INBOX"))
	require.False(t, IsAlias("@other"))
}
