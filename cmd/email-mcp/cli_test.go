package main

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"

	"github.com/EmadMokhtar/email-mcp-go/internal/app"
	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
	"github.com/EmadMokhtar/email-mcp-go/internal/store"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// ---------------------------------------------------------------- helpers

// alice is the address of the accounts that the tests connect.
const alice = "alice@example.org"

// fakeSystem is the system of the command tests: a terminal or not, passwords
// that the user "types", a browser that records the links, and a discovery
// with fixed answers. It never touches the real terminal, browser or network.
type fakeSystem struct {
	mu        sync.Mutex
	terminal  bool
	passwords []string
	opened    []string
	open      func(url string) error
	found     []account.MailServer
	findErr   error
	finds     int
}

func (f *fakeSystem) system() system {
	return system{
		isTerminal: func() bool { return f.terminal },
		readPassword: func(context.Context) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.passwords) == 0 {
				return nil, io.EOF
			}
			p := f.passwords[0]
			f.passwords = f.passwords[1:]
			return []byte(p), nil
		},
		openBrowser: func(url string) error {
			f.mu.Lock()
			f.opened = append(f.opened, url)
			open := f.open
			f.mu.Unlock()
			if open != nil {
				return open(url)
			}
			return nil
		},
		discover: func(context.Context, string) ([]account.MailServer, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.finds++
			return f.found, f.findErr
		},
	}
}

func (f *fakeSystem) discoveries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finds
}

// result is what a command did.
type result struct {
	code   int
	stdout string
	stderr string
}

// runCLI runs the command line with the fake system (no terminal when f is
// nil) and stdin as standard input.
func runCLI(t *testing.T, f *fakeSystem, stdin string, args ...string) result {
	t.Helper()
	if f == nil {
		f = &fakeSystem{}
	}
	var out, errOut syncBuffer
	code := runWith(context.Background(), f.system(), args, strings.NewReader(stdin), &out, &errOut)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// requireNoLeak checks that no secret is in the output: standard output,
// standard error, and so also the log.
func requireNoLeak(t *testing.T, r result, secretTexts ...string) {
	t.Helper()
	for _, s := range secretTexts {
		require.NotContains(t, r.stdout, s)
		require.NotContains(t, r.stderr, s)
	}
}

// newCLIEnv points EMAIL_MCP_KEK at a key file that the first command makes,
// and returns a data directory that does not exist yet.
func newCLIEnv(t *testing.T) (dataDir, keyPath string) {
	t.Helper()
	dataDir = filepath.Join(t.TempDir(), "data")
	keyPath = filepath.Join(t.TempDir(), "master.key")
	t.Setenv("EMAIL_MCP_KEK", "file:"+keyPath)
	return dataDir, keyPath
}

// localServers are a test IMAP server and a test SMTP server on 127.0.0.1,
// with the same login.
type localServers struct {
	imap *imaptest.Server
	smtp *smtptest.Server
}

func startLocal(t *testing.T, imapMode imaptest.Mode, smtpMode smtptest.Mode) localServers {
	t.Helper()
	return localServers{
		imap: imaptest.New(t, imaptest.WithMode(imapMode)),
		smtp: smtptest.New(t, smtptest.WithMode(smtpMode)),
	}
}

// flags are the flags of "account add" for the servers.
func (s localServers) flags() []string {
	return []string{
		"--imap-host", "127.0.0.1", "--imap-port", strconv.Itoa(s.imap.Port),
		"--smtp-host", "127.0.0.1", "--smtp-port", strconv.Itoa(s.smtp.Port),
		"--username", s.imap.User,
	}
}

// server returns the settings of the servers with TLS and both pins, as a
// connected account has them.
func (s localServers) server() account.MailServer {
	return account.MailServer{
		IMAPHost: "127.0.0.1", IMAPPort: s.imap.Port, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "127.0.0.1", SMTPPort: s.smtp.Port, SMTPSecurity: account.SecurityTLS,
		Username:       s.imap.User,
		PinnedIMAPCert: s.imap.CertSHA256,
		PinnedSMTPCert: s.smtp.CertSHA256,
	}
}

// openData opens the store and the vault of a data directory with the key file,
// as a second program would. The caller closes the store.
func openData(t *testing.T, dir, keyPath string) (*store.Store, *secrets.Vault) {
	t.Helper()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	kek, err := secrets.NewFileKEK(keyPath)
	require.NoError(t, err)
	return st, secrets.NewVault(st, kek)
}

// addAccount stores an account of the local tenant with its sealed password, as
// "account add" would. It makes the key file when it is missing.
func addAccount(t *testing.T, dir, keyPath, address string, server account.MailServer, password string) *account.Account {
	t.Helper()
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		require.NoError(t, secrets.GenerateKeyFile(keyPath))
	}
	ctx := context.Background()
	st, vault := openData(t, dir, keyPath)
	defer func() { require.NoError(t, st.Close()) }()
	tenant, err := st.EnsureLocalTenant(ctx)
	require.NoError(t, err)
	a := account.NewIMAPAccount(tenant, address, server, time.Now())
	version, sealed, err := vault.Encrypt(ctx, tenant, secrets.AAD(string(tenant), string(a.ID), "password"), []byte(password))
	require.NoError(t, err)
	require.NoError(t, st.CreateAccountWithSecret(ctx, a, "password", version, sealed))
	return a
}

// storedAccounts returns the accounts of the local tenant and their
// passwords, read with the key file.
func storedAccounts(t *testing.T, dir, keyPath string) ([]*account.Account, []string) {
	t.Helper()
	ctx := context.Background()
	st, vault := openData(t, dir, keyPath)
	defer func() { require.NoError(t, st.Close()) }()
	accounts, err := st.ListAccounts(ctx, store.LocalTenantID)
	require.NoError(t, err)
	var passwords []string
	for _, a := range accounts {
		version, sealed, err := st.GetAccountSecret(ctx, a.TenantID, a.ID, "password")
		require.NoError(t, err)
		plain, err := vault.Decrypt(ctx, a.TenantID, version, secrets.AAD(string(a.TenantID), string(a.ID), "password"), sealed)
		require.NoError(t, err)
		passwords = append(passwords, string(plain))
	}
	return accounts, passwords
}

// requireNoData checks that the command opened nothing: the data directory was
// not made.
func requireNoData(t *testing.T, dir string) {
	t.Helper()
	_, err := os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist, "nothing was opened")
}

// requireNoAccount checks that no account was saved. The data directory may
// exist.
func requireNoAccount(t *testing.T, dir, keyPath string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "email-mcp.db")); errors.Is(err, os.ErrNotExist) {
		return
	}
	accounts, _ := storedAccounts(t, dir, keyPath)
	require.Empty(t, accounts, "no account was saved")
}

// ---------------------------------------------------------------- help

func TestHelpListsTheCommands(t *testing.T) {
	r := runCLI(t, nil, "", "help")
	require.Equal(t, exitOK, r.code)
	for _, name := range []string{"serve", "setup", "account", "token", "import-env", "key", "doctor", "version"} {
		require.Contains(t, r.stdout, "  "+name+" ")
	}

	r = runCLI(t, nil, "", "account", "help")
	require.Equal(t, exitOK, r.code)
	for _, name := range []string{"add", "list", "reconnect", "remove", "test"} {
		require.Contains(t, r.stdout, "  "+name+" ")
	}

	for _, group := range []string{"account", "token", "key"} {
		r = runCLI(t, nil, "", group)
		require.Equal(t, exitUsage, r.code, group)
		require.Contains(t, r.stderr, "Usage:", group)
		r = runCLI(t, nil, "", group, "nope")
		require.Equal(t, exitUsage, r.code, group)
		require.Contains(t, r.stderr, "unknown command", group)
		require.NotContains(t, r.stderr, "nope", "an argument is never echoed")
	}
}

// ---------------------------------------------------------------- account add

// Without a terminal, the password is the first line of standard input. The
// self-signed certificates of the local servers are trusted only with the
// fingerprints that they show, given with --trust-fingerprint (with or without
// colons).
func TestAccountAddReadsPasswordFromStdin(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	args := append([]string{"account", "add", "--email", alice, "--data-dir", dir,
		"--trust-fingerprint", srv.imap.CertSHA256,
		"--trust-fingerprint", connect.GroupFingerprint(srv.smtp.CertSHA256)}, srv.flags()...)

	r := runCLI(t, nil, srv.imap.Pass+"\nthe second line is not read\n", args...)

	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, alice)
	require.NotContains(t, r.stderr, alice, "standard error, where the log goes, never has an address")
	requireNoLeak(t, r, srv.imap.Pass)
	accounts, passwords := storedAccounts(t, dir, keyPath)
	require.Len(t, accounts, 1)
	a := accounts[0]
	require.Equal(t, alice, a.Address)
	require.Equal(t, srv.server(), *a.Mail, "the pins are saved for the servers that were tested")
	require.Equal(t, []string{srv.imap.Pass}, passwords)
	require.Equal(t, account.MailSend, a.Level(account.AreaMail))
	require.Contains(t, r.stdout, string(a.ID))
}

// On a terminal, the password is read without echo, and each certificate of a
// server on this computer needs one "y". No second confirmation is asked.
func TestAccountAddOnATerminal(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	f := &fakeSystem{terminal: true, passwords: []string{srv.imap.Pass}}
	args := append([]string{"account", "add", "--email", alice, "--data-dir", dir}, srv.flags()...)

	r := runCLI(t, f, "y\ny\n", args...)

	require.Equal(t, exitOK, r.code, r.stderr)
	require.Equal(t, 2, strings.Count(r.stdout, "Trust this certificate? [y/N]"))
	require.Contains(t, r.stdout, connect.GroupFingerprint(srv.imap.CertSHA256))
	require.Contains(t, r.stdout, connect.GroupFingerprint(srv.smtp.CertSHA256))
	require.Contains(t, r.stdout, "127.0.0.1:"+strconv.Itoa(srv.imap.Port))
	require.Contains(t, r.stdout, connect.TextCertUnknown)
	require.NotContains(t, r.stdout, "Type yes", "a server on this computer needs no second confirmation")
	requireNoLeak(t, r, srv.imap.Pass)
	accounts, passwords := storedAccounts(t, dir, keyPath)
	require.Len(t, accounts, 1)
	require.Equal(t, srv.server(), *accounts[0].Mail)
	require.Equal(t, []string{srv.imap.Pass}, passwords)
}

