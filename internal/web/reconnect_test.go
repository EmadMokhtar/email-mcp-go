package web

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/connect"
	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/provider/imapsmtp"
	"github.com/EmadMokhtar/email-mcp-go/internal/secrets"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/imaptest"
	"github.com/EmadMokhtar/email-mcp-go/internal/testutil/smtptest"
)

// oldPin is a pin that no test server shows: the certificate that the user
// trusted before the server changed it.
var oldPin = strings.Repeat("0a", 32)

// textChangedWarning is the warning of a changed certificate, as the page
// shows it.
const textChangedWarning = "A changed certificate can mean that someone is in the middle of the connection"

// storedAccount stores an account of the local tenant with a sealed password,
// as a connect would.
func (c *connectFixture) storedAccount(address string, server account.MailServer, password string, status account.Status) *account.Account {
	c.t.Helper()
	ctx := context.Background()
	tenant, err := c.store.EnsureLocalTenant(ctx)
	require.NoError(c.t, err)
	a := account.NewIMAPAccount(tenant, address, server, t0)
	a.Status = status
	version, sealed, err := c.vault.Encrypt(ctx, tenant, secrets.AAD(string(tenant), string(a.ID), "password"), []byte(password))
	require.NoError(c.t, err)
	require.NoError(c.t, c.store.CreateAccountWithSecret(ctx, a, "password", version, sealed))
	return a
}

// reload reads the account again.
func (c *connectFixture) reload(a *account.Account) *account.Account {
	c.t.Helper()
	got, err := c.store.GetAccount(context.Background(), a.TenantID, a.ID)
	require.NoError(c.t, err)
	return got
}

// storedPassword returns the stored password of the account.
func (c *connectFixture) storedPassword(a *account.Account) string {
	c.t.Helper()
	ctx := context.Background()
	version, sealed, err := c.store.GetAccountSecret(ctx, a.TenantID, a.ID, "password")
	require.NoError(c.t, err)
	plain, err := c.vault.Decrypt(ctx, a.TenantID, version, secrets.AAD(string(a.TenantID), string(a.ID), "password"), sealed)
	require.NoError(c.t, err)
	return string(plain)
}

// pinnedServer returns the settings of the test servers with both
// certificates pinned.
func pinnedServer(imapSrv *imaptest.Server, smtpSrv *smtptest.Server) account.MailServer {
	return account.MailServer{
		IMAPHost: imapSrv.Host, IMAPPort: imapSrv.Port, IMAPSecurity: account.SecurityTLS,
		SMTPHost: smtpSrv.Host, SMTPPort: smtpSrv.Port, SMTPSecurity: account.SecurityTLS,
		Username: imapSrv.User, PinnedIMAPCert: imapSrv.CertSHA256, PinnedSMTPCert: smtpSrv.CertSHA256,
	}
}

// reconnectPath is the path of the reconnect page of the account.
func reconnectPath(a *account.Account) string {
	return "/accounts/" + string(a.ID) + "/reconnect"
}

// Every route of the reconnect names its method and is in the list, so the
// route tests check it for the CSRF token and for GET without changes.
func TestReconnectRoutesAreListed(t *testing.T) {
	var patterns []string
	for _, r := range routes {
		patterns = append(patterns, r.pattern)
	}
	require.Contains(t, patterns, "GET /accounts/{id}/reconnect")
	require.Contains(t, patterns, "POST /accounts/{id}/reconnect")
}

