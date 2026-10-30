package connect

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/service"
)

// Only a certificate whose only problem is an unknown authority, with no pin
// and a real SHA-256, may be trusted. Every other problem gets its own fixed
// text and no offer.
func TestCheckCertificate(t *testing.T) {
	fingerprint := strings.Repeat("ab", 32)
	for name, tc := range map[string]struct {
		cert     imapsmtp.CertificateUnknownError
		problem  string
		canTrust bool
	}{
		"unknown authority": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fingerprint, Reason: x509.UnknownAuthorityError{}},
			problem: TextCertUnknown, canTrust: true,
		},
		"unknown authority, wrapped": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fingerprint, Reason: fmt.Errorf("tls: %w", x509.UnknownAuthorityError{})},
			problem: TextCertUnknown, canTrust: true,
		},
		"unknown authority without a fingerprint": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: "", Reason: x509.UnknownAuthorityError{}},
			problem: TextCertInvalid,
		},
		"unknown authority with a fingerprint that is not a SHA-256": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: "ABCD", Reason: x509.UnknownAuthorityError{}},
			problem: TextCertInvalid,
		},
		"a pin that did not match": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fingerprint, Pinned: true},
			problem: TextCertPinChanged,
		},
		// The check of the new certificate fills Reason for a wrong pin too. A
		// wrong pin is still never a first pin: only a reconnect may change it.
		"a pin that did not match, with an unknown authority": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fingerprint, Pinned: true, Reason: x509.UnknownAuthorityError{}},
			problem: TextCertPinChanged,
		},
		"a host name that does not match": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fingerprint, Reason: x509.HostnameError{Host: "localhost"}},
			problem: TextCertWrongHost,
		},
		"an expired certificate": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fingerprint, Reason: x509.CertificateInvalidError{Reason: x509.Expired}},
			problem: TextCertExpired,
		},
		"another problem": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fingerprint, Reason: errors.New("bad")},
			problem: TextCertInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := CheckCertificate(&tc.cert)
			require.Equal(t, tc.problem, got.Problem)
			require.Equal(t, tc.canTrust, got.CanTrust)
			if account.IsFingerprint(tc.cert.FingerprintSHA256) {
				require.Equal(t, tc.cert.FingerprintSHA256, got.Fingerprint)
			} else {
				require.Empty(t, got.Fingerprint)
			}
		})
	}
}

// The texts are chosen by the type of the error. The text of the error, which
// can come from a server, is never used.
func TestErrorText(t *testing.T) {
	serverText := "NO [AUTHENTICATIONFAILED] secret server text"
	for name, tc := range map[string]struct {
		err   error
		text  string
		known bool
	}{
		"address":  {err: service.ErrInvalidAddress, text: TextAddress, known: true},
		"password": {err: service.ErrNoPassword, text: TextNoPassword, known: true},
		"settings": {err: fmt.Errorf("%w: plain", service.ErrInvalidSettings), text: TextSettings, known: true},
		"exists":   {err: service.ErrAccountExists, text: TextExists, known: true},
		"refused login": {
			err:  &service.ConnectionTestError{Err: fmt.Errorf("%s: %w", serverText, imapsmtp.ErrAuthFailed)},
			text: TextAuthFailed, known: true,
		},
		"busy server": {
			err:  &service.ConnectionTestError{Err: fmt.Errorf("%s: %w", serverText, imapsmtp.ErrServerBusy)},
			text: TextServerBusy, known: true,
		},
		"no such host": {
			err:  &service.ConnectionTestError{Err: &net.OpError{Op: "dial", Err: &net.DNSError{Err: serverText, IsNotFound: true}}},
			text: TextHostNotFound, known: true,
		},
		"time out": {
			err:  &service.ConnectionTestError{Err: fmt.Errorf("%s: %w", serverText, context.DeadlineExceeded)},
			text: TextTimeout, known: true,
		},
		"refused connection": {
			err:  &service.ConnectionTestError{Err: &net.OpError{Op: "dial", Err: errors.New(serverText)}},
			text: TextCannotConnect, known: true,
		},
		"other test failure": {
			err:  &service.ConnectionTestError{Err: errors.New(serverText)},
			text: TextTestFailed, known: true,
		},
		"stale certificate decision": {err: service.ErrCertificateDecisionStale, text: TextDecisionStale, known: true},
		"account changed":            {err: service.ErrAccountChanged, text: TextAccountChanged, known: true},
		"database":                   {err: errors.New("store: disk full")},
	} {
		t.Run(name, func(t *testing.T) {
			text, known := ErrorText(tc.err)
			require.Equal(t, tc.text, text)
			require.Equal(t, tc.known, known)
			require.NotContains(t, text, "secret")
		})
	}
}

