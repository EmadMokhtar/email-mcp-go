package smtp

import (
	"github.com/EmadMokhtar/email-mcp-go/internal/config"
)

type Client struct {
	config *config.Config
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		config: cfg,
	}
}