// The reconnect page has the password field (never with a value) and tells
// the user the command for a computer without a browser.
func TestReconnectPageShowsTheCommandLineHint(t *testing.T) {
	c := newConnectFixture(t)
	a := c.storedAccount("alice@example.org", pinnedServer(imaptest.New(t), smtptest.New(t)), marker, account.StatusNeedsReauth)

	resp, body := do(t, c.browser, newRequest(t, http.MethodGet, c.base+reconnectPath(a), nil))

	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	require.Contains(t, text, "email-mcp account reconnect "+string(a.ID))
	require.Contains(t, text, "alice@example.org")
	require.Contains(t, text, "Needs to be reconnected")
	require.Len(t, inputNamed(t, body, "password"), 1)
	requireSafePasswordFields(t, body)
	require.Len(t, inputNamed(t, body, csrfField), 1)
	require.Contains(t, body, `action="`+reconnectPath(a)+`"`)
	require.NotContains(t, body, marker)

	// An account of another tenant, or no account, is not found.
	c.addTenant("other")
	b := account.NewIMAPAccount("other", "bob@other.example", pinnedServer(imaptest.New(t), smtptest.New(t)), t0)
	require.NoError(t, c.store.CreateAccount(context.Background(), b))
	for _, path := range []string{reconnectPath(b), "/accounts/missing/reconnect"} {
		resp, body = do(t, c.browser, newRequest(t, http.MethodGet, c.base+path, nil))
		require.Equal(t, http.StatusNotFound, resp.StatusCode, path)
		require.NotContains(t, body, "bob@other.example")
	}
}

// The accounts page shows a "Reconnect" link for every account: a changed
// certificate does not mark the account, and the user may know that the
// password changed before anything fails. The settings page links to the
// reconnect page too.
func TestAccountsPageLinksToReconnect(t *testing.T) {
	c := newConnectFixture(t)
	server := pinnedServer(imaptest.New(t), smtptest.New(t))
	broken := c.storedAccount("alice@example.org", server, marker, account.StatusNeedsReauth)
	fine := c.storedAccount("bob@example.org", server, marker, account.StatusOK)

	resp, body := do(t, c.browser, newRequest(t, http.MethodGet, c.base+"/", nil))

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `href="`+reconnectPath(broken)+`"`)
	require.Contains(t, body, `href="`+reconnectPath(fine)+`"`, "an account with the status ok has the link too")
	require.Contains(t, visibleText(t, body), "Reconnect")

	_, body = do(t, c.browser, newRequest(t, http.MethodGet, c.base+"/accounts/"+string(fine.ID)+"/settings", nil))
	require.Contains(t, body, `href="`+reconnectPath(fine)+`"`)
}

// A new password that works is saved. The account keeps its id and is
// connected again. The password is never in an answer, a header or the log.
func TestReconnectSavesTheNewPassword(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv, smtpSrv := imaptest.New(t), smtptest.New(t)
	a := c.storedAccount("alice@example.org", pinnedServer(imapSrv, smtpSrv), "pw-old-revoked-3c", account.StatusNeedsReauth)

	resp, body := c.post(reconnectPath(a), url.Values{"password": {imapSrv.Pass}})

	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	require.Equal(t, "/", resp.Header.Get("Location"))
	c.requireNoLeak(resp, body, imapSrv.Pass)
	got := c.reload(a)
	require.Equal(t, a.ID, got.ID)
	require.Equal(t, account.StatusOK, got.Status)
	require.Equal(t, imapSrv.Pass, c.storedPassword(a))
}

// A password that the server refuses changes nothing. The page shows a fixed
// text and never the password.
func TestReconnectWithAWrongPassword(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv, smtpSrv := imaptest.New(t), smtptest.New(t)
	a := c.storedAccount("alice@example.org", pinnedServer(imapSrv, smtpSrv), imapSrv.Pass, account.StatusNeedsReauth)

	resp, body := c.post(reconnectPath(a), url.Values{"password": {marker}})

	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textAuthFailed)
	require.NotContains(t, strings.ToUpper(body), "AUTHENTICATIONFAILED")
	requireSafePasswordFields(t, body)
	c.requireNoLeak(resp, body, marker)
	require.Equal(t, account.StatusNeedsReauth, c.reload(a).Status)
	require.Equal(t, imapSrv.Pass, c.storedPassword(a))
}

