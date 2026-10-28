package imapsmtp

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-smtp"
	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/mail"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// ---------------------------------------------------------------- helpers

// smtpConfig returns a config that logs in to the test SMTP server, with its
// certificate pinned. When imapSrv is nil, the IMAP settings are valid but
// unused. The two test servers accept the same login.
func smtpConfig(imapSrv *imaptest.Server, smtpSrv *smtptest.Server, security account.Security) Config {
	var cfg Config
	if imapSrv != nil {
		cfg = pinnedConfig(imapSrv, account.SecurityTLS)
	} else {
		cfg = baseConfig()
		cfg.Server.IMAPHost = "imap.example.com"
		cfg.Server.IMAPPort = 993
		cfg.Server.IMAPSecurity = account.SecurityTLS
	}
	cfg.Server.Username = smtpSrv.User
	cfg.Password = secrets.Secret(smtpSrv.Pass)
	cfg.Server.SMTPHost = smtpSrv.Host
	cfg.Server.SMTPPort = smtpSrv.Port
	cfg.Server.SMTPSecurity = security
	cfg.Server.PinnedSMTPCert = smtpSrv.CertSHA256
	cfg.TokenScope = testScope
	return cfg
}

// newSMTPBackend returns a backend for the config. It is closed when the test
// ends.
func newSMTPBackend(t *testing.T, cfg Config) *Backend {
	t.Helper()
	b := New(cfg, testSigner())
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// outgoing returns a message to send. The id makes the Message-ID unique.
func outgoing(id, subject string, rcpt ...string) *mail.RawMessage {
	return &mail.RawMessage{
		From:       "me@example.com",
		Recipients: rcpt,
		Data: crlf("From: Me <me@example.com>\n" +
			"To: Bob <bob@example.com>\n" +
			"Subject: " + subject + "\n" +
			"Message-ID: <" + id + "@example.com>\n" +
			"Date: Tue, 10 Mar 2026 10:00:00 +0000\n" +
			"MIME-Version: 1.0\n" +
			"Content-Type: text/plain; charset=utf-8\n" +
			"\n" +
			"Hello Bob.\n" +
			// A line with only a dot must survive the dot-stuffing of SMTP.
			".\n" +
			".. and a line that starts with dots.\n" +
			"Bye.\n"),
	}
}

// syncBuffer is a bytes.Buffer that many goroutines may write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLog gives cfg a logger that writes to the returned buffer.
func captureLog(cfg *Config) *syncBuffer {
	buf := &syncBuffer{}
	cfg.Logger = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return buf
}

// requireNoneOf checks that text holds none of the parts.
func requireNoneOf(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		require.NotContains(t, text, part)
	}
}

// ---------------------------------------------------------------- sending

func TestSendDeliversOverEveryTransport(t *testing.T) {
	for name, tc := range map[string]struct {
		mode     smtptest.Mode
		security account.Security
	}{
		"implicit TLS": {smtptest.ModeTLS, account.SecurityTLS},
		"STARTTLS":     {smtptest.ModeSTARTTLS, account.SecuritySTARTTLS},
		"plain":        {smtptest.ModePlain, account.SecurityPlain},
	} {
		t.Run(name, func(t *testing.T) {
			smtpSrv := smtptest.New(t, smtptest.WithMode(tc.mode))
			b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, tc.security))
			raw := outgoing("one", "Lunch", "bob@example.com")

			require.NoError(t, b.Send(t.Context(), raw))

			got := smtpSrv.Messages()
			require.Len(t, got, 1)
			require.Equal(t, "me@example.com", got[0].From)
			require.Equal(t, []string{"bob@example.com"}, got[0].Rcpt)
			require.Equal(t, string(raw.Data), string(got[0].Data))
		})
	}
}

func TestSendDeliversToAllRecipientsIncludingBcc(t *testing.T) {
	smtpSrv := smtptest.New(t)
	b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))
	// The message names bob (To) and carol (Cc). dave is a Bcc: he is in the
	// envelope and not in the headers.
	raw := outgoing("bcc", "Plan", "bob@example.com", "carol@example.com", "dave@example.com")

	require.NoError(t, b.Send(t.Context(), raw))

	got := smtpSrv.Messages()
	require.Len(t, got, 1, "one message, not one for each recipient")
	require.Equal(t, []string{"bob@example.com", "carol@example.com", "dave@example.com"}, got[0].Rcpt)
	require.NotContains(t, string(got[0].Data), "dave")
}

