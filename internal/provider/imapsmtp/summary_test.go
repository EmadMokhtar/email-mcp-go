package imapsmtp

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/require"
)

// The address of a message is kept as the server gives it. A cleaned address
// would be another address: a reply would go to it, and a tool result would show
// it as clean. Each reader of the address decides what to show.
func TestConvertAddressKeepsTheRealAddress(t *testing.T) {
	got, ok := convertAddress(imap.Address{Name: "N\U0000200b", Mailbox: "ceo\U0000200b", Host: "corp.example"})

	require.True(t, ok)
	require.Equal(t, "ceo\U0000200b@corp.example", got.Email)
	require.Equal(t, "N", got.Name, "a display name is cleaned as before")
}

func TestConvertAddressLeavesOutAGroupMarker(t *testing.T) {
	_, ok := convertAddress(imap.Address{Mailbox: "group"})

	require.False(t, ok)
}