// A "no" to a certificate saves nothing, and the password goes to no server
// again.
func TestAccountAddStopsWhenTheUserDoesNotTrust(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	f := &fakeSystem{terminal: true, passwords: []string{srv.imap.Pass}}
	args := append([]string{"account", "add", "--email", alice, "--data-dir", dir}, srv.flags()...)

	r := runCLI(t, f, "n\n", args...)

	require.Equal(t, exitError, r.code)
	require.Equal(t, 1, strings.Count(r.stdout, "Trust this certificate?"))
	require.Contains(t, r.stderr, "Nothing was saved")
	require.Zero(t, srv.smtp.AuthAttempts(), "the password went to no server")
	requireNoAccount(t, dir, keyPath)
}

// Trust is offered only for a certificate that no known authority signed. The
// certificate of the test server is for 127.0.0.1 only, so for "localhost" its
// name does not match: it is never offered, on a terminal or with
// --trust-fingerprint.
func TestAccountAddNeverOffersTrustForAHostNameMismatch(t *testing.T) {
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	args := func(dir string) []string {
		return append([]string{"account", "add", "--email", alice, "--data-dir", dir}, slices.Concat(srv.flags(), []string{"--imap-host", "LOCALHOST"})...)
	}

	t.Run("on a terminal", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		f := &fakeSystem{terminal: true, passwords: []string{srv.imap.Pass}}

		r := runCLI(t, f, "y\ny\ny\nyes\n", args(dir)...)

		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stdout, connect.TextCertWrongHost)
		require.NotContains(t, r.stdout, "Trust this certificate?", "nothing is offered while one certificate cannot be trusted")
		require.Contains(t, r.stderr, "cannot be trusted")
		requireNoAccount(t, dir, keyPath)
	})

	t.Run("without a terminal", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		r := runCLI(t, nil, srv.imap.Pass+"\n", append(args(dir),
			"--trust-fingerprint", srv.imap.CertSHA256, "--trust-fingerprint", srv.smtp.CertSHA256)...)

		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stdout, connect.TextCertWrongHost)
		require.Contains(t, r.stderr, "cannot be trusted")
		requireNoAccount(t, dir, keyPath)
		requireNoLeak(t, r, srv.imap.Pass)
	})
}

// Without a terminal, nothing is trusted on its own: --trust-fingerprint must
// be the fingerprint that the server showed in this run.
func TestAccountAddWithoutTerminalNeedsTheMatchingFingerprint(t *testing.T) {
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	base := func(dir string) []string {
		return append([]string{"account", "add", "--email", alice, "--data-dir", dir}, srv.flags()...)
	}

	t.Run("no fingerprint", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		r := runCLI(t, nil, srv.imap.Pass+"\n", base(dir)...)
		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stderr, "--trust-fingerprint "+srv.imap.CertSHA256)
		requireNoAccount(t, dir, keyPath)
		requireNoLeak(t, r, srv.imap.Pass)
	})

	t.Run("another fingerprint", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		r := runCLI(t, nil, srv.imap.Pass+"\n", append(base(dir),
			"--trust-fingerprint", strings.Repeat("0", 64), "--trust-fingerprint", srv.smtp.CertSHA256)...)
		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stderr, "--trust-fingerprint "+srv.imap.CertSHA256)
		requireNoAccount(t, dir, keyPath)
	})

	t.Run("only the IMAP fingerprint", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		r := runCLI(t, nil, srv.imap.Pass+"\n", append(base(dir), "--trust-fingerprint", srv.imap.CertSHA256)...)
		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stderr, "--trust-fingerprint "+srv.smtp.CertSHA256)
		requireNoAccount(t, dir, keyPath)
	})

	t.Run("not a fingerprint", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		r := runCLI(t, nil, srv.imap.Pass+"\n", append(base(dir), "--trust-fingerprint", "abc")...)
		require.Equal(t, exitUsage, r.code)
		requireNoData(t, dir)
	})
}

// A plain (unencrypted) connection to a host that is not on this computer is
// refused, also with --plaintext-i-understand, before the password is read.
func TestAccountAddRefusesPlainToARemoteHost(t *testing.T) {
	dir, _ := newCLIEnv(t)

	for _, side := range []string{"imap", "smtp"} {
		f := &fakeSystem{terminal: true, passwords: []string{"pw-never-read"}}
		r := runCLI(t, f, "yes\nyes\n", "account", "add", "--email", alice, "--data-dir", dir,
			"--imap-host", "imap.example.org", "--smtp-host", "smtp.example.org",
			"--"+side+"-security", "plain", "--"+side+"-port", "1143", "--plaintext-i-understand")

		require.Equal(t, exitError, r.code, side)
		require.Contains(t, r.stderr, "only to a server on this computer", side)
		require.Len(t, f.passwords, 1, "the password was not read")
		requireNoData(t, dir)
	}
}

// A plain connection to a server on this computer needs
// --plaintext-i-understand, or a typed "yes" on a terminal.
func TestAccountAddPlainToThisComputerNeedsConsent(t *testing.T) {
	srv := startLocal(t, imaptest.ModePlain, smtptest.ModePlain)
	args := func(dir string) []string {
		return append([]string{"account", "add", "--email", alice, "--data-dir", dir,
			"--imap-security", "plain", "--smtp-security", "plain"}, srv.flags()...)
	}

	t.Run("without a terminal and without the flag", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		r := runCLI(t, nil, srv.imap.Pass+"\n", args(dir)...)
		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stderr, "--plaintext-i-understand")
		requireNoData(t, dir)
	})

	t.Run("with the flag", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		r := runCLI(t, nil, srv.imap.Pass+"\n", append(args(dir), "--plaintext-i-understand")...)
		require.Equal(t, exitOK, r.code, r.stderr)
		accounts, _ := storedAccounts(t, dir, keyPath)
		require.Len(t, accounts, 1)
		require.Equal(t, account.SecurityPlain, accounts[0].Mail.IMAPSecurity)
		require.Equal(t, account.SecurityPlain, accounts[0].Mail.SMTPSecurity)
		requireNoLeak(t, r, srv.imap.Pass)
	})

	t.Run("on a terminal, typed yes", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		f := &fakeSystem{terminal: true, passwords: []string{srv.imap.Pass}}
		r := runCLI(t, f, "yes\n", args(dir)...)
		require.Equal(t, exitOK, r.code, r.stderr)
		require.Contains(t, r.stdout, "not encrypted")
		accounts, _ := storedAccounts(t, dir, keyPath)
		require.Len(t, accounts, 1)
	})

	t.Run("on a terminal, typed y", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		f := &fakeSystem{terminal: true, passwords: []string{srv.imap.Pass}}
		r := runCLI(t, f, "y\n", args(dir)...)
		require.Equal(t, exitError, r.code, "only the word yes counts")
		require.Len(t, f.passwords, 1, "the password was not read")
		requireNoData(t, dir)
	})
}

// Hosts from discovery are only suggestions. Each one is shown, and the user
// confirms before the password goes to them. Without a terminal, discovery
// alone never leads to a test: the user must give --imap-host and
// --smtp-host.
func TestAccountAddConfirmsDiscoveredHosts(t *testing.T) {
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	local := srv.server()
	local.PinnedIMAPCert, local.PinnedSMTPCert = "", ""
	remote := account.MailServer{
		IMAPHost: "imap.filter-provider.net", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.filter-provider.net", SMTPPort: 587, SMTPSecurity: account.SecuritySTARTTLS,
		Username: alice,
	}

	t.Run("without a terminal", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		f := &fakeSystem{found: []account.MailServer{local, remote}}
		r := runCLI(t, f, srv.imap.Pass+"\n", "account", "add", "--email", alice, "--data-dir", dir)
		require.Equal(t, exitError, r.code)
		require.Equal(t, 1, f.discoveries())
		require.Contains(t, r.stdout, "imap.filter-provider.net")
		require.Contains(t, r.stdout, "smtp.filter-provider.net")
		require.Contains(t, r.stderr, "--imap-host")
		require.Zero(t, srv.smtp.AuthAttempts())
		requireNoData(t, dir)
	})

	t.Run("on a terminal, not confirmed", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		f := &fakeSystem{terminal: true, passwords: []string{srv.imap.Pass}, found: []account.MailServer{local}}
		r := runCLI(t, f, "n\n", "account", "add", "--email", alice, "--data-dir", dir)
		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stdout, "127.0.0.1:"+strconv.Itoa(srv.imap.Port))
		require.Contains(t, r.stdout, "127.0.0.1:"+strconv.Itoa(srv.smtp.Port))
		require.Len(t, f.passwords, 1, "the password was not asked for")
		requireNoData(t, dir)
	})

	t.Run("on a terminal, the user chooses one", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		f := &fakeSystem{terminal: true, passwords: []string{srv.imap.Pass}, found: []account.MailServer{remote, local}}
		r := runCLI(t, f, "2\ny\ny\n", "account", "add", "--email", alice, "--username", srv.imap.User, "--data-dir", dir)
		require.Equal(t, exitOK, r.code, r.stderr)
		require.Contains(t, r.stdout, "imap.filter-provider.net", "every suggestion is shown")
		accounts, _ := storedAccounts(t, dir, keyPath)
		require.Len(t, accounts, 1)
		require.Equal(t, srv.server(), *accounts[0].Mail)
	})

	t.Run("on a terminal, nothing chosen", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		f := &fakeSystem{terminal: true, passwords: []string{srv.imap.Pass}, found: []account.MailServer{remote, local}}
		r := runCLI(t, f, "\n", "account", "add", "--email", alice, "--data-dir", dir)
		require.Equal(t, exitError, r.code)
		require.Len(t, f.passwords, 1)
		requireNoData(t, dir)
	})

	t.Run("nothing found", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		f := &fakeSystem{terminal: true, findErr: errors.New("discovery: no mail server settings found")}
		r := runCLI(t, f, "", "account", "add", "--email", alice, "--data-dir", dir)
		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stderr, "--imap-host")
		requireNoData(t, dir)
	})

	t.Run("only one host given", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		f := &fakeSystem{terminal: true}
		r := runCLI(t, f, "", "account", "add", "--email", alice, "--data-dir", dir, "--imap-host", "127.0.0.1")
		require.Equal(t, exitUsage, r.code)
		require.Zero(t, f.discoveries())
		requireNoData(t, dir)
	})
}

