package oauth

import (
	"fmt"
	"net/smtp"

	"github.com/emersion/go-sasl"
)

// XOAUTH2 is the SASL mechanism name used by Gmail and Outlook for OAuth2.
const XOAUTH2 = "XOAUTH2"

// initialResponse builds the XOAUTH2 initial client response. The format is
// fixed by the mechanism: the two fields are separated by control-A (\x01) and
// the string ends with two of them.
func initialResponse(username, accessToken string) []byte {
	return []byte(fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", username, accessToken))
}

// saslClient implements XOAUTH2 for IMAP through go-sasl.
type saslClient struct {
	username    string
	accessToken string
}

// NewSASLClient returns an XOAUTH2 SASL client for go-imap's Authenticate.
func NewSASLClient(username, accessToken string) sasl.Client {
	return &saslClient{username: username, accessToken: accessToken}
}

func (c *saslClient) Start() (string, []byte, error) {
	return XOAUTH2, initialResponse(c.username, c.accessToken), nil
}

// Next is only reached when the server rejects the token. It answers with an
// empty response, which is what the mechanism requires to make the server
// report the failure as a normal error rather than leaving the exchange open.
func (c *saslClient) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}

// smtpAuth implements XOAUTH2 for SMTP through net/smtp, which is the
// interface the mail library's Dialer accepts.
type smtpAuth struct {
	username    string
	accessToken string
}

// NewSMTPAuth returns an XOAUTH2 authenticator for an SMTP dialer.
func NewSMTPAuth(username, accessToken string) smtp.Auth {
	return &smtpAuth{username: username, accessToken: accessToken}
}

func (a *smtpAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return XOAUTH2, initialResponse(a.username, a.accessToken), nil
}

func (a *smtpAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		// The server sent a challenge, which for XOAUTH2 means the token was
		// refused. Reply empty so it returns the actual error.
		return []byte{}, nil
	}

	return nil, nil
}