func TestSendRequiresSTARTTLS(t *testing.T) {
	// The server offers AUTH on the plain connection and not STARTTLS. A client
	// that sent the password anyway would send it without encryption.
	smtpSrv := smtptest.New(t, smtptest.WithMode(smtptest.ModeNoSTARTTLS))
	b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecuritySTARTTLS))

	err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

	require.Error(t, err)
	require.ErrorContains(t, err, "STARTTLS")
	require.Empty(t, smtpSrv.Messages(), "nothing is delivered")
	require.Zero(t, smtpSrv.AuthAttempts(), "the password is never sent without TLS")
}

func TestSendNeverFallsBackToPlainText(t *testing.T) {
	// Each fake offers STARTTLS and then does not give a TLS connection. The
	// fake says yes to AUTH, MAIL and RCPT, so only the client can stop the
	// send. It must stop before AUTH, so that the password does not go out.
	const greeting = "220 fake ESMTP ready\r\n"
	ehlo := "250-fake\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n"
	lenient := func(line string) (string, fakeThen) {
		switch smtpVerb(line) {
		case "AUTH":
			return "235 2.7.0 accepted\r\n", fakeStay
		case "MAIL", "RCPT":
			return "250 ok\r\n", fakeStay
		}
		return "502 unexpected\r\n", fakeStay
	}
	for name, reply := range map[string]smtpReply{
		"server refuses STARTTLS": func(line string) (string, fakeThen) {
			switch smtpVerb(line) {
			case "EHLO":
				return ehlo, fakeStay
			case "STARTTLS":
				return "454 4.7.0 TLS not available\r\n", fakeStay
			}
			return lenient(line)
		},
		"server says go ahead but stays in plain text": func(line string) (string, fakeThen) {
			switch smtpVerb(line) {
			case "EHLO":
				return ehlo, fakeStay
			case "STARTTLS":
				return "220 2.0.0 go ahead\r\n250-fake\r\n250 AUTH PLAIN\r\n", fakeStay
			}
			return lenient(line)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeSMTP(t, greeting, reply)
			// The fake is on loopback, so the settings are valid. They ask
			// for STARTTLS.
			b := newSMTPBackend(t, fake.config(account.SecuritySTARTTLS))

			err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

			require.Error(t, err)
			got := strings.ToUpper(fake.Received())
			requireNoneOf(t, got, "AUTH", "MAIL FROM", "RCPT TO", "DATA")
		})
	}
}