func TestAccountAddUsageErrors(t *testing.T) {
	dir, _ := newCLIEnv(t)
	for name, args := range map[string][]string{
		"no address":       {},
		"a bad security":   {"--email", alice, "--imap-host", "a", "--smtp-host", "b", "--imap-security", "ssl"},
		"an extra word":    {"--email", alice, "extra"},
		"a password flag":  {"--email", alice, "--password", "pw"},
		"a port too large": {"--email", alice, "--imap-host", "a", "--smtp-host", "b", "--imap-port", "70000"},
	} {
		r := runCLI(t, nil, "", append([]string{"account", "add", "--data-dir", dir}, args...)...)
		require.Equal(t, exitUsage, r.code, name)
		requireNoData(t, dir)
	}
}

// ---------------------------------------------------------------- the connect flow

// recordingCreate is a fake Service.CreateIMAPAccount. It records the settings
// of each call and answers with the next error of answers (nil saves).
type recordingCreate struct {
	answers []error
	servers []account.MailServer
}

func (r *recordingCreate) create(_ context.Context, address string, server account.MailServer, _ secrets.Secret) (*account.Account, error) {
	r.servers = append(r.servers, server)
	var err error
	if len(r.answers) > 0 {
		err, r.answers = r.answers[0], r.answers[1:]
	}
	if err != nil {
		return nil, err
	}
	return account.NewIMAPAccount(store.LocalTenantID, address, server, time.Now()), nil
}

// connectWith runs the connect flow of the command line with a fake create.
func connectWith(t *testing.T, f *fakeSystem, answers string, create *recordingCreate, opts connectOptions) (bool, result) {
	t.Helper()
	var out, errOut syncBuffer
	c := &cli{stdin: strings.NewReader(answers), stdout: &out, stderr: &errOut, sys: f.system()}
	server := account.MailServer{
		IMAPHost: "imap.example.org", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.org", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: alice,
	}
	_, ok := c.saveAccount(context.Background(), "account add", create.create, alice, server, "pw-flow", opts)
	return ok, result{stdout: out.String(), stderr: errOut.String()}
}

// certError is a certificate error of the IMAP server.
func certError(fingerprint string, pinned bool, reason error) *service.CertificateError {
	return &service.CertificateError{IMAP: &imapsmtp.CertificateUnknownError{
		Protocol: "imap", Host: "imap.example.org", FingerprintSHA256: fingerprint, Pinned: pinned, Reason: reason,
	}}
}

func TestConnectFlowTrustRules(t *testing.T) {
	fingerprint := strings.Repeat("e1", 32)
	unknown := func() error { return certError(fingerprint, false, x509.UnknownAuthorityError{}) }

	t.Run("a remote server needs a typed yes on a terminal", func(t *testing.T) {
		create := &recordingCreate{answers: []error{unknown(), nil}}
		ok, r := connectWith(t, &fakeSystem{terminal: true}, "y\nyes\n", create, connectOptions{})
		require.True(t, ok, r.stderr)
		require.Contains(t, r.stdout, "Type yes")
		require.Len(t, create.servers, 2)
		require.Empty(t, create.servers[0].PinnedIMAPCert)
		require.Equal(t, fingerprint, create.servers[1].PinnedIMAPCert, "the pin is for the server that showed it")
		require.Empty(t, create.servers[1].PinnedSMTPCert)
	})

	t.Run("a remote server, y without the typed yes", func(t *testing.T) {
		create := &recordingCreate{answers: []error{unknown(), nil}}
		ok, _ := connectWith(t, &fakeSystem{terminal: true}, "y\ny\n", create, connectOptions{})
		require.False(t, ok)
		require.Len(t, create.servers, 1, "no second test")
	})

	t.Run("a remote server without a terminal needs both flags", func(t *testing.T) {
		create := &recordingCreate{answers: []error{unknown(), nil}}
		ok, r := connectWith(t, &fakeSystem{}, "", create, connectOptions{trust: []string{fingerprint}})
		require.False(t, ok, "the fingerprint alone is not enough for a server that is not on this computer")
		require.Contains(t, r.stderr, "--accept-remote-certificate")
		require.Contains(t, r.stdout, "not on this computer", "the risk is shown")
		require.Len(t, create.servers, 1)

		create = &recordingCreate{answers: []error{unknown(), nil}}
		ok, r = connectWith(t, &fakeSystem{}, "", create, connectOptions{trust: []string{fingerprint}, acceptRemote: true})
		require.True(t, ok, r.stderr)
		require.Equal(t, fingerprint, create.servers[1].PinnedIMAPCert)
	})

	t.Run("a refusal tells the user to check the fingerprint elsewhere", func(t *testing.T) {
		create := &recordingCreate{answers: []error{unknown(), nil}}
		ok, r := connectWith(t, &fakeSystem{}, "", create, connectOptions{})
		require.False(t, ok)
		require.Contains(t, r.stderr, "--trust-fingerprint "+fingerprint+" --accept-remote-certificate")
		require.Contains(t, r.stderr, msgCheckFingerprint)
	})

	t.Run("an expired certificate is never offered", func(t *testing.T) {
		create := &recordingCreate{answers: []error{certError(fingerprint, false, x509.CertificateInvalidError{Reason: x509.Expired}), nil}}
		ok, r := connectWith(t, &fakeSystem{terminal: true}, "y\nyes\n", create, connectOptions{trust: []string{fingerprint}})
		require.False(t, ok)
		require.Contains(t, r.stdout, connect.TextCertExpired)
		require.NotContains(t, r.stdout, "Trust this certificate?")
		require.Len(t, create.servers, 1)
	})

	t.Run("a pin that does not match is never offered again", func(t *testing.T) {
		create := &recordingCreate{answers: []error{unknown(), certError(strings.Repeat("f2", 32), true, nil), nil}}
		ok, r := connectWith(t, &fakeSystem{}, "", create, connectOptions{trust: []string{fingerprint, strings.Repeat("f2", 32)}, acceptRemote: true})
		require.False(t, ok)
		require.Contains(t, r.stdout, connect.TextCertPinChanged)
		require.Len(t, create.servers, 2)
	})

	t.Run("a failed test shows a fixed text", func(t *testing.T) {
		create := &recordingCreate{answers: []error{&service.ConnectionTestError{Err: fmt.Errorf("NO [AUTHENTICATIONFAILED] server text: %w", imapsmtp.ErrAuthFailed)}}}
		ok, r := connectWith(t, &fakeSystem{}, "", create, connectOptions{})
		require.False(t, ok)
		require.Contains(t, r.stderr, connect.TextAuthFailed)
		require.NotContains(t, r.stderr+r.stdout, "server text")
	})
}

// ---------------------------------------------------------------- account list, test, remove

func TestAccountListAndTest(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)

	r := runCLI(t, nil, "", "account", "list", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "No account")

	good := addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)
	wrong := addAccount(t, dir, keyPath, "bob@example.org", srv.server(), "pw-wrong-91c2")

	r = runCLI(t, nil, "", "account", "list", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	for _, text := range []string{string(good.ID), alice, string(wrong.ID), "bob@example.org", "Send"} {
		require.Contains(t, r.stdout, text)
	}
	require.NotContains(t, r.stdout, srv.imap.User, "no user name")
	requireNoLeak(t, r, srv.imap.Pass, "pw-wrong-91c2")
	require.NotContains(t, r.stderr, alice)

	r = runCLI(t, nil, "", "account", "test", string(good.ID), "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "ok")

	r = runCLI(t, nil, "", "account", "test", "--data-dir", dir, string(wrong.ID))
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, connect.TextAuthFailed)
	require.NotContains(t, strings.ToUpper(r.stderr+r.stdout), "AUTHENTICATIONFAILED", "never the text of the server")
	requireNoLeak(t, r, "pw-wrong-91c2")

	r = runCLI(t, nil, "", "account", "test", "--data-dir", dir, "no-such-account")
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, "No account")

	r = runCLI(t, nil, "", "account", "test", "--data-dir", dir)
	require.Equal(t, exitUsage, r.code)
}

// account test uses the stored pin: a server that now shows another
// certificate fails, and its certificate is not offered.
func TestAccountTestUsesTheStoredPin(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	server := srv.server()
	server.PinnedIMAPCert = strings.Repeat("0", 64)
	a := addAccount(t, dir, keyPath, alice, server, srv.imap.Pass)

	r := runCLI(t, &fakeSystem{terminal: true}, "y\nyes\n", "account", "test", string(a.ID), "--data-dir", dir)

	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, connect.TextCertPinChanged)
	require.Contains(t, r.stderr, srv.imap.CertSHA256, "the fingerprint that the server shows now")
	require.NotContains(t, r.stdout, "Trust this certificate?")
}

