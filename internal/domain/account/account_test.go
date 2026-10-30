package account

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// validServer returns a hosted-safe server: TLS to remote hosts, valid ports.
func validServer() MailServer {
	return MailServer{
		IMAPHost:     "imap.example.com",
		IMAPPort:     993,
		IMAPSecurity: SecurityTLS,
		SMTPHost:     "smtp.example.com",
		SMTPPort:     465,
		SMTPSecurity: SecurityTLS,
		Username:     "user@example.com",
		SaveSentCopy: true,
	}
}

func TestNewIMAPAccountDefaults(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	server := validServer()

	a := NewIMAPAccount(TenantID("tenant-1"), "user@example.com", server, now)

	require.Equal(t, MailSend, a.Levels[AreaMail])
	require.Equal(t, MailSend, a.Level(AreaMail))
	require.True(t, a.ConfirmSends)
	require.True(t, a.ConfirmDeletes)
	require.Equal(t, DefaultLimits(), a.Limits)
	require.Equal(t, Level(0), a.Level(AreaCalendar))
	require.Equal(t, Level(0), a.Level(AreaContacts))

	require.Equal(t, TenantID("tenant-1"), a.TenantID)
	require.Equal(t, "user@example.com", a.Address)
	require.Empty(t, a.DisplayName)
	require.Equal(t, ProviderIMAP, a.Provider)
	require.Equal(t, StatusOK, a.Status)
	require.Equal(t, now, a.CreatedAt)
	require.Nil(t, a.AutonomyAcceptedAt)
	require.Len(t, a.Levels, 1)
	require.Regexp(t, `^[a-z2-7]{26}$`, string(a.ID))

	require.NotNil(t, a.Mail)
	require.Equal(t, server, *a.Mail)
}

func TestNewIMAPAccountCopiesServer(t *testing.T) {
	server := validServer()
	a := NewIMAPAccount("t", "user@example.com", server, time.Now())

	// Changing the caller's value later must not change the account.
	server.IMAPHost = "changed.example.com"
	require.Equal(t, "imap.example.com", a.Mail.IMAPHost)
}

func TestNewIMAPAccountDoesNotShareState(t *testing.T) {
	a := NewIMAPAccount("t", "a@example.com", validServer(), time.Now())
	b := NewIMAPAccount("t", "b@example.com", validServer(), time.Now())

	a.Levels[AreaCalendar] = MailRead
	require.Equal(t, Level(0), b.Level(AreaCalendar))
	require.NotEqual(t, a.ID, b.ID)
}

func TestLevelMissingAreaOnNilMap(t *testing.T) {
	a := &Account{}
	require.Equal(t, Level(0), a.Level(AreaMail))
}

func TestMailName(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{MailOff, "Off"},
		{MailRead, "Read"},
		{MailOrganize, "Organize"},
		{MailDraft, "Draft"},
		{MailSend, "Send"},
		{MailDelete, "Delete"},
		{Level(-1), "Unknown"},
		{Level(6), "Unknown"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, tt.level.MailName(), "level %d", int(tt.level))
	}
}

func TestLevelValues(t *testing.T) {
	// These numbers are stored and compared. They must not change.
	require.Equal(t, 0, int(MailOff))
	require.Equal(t, 1, int(MailRead))
	require.Equal(t, 2, int(MailOrganize))
	require.Equal(t, 3, int(MailDraft))
	require.Equal(t, 4, int(MailSend))
	require.Equal(t, 5, int(MailDelete))
}

func TestConstantValues(t *testing.T) {
	require.Equal(t, "mail", string(AreaMail))
	require.Equal(t, "calendar", string(AreaCalendar))
	require.Equal(t, "contacts", string(AreaContacts))
	require.Equal(t, "imap", string(ProviderIMAP))
	require.Equal(t, "tls", string(SecurityTLS))
	require.Equal(t, "starttls", string(SecuritySTARTTLS))
	require.Equal(t, "plain", string(SecurityPlain))
	require.Equal(t, "ok", string(StatusOK))
	require.Equal(t, "needs_reauth", string(StatusNeedsReauth))
}