// A changed pinned certificate needs a second step: the page shows the host,
// the port, the old and the new fingerprint, and the warning, and the box
// whose label states the warning. The decision travels in a signed offer for
// this session and account. Only the server that showed it gets the new pin.
func TestReconnectReplacesAPinInASecondStep(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv, smtpSrv := imaptest.New(t), smtptest.New(t)
	server := pinnedServer(imapSrv, smtpSrv)
	server.PinnedIMAPCert = oldPin
	a := c.storedAccount("alice@example.org", server, imapSrv.Pass, account.StatusOK)
	form := url.Values{"password": {imapSrv.Pass}}

	resp, body := c.post(reconnectPath(a), form)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	text := visibleText(t, body)
	require.Contains(t, text, imapSrv.Host)
	require.Contains(t, text, ":"+strconv.Itoa(imapSrv.Port))
	require.Contains(t, text, grouped(oldPin))
	require.Contains(t, text, grouped(imapSrv.CertSHA256))
	require.Contains(t, text, textChangedWarning)
	boxes := inputNamed(t, body, "trust_imap")
	require.Len(t, boxes, 1)
	require.False(t, boxes[0].has["checked"], "the box starts empty")
	require.Contains(t, strings.ToLower(labelOfBox(t, body, "trust_imap")), strings.ToLower(textChangedWarning),
		"the label of the box states the warning")
	requireTrustNotOffered(t, body, "smtp")
	offer := offerOf(t, body, "imap")
	var got certOffer
	require.NoError(t, c.signer.Open(purposeCertOffer, offer, &got))
	require.Equal(t, certOffer{
		Session: c.sessionHash, Protocol: "imap", Host: imapSrv.Host, Port: imapSrv.Port,
		Fingerprint: imapSrv.CertSHA256, Account: string(a.ID), Old: oldPin,
	}, got)
	requireSafePasswordFields(t, body)
	c.requireNoLeak(resp, body, imapSrv.Pass)
	require.Equal(t, oldPin, c.reload(a).Mail.PinnedIMAPCert, "nothing changed")

	// Without the box, the page asks again and changes nothing.
	resp, body = c.post(reconnectPath(a), with(form, "imap_offer", offer))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, inputNamed(t, body, "trust_imap"), 1)
	require.Equal(t, oldPin, c.reload(a).Mail.PinnedIMAPCert)

	// With the box, the pin is replaced.
	resp, body = c.post(reconnectPath(a), with(form, "trust_imap", "1", "imap_offer", offer))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	c.requireNoLeak(resp, body, imapSrv.Pass)
	after := c.reload(a)
	require.Equal(t, a.ID, after.ID)
	require.Equal(t, imapSrv.CertSHA256, after.Mail.PinnedIMAPCert)
	require.Equal(t, smtpSrv.CertSHA256, after.Mail.PinnedSMTPCert, "the SMTP pin did not change")
}

// An offer is good only for the session, the account, the server and the old
// pin that it was made for. An offer of the connect flow is not good for a
// reconnect, and an offer of a reconnect is not good for the connect flow.
func TestReconnectOffersAreBound(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv, smtpSrv := imaptest.New(t), smtptest.New(t)
	server := pinnedServer(imapSrv, smtpSrv)
	server.PinnedIMAPCert = oldPin
	a := c.storedAccount("alice@example.org", server, imapSrv.Pass, account.StatusOK)
	other := c.storedAccount("bob@example.org", server, imapSrv.Pass, account.StatusOK)
	form := url.Values{"password": {imapSrv.Pass}, "trust_imap": {"1"}}
	seal := func(o certOffer) string {
		t.Helper()
		o.Session, o.Protocol, o.Host, o.Port = c.sessionHash, "imap", imapSrv.Host, imapSrv.Port
		token, err := c.signer.Seal(purposeCertOffer, o, certOfferTTL)
		require.NoError(t, err)
		return token
	}
	probes := c.probe.count()

	for name, token := range map[string]string{
		"an offer of the connect flow": seal(certOffer{Fingerprint: imapSrv.CertSHA256}),
		"an offer for another account": seal(certOffer{Fingerprint: imapSrv.CertSHA256, Account: string(other.ID), Old: oldPin}),
		"an offer with another old pin": seal(certOffer{Fingerprint: imapSrv.CertSHA256, Account: string(a.ID),
			Old: strings.Repeat("ef", 32)}),
		"no offer": "",
	} {
		resp, body := c.post(reconnectPath(a), with(form, "imap_offer", token))
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, name)
		require.Contains(t, visibleText(t, body), textBadTrust, name)
	}
	require.Equal(t, probes, c.probe.count(), "nothing was tested")
	require.Equal(t, oldPin, c.reload(a).Mail.PinnedIMAPCert)

	// A reconnect offer in the connect flow.
	reconnectOffer := seal(certOffer{Fingerprint: imapSrv.CertSHA256, Account: string(a.ID), Old: oldPin})
	resp, body := c.post("/connect/save", with(saveForm(imapSrv, smtpSrv, imapSrv.Pass), "email", "carol@example.org",
		"trust_imap", "1", "imap_offer", reconnectOffer))
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textBadTrust)
	require.Equal(t, probes, c.probe.count(), "nothing was tested")
}