// account remove warns that the queued messages of the account go with it. It
// asks on a terminal, and needs --yes without one.
func TestAccountRemoveNeedsConfirmation(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	a := addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)
	remaining := func() int {
		accounts, _ := storedAccounts(t, dir, keyPath)
		return len(accounts)
	}

	r := runCLI(t, nil, "y\n", "account", "remove", string(a.ID), "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stdout, "queued")
	require.Contains(t, r.stderr, "--yes")
	require.Equal(t, 1, remaining())

	r = runCLI(t, &fakeSystem{terminal: true}, "n\n", "account", "remove", string(a.ID), "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stdout, "Remove this account? [y/N]")
	require.Equal(t, 1, remaining())

	r = runCLI(t, &fakeSystem{terminal: true}, "y\n", "account", "remove", string(a.ID), "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	require.Equal(t, 0, remaining())

	b := addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)
	r = runCLI(t, nil, "", "account", "remove", "--yes", string(b.ID), "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "queued")
	require.Equal(t, 0, remaining())

	r = runCLI(t, nil, "", "account", "remove", "--yes", string(b.ID), "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, "No account")
}

// ---------------------------------------------------------------- account reconnect

// oldPin is a pin that no test server shows: the certificate that the user
// trusted before the server changed it.
var oldPin = strings.Repeat("0a", 32)

// reconnectArgs are the arguments of "account reconnect" for the account.
func reconnectArgs(dir string, a *account.Account, more ...string) []string {
	return append([]string{"account", "reconnect", string(a.ID), "--data-dir", dir}, more...)
}

// A new password from standard input is tested and saved. The account keeps
// its id and is connected again. The password is never printed.
func TestAccountReconnectSavesANewPassword(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	a := addAccount(t, dir, keyPath, alice, srv.server(), "pw-old-revoked-4b")
	setStatus(t, dir, a, account.StatusNeedsReauth)

	r := runCLI(t, nil, srv.imap.Pass+"\n", reconnectArgs(dir, a)...)

	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "Reconnected")
	require.Contains(t, r.stdout, string(a.ID))
	requireNoLeak(t, r, srv.imap.Pass, "pw-old-revoked-4b")
	accounts, passwords := storedAccounts(t, dir, keyPath)
	require.Len(t, accounts, 1)
	require.Equal(t, a.ID, accounts[0].ID)
	require.Equal(t, account.StatusOK, accounts[0].Status)
	require.Equal(t, []string{srv.imap.Pass}, passwords)
}

// An empty password keeps the stored one: the reconnect only tests the
// servers and marks the account as connected again.
func TestAccountReconnectKeepsTheStoredPassword(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	a := addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)
	setStatus(t, dir, a, account.StatusNeedsReauth)

	r := runCLI(t, nil, "", reconnectArgs(dir, a)...)

	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "stored password")
	accounts, passwords := storedAccounts(t, dir, keyPath)
	require.Equal(t, account.StatusOK, accounts[0].Status)
	require.Equal(t, []string{srv.imap.Pass}, passwords)
}

// A password that the server refuses changes nothing. The text is the fixed
// text of a refused login, never the text of the server, and the password is
// not printed.
func TestAccountReconnectWithAWrongPassword(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	a := addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)
	setStatus(t, dir, a, account.StatusNeedsReauth)

	r := runCLI(t, nil, "pw-wrong-77aa\n", reconnectArgs(dir, a)...)

	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, connect.TextAuthFailed)
	require.Contains(t, r.stderr, "Nothing was saved.")
	require.NotContains(t, strings.ToUpper(r.stdout+r.stderr), "AUTHENTICATIONFAILED")
	requireNoLeak(t, r, "pw-wrong-77aa", srv.imap.Pass)
	accounts, passwords := storedAccounts(t, dir, keyPath)
	require.Equal(t, account.StatusNeedsReauth, accounts[0].Status)
	require.Equal(t, []string{srv.imap.Pass}, passwords)
}

// Without a terminal, a changed pinned certificate is replaced only with
// --trust-fingerprint of the fingerprint that the server shows in this run AND
// --replace-pinned-certificate. The old and the new fingerprint and the
// warning are shown. Only the server that showed it gets the new pin.
func TestAccountReconnectReplacesAPinWithoutATerminal(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	server := srv.server()
	server.PinnedIMAPCert = oldPin
	a := addAccount(t, dir, keyPath, alice, server, srv.imap.Pass)
	newPin := srv.imap.CertSHA256

	for name, flags := range map[string][]string{
		"no flag":                     nil,
		"only the fingerprint":        {"--trust-fingerprint", newPin},
		"only the replace flag":       {"--replace-pinned-certificate"},
		"the wrong fingerprint":       {"--replace-pinned-certificate", "--trust-fingerprint", strings.Repeat("ef", 32)},
		"the fingerprint of the SMTP": {"--replace-pinned-certificate", "--trust-fingerprint", srv.smtp.CertSHA256},
	} {
		r := runCLI(t, nil, srv.imap.Pass+"\n", reconnectArgs(dir, a, flags...)...)
		require.Equal(t, exitError, r.code, name)
		require.Contains(t, r.stdout, connect.GroupFingerprint(oldPin), name)
		require.Contains(t, r.stdout, connect.GroupFingerprint(newPin), name)
		require.Contains(t, r.stdout, connect.TextCertChangedWarning, name)
		require.Contains(t, r.stdout, fmt.Sprintf("127.0.0.1:%d", srv.imap.Port), name)
		require.Contains(t, r.stderr, "Nothing was saved.", name)
		requireNoLeak(t, r, srv.imap.Pass)
		accounts, _ := storedAccounts(t, dir, keyPath)
		require.Equal(t, oldPin, accounts[0].Mail.PinnedIMAPCert, name)
	}
	r := runCLI(t, nil, "", reconnectArgs(dir, a)...)
	require.Contains(t, r.stderr, "--trust-fingerprint "+newPin+" --replace-pinned-certificate", "the hint names both flags")

	r = runCLI(t, nil, srv.imap.Pass+"\n", reconnectArgs(dir, a,
		"--trust-fingerprint", connect.GroupFingerprint(newPin), "--replace-pinned-certificate")...)

	require.Equal(t, exitOK, r.code, r.stderr)
	requireNoLeak(t, r, srv.imap.Pass)
	accounts, passwords := storedAccounts(t, dir, keyPath)
	require.Equal(t, a.ID, accounts[0].ID)
	require.Equal(t, newPin, accounts[0].Mail.PinnedIMAPCert)
	require.Equal(t, srv.smtp.CertSHA256, accounts[0].Mail.PinnedSMTPCert, "the SMTP pin did not change")
	require.Equal(t, []string{srv.imap.Pass}, passwords)
}

// On a terminal, replacing a pin needs the word "yes" typed, also for a server
// on this computer: "y" is not enough.
func TestAccountReconnectOnATerminalNeedsATypedYes(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	server := srv.server()
	server.PinnedIMAPCert = oldPin
	a := addAccount(t, dir, keyPath, alice, server, srv.imap.Pass)

	r := runCLI(t, &fakeSystem{terminal: true, passwords: []string{""}}, "y\n", reconnectArgs(dir, a)...)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stdout, connect.TextCertChangedWarning)
	require.Contains(t, r.stdout, "Type yes to continue")
	accounts, _ := storedAccounts(t, dir, keyPath)
	require.Equal(t, oldPin, accounts[0].Mail.PinnedIMAPCert)

	// A flag does not answer for the user on a terminal.
	r = runCLI(t, &fakeSystem{terminal: true, passwords: []string{""}}, "\n",
		reconnectArgs(dir, a, "--trust-fingerprint", srv.imap.CertSHA256, "--replace-pinned-certificate")...)
	require.Equal(t, exitError, r.code)

	r = runCLI(t, &fakeSystem{terminal: true, passwords: []string{""}}, "yes\n", reconnectArgs(dir, a)...)
	require.Equal(t, exitOK, r.code, r.stderr)
	accounts, _ = storedAccounts(t, dir, keyPath)
	require.Equal(t, srv.imap.CertSHA256, accounts[0].Mail.PinnedIMAPCert)
}

// A certificate for another host name is never accepted, also with both flags.
func TestAccountReconnectRefusesACertificateForAnotherHost(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	server := srv.server()
	server.IMAPHost = "localhost" // the certificate is for 127.0.0.1 only
	server.PinnedIMAPCert = oldPin
	a := addAccount(t, dir, keyPath, alice, server, srv.imap.Pass)

	r := runCLI(t, nil, "", reconnectArgs(dir, a, "--trust-fingerprint", srv.imap.CertSHA256, "--replace-pinned-certificate")...)

	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stdout, connect.TextCertChangedWrongHost)
	require.Contains(t, r.stderr, "cannot be trusted")
	accounts, _ := storedAccounts(t, dir, keyPath)
	require.Equal(t, oldPin, accounts[0].Mail.PinnedIMAPCert)
}

// An id that does not exist asks for no password.
func TestAccountReconnectUnknownID(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)
	f := &fakeSystem{terminal: true}

	r := runCLI(t, f, "", "account", "reconnect", "missing", "--data-dir", dir)

	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, "No account has this id")
	require.NotContains(t, r.stdout, "password")
	r = runCLI(t, nil, "", "account", "reconnect", "--data-dir", dir)
	require.Equal(t, exitUsage, r.code)
}

