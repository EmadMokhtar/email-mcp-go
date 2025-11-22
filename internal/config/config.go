package config

import (
	"fmt"
	"os"
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

		UseOAuth:     getEnv("USE_OAUTH", "false") == "true",
		ClientID:     os.Getenv("OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
		RefreshToken: os.Getenv("OAUTH_REFRESH_TOKEN"),
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
	return nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
