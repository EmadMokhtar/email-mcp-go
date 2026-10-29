package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
)

func TestPermissionLink(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw")
	f.addTenant("other")
	theirs := f.addAccount("other", "them@example.com", offlineSettings(), "pw")
	backends := &fakeFactory{backend: &fakeBackend{}}
	links := &fakeLinks{}
	svc := f.newService(backends, links)

	t.Run("an account of the tenant", func(t *testing.T) {
		link, err := svc.PermissionLink(f.ctx(), a.ID, account.AreaMail, account.MailDelete)

		require.NoError(t, err)
		require.Equal(t, "https://mcp.test/settings/"+string(a.ID)+"?area=mail&level=5", link)
		require.Equal(t, []settingsCall{{ID: a.ID, Area: account.AreaMail, Level: account.MailDelete}}, links.settingsCalls())
	})

	t.Run("nothing changes", func(t *testing.T) {
		got := f.reload(a)
		require.Equal(t, account.MailSend, got.Level(account.AreaMail))
		require.True(t, got.ConfirmSends)
		require.True(t, got.ConfirmDeletes)
		require.Equal(t, a.Limits, got.Limits)
		require.Zero(t, backends.calls(), "a link needs no mail server")
	})

	t.Run("an account of another tenant", func(t *testing.T) {
		_, err := svc.PermissionLink(f.ctx(), theirs.ID, account.AreaMail, account.MailDelete)
		require.ErrorIs(t, err, ErrAccountNotFound)
	})

	t.Run("no principal", func(t *testing.T) {
		_, err := svc.PermissionLink(context.Background(), a.ID, account.AreaMail, account.MailDelete)
		require.ErrorIs(t, err, ErrNoPrincipal)
	})

	t.Run("a level that does not exist", func(t *testing.T) {
		for _, level := range []account.Level{-1, 6} {
			_, err := svc.PermissionLink(f.ctx(), a.ID, account.AreaMail, level)
			require.ErrorIs(t, err, ErrInvalidRequest)
		}
	})

	t.Run("an area that does not exist", func(t *testing.T) {
		_, err := svc.PermissionLink(f.ctx(), a.ID, account.Area("files"), account.MailRead)
		require.ErrorIs(t, err, ErrInvalidRequest)
	})

	t.Run("no link", func(t *testing.T) {
		for name, l := range map[string]Links{"links fail": &fakeLinks{err: errors.New("no base address")}, "no links set": nil} {
			t.Run(name, func(t *testing.T) {
				_, err := f.newService(backends, l).PermissionLink(f.ctx(), a.ID, account.AreaMail, account.MailDelete)
				require.Error(t, err)
			})
		}
	})
}

func TestConnectLink(t *testing.T) {
	f := newFixture(t)
	svc := f.newService(&fakeFactory{}, &fakeLinks{})

	link, err := svc.ConnectLink(f.ctx())
	require.NoError(t, err)
	require.Equal(t, "https://mcp.test/connect", link)

	_, err = svc.ConnectLink(context.Background())
	require.ErrorIs(t, err, ErrNoPrincipal)

	_, err = f.newService(&fakeFactory{}, nil).ConnectLink(f.ctx())
	require.Error(t, err)
}