// The question to remove a pin says what the removal does: from then on, any
// certificate that an authority signs for the host is accepted. It differs
// from the question to replace a pin, and the decision says which one the user
// agreed to.
func TestAccountReconnectAsksAboutRemovingAPin(t *testing.T) {
	fingerprint := strings.Repeat("cd", 32)
	certErr := &service.ReconnectCertificateError{IMAP: &service.ReconnectCertificate{
		Host: "imap.example.org", Port: 993, OldPin: oldPin, Change: service.PinChangeRemove,
		Cert: &imapsmtp.CertificateUnknownError{Protocol: "imap", Host: "imap.example.org", FingerprintSHA256: fingerprint, Pinned: true},
	}}
	f := &fakeSystem{terminal: true}
	var out, errOut syncBuffer
	c := &cli{stdin: strings.NewReader("yes\n"), stdout: &out, stderr: &errOut, sys: f.system()}
	var r service.Reconnect

	require.True(t, c.decideReconnect("account reconnect", &r, certErr, reconnectOptions{}))

	require.Contains(t, out.String(), connect.TextRemovePin)
	require.Contains(t, out.String(), connect.TextCertChangedWarning)
	require.Contains(t, out.String(), "Stop pinning")
	require.NotContains(t, out.String(), "instead of the old one")
	require.Equal(t, service.Reconnect{PinnedIMAPCert: fingerprint, RemoveIMAPPin: true}, r)

	// A replace asks the other question, and the decision is not a removal.
	certErr.IMAP.Change = service.PinChangeReplace
	certErr.IMAP.Cert.Reason = x509.UnknownAuthorityError{}
	out = syncBuffer{}
	c = &cli{stdin: strings.NewReader("yes\n"), stdout: &out, stderr: &errOut, sys: f.system()}
	r = service.Reconnect{}
	require.True(t, c.decideReconnect("account reconnect", &r, certErr, reconnectOptions{}))
	require.Contains(t, out.String(), "instead of the old one")
	require.NotContains(t, out.String(), connect.TextRemovePin)
	require.Equal(t, service.Reconnect{PinnedIMAPCert: fingerprint}, r)
}

// setStatus sets the status of a stored account, as a refused login does.
func setStatus(t *testing.T, dir string, a *account.Account, status account.Status) {
	t.Helper()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	require.NoError(t, st.SetAccountStatus(context.Background(), a.TenantID, a.ID, status))
}

// ---------------------------------------------------------------- token

// The token is printed once, on standard output, and nowhere else: not on
// standard error, not in the log, not in the list.
func TestTokenCreatePrintsOnce(t *testing.T) {
	dir, _ := newCLIEnv(t)

	r := runCLI(t, nil, "", "token", "create", "--name", "laptop", "--scopes", "email:send, email:read", "--data-dir", dir)

	require.Equal(t, exitOK, r.code, r.stderr)
	token := strings.TrimSpace(r.stdout)
	require.Regexp(t, regexp.MustCompile(`^emcp_[a-z2-7]{52}$`), token, "standard output holds the token only")
	require.NotContains(t, r.stderr, token)
	require.Contains(t, r.stderr, "only once")

	a, err := app.Open(context.Background(), app.Config{DataDir: dir, Local: true, LogOutput: io.Discard})
	require.NoError(t, err)
	p, err := a.Tokens.Verify(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, policy.Scopes{"email:read", "email:send"}, p.Scopes)
	list, err := a.Tokens.List(context.Background(), store.LocalTenantID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	id := list[0].ID
	require.NoError(t, a.CloseNow(context.Background()))

	r = runCLI(t, nil, "", "token", "list", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	for _, text := range []string{id, "laptop", "email:read", "email:send"} {
		require.Contains(t, r.stdout, text)
	}
	requireNoLeak(t, r, token)

	r = runCLI(t, nil, "", "token", "revoke", id, "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	r = runCLI(t, nil, "", "token", "revoke", "no-such-token", "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, "No token")

	a, err = app.Open(context.Background(), app.Config{DataDir: dir, Local: true, LogOutput: io.Discard})
	require.NoError(t, err)
	defer func() { require.NoError(t, a.CloseNow(context.Background())) }()
	_, err = a.Tokens.Verify(context.Background(), token)
	require.Error(t, err, "a revoked token no longer works")

	r = runCLI(t, nil, "", "token", "list", "--data-dir", dir)
	require.Contains(t, r.stdout, "revoked")
}

// A token always has an explicit list of known scopes. No scope is never the
// default, and "all" stands for every known scope.
func TestTokenCreateNeedsExplicitScopes(t *testing.T) {
	dir, _ := newCLIEnv(t)
	for name, args := range map[string][]string{
		"no scopes":       {"--name", "laptop"},
		"an empty list":   {"--name", "laptop", "--scopes", " , "},
		"an unknown one":  {"--name", "laptop", "--scopes", "email:read,email:everything"},
		"no name":         {"--scopes", "all"},
		"an extra word":   {"--name", "laptop", "--scopes", "all", "extra"},
		"an empty name":   {"--name", "  ", "--scopes", "all"},
		"a token as flag": {"--name", "laptop", "--scopes", "all", "--token", "emcp_x"},
	} {
		r := runCLI(t, nil, "", append([]string{"token", "create", "--data-dir", dir}, args...)...)
		require.Equal(t, exitUsage, r.code, name)
		require.Empty(t, r.stdout, name)
		requireNoData(t, dir)
	}
	r := runCLI(t, nil, "", "token", "create", "--name", "laptop", "--data-dir", dir)
	require.Contains(t, r.stderr, "--scopes")

	r = runCLI(t, nil, "", "token", "create", "--name", "all of it", "--scopes", "all", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	a, err := app.Open(context.Background(), app.Config{DataDir: dir, Local: true, LogOutput: io.Discard})
	require.NoError(t, err)
	defer func() { require.NoError(t, a.CloseNow(context.Background())) }()
	p, err := a.Tokens.Verify(context.Background(), strings.TrimSpace(r.stdout))
	require.NoError(t, err)
	want := policy.KnownScopes()
	slices.Sort(want)
	require.Equal(t, policy.Scopes(want), p.Scopes)
}

// ---------------------------------------------------------------- import-env

// writeEnv writes a .env file and returns its path.
func writeEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// The prototype's .env becomes one account. IMAP_TLS=true on another port than
// 143 is TLS, and SMTP_TLS=true on another port than 465 is STARTTLS. Values may
// be quoted. Without a terminal, the self-signed certificates need
// --trust-fingerprint.
func TestImportEnvCreatesAccount(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeSTARTTLS)
	path := writeEnv(t, fmt.Sprintf(`# The settings of the old email-mcp.
IMAP_HOST=127.0.0.1
IMAP_PORT=%d
IMAP_USERNAME=%s
IMAP_PASSWORD="%s"
IMAP_TLS=true

SMTP_HOST = 127.0.0.1
SMTP_PORT=%d
SMTP_USERNAME='%s'
SMTP_PASSWORD=%s
SMTP_TLS=true
USE_OAUTH=false
`, srv.imap.Port, srv.imap.User, srv.imap.Pass, srv.smtp.Port, srv.imap.User, srv.imap.Pass))

	r := runCLI(t, nil, "", "import-env", path, "--email", alice, "--data-dir", dir,
		"--trust-fingerprint", srv.imap.CertSHA256, "--trust-fingerprint", srv.smtp.CertSHA256)

	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, `Imported `+alice+`. Delete `+path+` and remove the old "env" block from your harness config.`+"\n")
	requireNoLeak(t, r, srv.imap.Pass, srv.imap.User)
	require.NotContains(t, r.stderr, alice)
	accounts, passwords := storedAccounts(t, dir, keyPath)
	require.Len(t, accounts, 1)
	want := srv.server()
	want.SMTPSecurity = account.SecuritySTARTTLS
	require.Equal(t, want, *accounts[0].Mail)
	require.Equal(t, alice, accounts[0].Address)
	require.Equal(t, []string{srv.imap.Pass}, passwords)
}

// A .env file that uses OAuth is refused with the fixed message, before any
// other variable is read and before anything is opened.
func TestImportEnvRefusesOAuth(t *testing.T) {
	const msg = "This .env uses OAuth (USE_OAUTH=true). OAuth accounts are not supported until Google (M1) and Microsoft (M2) support ships. Connect this account with an app password instead."
	for name, content := range map[string]string{
		"only the switch": "USE_OAUTH=true\n",
		"with bad values": "IMAP_PORT=not-a-number\nIMAP_TLS=maybe\nUSE_OAUTH=true\nOAUTH_CLIENT_SECRET=client-secret-5b1\n",
		"quoted":          `USE_OAUTH="TRUE"` + "\nIMAP_PASSWORD=pw-3f1a\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir, _ := newCLIEnv(t)
			path := writeEnv(t, content)

			r := runCLI(t, nil, "", "import-env", path, "--data-dir", dir)

			require.Equal(t, exitError, r.code)
			require.Equal(t, msg+"\n", r.stderr)
			require.Empty(t, r.stdout)
			requireNoData(t, dir)
		})
	}
}