// A certificate for another host name, or out of its time, is never offered.
func TestReconnectNeverOffersAnInvalidCertificate(t *testing.T) {
	for name, tc := range map[string]struct {
		reason error
		text   string
	}{
		"wrong host": {x509.HostnameError{Host: "imap.example.org"}, connect.TextCertChangedWrongHost},
		"expired":    {x509.CertificateInvalidError{Reason: x509.Expired}, connect.TextCertChangedExpired},
	} {
		t.Run(name, func(t *testing.T) {
			c := newConnectFixture(t)
			c.probe.set(func(_ context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
				return &imapsmtp.ProbeResult{SMTPOK: true, IMAPCert: &imapsmtp.CertificateUnknownError{
					Protocol: "imap", Host: cfg.Server.IMAPHost, FingerprintSHA256: strings.Repeat("cd", 32),
					Pinned: true, Reason: tc.reason,
				}}, nil
			})
			server := account.MailServer{
				IMAPHost: "imap.example.org", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
				SMTPHost: "smtp.example.org", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
				Username: "alice@example.org", PinnedIMAPCert: oldPin,
			}
			a := c.storedAccount("alice@example.org", server, marker, account.StatusOK)

			resp, body := c.post(reconnectPath(a), url.Values{})

			require.Equal(t, http.StatusOK, resp.StatusCode)
			requireTrustNotOffered(t, body, "imap")
			require.Contains(t, visibleText(t, body), tc.text)
			require.Equal(t, oldPin, c.reload(a).Mail.PinnedIMAPCert)
		})
	}
}

// A POST without the CSRF token of the session is refused and changes nothing.
func TestReconnectNeedsTheCSRFToken(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv, smtpSrv := imaptest.New(t), smtptest.New(t)
	a := c.storedAccount("alice@example.org", pinnedServer(imapSrv, smtpSrv), "pw-old-revoked-3c", account.StatusNeedsReauth)

	resp, body := do(t, c.browser, sameOrigin(newRequest(t, http.MethodPost, c.base+reconnectPath(a),
		url.Values{"password": {imapSrv.Pass}}), c.base))

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.NotContains(t, body, imapSrv.Pass)
	require.Zero(t, c.probe.count(), "nothing was tested")
	require.Equal(t, account.StatusNeedsReauth, c.reload(a).Status)
}

// labelOfBox returns the text of the label that holds the box with the name.
func labelOfBox(t *testing.T, page, name string) string {
	t.Helper()
	for _, block := range strings.Split(page, "<label") {
		if strings.Contains(block, `name="`+name+`"`) {
			end := strings.Index(block, "</label>")
			require.GreaterOrEqual(t, end, 0)
			return visibleText(t, "<label"+block[:end]+"</label>")
		}
	}
	t.Fatalf("no label holds the box %q", name)
	return ""
}

