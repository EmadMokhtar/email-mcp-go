package imaptest_test

import (
	"crypto/tls"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/stretchr/testify/require"

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

// TestWithLoginAndWriteCommands checks the login of WithLogin, and that the
// server records each command that changes the mailboxes, in order, but no
// command that only reads.
func TestWithLoginAndWriteCommands(t *testing.T) {
	srv := imaptest.New(t, imaptest.WithLogin("someone", "another-pass"))
	srv.Append("INBOX", []byte("Subject: one\r\n\r\nbody\r\n"))
	srv.Create("Archive")
	require.Equal(t, "someone", srv.User)
	require.Equal(t, "another-pass", srv.Pass)
	require.Empty(t, srv.WriteCommands(), "the setup of a test is not a command of a client")

	dial := func() *imapclient.Client {
		c, err := imapclient.DialTLS(srv.Addr, &imapclient.Options{TLSConfig: &tls.Config{RootCAs: srv.CertPool, MinVersion: tls.VersionTLS12}})
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	require.Error(t, dial().Login("tester", "s3cret-pass").Wait(), "the default login no longer works")

	c := dial()
	require.NoError(t, c.Login("someone", "another-pass").Wait())
	_, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	require.NoError(t, err)
	_, err = c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect()
	require.NoError(t, err)
	_, err = c.List("", "*", nil).Collect()
	require.NoError(t, err)
	require.Empty(t, srv.WriteCommands(), "reading changes nothing")

	_, err = c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	require.NoError(t, c.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close())
	require.NoError(t, c.Create("Projects", nil).Wait())
	_, err = c.Copy(imap.SeqSetNum(1), "Archive").Wait()
	require.NoError(t, err)
	_, err = c.Move(imap.SeqSetNum(1), "Projects").Wait()
	require.NoError(t, err)
	require.NoError(t, c.Expunge().Close())
	require.NoError(t, c.Subscribe("Projects").Wait())
	require.NoError(t, c.Unsubscribe("Projects").Wait())
	require.NoError(t, c.Rename("Projects", "Done", nil).Wait())
	require.NoError(t, c.Delete("Done").Wait())
	appendCmd := c.Append("Archive", 4, nil)
	_, err = appendCmd.Write([]byte("x\r\n\r"))
	require.NoError(t, err)
	require.NoError(t, appendCmd.Close())
	_, err = appendCmd.Wait()
	require.NoError(t, err)

	require.Equal(t, []string{"STORE", "CREATE", "COPY", "MOVE", "EXPUNGE", "SUBSCRIBE", "UNSUBSCRIBE", "RENAME", "DELETE", "APPEND"},
		srv.WriteCommands())
}

// TestCommandsRecordsEveryCommand checks that Commands has every command of a
// client that reaches the mailboxes, reads too, in order, and that a
// connection without a login records nothing.
func TestCommandsRecordsEveryCommand(t *testing.T) {
	srv := imaptest.New(t)
	srv.Append("INBOX", []byte("Subject: one\r\n\r\nbody\r\n"))

	c, err := imapclient.DialTLS(srv.Addr, &imapclient.Options{TLSConfig: &tls.Config{RootCAs: srv.CertPool, MinVersion: tls.VersionTLS12}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.Empty(t, srv.Commands(), "a connection alone is not a command")

	require.NoError(t, c.Login(srv.User, srv.Pass).Wait())
	_, err = c.List("", "*", nil).Collect()
	require.NoError(t, err)
	_, err = c.Status("INBOX", &imap.StatusOptions{NumMessages: true}).Wait()
	require.NoError(t, err)
	_, err = c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	require.NoError(t, err)
	_, err = c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	require.NoError(t, err)
	_, err = c.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect()
	require.NoError(t, err)
	require.NoError(t, c.Unselect().Wait())
	_, err = c.Select("INBOX", nil).Wait()
	require.NoError(t, err)
	require.NoError(t, c.Store(imap.SeqSetNum(1), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close())

	require.Equal(t, []string{"LOGIN", "LIST", "STATUS", "EXAMINE", "SEARCH", "FETCH", "UNSELECT", "SELECT", "STORE"}, srv.Commands())
	require.Equal(t, []string{"STORE"}, srv.WriteCommands())
}