// A missing or bad variable is named, and its value is never printed.
func TestImportEnvNamesTheProblemVariable(t *testing.T) {
	complete := map[string]string{ //nolint:gosec // G101: test values, used to check that they are never shown
		"IMAP_HOST": "imap.example.org", "IMAP_USERNAME": alice, "IMAP_PASSWORD": "pw-value-7c2e", "SMTP_HOST": "smtp.example.org",
	}
	envOf := func(change map[string]string) string {
		vars := map[string]string{}
		for k, v := range complete {
			vars[k] = v
		}
		for k, v := range change {
			vars[k] = v
		}
		var b strings.Builder
		for k, v := range vars {
			if v != "<unset>" {
				fmt.Fprintf(&b, "%s=%s\n", k, v)
			}
		}
		return b.String()
	}
	for name, tc := range map[string]struct {
		change map[string]string
		names  []string
	}{
		"missing hosts":        {change: map[string]string{"IMAP_HOST": "<unset>", "SMTP_HOST": ""}, names: []string{"IMAP_HOST", "SMTP_HOST"}},
		"missing password":     {change: map[string]string{"IMAP_PASSWORD": "<unset>"}, names: []string{"IMAP_PASSWORD"}},
		"missing user name":    {change: map[string]string{"IMAP_USERNAME": "<unset>"}, names: []string{"IMAP_USERNAME"}},
		"a bad port":           {change: map[string]string{"IMAP_PORT": "99999"}, names: []string{"IMAP_PORT"}},
		"a port that is text":  {change: map[string]string{"SMTP_PORT": "pw-port-1d"}, names: []string{"SMTP_PORT"}},
		"a bad TLS switch":     {change: map[string]string{"SMTP_TLS": "yes-please"}, names: []string{"SMTP_TLS"}},
		"another SMTP login":   {change: map[string]string{"SMTP_USERNAME": "other-login-9e"}, names: []string{"SMTP_USERNAME"}},
		"another SMTP secret":  {change: map[string]string{"SMTP_PASSWORD": "pw-other-4b8a"}, names: []string{"SMTP_PASSWORD"}}, //nolint:gosec // G101: a test value
		"a user name, no mail": {change: map[string]string{"IMAP_USERNAME": "login-name-6f"}, names: []string{"IMAP_USERNAME", "--email"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir, _ := newCLIEnv(t)
			path := writeEnv(t, envOf(tc.change))

			r := runCLI(t, nil, "", "import-env", path, "--data-dir", dir)

			require.Equal(t, exitError, r.code)
			for _, n := range tc.names {
				require.Contains(t, r.stderr, n)
			}
			secretValues := []string{"pw-value-7c2e", "99999", "pw-port-1d", "yes-please", "other-login-9e", "pw-other-4b8a", "login-name-6f"}
			requireNoLeak(t, r, secretValues...)
			requireNoData(t, dir)
		})
	}

	t.Run("a missing file", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		r := runCLI(t, nil, "", "import-env", filepath.Join(t.TempDir(), "nope.env"), "--data-dir", dir)
		require.Equal(t, exitError, r.code)
		requireNoData(t, dir)
	})

	t.Run("two paths", func(t *testing.T) {
		r := runCLI(t, nil, "", "import-env", "a.env", "b.env")
		require.Equal(t, exitUsage, r.code)
	})
}

// IMAP_TLS=false and SMTP_TLS=false follow the rules of plain connections: only
// to a server on this computer, and only with --plaintext-i-understand.
func TestImportEnvPlainRules(t *testing.T) {
	srv := startLocal(t, imaptest.ModePlain, smtptest.ModePlain)
	content := func(host string) string {
		return fmt.Sprintf("IMAP_HOST=%s\nIMAP_PORT=%d\nIMAP_USERNAME=%s\nIMAP_PASSWORD=%s\nIMAP_TLS=false\n"+
			"SMTP_HOST=%s\nSMTP_PORT=%d\nSMTP_TLS=false\n", host, srv.imap.Port, srv.imap.User, srv.imap.Pass, host, srv.smtp.Port)
	}

	t.Run("a remote host, even with the flag", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		r := runCLI(t, nil, "", "import-env", writeEnv(t, content("mail.example.org")), "--email", alice,
			"--plaintext-i-understand", "--data-dir", dir)
		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stderr, "only to a server on this computer")
		requireNoData(t, dir)
	})

	t.Run("this computer without the flag", func(t *testing.T) {
		dir, _ := newCLIEnv(t)
		r := runCLI(t, nil, "", "import-env", writeEnv(t, content("127.0.0.1")), "--email", alice, "--data-dir", dir)
		require.Equal(t, exitError, r.code)
		require.Contains(t, r.stderr, "--plaintext-i-understand")
		requireNoData(t, dir)
	})

	t.Run("this computer with the flag", func(t *testing.T) {
		dir, keyPath := newCLIEnv(t)
		r := runCLI(t, nil, "", "import-env", writeEnv(t, content("127.0.0.1")), "--email", alice,
			"--plaintext-i-understand", "--data-dir", dir)
		require.Equal(t, exitOK, r.code, r.stderr)
		accounts, _ := storedAccounts(t, dir, keyPath)
		require.Len(t, accounts, 1)
		require.Equal(t, account.SecurityPlain, accounts[0].Mail.IMAPSecurity)
		requireNoLeak(t, r, srv.imap.Pass)
	})
}

func TestParseEnvFile(t *testing.T) {
	vars, err := parseEnvFile([]byte("# comment\r\n\n  # indented comment\nA=1\nB = two words \nC=\"quoted # not a comment\"\nD='single'\nE=\nF=a=b\nG=\"\"\nH=ends with quote\"\n"))
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"A": "1", "B": "two words", "C": "quoted # not a comment", "D": "single", "E": "", "F": "a=b", "G": "", "H": `ends with quote"`,
	}, vars)

	for name, content := range map[string]string{
		"no equals sign":   "IMAP_PASSWORD pw-secret-1\n",
		"a bad name":       "IMAP PASSWORD=pw-secret-1\n",
		"a quote not shut": "IMAP_PASSWORD=\"pw-secret-1\n",
		"quotes not equal": "IMAP_PASSWORD=\"pw-secret-1'\n",
		"a name twice":     "IMAP_PASSWORD=pw-secret-1\nIMAP_PASSWORD=pw-secret-1\n",
		"export":           "export IMAP_PASSWORD=pw-secret-1\n",
	} {
		_, err := parseEnvFile([]byte(content))
		require.Error(t, err, name)
		require.NotContains(t, err.Error(), "pw-secret-1", name)
		require.Contains(t, err.Error(), "line", name)
	}

	_, err = parseEnvFile([]byte(strings.Repeat("A", maxEnvFileBytes+1)))
	require.Error(t, err)
}

// ---------------------------------------------------------------- doctor

// doctorLine returns the line of the check whose name starts with prefix.
func doctorLine(t *testing.T, stdout, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("no line starts with %q in:\n%s", prefix, stdout)
	return ""
}

func TestDoctorFlagsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix file modes")
	}
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	a := addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)

	r := runCLI(t, nil, "", "doctor", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stdout+r.stderr)
	require.True(t, strings.HasSuffix(doctorLine(t, r.stdout, "data directory"), ": ok"))
	require.True(t, strings.HasSuffix(doctorLine(t, r.stdout, "database file"), ": ok"))
	require.True(t, strings.HasSuffix(doctorLine(t, r.stdout, "master key"), ": ok"))
	require.Equal(t, "account "+string(a.ID)+" ("+alice+"): ok", doctorLine(t, r.stdout, "account "))

	require.NoError(t, os.Chmod(dir, 0o755))                                //nolint:gosec // G302: the test makes the mode too open on purpose
	require.NoError(t, os.Chmod(filepath.Join(dir, "email-mcp.db"), 0o644)) //nolint:gosec // G302: the test makes the mode too open on purpose
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })                          //nolint:gosec // G302: a directory needs the x bit; 0700 is the mode the program wants

	r = runCLI(t, nil, "", "doctor", "--data-dir", dir)

	require.NotEqual(t, exitOK, r.code)
	line := doctorLine(t, r.stdout, "data directory")
	require.Contains(t, line, ": FAIL")
	require.Contains(t, line, "0755")
	require.Contains(t, line, "0700")
	line = doctorLine(t, r.stdout, "database file")
	require.Contains(t, line, ": FAIL")
	require.Contains(t, line, "0644")
	require.Contains(t, line, "0600")
	require.True(t, strings.HasSuffix(doctorLine(t, r.stdout, "master key"), ": ok"), "the other checks still run")
	require.True(t, strings.HasSuffix(doctorLine(t, r.stdout, "account "), ": ok"))
	requireNoLeak(t, r, srv.imap.Pass)
}

// doctor probes each account with its stored settings and prints a fixed text
// for a failure, never the text of the server or the password.
func TestDoctorReportsAFailedAccount(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	addAccount(t, dir, keyPath, alice, srv.server(), "pw-wrong-0d3e")

	r := runCLI(t, nil, "", "doctor", "--data-dir", dir)

	require.Equal(t, exitError, r.code)
	line := doctorLine(t, r.stdout, "account ")
	require.Contains(t, line, "FAIL "+connect.TextAuthFailed)
	require.NotContains(t, strings.ToUpper(r.stdout+r.stderr), "AUTHENTICATIONFAILED")
	requireNoLeak(t, r, "pw-wrong-0d3e")
}

// doctor makes nothing: a data directory that does not exist is a failure, and
// it stays missing.
func TestDoctorWithoutData(t *testing.T) {
	dir, keyPath := newCLIEnv(t)

	r := runCLI(t, nil, "", "doctor", "--data-dir", dir)

	require.Equal(t, exitError, r.code)
	require.Contains(t, doctorLine(t, r.stdout, "data directory"), "FAIL")
	requireNoData(t, dir)
	_, err := os.Stat(keyPath)
	require.ErrorIs(t, err, os.ErrNotExist, "no master key was made")
}

// doctor never makes a master key: with the keychain and no entry in it, or
// with a key file that is missing, the master key check fails with a fixed
// text, and no key appears.
func TestDoctorNeverMakesAKey(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	setUserDirs(t)
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)

	t.Setenv("EMAIL_MCP_KEK", "keychain")
	r := runCLI(t, nil, "", "doctor", "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Equal(t, "master key (keychain): FAIL "+msgNoMasterKey, doctorLine(t, r.stdout, "master key"))
	_, err := keyring.Get("email-mcp", "master-key")
	require.ErrorIs(t, err, keyring.ErrNotFound, "no keychain entry was made")

	// A database without keys, and a key file that does not exist: Open would
	// make one on a first start. doctor does not.
	empty := filepath.Join(t.TempDir(), "data")
	st, err := store.Open(context.Background(), empty)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	missing := filepath.Join(t.TempDir(), "missing.key")
	t.Setenv("EMAIL_MCP_KEK", "file:"+missing)
	r = runCLI(t, nil, "", "doctor", "--data-dir", empty)
	require.Equal(t, exitError, r.code)
	require.Contains(t, doctorLine(t, r.stdout, "master key"), "FAIL "+msgNoMasterKey)
	_, err = os.Stat(missing)
	require.ErrorIs(t, err, os.ErrNotExist, "no key file was made")
}