func TestDefaultLimits(t *testing.T) {
	require.Equal(t, Limits{20, 200, 50, 500, 30 * time.Second, 0, false}, DefaultLimits())
	require.NoError(t, DefaultLimits().Validate())
}

func TestMailServerValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(m *MailServer)
		local   bool
		wantErr bool
	}{
		{
			name:   "tls to remote host, hosted",
			mutate: func(m *MailServer) {},
		},
		{
			name:  "tls to remote host, local",
			local: true, mutate: func(m *MailServer) {},
		},
		{
			name: "starttls to remote host, hosted",
			mutate: func(m *MailServer) {
				m.IMAPSecurity = SecuritySTARTTLS
				m.SMTPSecurity = SecuritySTARTTLS
			},
		},
		{
			name:  "plain imap to remote host, local",
			local: true,
			mutate: func(m *MailServer) {
				m.IMAPSecurity = SecurityPlain
			},
			wantErr: true,
		},
		{
			name:  "plain smtp to remote host, local",
			local: true,
			mutate: func(m *MailServer) {
				m.SMTPSecurity = SecurityPlain
			},
			wantErr: true,
		},
		{
			name:  "plain to 127.0.0.1, local",
			local: true,
			mutate: func(m *MailServer) {
				m.IMAPHost, m.IMAPSecurity = "127.0.0.1", SecurityPlain
				m.SMTPHost, m.SMTPSecurity = "127.0.0.1", SecurityPlain
			},
		},
		{
			name:  "plain to localhost, local",
			local: true,
			mutate: func(m *MailServer) {
				m.IMAPHost, m.IMAPSecurity = "localhost", SecurityPlain
				m.SMTPHost, m.SMTPSecurity = "localhost", SecurityPlain
			},
		},
		{
			name:  "plain to ipv6 loopback, local",
			local: true,
			mutate: func(m *MailServer) {
				m.IMAPHost, m.IMAPSecurity = "::1", SecurityPlain
				m.SMTPHost, m.SMTPSecurity = "::1", SecurityPlain
			},
		},
		{
			name:  "plain imap to loopback but plain smtp to remote, local",
			local: true,
			mutate: func(m *MailServer) {
				m.IMAPHost, m.IMAPSecurity = "127.0.0.1", SecurityPlain
				m.SMTPSecurity = SecurityPlain
			},
			wantErr: true,
		},
		{
			name:  "plain to 127.0.0.1, hosted",
			local: false,
			mutate: func(m *MailServer) {
				m.IMAPHost, m.IMAPSecurity = "127.0.0.1", SecurityPlain
				m.SMTPHost, m.SMTPSecurity = "127.0.0.1", SecurityPlain
			},
			wantErr: true,
		},
		{
			name:  "plain imap to localhost, hosted",
			local: false,
			mutate: func(m *MailServer) {
				m.IMAPHost, m.IMAPSecurity = "localhost", SecurityPlain
			},
			wantErr: true,
		},
		{
			name:  "plain to a name that only starts like localhost, local",
			local: true,
			mutate: func(m *MailServer) {
				m.IMAPHost, m.IMAPSecurity = "localhost.example.com", SecurityPlain
			},
			wantErr: true,
		},
		{
			name:  "plain to a non-loopback ip, local",
			local: true,
			mutate: func(m *MailServer) {
				m.IMAPHost, m.IMAPSecurity = "192.168.1.10", SecurityPlain
			},
			wantErr: true,
		},
		{
			name:    "imap port 0",
			mutate:  func(m *MailServer) { m.IMAPPort = 0 },
			wantErr: true,
		},
		{
			name:    "imap port negative",
			mutate:  func(m *MailServer) { m.IMAPPort = -1 },
			wantErr: true,
		},
		{
			name:    "imap port too high",
			mutate:  func(m *MailServer) { m.IMAPPort = 65536 },
			wantErr: true,
		},
		{
			name:    "smtp port 0",
			mutate:  func(m *MailServer) { m.SMTPPort = 0 },
			wantErr: true,
		},
		{
			name:    "smtp port too high",
			mutate:  func(m *MailServer) { m.SMTPPort = 65536 },
			wantErr: true,
		},
		{
			name:   "port 1 is allowed",
			mutate: func(m *MailServer) { m.IMAPPort, m.SMTPPort = 1, 1 },
		},
		{
			name:   "port 65535 is allowed",
			mutate: func(m *MailServer) { m.IMAPPort, m.SMTPPort = 65535, 65535 },
		},
		{
			name:    "empty imap host",
			mutate:  func(m *MailServer) { m.IMAPHost = "" },
			wantErr: true,
		},
		{
			name:    "empty smtp host",
			mutate:  func(m *MailServer) { m.SMTPHost = "" },
			wantErr: true,
		},
		{
			name:    "unknown imap security",
			mutate:  func(m *MailServer) { m.IMAPSecurity = Security("ssl") },
			wantErr: true,
		},
		{
			name:    "empty imap security",
			mutate:  func(m *MailServer) { m.IMAPSecurity = "" },
			wantErr: true,
		},
		{
			name:    "unknown smtp security",
			mutate:  func(m *MailServer) { m.SMTPSecurity = Security("ssl") },
			wantErr: true,
		},
		{
			name:    "empty smtp security",
			mutate:  func(m *MailServer) { m.SMTPSecurity = "" },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validServer()
			tt.mutate(&m)
			err := m.Validate(tt.local)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestMailServerValidateErrorHasNoSecrets(t *testing.T) {
	m := validServer()
	m.Username = "secret-user@example.com"
	m.IMAPHost = ""
	err := m.Validate(false)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-user@example.com")
}

func TestLimitsValidateUndoWindow(t *testing.T) {
	tests := []struct {
		name    string
		auto    time.Duration
		conf    time.Duration
		wantErr bool
	}{
		{name: "both zero", auto: 0, conf: 0},
		{name: "both at the maximum", auto: 300 * time.Second, conf: 300 * time.Second},
		{name: "auto above the maximum", auto: 301 * time.Second, wantErr: true},
		{name: "confirmed above the maximum", conf: 301 * time.Second, wantErr: true},
		{name: "auto just above the maximum", auto: 300*time.Second + time.Nanosecond, wantErr: true},
		{name: "auto negative", auto: -time.Second, wantErr: true},
		{name: "confirmed negative", conf: -time.Second, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := DefaultLimits()
			l.UndoWindowAuto = tt.auto
			l.UndoWindowConfirmed = tt.conf
			err := l.Validate()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestLimitsValidateRejectsNegativeCounts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(l *Limits)
	}{
		{"sends per hour", func(l *Limits) { l.SendsPerHour = -1 }},
		{"sends per day", func(l *Limits) { l.SendsPerDay = -1 }},
		{"recipients per message", func(l *Limits) { l.RecipientsPerMessage = -1 }},
		{"trash per day", func(l *Limits) { l.TrashPerDay = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := DefaultLimits()
			tt.mutate(&l)
			require.Error(t, l.Validate())
		})
	}
}

func TestNewAccountIDUnique(t *testing.T) {
	alphabet := regexp.MustCompile(`^[a-z2-7]{26}$`)
	seen := make(map[AccountID]struct{}, 1000)
	for range 1000 {
		id := NewAccountID()
		require.Len(t, string(id), 26)
		// The id goes into encryption associated data, so it must never
		// contain "|". The lowercase base32 alphabet cannot contain it.
		require.Regexp(t, alphabet, string(id))
		require.NotContains(t, string(id), "|")
		_, dup := seen[id]
		require.False(t, dup, "duplicate id %s", id)
		seen[id] = struct{}{}
	}
	require.Len(t, seen, 1000)
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":        true,
		"127.0.0.1":        true,
		"127.1.2.3":        true,
		"::1":              true,
		"LOCALHOST":        false, // the caller cleans the host first
		"localhost.":       false,
		"imap.localhost":   false,
		"[::1]":            false,
		"10.0.0.1":         false,
		"imap.example.com": false,
		"":                 false,
	} {
		require.Equal(t, want, IsLoopbackHost(host), host)
	}
}

func TestIsFingerprint(t *testing.T) {
	good := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	require.True(t, IsFingerprint(good))
	for _, bad := range []string{
		"", "abc", good[:63], good + "0", "0123456789ABCDEF" + good[16:], " " + good[1:], "g" + good[1:],
	} {
		require.False(t, IsFingerprint(bad), bad)
	}
}
