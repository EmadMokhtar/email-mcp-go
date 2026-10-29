package smtptest_test

import (
	"crypto/tls"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// TestConnectionsCountsEveryConnection checks that the server counts each
// connection that it accepts, also one that never says anything, in every
// mode.
func TestConnectionsCountsEveryConnection(t *testing.T) {
	for _, mode := range []smtptest.Mode{smtptest.ModeTLS, smtptest.ModeSTARTTLS, smtptest.ModeNoSTARTTLS, smtptest.ModePlain} {
		srv := smtptest.New(t, smtptest.WithMode(mode))
		require.Zero(t, srv.Connections())
		for range 2 {
			var conn net.Conn
			var err error
			if mode == smtptest.ModeTLS {
				conn, err = tls.Dial("tcp", srv.Addr, &tls.Config{RootCAs: srv.CertPool, MinVersion: tls.VersionTLS12})
			} else {
				conn, err = net.Dial("tcp", srv.Addr)
			}
			require.NoError(t, err)
			buf := make([]byte, 64)
			_, err = conn.Read(buf) // the greeting, so the server has accepted it
			require.NoError(t, err)
			require.NoError(t, conn.Close())
		}
		require.Equal(t, 2, srv.Connections())
	}
}