// A database that a newer email-mcp wrote has a migration version that this
// binary does not know. doctor reports it under "database schema" with the
// fixed text of store.ErrNewerSchema, not under "master key": the key is not
// the problem (follow-up N-2).
func TestDoctorReportsANewerSchema(t *testing.T) {
	dir, keyPath := newCLIEnv(t)
	srv := startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "email-mcp.db"))+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), "INSERT INTO schema_migrations (version) VALUES (9999)")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	r := runCLI(t, nil, "", "doctor", "--data-dir", dir)

	require.Equal(t, exitError, r.code)
	require.Equal(t, "database schema: FAIL "+store.ErrNewerSchema.Error(), doctorLine(t, r.stdout, "database schema"))
	require.NotContains(t, r.stdout, "master key")
}

// ---------------------------------------------------------------- key rotate

// rotateFixture is a data directory with an account on local servers, opened
// once so the server key exists.
func rotateFixture(t *testing.T) (dir, keyPath string, srv localServers) {
	t.Helper()
	dir, keyPath = newCLIEnv(t)
	srv = startLocal(t, imaptest.ModeTLS, smtptest.ModeTLS)
	addAccount(t, dir, keyPath, alice, srv.server(), srv.imap.Pass)
	r := runCLI(t, nil, "", "account", "list", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	return dir, keyPath, srv
}

// kekIDs returns the master key ids of the stored keys.
func kekIDs(t *testing.T, dir string) []string {
	t.Helper()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	rows, err := st.ListTenantKeys(context.Background())
	require.NoError(t, err)
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.KEKID)
	}
	_, id, err := st.ServerKey(context.Background())
	require.NoError(t, err)
	return append(ids, id)
}

// requireOpens checks that the key file opens every stored key: the server key
// and the password of the account.
func requireOpens(t *testing.T, dir, keyPath, password string) {
	t.Helper()
	st, vault := openData(t, dir, keyPath)
	_, err := vault.ServerKey(context.Background())
	require.NoError(t, st.Close())
	require.NoError(t, err)
	_, passwords := storedAccounts(t, dir, keyPath)
	require.Equal(t, []string{password}, passwords)
}

// key rotate wraps every stored key with the new key, keeps the old key file,
// and tells the user the exact setting that the next start needs.
func TestKeyRotateToAFile(t *testing.T) {
	dir, oldKey, srv := rotateFixture(t)
	newKey := filepath.Join(t.TempDir(), "new.key")

	r := runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "file:"+newKey, "--data-dir", dir)

	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "  EMAIL_MCP_KEK='file:"+newKey+"'\n")
	require.Contains(t, r.stdout, `  "EMAIL_MCP_KEK": "file:`+newKey+`"`+"\n")
	require.Contains(t, r.stdout, "The old key file "+oldKey+" was not deleted")
	require.Contains(t, r.stdout, "doctor")
	_, err := os.Stat(oldKey)
	require.NoError(t, err, "the old key file is never deleted")
	requireOpens(t, dir, newKey, srv.imap.Pass)
	newKEK, err := secrets.NewFileKEK(newKey)
	require.NoError(t, err)
	for _, id := range kekIDs(t, dir) {
		require.Equal(t, newKEK.ID(), id)
	}

	// With the old setting, the next start finds the old key, which opens
	// nothing now: doctor says so.
	r = runCLI(t, nil, "", "doctor", "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, doctorLine(t, r.stdout, "master key"), "FAIL")

	// With the printed setting, everything works.
	t.Setenv("EMAIL_MCP_KEK", "file:"+newKey)
	r = runCLI(t, nil, "", "doctor", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stdout+r.stderr)
}

// A rotation that fails changes nothing: every stored key is still wrapped by
// the old key, and the old setting still works. Here the database refuses the
// change of the server key, after the tenant keys were changed in the same
// transaction.
func TestKeyRotateFailureLeavesEveryKeyReadableWithTheOldKey(t *testing.T) {
	dir, oldKey, srv := rotateFixture(t)
	oldIDs := kekIDs(t, dir)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "email-mcp.db"))+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(),
		`CREATE TRIGGER refuse_rewrap BEFORE UPDATE ON server_keys BEGIN SELECT RAISE(ABORT, 'refused by the test'); END`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	newKey := filepath.Join(t.TempDir(), "new.key")

	r := runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "file:"+newKey, "--data-dir", dir)

	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, "nothing was changed")
	require.NotContains(t, r.stdout, "EMAIL_MCP_KEK=")
	require.Equal(t, oldIDs, kekIDs(t, dir), "no key is wrapped by the new key")
	requireOpens(t, dir, oldKey, srv.imap.Pass)
	r = runCLI(t, nil, "", "account", "test", "--data-dir", dir, string(mustFirstAccount(t, dir, oldKey).ID))
	require.Equal(t, exitOK, r.code, r.stderr)
}

func mustFirstAccount(t *testing.T, dir, keyPath string) *account.Account {
	t.Helper()
	accounts, _ := storedAccounts(t, dir, keyPath)
	require.NotEmpty(t, accounts)
	return accounts[0]
}

func TestKeyRotateRefusesBadTargets(t *testing.T) {
	dir, oldKey, _ := rotateFixture(t)
	for name, args := range map[string][]string{
		"no target":      {},
		"a relative key": {"--to", "file:new.key"},
		"another kind":   {"--to", "vault:transit"},
		"an extra word":  {"--to", "keychain", "extra"},
	} {
		r := runCLI(t, nil, "", append([]string{"key", "rotate", "--yes", "--data-dir", dir}, args...)...)
		require.Equal(t, exitUsage, r.code, name)
	}

	r := runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "file:"+oldKey, "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, "already uses")
	require.Contains(t, r.stderr, "nothing was changed")

	other := filepath.Join(t.TempDir(), "other.key")
	require.NoError(t, secrets.GenerateKeyFile(other))
	r = runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "file:"+other, "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stderr, "never overwritten")
	require.Contains(t, r.stderr, "nothing was changed")
}

// The key in use is refused under another spelling of its path, with exit 1,
// and nothing changes. No line tells the user to delete a key file.
func TestKeyRotateRefusesTheKeyInUse(t *testing.T) {
	dir, oldKey, srv := rotateFixture(t)
	ids := kekIDs(t, dir)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(filepath.Dir(oldKey), link))

	for _, spelling := range []string{
		filepath.Dir(oldKey) + "/./" + filepath.Base(oldKey),
		filepath.Dir(oldKey) + "//" + filepath.Base(oldKey),
		filepath.Join(link, filepath.Base(oldKey)),
	} {
		r := runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "file:"+spelling, "--data-dir", dir)
		require.Equal(t, exitError, r.code, spelling)
		require.Contains(t, r.stderr, "already uses", spelling)
		require.NotContains(t, r.stdout+r.stderr, "delete", spelling)
		require.NotContains(t, r.stdout, "EMAIL_MCP_KEK=", spelling)
	}
	require.Equal(t, ids, kekIDs(t, dir))
	requireOpens(t, dir, oldKey, srv.imap.Pass)
}

// Every other email-mcp program must be stopped first. The command says so
// before it opens anything, asks on a terminal, and needs --yes without one.
func TestKeyRotateAsksFirst(t *testing.T) {
	dir, _, _ := rotateFixture(t)
	ids := kekIDs(t, dir)
	newKey := filepath.Join(t.TempDir(), "new.key")

	r := runCLI(t, nil, "", "key", "rotate", "--to", "file:"+newKey, "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stdout, "Stop every other email-mcp program")
	require.Contains(t, r.stderr, "--yes")

	r = runCLI(t, &fakeSystem{terminal: true}, "n\n", "key", "rotate", "--to", "file:"+newKey, "--data-dir", dir)
	require.Equal(t, exitError, r.code)
	require.Contains(t, r.stdout, "[y/N]")
	require.Equal(t, ids, kekIDs(t, dir), "nothing changed")
	_, err := os.Stat(newKey)
	require.ErrorIs(t, err, os.ErrNotExist, "no key was made")

	r = runCLI(t, &fakeSystem{terminal: true}, "y\n", "key", "rotate", "--to", "file:"+newKey, "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	require.NotEqual(t, ids, kekIDs(t, dir))
}

// The printed setting is quoted for a POSIX shell, so a path with a space can
// be pasted as it is.
func TestKeyRotateQuotesTheSetting(t *testing.T) {
	dir, _, srv := rotateFixture(t)
	keys := filepath.Join(t.TempDir(), "My Keys")
	require.NoError(t, os.Mkdir(keys, 0o700))
	newKey := filepath.Join(keys, "new key.key")

	r := runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "file:"+newKey, "--data-dir", dir)

	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "  EMAIL_MCP_KEK='file:"+newKey+"'\n")
	requireOpens(t, dir, newKey, srv.imap.Pass)
}

func TestShellQuote(t *testing.T) {
	require.Equal(t, `'file:/a b/k.key'`, shellQuote("file:/a b/k.key"))
	require.Equal(t, `'file:/it'\''s/k.key'`, shellQuote("file:/it's/k.key"))
	require.Equal(t, `'keychain'`, shellQuote("keychain"))
}

// setUserDirs points the user directories of the platform (os.UserConfigDir)
// at a new folder, so the lock of the keychain is not in the real one.
func setUserDirs(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", home)
}

