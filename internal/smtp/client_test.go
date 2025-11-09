package smtp

import (
	"testing"

	"github.com/EmadMokhtar/email-mcp-go/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestNewSMTPClient(t *testing.T) {
	tests := []struct {
		name   string
		config *config.Config
	}{
		{
			name: "create client with TLS",
			config: &config.Config{
				SMTPHost:     "smtp.gmail.com",
				SMTPPort:     "587",
				SMTPUsername: "user@gmail.com",
				SMTPPassword: "password",
				SMTPTLS:      true,
			},
		},
		{
			name: "create client without TLS",
			config: &config.Config{
				SMTPHost:     "smtp.example.com",
				SMTPPort:     "25",
				SMTPUsername: "user@example.com",
				SMTPPassword: "password",
				SMTPTLS:      false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config)

			assert.NotNil(t, client)
			assert.NotNil(t, client.config)
			assert.Equal(t, tt.config.SMTPHost, client.config.SMTPHost)
			assert.Equal(t, tt.config.SMTPPort, client.config.SMTPPort)
			assert.Equal(t, tt.config.SMTPUsername, client.config.SMTPUsername)
			assert.Equal(t, tt.config.SMTPTLS, client.config.SMTPTLS)
		})
	}
}