func TestSendChecksTheCertificate(t *testing.T) {
	for name, tc := range map[string]struct {
		mode     smtptest.Mode
		security account.Security
	}{
		"implicit TLS": {smtptest.ModeTLS, account.SecurityTLS},
		"STARTTLS":     {smtptest.ModeSTARTTLS, account.SecuritySTARTTLS},
	} {
		t.Run(name, func(t *testing.T) {
			smtpSrv := smtptest.New(t, smtptest.WithMode(tc.mode))

			t.Run("unknown certificate", func(t *testing.T) {
				cfg := smtpConfig(nil, smtpSrv, tc.security)
				cfg.Server.PinnedSMTPCert = ""
				b := newSMTPBackend(t, cfg)

				err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

				var certErr *CertificateUnknownError
				require.ErrorAs(t, err, &certErr)
				require.False(t, certErr.Pinned)
				require.Equal(t, smtpSrv.CertSHA256, certErr.FingerprintSHA256)
				require.Equal(t, "smtp", certErr.Protocol)
				require.True(t, strings.HasPrefix(certErr.Error(), "smtp: "), certErr.Error())
				// This is the one case where the UI may offer "trust this".
				var unknownAuthority x509.UnknownAuthorityError
				require.ErrorAs(t, err, &unknownAuthority)
				require.Empty(t, smtpSrv.Messages())
				require.Zero(t, smtpSrv.AuthAttempts(), "the password never goes to a server that is not trusted")
			})

			t.Run("wrong pin", func(t *testing.T) {
				cfg := smtpConfig(nil, smtpSrv, tc.security)
				cfg.Server.PinnedSMTPCert = strings.Repeat("0", 64)
				// A trusted root does not replace a pin.
				cfg.RootCAs = smtpSrv.CertPool
				b := newSMTPBackend(t, cfg)

				err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

				var certErr *CertificateUnknownError
				require.ErrorAs(t, err, &certErr)
				require.True(t, certErr.Pinned)
				require.Equal(t, smtpSrv.CertSHA256, certErr.FingerprintSHA256)
				require.Zero(t, smtpSrv.AuthAttempts())
			})

			t.Run("trusted root without a pin", func(t *testing.T) {
				cfg := smtpConfig(nil, smtpSrv, tc.security)
				cfg.Server.PinnedSMTPCert = ""
				cfg.RootCAs = smtpSrv.CertPool
				b := newSMTPBackend(t, cfg)

				require.NoError(t, b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com")))
			})

			t.Run("trusted certificate for another host", func(t *testing.T) {
				cfg := smtpConfig(nil, smtpSrv, tc.security)
				cfg.Server.PinnedSMTPCert = ""
				cfg.RootCAs = smtpSrv.CertPool
				// The certificate is valid for the address 127.0.0.1 only.
				cfg.Server.SMTPHost = "localhost"
				b := newSMTPBackend(t, cfg)

				err := b.Send(t.Context(), outgoing("two", "Lunch", "bob@example.com"))

				var certErr *CertificateUnknownError
				require.ErrorAs(t, err, &certErr)
				var hostnameErr x509.HostnameError
				require.ErrorAs(t, err, &hostnameErr)
			})
		})
	}
}

func TestSendUsesTheSMTPPinAndNotTheIMAPPin(t *testing.T) {
	smtpSrv := smtptest.New(t)

	t.Run("the IMAP pin does not help", func(t *testing.T) {
		cfg := smtpConfig(nil, smtpSrv, account.SecurityTLS)
		cfg.Server.PinnedSMTPCert = ""
		cfg.Server.PinnedIMAPCert = smtpSrv.CertSHA256
		b := newSMTPBackend(t, cfg)

		var certErr *CertificateUnknownError
		require.ErrorAs(t, b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com")), &certErr)
	})

	t.Run("a wrong IMAP pin does not hurt", func(t *testing.T) {
		cfg := smtpConfig(nil, smtpSrv, account.SecurityTLS)
		cfg.Server.PinnedIMAPCert = strings.Repeat("0", 64)
		b := newSMTPBackend(t, cfg)

		require.NoError(t, b.Send(t.Context(), outgoing("two", "Lunch", "bob@example.com")))
	})
}

func TestSendPlainIsOnlyForLoopbackOnALocalInstall(t *testing.T) {
	smtpSrv := smtptest.New(t, smtptest.WithMode(smtptest.ModePlain))

	t.Run("not a local install", func(t *testing.T) {
		cfg := smtpConfig(nil, smtpSrv, account.SecurityPlain)
		cfg.Local = false
		b := newSMTPBackend(t, cfg)

		require.Error(t, b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com")))
		require.Zero(t, smtpSrv.AuthAttempts())
	})

	t.Run("a host that is not loopback", func(t *testing.T) {
		cfg := smtpConfig(nil, smtpSrv, account.SecurityPlain)
		cfg.Server.SMTPHost = "mail.example.com"
		b := newSMTPBackend(t, cfg)
		// A cancelled context stops any dial at once. If the error is not the
		// context error, the check ran before any network step.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := b.Send(ctx, outgoing("two", "Lunch", "bob@example.com"))

		require.Error(t, err)
		require.NotErrorIs(t, err, context.Canceled)
	})
}

func TestSendRejectedRecipientSendsNothing(t *testing.T) {
	recipients := []string{"bob@example.com", "carol@example.org", "dave@example.com"}

	t.Run("end to end", func(t *testing.T) {
		smtpSrv := smtptest.New(t, smtptest.RejectRcpt("carol@example.org"))
		b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))

		err := b.Send(t.Context(), outgoing("one", "Lunch", recipients...))

		var rejected *RecipientRejectedError
		require.ErrorAs(t, err, &rejected)
		require.Equal(t, 1, rejected.Index)
		require.Equal(t, 3, rejected.Total)
		require.Equal(t, 550, rejected.Code)
		require.ErrorContains(t, err, "recipient 2 of 3")
		// Neither the address nor the text of the server is in the error.
		requireNoneOf(t, err.Error(), "carol", "example.org", "bob", "dave",
			"Recipient address rejected", "User unknown")
		require.Empty(t, smtpSrv.Messages(), "the message went to nobody, also not to the accepted recipients")
		require.Equal(t, 1, smtpSrv.Resets(), "the transaction was reset")
	})

	t.Run("RSET is sent and DATA is not", func(t *testing.T) {
		fake := newFakeSMTP(t, "220 fake ESMTP ready\r\n", func(line string) (string, fakeThen) {
			switch smtpVerb(line) {
			case "EHLO":
				return "250-fake\r\n250 AUTH PLAIN\r\n", fakeStay
			case "AUTH":
				return "235 2.7.0 accepted\r\n", fakeStay
			case "MAIL":
				return "250 ok\r\n", fakeStay
			case "RCPT":
				if strings.Contains(line, "carol") {
					return "550 5.1.1 <carol@example.org> unknown\r\n", fakeStay
				}
				return "250 ok\r\n", fakeStay
			case "RSET":
				return "250 reset\r\n", fakeStay
			}
			return "502 unexpected\r\n", fakeStay
		})
		b := newSMTPBackend(t, fake.config(account.SecurityPlain))

		err := b.Send(t.Context(), outgoing("one", "Lunch", recipients...))

		var rejected *RecipientRejectedError
		require.ErrorAs(t, err, &rejected)
		require.Equal(t, 1, rejected.Index)
		got := fake.Received()
		require.Equal(t, 2, strings.Count(got, "RCPT TO"), "no RCPT after the one that was refused")
		require.Equal(t, 1, strings.Count(got, "RSET"))
		require.NotContains(t, got, "DATA")
		require.Less(t, strings.Index(got, "RCPT TO:<carol"), strings.Index(got, "RSET"), "RSET comes after the refusal")
	})
}

func TestSendAuthFailureHidesTheServerText(t *testing.T) {
	// The server repeats the user name in the text of its refusal. The error
	// must carry the reply code and not that text.
	for name, tc := range map[string]struct {
		mode     smtptest.Mode
		security account.Security
	}{
		"implicit TLS": {smtptest.ModeTLS, account.SecurityTLS},
		"STARTTLS":     {smtptest.ModeSTARTTLS, account.SecuritySTARTTLS},
		"plain":        {smtptest.ModePlain, account.SecurityPlain},
	} {
		t.Run(name, func(t *testing.T) {
			smtpSrv := smtptest.New(t, smtptest.WithMode(tc.mode))
			cfg := smtpConfig(nil, smtpSrv, tc.security)
			cfg.Password = secrets.Secret("not-the-password")
			cfg.Server.Username = smtpSrv.User
			b := newSMTPBackend(t, cfg)

			err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

			require.Error(t, err)
			require.ErrorContains(t, err, "smtp: login failed")
			require.ErrorContains(t, err, "535")
			requireNoneOf(t, err.Error(), "Authentication failed for", smtpSrv.User, "not-the-password")
			require.Empty(t, smtpSrv.Messages())
		})
	}
}

func TestSendNeedsAuthPlain(t *testing.T) {
	fake := newFakeSMTP(t, "220 fake ESMTP ready\r\n", func(line string) (string, fakeThen) {
		switch smtpVerb(line) {
		case "EHLO":
			// Only LOGIN is offered.
			return "250-fake\r\n250 AUTH LOGIN\r\n", fakeStay
		case "MAIL":
			return "250 ok\r\n", fakeStay
		}
		return "502 unexpected\r\n", fakeStay
	})
	b := newSMTPBackend(t, fake.config(account.SecurityPlain))

	err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

	require.ErrorContains(t, err, "AUTH PLAIN")
	requireNoneOf(t, strings.ToUpper(fake.Received()), "AUTH ", "MAIL FROM")
}

func TestSendRefusesAMessageThatCannotBeSentSafely(t *testing.T) {
	good := outgoing("one", "Lunch", "bob@example.com")
	with := func(change func(*mail.RawMessage)) *mail.RawMessage {
		raw := *good
		raw.Recipients = append([]string(nil), good.Recipients...)
		change(&raw)
		return &raw
	}
	for name, raw := range map[string]*mail.RawMessage{
		"no message":       nil,
		"no sender":        with(func(r *mail.RawMessage) { r.From = "" }),
		"no recipients":    with(func(r *mail.RawMessage) { r.Recipients = nil }),
		"no data":          with(func(r *mail.RawMessage) { r.Data = nil }),
		"empty recipient":  with(func(r *mail.RawMessage) { r.Recipients = []string{"bob@example.com", ""} }),
		"angle bracket":    with(func(r *mail.RawMessage) { r.Recipients = []string{"bob@example.com> NOTIFY=NEVER"} }),
		"space in address": with(func(r *mail.RawMessage) { r.Recipients = []string{"b ob@example.com"} }),
		"line break":       with(func(r *mail.RawMessage) { r.Recipients = []string{"bob@example.com\r\nRSET"} }),
		"sender with a >":  with(func(r *mail.RawMessage) { r.From = "me@example.com> SIZE=1" }),
		"NUL in address":   with(func(r *mail.RawMessage) { r.Recipients = []string{"bob\x00@example.com"} }),
	} {
		t.Run(name, func(t *testing.T) {
			smtpSrv := smtptest.New(t)
			b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))

			err := b.Send(t.Context(), raw)

			require.Error(t, err)
			requireNoneOf(t, err.Error(), "bob", "example.com", "NOTIFY", "RSET", "SIZE=1")
			require.Empty(t, smtpSrv.Messages())
			require.Zero(t, smtpSrv.AuthAttempts(), "nothing was sent, not even the login")
		})
	}
}

