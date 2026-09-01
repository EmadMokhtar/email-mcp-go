package smtp

import (
	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/EmadMokhtar/email-mcp-go/internal/oauth"
)

type Client struct {
	config *config.Config
	// tokens is set only when OAuth is enabled. Each send asks it for a
	// token; it refreshes only when the cached one is about to expire.
	tokens *oauth.TokenSource
}

func NewClient(cfg *config.Config) *Client {
	c := &Client{config: cfg}

	if cfg.UseOAuth {
		c.tokens = oauth.NewTokenSource(cfg.OAuthTokenURL, cfg.ClientID, cfg.ClientSecret, cfg.RefreshToken)
	}

	return c
}