// A server that had no pin and is not on this computer follows the first-pin
// rule of the connect flow: trusting its certificate also needs the "I
// understand the risk" box, which is checked before any test.
func TestReconnectFirstPinOfARemoteServerNeedsTheRiskBox(t *testing.T) {
	c := newConnectFixture(t)
	fingerprint := strings.Repeat("cd", 32)
	c.probe.set(unknownIMAPCert(fingerprint))
	server := account.MailServer{
		IMAPHost: "imap.example.org", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.org", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: "alice@example.org",
	}
	a := c.storedAccount("alice@example.org", server, marker, account.StatusOK)

	resp, body := c.post(reconnectPath(a), url.Values{})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, msgTrustLabel, labelOfBox(t, body, "trust_imap"))
	require.Len(t, inputNamed(t, body, "understand"), 1)
	require.NotContains(t, visibleText(t, body), textChangedWarning, "a first pin is not a changed pin")
	offer := offerOf(t, body, "imap")
	probes := c.probe.count()

	resp, body = c.post(reconnectPath(a), url.Values{"trust_imap": {"1"}, "imap_offer": {offer}})
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, visibleText(t, body), textUnderstand)
	require.Equal(t, probes, c.probe.count(), "nothing was tested")
	require.Empty(t, c.reload(a).Mail.PinnedIMAPCert)

	resp, body = c.post(reconnectPath(a), url.Values{"trust_imap": {"1"}, "imap_offer": {offer}, "understand": {"1"}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	require.Equal(t, fingerprint, c.reload(a).Mail.PinnedIMAPCert)
}

// When an authority now signs the certificate of a pinned server, the step
// offers to stop pinning. Its box has its own label, which says that any
// certificate that an authority signs for the host is then accepted, and keeps
// the warning. The offer says that it is a removal, and the pin goes.
func TestReconnectRemoveStepShowsTheRemoveLabel(t *testing.T) {
	c := newConnectFixture(t)
	fingerprint := strings.Repeat("cd", 32)
	c.probe.set(func(_ context.Context, cfg imapsmtp.Config) (*imapsmtp.ProbeResult, error) {
		if cfg.Server.PinnedIMAPCert == oldPin {
			return &imapsmtp.ProbeResult{SMTPOK: true, IMAPCert: &imapsmtp.CertificateUnknownError{
				Protocol: "imap", Host: cfg.Server.IMAPHost, FingerprintSHA256: fingerprint, Pinned: true,
			}}, nil
		}
		return &imapsmtp.ProbeResult{IMAPOK: true, SMTPOK: true}, nil
	})
	server := account.MailServer{
		IMAPHost: "imap.example.org", IMAPPort: 993, IMAPSecurity: account.SecurityTLS,
		SMTPHost: "smtp.example.org", SMTPPort: 465, SMTPSecurity: account.SecurityTLS,
		Username: "alice@example.org", PinnedIMAPCert: oldPin,
	}
	a := c.storedAccount("alice@example.org", server, marker, account.StatusOK)

	resp, body := c.post(reconnectPath(a), url.Values{})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	label := labelOfBox(t, body, "trust_imap")
	require.Equal(t, msgRemoveLabel, label)
	require.Contains(t, label, connect.TextRemovePin)
	require.Contains(t, strings.ToLower(label), strings.ToLower(textChangedWarning), "the warning stays")
	require.NotEqual(t, msgReplaceLabel, label)
	offer := offerOf(t, body, "imap")
	var got certOffer
	require.NoError(t, c.signer.Open(purposeCertOffer, offer, &got))
	require.True(t, got.Remove)

	// With the box ticked, the pin goes.
	resp, body = c.post(reconnectPath(a), url.Values{"trust_imap": {"1"}, "imap_offer": {offer}})
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	require.Empty(t, c.reload(a).Mail.PinnedIMAPCert, "the pin is removed")
}

// A replace step keeps the replace label.
func TestReconnectReplaceStepShowsTheReplaceLabel(t *testing.T) {
	c := newConnectFixture(t)
	imapSrv, smtpSrv := imaptest.New(t), smtptest.New(t)
	server := pinnedServer(imapSrv, smtpSrv)
	server.PinnedIMAPCert = oldPin
	a := c.storedAccount("alice@example.org", server, imapSrv.Pass, account.StatusOK)

	_, body := c.post(reconnectPath(a), url.Values{})

	require.Equal(t, msgReplaceLabel, labelOfBox(t, body, "trust_imap"))
	require.NotContains(t, body, connect.TextRemovePin)
}
