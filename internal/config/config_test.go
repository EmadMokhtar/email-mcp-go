package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name        string
		envVars     map[string]string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid configuration with all fields",
			envVars: map[string]string{
				"IMAP_HOST":     "imap.example.com",
				"IMAP_PORT":     "993",
				"IMAP_USERNAME": "user@example.com",
				"IMAP_PASSWORD": "password123",
				"IMAP_TLS":      "true",
				"SMTP_HOST":     "smtp.example.com",
				"SMTP_PORT":     "587",
				"SMTP_USERNAME": "user@example.com",
				"SMTP_PASSWORD": "password123",
				"SMTP_TLS":      "true",
			},
			wantErr: false,
		},
		{
			name: "valid configuration with defaults",
			envVars: map[string]string{
				"IMAP_USERNAME": "user@example.com",
				"IMAP_PASSWORD": "password123",
				"SMTP_USERNAME": "user@example.com",
				"SMTP_PASSWORD": "password123",
			},
			wantErr: false,
		},
		{
			name: "missing IMAP credentials",
			envVars: map[string]string{
				"SMTP_USERNAME": "user@example.com",
				"SMTP_PASSWORD": "password123",
			},
			wantErr:     true,
			errContains: "IMAP credentials are required",
		},
		{
			name: "OAuth configuration",
			envVars: map[string]string{
				"USE_OAUTH":           "true",
				"OAUTH_CLIENT_ID":     "client123",
				"OAUTH_CLIENT_SECRET": "secret123",
				"OAUTH_REFRESH_TOKEN": "refresh123",
			},
			wantErr: false,
		},
		{
			name: "TLS disabled",
			envVars: map[string]string{
				"IMAP_USERNAME": "user@example.com",
				"IMAP_PASSWORD": "password123",
				"IMAP_TLS":      "false",
				"SMTP_TLS":      "false",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear environment
			clearEnv()

			// Set test environment variables
			for key, value := range tt.envVars {
				_ = os.Setenv(key, value)
			}
			defer clearEnv()

			// Load configuration
			cfg, err := Load()

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, cfg)

			// Verify configuration values
			if tt.envVars["IMAP_USERNAME"] != "" {
				assert.Equal(t, tt.envVars["IMAP_USERNAME"], cfg.IMAPUsername)
			}
			if tt.envVars["IMAP_PASSWORD"] != "" {
				assert.Equal(t, tt.envVars["IMAP_PASSWORD"], cfg.IMAPPassword)
			}

			// Verify defaults
			if tt.envVars["IMAP_HOST"] == "" {
				assert.Equal(t, "imap.gmail.com", cfg.IMAPHost)
			}
			if tt.envVars["IMAP_PORT"] == "" {
				assert.Equal(t, "993", cfg.IMAPPort)
			}
			if tt.envVars["SMTP_HOST"] == "" {
				assert.Equal(t, "smtp.gmail.com", cfg.SMTPHost)
			}
			if tt.envVars["SMTP_PORT"] == "" {
				assert.Equal(t, "587", cfg.SMTPPort)
			}

			// Verify TLS settings
			expectedTLS := tt.envVars["IMAP_TLS"] != "false"
			assert.Equal(t, expectedTLS, cfg.IMAPTLS)
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				IMAPUsername: "user@example.com",
				IMAPPassword: "password",
			},
			wantErr: false,
		},
		{
			name: "missing credentials without OAuth",
			config: &Config{
				IMAPUsername: "",
				IMAPPassword: "",
				UseOAuth:     false,
			},
			wantErr: true,
		},
		{
			name: "missing credentials with OAuth",
			config: &Config{
				IMAPUsername: "",
				IMAPPassword: "",
				UseOAuth:     true,
				ClientID:     "client123",
				ClientSecret: "secret123",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetEnv(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		defaultValue string
		envValue     string
		expected     string
	}{
		{
			name:         "environment variable exists",
			key:          "TEST_VAR",
			defaultValue: "default",
			envValue:     "custom",
			expected:     "custom",
		},
		{
			name:         "environment variable does not exist",
			key:          "TEST_VAR",
			defaultValue: "default",
			envValue:     "",
			expected:     "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv()
			if tt.envValue != "" {
				_ = os.Setenv(tt.key, tt.envValue)
			}
			defer clearEnv()

			result := getEnv(tt.key, tt.defaultValue)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Helper function to clear test environment variables
func clearEnv() {
	envVars := []string{
		"IMAP_HOST", "IMAP_PORT", "IMAP_USERNAME", "IMAP_PASSWORD", "IMAP_TLS",
		"SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_TLS",
		"USE_OAUTH", "OAUTH_CLIENT_ID", "OAUTH_CLIENT_SECRET", "OAUTH_REFRESH_TOKEN",
		"TEST_VAR",
	}
	for _, v := range envVars {
		_ = os.Unsetenv(v)
	}
}
