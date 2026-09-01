package imap

import (
	"crypto/tls"
	"fmt"
	"sync"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/EmadMokhtar/email-mcp-go/internal/oauth"
	"github.com/emersion/go-imap/client"
)

type Client struct {
	// mu serializes every IMAP command. The client holds a single
	// connection, and IMAP is stateful: each operation selects a mailbox
	// and then acts on it. Two commands running at the same time would
	// interleave and act on the wrong mailbox. Callers do not need to
	// lock; every exported method locks for itself.
	mu     sync.Mutex
	client *client.Client
	config *config.Config
	// tokens is set only when OAuth is enabled. It caches the access token
	// and refreshes it when it expires.
	tokens *oauth.TokenSource
}

func NewClient(cfg *config.Config) (*Client, error) {
	var c *client.Client
	var err error

	addr := fmt.Sprintf("%s:%s", cfg.IMAPHost, cfg.IMAPPort)

	if cfg.IMAPTLS {
		// Connect with TLS
		c, err = client.DialTLS(addr, &tls.Config{
			ServerName: cfg.IMAPHost,
		})
	} else {
		// Connect without TLS (not recommended for production)
		c, err = client.Dial(addr)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to IMAP server: %w", err)
	}

	client := &Client{
		client: c,
		config: cfg,
	}

	if cfg.UseOAuth {
		client.tokens = oauth.NewTokenSource(cfg.OAuthTokenURL, cfg.ClientID, cfg.ClientSecret, cfg.RefreshToken)

		if err := client.authenticateOAuth(); err != nil {
			_ = c.Logout()
			return nil, err
		}

		return client, nil
	}

	// Login
	if err := c.Login(cfg.IMAPUsername, cfg.IMAPPassword); err != nil {
		// Close the connection, but report the login failure. A logout error
		// here is a consequence of the failed login, not the cause.
		_ = c.Logout()
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	return client, nil
}

// authenticateOAuth signs in with XOAUTH2 using a freshly minted access token.
func (c *Client) authenticateOAuth() error {
	supported, err := c.client.SupportAuth(oauth.XOAUTH2)
	if err != nil {
		return fmt.Errorf("failed to check for XOAUTH2 support: %w", err)
	}
	if !supported {
		return fmt.Errorf("IMAP server does not advertise the %s authentication mechanism", oauth.XOAUTH2)
	}

	token, err := c.tokens.Token()
	if err != nil {
		return fmt.Errorf("failed to obtain an OAuth access token: %w", err)
	}

	if err := c.client.Authenticate(oauth.NewSASLClient(c.config.IMAPUsername, token)); err != nil {
		return fmt.Errorf("failed to authenticate with OAuth: %w", err)
	}

	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil {
		return c.client.Logout()
	}
	return nil
}

// reconnect drops the current connection and opens a new one.
// The caller must already hold c.mu.
func (c *Client) reconnect() error {
	if c.client != nil {
		// Ignore the logout error. Reconnecting is most often needed because
		// the old connection is already broken, and a failing logout must not
		// stop us from opening a new one.
		_ = c.client.Logout()
	}

	newClient, err := NewClient(c.config)
	if err != nil {
		return err
	}

	c.client = newClient.client
	c.tokens = newClient.tokens

	return nil
}