// In a reconnect, the user may replace or remove a pin only for a certificate
// whose only problem is the pin and an unknown authority. A first pin follows
// the rules of a new account. The text names the problem, and a replace or a
// remove needs the warning.
func TestCheckReconnect(t *testing.T) {
	fp := strings.Repeat("ab", 32)
	old := strings.Repeat("cd", 32)
	for name, tc := range map[string]struct {
		cert       imapsmtp.CertificateUnknownError
		oldPin     string
		problem    string
		change     service.PinChange
		replacePin bool
	}{
		"changed pin, unknown authority": {
			cert: imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: x509.UnknownAuthorityError{}}, oldPin: old,
			problem: TextCertReplace, change: service.PinChangeReplace, replacePin: true,
		},
		"changed pin, signed by an authority": {
			cert: imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true}, oldPin: old,
			problem: TextCertRemove, change: service.PinChangeRemove, replacePin: true,
		},
		"changed pin, wrong host": {
			cert: imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: x509.HostnameError{}}, oldPin: old,
			problem: TextCertChangedWrongHost, change: service.PinChangeRefused,
		},
		"changed pin, expired": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: x509.CertificateInvalidError{Reason: x509.Expired}},
			oldPin:  old,
			problem: TextCertChangedExpired, change: service.PinChangeRefused,
		},
		"changed pin, another problem": {
			cert: imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: errors.New("bad")}, oldPin: old,
			problem: TextCertChangedInvalid, change: service.PinChangeRefused,
		},
		"first pin": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Reason: x509.UnknownAuthorityError{}},
			problem: TextCertUnknown, change: service.PinChangeAdd,
		},
		"first pin, wrong host": {
			cert:    imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Reason: x509.HostnameError{}},
			problem: TextCertWrongHost, change: service.PinChangeRefused,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := CheckReconnect(&service.ReconnectCertificate{Host: "imap.example.com", Port: 993, OldPin: tc.oldPin, Cert: &tc.cert,
				Change: service.ReconnectPinChange(&tc.cert)})
			require.Equal(t, tc.problem, got.Problem)
			require.Equal(t, tc.change, got.Change)
			require.Equal(t, tc.change != service.PinChangeRefused, got.CanChange())
			require.Equal(t, tc.replacePin, got.ReplacesPin())
			require.Equal(t, fp, got.Fingerprint)
			require.Equal(t, tc.oldPin, got.OldFingerprint)
		})
	}

	// The change comes from the service. A change that the service refused is
	// never offered, whatever the certificate looks like.
	refused := CheckReconnect(&service.ReconnectCertificate{OldPin: old, Change: service.PinChangeRefused,
		Cert: &imapsmtp.CertificateUnknownError{FingerprintSHA256: fp, Pinned: true, Reason: x509.UnknownAuthorityError{}}})
	require.False(t, refused.CanChange())
	require.NotEqual(t, TextCertReplace, refused.Problem)
}

func TestPlainOnThisComputer(t *testing.T) {
	server := account.MailServer{
		IMAPHost: "imap.example.com", IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.com", SMTPSecurity: account.SecurityTLS,
	}
	require.False(t, PlainOnThisComputer(server))

	local := server
	local.IMAPHost, local.IMAPSecurity = "127.0.0.1", account.SecurityPlain
	require.True(t, PlainOnThisComputer(local))

	local = server
	local.SMTPHost, local.SMTPSecurity = "localhost", account.SecurityPlain
	require.True(t, PlainOnThisComputer(local))

	remote := server
	remote.IMAPSecurity = account.SecurityPlain
	require.False(t, PlainOnThisComputer(remote), "plain to another host is refused, not consented to")
}

func TestCleanHost(t *testing.T) {
	require.Equal(t, "imap.example.com", CleanHost("  IMAP.Example.COM\t"))
	require.Equal(t, "", CleanHost("   "))
}

func TestGroupFingerprint(t *testing.T) {
	require.Equal(t, "AB:CD:EF", GroupFingerprint("abcdef"))
	require.Empty(t, GroupFingerprint(""))
}