func TestSendNeedsSMTPUTF8ForNonASCIIAddresses(t *testing.T) {
	smtpSrv := smtptest.New(t)
	b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))
	raw := outgoing("one", "Lunch", "jörg@example.com")

	err := b.Send(t.Context(), raw)

	// The test server does not offer SMTPUTF8. The client must not send an
	// address that the server cannot read.
	require.ErrorContains(t, err, "SMTPUTF8")
	require.Empty(t, smtpSrv.Messages())
}

// ---------------------------------------------------------------- one send, never two

func TestSendReportsARefusedMessageWithoutTheServerText(t *testing.T) {
	smtpSrv := smtptest.New(t, smtptest.WithDataHook(func(smtptest.Message) error {
		return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "rejected for bob@example.com"}
	}))
	b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))

	err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

	require.Error(t, err)
	require.ErrorContains(t, err, "554")
	requireNoneOf(t, err.Error(), "bob", "rejected for")
	require.Empty(t, smtpSrv.Messages())
	require.NotContains(t, err.Error(), "may have been delivered", "the server said no, so a retry is safe")
}

func TestSendSaysWhenTheOutcomeIsNotKnown(t *testing.T) {
	// The server keeps the message and then drops the connection before its
	// final reply. The client cannot tell. The error must not look like a
	// plain refusal, because a retry could send the message twice.
	smtpSrv := smtptest.New(t, smtptest.WithDataHook(func(smtptest.Message) error { return smtptest.ErrDropConnection }))
	b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))

	err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

	require.Error(t, err)
	require.ErrorIs(t, err, ErrDeliveryUnknown)
	require.ErrorContains(t, err, "may have been delivered")
	require.Len(t, smtpSrv.Messages(), 1)
}