// A rotation to the keychain and back. The keychain is the in-memory mock of
// go-keyring, so the test never touches the real keychain. The old key of the
// keychain is never offered for deletion: other data directories may use it.
func TestKeyRotateToTheKeychainAndBack(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	setUserDirs(t)
	dir, oldKey, srv := rotateFixture(t)

	r := runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "keychain", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "  EMAIL_MCP_KEK='keychain'\n")
	require.Contains(t, r.stdout, "The old key file "+oldKey+" was not deleted")
	_, err := keyring.Get("email-mcp", "master-key")
	require.NoError(t, err)

	t.Setenv("EMAIL_MCP_KEK", "keychain")
	r = runCLI(t, nil, "", "doctor", "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stdout+r.stderr)

	back := filepath.Join(t.TempDir(), "back.key")
	r = runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "file:"+back, "--data-dir", dir)
	require.Equal(t, exitOK, r.code, r.stderr)
	require.Contains(t, r.stdout, "stays in the keychain")
	require.NotContains(t, r.stdout, "delete")
	requireOpens(t, dir, back, srv.imap.Pass)
}

// ---------------------------------------------------------------- setup

// setup prints the sign-in link and gives it to the browser as one argument. The
// link works, and it never reaches the log. The command serves until it is
// stopped. A browser that cannot be opened is only a note.
func TestSetupOpensTheBrowserAndNeverLogsTheLink(t *testing.T) {
	dir, _ := newCLIEnv(t)
	links := make(chan string, 1)
	pages := make(chan int, 1)
	f := &fakeSystem{open: func(url string) error {
		links <- url
		jar, err := cookiejar.New(nil)
		if err != nil {
			return err
		}
		client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
		resp, err := client.Get(url) //nolint:gosec // G107: the local link of the test
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		pages <- resp.StatusCode
		return errors.New("no browser here")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- runWith(ctx, f.system(), []string{"setup", "--data-dir", dir}, strings.NewReader(""), &out, &errOut)
	}()

	var link string
	select {
	case link = <-links:
	case <-time.After(30 * time.Second):
		t.Fatal("setup did not open the browser")
	}
	require.Regexp(t, regexp.MustCompile(`^http://127\.0\.0\.1:\d+/setup\?token=[A-Za-z0-9_-]+$`), link)
	require.Equal(t, http.StatusOK, <-pages, "the link signs the browser in")
	require.Eventually(t, func() bool { return strings.Contains(errOut.String(), "could not open") }, 10*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case code := <-done:
		require.Equal(t, exitOK, code, errOut.String())
	case <-time.After(30 * time.Second):
		t.Fatal("setup did not stop")
	}
	require.Equal(t, 1, strings.Count(out.String(), link), "standard output shows the link once")
	token := link[strings.Index(link, "token=")+len("token="):]
	require.NotContains(t, errOut.String(), token, "the log never has the link")
	require.NotContains(t, errOut.String(), "/setup?")
}

// Flags may come before and after the other arguments. After "--" every
// argument is an argument.
func TestParseFlagsTakesFlagsAnywhere(t *testing.T) {
	var out, errOut syncBuffer
	c := &cli{stdin: strings.NewReader(""), stdout: &out, stderr: &errOut, sys: (&fakeSystem{}).system()}
	fs := c.newFlags("t")
	yes := fs.Bool("yes", false, "remove without a question")
	rest, _, ok := c.parseFlags("t", fs, []string{"ID", "--yes"})
	require.True(t, ok)
	require.True(t, *yes)
	require.Equal(t, []string{"ID"}, rest)

	*yes = false
	rest, _, ok = c.parseFlags("t", fs, []string{"--", "--yes", "ID"})
	require.True(t, ok)
	require.False(t, *yes)
	require.Equal(t, []string{"--yes", "ID"}, rest)

	_, code, ok := c.parseFlags("t", fs, []string{"--nope"})
	require.False(t, ok)
	require.Equal(t, exitUsage, code)
	require.Contains(t, errOut.String(), "unknown flag")
	require.NotContains(t, errOut.String(), "nope")
	_, code, ok = c.parseFlags("t", fs, []string{"-h"})
	require.False(t, ok)
	require.Equal(t, exitOK, code)
	require.Contains(t, out.String(), "remove without a question")
}

// No argument and no flag value that the command does not expect is echoed:
// the user may have typed a password in the wrong place, and standard error is
// also the log.
func TestNoArgumentIsEchoed(t *testing.T) {
	dir, _ := newCLIEnv(t)
	const secret = "pw-typed-here-5e1c" //nolint:gosec // G101: a test value, used to check that it is never shown
	for name, args := range map[string][]string{
		"a top-level command":      {secret},
		"a subcommand":             {"account", secret},
		"an extra argument":        {"account", "add", "--email", alice, secret},
		"an unknown flag":          {"account", "add", "--" + secret},
		"an unknown flag value":    {"account", "add", "--" + secret + "=x"},
		"a bad fingerprint":        {"account", "add", "--email", alice, "--trust-fingerprint", secret},
		"a bad port":               {"account", "add", "--email", alice, "--imap-host", "a", "--smtp-host", "b", "--imap-port", secret},
		"a bad scope":              {"token", "create", "--name", "n", "--scopes", "email:read," + secret},
		"a token argument":         {"token", "create", "--name", "n", "--scopes", "all", secret},
		"a key argument":           {"key", "rotate", "--yes", "--to", "keychain", secret},
		"a bad key source":         {"key", "rotate", "--yes", "--to", "file:" + secret},
		"a doctor argument":        {"doctor", secret},
		"a setup argument":         {"setup", secret},
		"a list argument":          {"account", "list", secret},
		"a token list argument":    {"token", "list", secret},
		"two import paths":         {"import-env", "a.env", secret},
		"a bad boolean flag value": {"account", "remove", "--yes=" + secret, "id"},
	} {
		r := runCLI(t, nil, "", append(args, "--data-dir", dir)...)
		require.Equal(t, exitUsage, r.code, name)
		requireNoLeak(t, r, secret)
		require.NotEmpty(t, r.stderr, name)
	}
	requireNoData(t, dir)

	// The name of a known flag is not secret, and it helps.
	r := runCLI(t, nil, "", "account", "add", "--email", alice, "--imap-host", "a", "--smtp-host", "b", "--imap-port", secret, "--data-dir", dir)
	require.Contains(t, r.stderr, "-imap-port")
	r = runCLI(t, nil, "", "account", "add", "--email")
	require.Contains(t, r.stderr, "-email")
	r = runCLI(t, nil, "", "account", "add", "-h")
	require.Equal(t, exitOK, r.code)
	require.Contains(t, r.stdout, "-trust-fingerprint")
}

// fakeTerminal is the terminal of readTerminalPassword in a test. Its read
// waits until the test lets it go.
type fakeTerminal struct {
	mu       sync.Mutex
	state    *term.State
	restored []*term.State
	release  chan struct{}
}

func (f *fakeTerminal) funcs() terminalFuncs {
	return terminalFuncs{
		getState: func(int) (*term.State, error) { return f.state, nil },
		restore: func(_ int, s *term.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.restored = append(f.restored, s)
			return nil
		},
		readPassword: func(int) ([]byte, error) {
			<-f.release
			return []byte("pw-typed-9a"), nil
		},
	}
}

// The first Ctrl-C ends the context: the read returns, and the terminal gets
// back the state it had before the read (the echo is on again). A read that
// ends normally does not restore: term.ReadPassword does that itself.
func TestReadTerminalPasswordRestoresOnCtrlC(t *testing.T) {
	f := &fakeTerminal{state: &term.State{}, release: make(chan struct{})}
	defer close(f.release)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := readTerminalPassword(ctx, f.funcs(), 0)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("the read did not return on Ctrl-C")
	}
	f.mu.Lock()
	require.Equal(t, []*term.State{f.state}, f.restored)
	f.mu.Unlock()

	g := &fakeTerminal{state: &term.State{}, release: make(chan struct{})}
	close(g.release)
	line, err := readTerminalPassword(context.Background(), g.funcs(), 0)
	require.NoError(t, err)
	require.Equal(t, []byte("pw-typed-9a"), line)
	require.Empty(t, g.restored)
}

// A --to path with a ".." part is refused before anything is opened: after a
// folder that is a symbolic link, ".." does not go where the text says.
func TestKeyRotateRefusesDotDotInTheTarget(t *testing.T) {
	dir, _, _ := rotateFixture(t)
	ids := kekIDs(t, dir)
	keys := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(keys, "sub"), 0o700))
	target := keys + "/sub/../new.key"

	r := runCLI(t, nil, "", "key", "rotate", "--yes", "--to", "file:"+target, "--data-dir", dir)

	require.Equal(t, exitUsage, r.code)
	require.Contains(t, r.stderr, `".."`)
	require.NotContains(t, r.stderr, target)
	_, err := os.Stat(filepath.Join(keys, "new.key"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Equal(t, ids, kekIDs(t, dir))
}

// serve and version never echo a bad flag either: an MCP client starts serve
// from its config, and its standard error goes to the log of the client.
func TestServeAndVersionNeverEchoABadFlag(t *testing.T) {
	const secret = "pw-flag-41b7" //nolint:gosec // G101: a test value, used to check that it is never shown
	dir, _ := newCLIEnv(t)
	for name, args := range map[string][]string{
		"serve, unknown flag":       {"serve", "-" + secret},
		"serve, unknown with value": {"serve", "--" + secret + "=x"},
		"serve, bad syntax":         {"serve", "-=" + secret},
		"version, unknown flag":     {"version", "-" + secret},
		"version, bad syntax":       {"version", "-=" + secret},
	} {
		r := runCLI(t, nil, "", append(args, "--data-dir", dir)...)
		require.Equal(t, exitUsage, r.code, name)
		require.NotEmpty(t, r.stderr, name)
		requireNoLeak(t, r, secret)
	}
	requireNoData(t, dir)

	r := runCLI(t, nil, "", "serve", "-h")
	require.Equal(t, exitOK, r.code)
	require.Contains(t, r.stderr, "-http", "the help of serve goes to standard error: standard output carries the protocol")
	require.Empty(t, r.stdout)
}
