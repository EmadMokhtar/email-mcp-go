package imap

import (
	"crypto/tls"
	"fmt"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/emersion/go-imap/client"
)

type Client struct {
	client *client.Client
	config *config.Config
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

	// Login
	if err := c.Login(cfg.IMAPUsername, cfg.IMAPPassword); err != nil {
		err = c.Logout()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	return &Client{
		client: c,
		config: cfg,
	}, nil
}

func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Logout()
	}
	return nil
}

func (c *Client) reconnect() error {
	if c.client != nil {
		err := c.client.Logout()
		if err != nil {
			return err
		}
	}

	newClient, err := NewClient(c.config)
	if err != nil {
		return err
	}

	c.client = newClient.client
	return nil
}