func TestSendKeepsTheWarningWhenTimeEndsAfterTheData(t *testing.T) {
	// The server has read the whole message and does not answer. The context
	// ends, or the time limit passes. The message may be delivered, and the
	// error must still say so.
	stuckServer := func(t *testing.T) (*smtptest.Server, <-chan struct{}) {
		entered := make(chan struct{})
		release := make(chan struct{})
		srv := smtptest.New(t, smtptest.WithDataHook(func(smtptest.Message) error {
			close(entered)
			<-release
			return nil
		}))
		// Cleanups run in reverse order, so the hook is free before the server
		// stops.
		t.Cleanup(func() { close(release) })
		return srv, entered
	}

	t.Run("context canceled", func(t *testing.T) {
		smtpSrv, entered := stuckServer(t)
		b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			<-entered
			cancel()
		}()

		err := b.Send(ctx, outgoing("one", "Lunch", "bob@example.com"))

		require.ErrorIs(t, err, ErrDeliveryUnknown)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("time limit", func(t *testing.T) {
		smtpSrv, _ := stuckServer(t)
		cfg := smtpConfig(nil, smtpSrv, account.SecurityTLS)
		cfg.CommandTimeout = time.Second
		b := newSMTPBackend(t, cfg)

		err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

		require.ErrorIs(t, err, ErrDeliveryUnknown)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestSendAppendsSentCopyWhenEnabled(t *testing.T) {
	imapSrv := imaptest.New(t)
	imapSrv.Create("Sent", imap.MailboxAttrSent)
	smtpSrv := smtptest.New(t)
	cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
	cfg.Server.SaveSentCopy = true
	b := newSMTPBackend(t, cfg)

	require.NoError(t, b.Send(t.Context(), outgoing("copy", "Sent copy", "bob@example.com", "dave@example.com")))

	require.Len(t, smtpSrv.Messages(), 1)
	stored := serverMessages(t, imapSrv, "Sent")
	require.Len(t, stored, 1)
	require.Equal(t, "Sent copy", stored[0].Subject)
	require.Equal(t, []string{`\seen`}, stored[0].lowerFlags(), "only \\Seen, so the copy is not a draft and not new")
}

func TestSendNoSentCopyWhenDisabled(t *testing.T) {
	imapSrv := imaptest.New(t)
	imapSrv.Create("Sent", imap.MailboxAttrSent)
	smtpSrv := smtptest.New(t)
	cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
	cfg.Server.SaveSentCopy = false
	b := newSMTPBackend(t, cfg)

	require.NoError(t, b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com")))

	require.Len(t, smtpSrv.Messages(), 1)
	require.Zero(t, imapSrv.TotalConns(), "the IMAP server is not used at all")
	require.Empty(t, serverMessages(t, imapSrv, "Sent"))
}

func TestSendDoesNotReportASentCopyFailure(t *testing.T) {
	// The message is delivered when the SMTP server takes it. A failure to save
	// the copy must not make Send fail: the caller would send the message again.
	const secretSubject = "Quarterly layoffs"
	overQuota := func(string) error {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeOverQuota, Text: "no room for bob@example.com"}
	}
	for name, tc := range map[string]struct {
		imapOpts []imaptest.Option
		hasSent  bool
		code     string
		// sameConn is true when the IMAP connection is still good after the
		// failure. go-imap closes it when the server refuses a message before
		// it is sent, and the pool then makes a new one.
		sameConn bool
	}{
		"no Sent folder":                 {nil, false, "code=no_sent_folder", true},
		"the server refuses the message": {[]imaptest.Option{imaptest.WithAppendError(overQuota)}, true, "code=OVERQUOTA", true},
		"the message is too large":       {[]imaptest.Option{imaptest.WithAppendLimit(10)}, true, "code=TOOBIG", false},
	} {
		t.Run(name, func(t *testing.T) {
			imapSrv := imaptest.New(t, tc.imapOpts...)
			if tc.hasSent {
				imapSrv.Create("Sent", imap.MailboxAttrSent)
			}
			smtpSrv := smtptest.New(t)
			cfg := smtpConfig(imapSrv, smtpSrv, account.SecurityTLS)
			cfg.Server.SaveSentCopy = true
			logs := captureLog(&cfg)
			b := newSMTPBackend(t, cfg)

			err := b.Send(t.Context(), outgoing("one", secretSubject, "bob@example.com"))

			require.NoError(t, err)
			require.Len(t, smtpSrv.Messages(), 1, "delivered once")
			// The log has an error code and nothing else about the message.
			out := logs.String()
			require.Contains(t, out, tc.code)
			requireNoneOf(t, out, secretSubject, "bob@", "dave@", "me@example.com", "no room for", "s3cret-pass")

			// The backend works after the failed APPEND.
			_, err = b.ListFolders(t.Context())
			require.NoError(t, err)
			if tc.sameConn {
				require.Equal(t, 1, imapSrv.TotalConns(), "the connection was reused")
			}
			if tc.hasSent {
				require.Empty(t, serverMessages(t, imapSrv, "Sent"))
			}
		})
	}
}

func TestSendDoesNotRetryTheSentCopy(t *testing.T) {
	// An APPEND that is sent again after a drop can store the copy twice.
	smtpSrv := smtptest.New(t)
	fake := newFakeIMAP(t, "* OK [CAPABILITY IMAP4rev1 UIDPLUS] ready\r\n", func(tag, cmd string) (string, fakeThen) {
		switch cmd {
		case "LOGIN":
			return tag + " OK LOGIN completed\r\n", fakeStay
		case "CAPABILITY":
			return "* CAPABILITY IMAP4rev1 UIDPLUS\r\n" + tag + " OK CAPABILITY completed\r\n", fakeStay
		case "LIST":
			return "* LIST (\\Sent) \"/\" Sent\r\n" + tag + " OK LIST completed\r\n", fakeStay
		case "APPEND":
			return "", fakeReset
		}
		return tag + " BAD unexpected\r\n", fakeStay
	})
	cfg := fake.plainConfig()
	cfg.Server.SMTPHost = smtpSrv.Host
	cfg.Server.SMTPPort = smtpSrv.Port
	cfg.Server.SMTPSecurity = account.SecurityTLS
	cfg.Server.PinnedSMTPCert = smtpSrv.CertSHA256
	cfg.Server.SaveSentCopy = true
	cfg.TokenScope = testScope
	b := newSMTPBackend(t, cfg)

	require.NoError(t, b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com")), "a lost connection does not fail a send that was delivered")

	require.Equal(t, 1, strings.Count(fake.Received(), " APPEND "), "APPEND was sent once")
	require.Len(t, smtpSrv.Messages(), 1)
}

// ---------------------------------------------------------------- time

func TestSMTPDeadlineGrowsWithTheMessage(t *testing.T) {
	const minute = time.Minute
	require.Equal(t, minute, smtpDeadline(minute, 0))
	// 100 KiB per second.
	require.Equal(t, minute+time.Second, smtpDeadline(minute, 100*1024))
	require.Equal(t, minute+10*time.Second, smtpDeadline(minute, 1000*1024))
	// 25 MiB is 256 seconds of data time.
	require.Equal(t, minute+256*time.Second, smtpDeadline(minute, 25*1024*1024))
	require.Equal(t, minute, smtpDeadline(minute, -5), "a negative size adds nothing")
}

func TestSendHasOneDeadlineForTheWholeConversation(t *testing.T) {
	// Each command is quick, but together they take longer than the limit.
	// go-smtp sets and clears a deadline around every command, so a limit that
	// is only set once on the connection would not last.
	smtpSrv := smtptest.New(t, smtptest.WithDelay(150*time.Millisecond))
	cfg := smtpConfig(nil, smtpSrv, account.SecurityTLS)
	cfg.CommandTimeout = 400 * time.Millisecond
	b := newSMTPBackend(t, cfg)
	start := time.Now()

	err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
	require.Empty(t, smtpSrv.Messages())
}

func TestSendStopsWhenTheContextEnds(t *testing.T) {
	smtpSrv := smtptest.New(t, smtptest.WithDelay(2*time.Second))
	b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(150*time.Millisecond, cancel)
	start := time.Now()

	err := b.Send(ctx, outgoing("one", "Lunch", "bob@example.com"))

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 1500*time.Millisecond)
	require.Empty(t, smtpSrv.Messages())
}

func TestSendStopsWhenTheServerSaysNothing(t *testing.T) {
	// The fake sends no greeting. The deadline ends the wait.
	fake := newFakeSMTP(t, "", func(string) (string, fakeThen) { return "", fakeStay })
	cfg := fake.config(account.SecurityPlain)
	cfg.CommandTimeout = 300 * time.Millisecond
	b := newSMTPBackend(t, cfg)
	start := time.Now()

	err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 3*time.Second)
}

func TestSendFailsWhenTheServerIsNotThere(t *testing.T) {
	smtpSrv := smtptest.New(t)
	cfg := smtpConfig(nil, smtpSrv, account.SecurityTLS)
	smtpSrv.Close()
	b := newSMTPBackend(t, cfg)

	err := b.Send(t.Context(), outgoing("one", "Lunch", "bob@example.com"))

	require.ErrorContains(t, err, "smtp: connect")
	require.NotContains(t, fmt.Sprint(err), "s3cret-pass")
}

func TestSendStopsBeforeConnectingWhenTheContextEnded(t *testing.T) {
	smtpSrv := smtptest.New(t)
	b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := b.Send(ctx, outgoing("one", "Lunch", "bob@example.com"))

	require.True(t, errors.Is(err, context.Canceled), "%v", err)
	require.Empty(t, smtpSrv.Messages())
}

// ---------------------------------------------------------------- more

func TestSendDeliversALargeMessageWhole(t *testing.T) {
	smtpSrv := smtptest.New(t)
	b := newSMTPBackend(t, smtpConfig(nil, smtpSrv, account.SecurityTLS))
	raw := outgoing("large", "Large", "bob@example.com")
	// 3 MiB of lines, some of them with a dot at the start.
	var body bytes.Buffer
	for body.Len() < 3<<20 {
		body.WriteString("A line of text that is not very short, to fill the message.\r\n.A line that starts with a dot.\r\n")
	}
	raw.Data = append(raw.Data, body.Bytes()...)

	require.NoError(t, b.Send(t.Context(), raw))

	got := smtpSrv.Messages()
	require.Len(t, got, 1)
	require.Equal(t, len(raw.Data), len(got[0].Data))
	require.True(t, bytes.Equal(raw.Data, got[0].Data), "the message arrived unchanged")
}

func TestAppendErrorCodeUsesAFixedList(t *testing.T) {
	serverErr := func(typ imap.StatusResponseType, code imap.ResponseCode, text string) error {
		return &imap.Error{Type: typ, Code: code, Text: text}
	}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"no Sent folder":         {fmt.Errorf("%w: no @sent folder", mail.ErrNotFound), "no_sent_folder"},
		"pool closed":            {errPoolClosed, "closed"},
		"timeout":                {fmt.Errorf("imap: command took longer: %w", context.DeadlineExceeded), "timeout"},
		"canceled":               {fmt.Errorf("imap: command stopped: %w", context.Canceled), "canceled"},
		"known response code":    {serverErr(imap.StatusResponseTypeNo, imap.ResponseCodeOverQuota, "text"), "OVERQUOTA"},
		"unknown code is hidden": {serverErr(imap.StatusResponseTypeNo, "alice@example.com", "text"), "server_no"},
		"no code":                {serverErr(imap.StatusResponseTypeBad, "", "text"), "server_bad"},
		"network":                {fmt.Errorf("read: %w", net.ErrClosed), "network"},
		"anything else":          {errors.New("something with bob@example.com"), "failed"},
	} {
		t.Run(name, func(t *testing.T) {
			got := appendErrorCode(tc.err)
			require.Equal(t, tc.want, got)
			requireNoneOf(t, got, "@", "text")
		})
	}
}

func TestValidEnvelopeAddress(t *testing.T) {
	for _, ok := range []string{"bob@example.com", "jörg@example.com", "a+b@x.org", "bob@[1.2.3.4]"} {
		require.True(t, validEnvelopeAddress(ok), "%q", ok)
	}
	for _, bad := range []string{
		"", "bob@example.com>", "<bob@example.com", "b ob@example.com", "bob@example.com\n", "bob@example.com\r",
		"bob\x00@example.com", "bob@example.com\t", "\xff@example.com", strings.Repeat("a", 250) + "@example.com",
	} {
		require.False(t, validEnvelopeAddress(bad), "%q", bad)
	}
}
