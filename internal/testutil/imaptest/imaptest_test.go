package imaptest_test

import (
	"testing"

	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
)

// TestNewThenCloseAtOnce starts servers and closes them with no delay. This
// used to be a data race: imapserver.Server.Serve calls WaitGroup.Add after
// it registers the listener, and Server.Close calls WaitGroup.Wait. If Close
// ran before Serve reached the Add call, the race detector reported it.
func TestNewThenCloseAtOnce(t *testing.T) {
	for _, mode := range []imaptest.Mode{imaptest.ModeTLS, imaptest.ModeSTARTTLS, imaptest.ModePlain} {
		for range 20 {
			srv := imaptest.New(t, imaptest.WithMode(mode))
			srv.Close()
		}
	}
}
