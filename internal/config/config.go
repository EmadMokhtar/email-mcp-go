package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// IMAP Configuration
	IMAPHost     string
	IMAPPort     string
	IMAPUsername string
	IMAPPassword string
	IMAPTLS      bool

	// SMTP Configuration
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	SMTPTLS      bool

	// OAuth2 (optional for Gmail, etc.)
	UseOAuth     bool
	ClientID     string
	ClientSecret string
	RefreshToken string
	// OAuthTokenURL overrides the token endpoint. Empty uses Google's.
	OAuthTokenURL string

	// HTTP mode only.
	// AuthToken is the bearer token every /mcp request must carry.
	AuthToken string
	// AllowedOrigins lists the browser origins allowed to call the server.
	// Empty means no cross-origin access.
	AllowedOrigins []string
}

func Load() (*Config, error) {
	cfg := &Config{
		IMAPHost:     getEnv("IMAP_HOST", "imap.gmail.com"),
		IMAPPort:     getEnv("IMAP_PORT", "993"),
		IMAPUsername: os.Getenv("IMAP_USERNAME"),
		IMAPPassword: os.Getenv("IMAP_PASSWORD"),
		IMAPTLS:      getEnv("IMAP_TLS", "true") == "true",

		SMTPHost:     getEnv("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:     getEnv("SMTP_PORT", "587"),
		SMTPUsername: os.Getenv("SMTP_USERNAME"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"),
		SMTPTLS:      getEnv("SMTP_TLS", "true") == "true",

		UseOAuth:      getEnv("USE_OAUTH", "false") == "true",
		ClientID:      os.Getenv("OAUTH_CLIENT_ID"),
		ClientSecret:  os.Getenv("OAUTH_CLIENT_SECRET"),
		RefreshToken:  os.Getenv("OAUTH_REFRESH_TOKEN"),
		OAuthTokenURL: os.Getenv("OAUTH_TOKEN_URL"),

		AuthToken:      os.Getenv("MCP_AUTH_TOKEN"),
		AllowedOrigins: splitList(os.Getenv("MCP_ALLOWED_ORIGINS")),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.IMAPUsername == "" || c.IMAPPassword == "" {
		if !c.UseOAuth {
			return fmt.Errorf("IMAP credentials are required")
		}
	}

	if c.UseOAuth {
		if c.ClientID == "" || c.ClientSecret == "" || c.RefreshToken == "" {
			return fmt.Errorf("USE_OAUTH is set, so OAUTH_CLIENT_ID, OAUTH_CLIENT_SECRET and OAUTH_REFRESH_TOKEN are all required")
		}
	}

	// The SMTP username doubles as the From address on every message, so a
	// send tool without it can only fail once a user tries to use it.
	if c.SMTPUsername == "" {
		return fmt.Errorf("SMTP_USERNAME is required: it is used as the From address")
	}

	// Ports are strings from the environment. Check them at startup instead of
	// letting a typo become port 0 on the first connection attempt.
	if _, err := strconv.Atoi(c.IMAPPort); err != nil {
		return fmt.Errorf("IMAP_PORT %q is not a number", c.IMAPPort)
	}
	if _, err := strconv.Atoi(c.SMTPPort); err != nil {
		return fmt.Errorf("SMTP_PORT %q is not a number", c.SMTPPort)
	}

	return nil
}

// splitList parses a comma-separated environment variable into a list,
// ignoring empty entries and surrounding spaces.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}

	return out
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
