package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EmadMokhtar/email-mcp-go/internal/domain/account"
	"github.com/EmadMokhtar/email-mcp-go/internal/policy"
)

func TestPrincipalRoundTrip(t *testing.T) {
	in := Principal{Tenant: "t1", Scopes: policy.Scopes{"email:read"}, Client: "client 1"}

	got, err := PrincipalFrom(WithPrincipal(context.Background(), in))

	require.NoError(t, err)
	require.Equal(t, in, got)
}

func TestPrincipalKeepsNilScopesNil(t *testing.T) {
	// A nil Scopes means "no limit". An empty one means "allow nothing". The
	// context must not turn one into the other.
	got, err := PrincipalFrom(WithPrincipal(context.Background(), Principal{Tenant: "t1"}))
	require.NoError(t, err)
	require.Nil(t, got.Scopes)

	got, err = PrincipalFrom(WithPrincipal(context.Background(), Principal{Tenant: "t1", Scopes: policy.Scopes{}}))
	require.NoError(t, err)
	require.NotNil(t, got.Scopes)
	require.Empty(t, got.Scopes)
}

func TestPrincipalMissing(t *testing.T) {
	_, err := PrincipalFrom(context.Background())
	require.ErrorIs(t, err, ErrNoPrincipal)
}

func TestPrincipalWithoutTenantCountsAsMissing(t *testing.T) {
	_, err := PrincipalFrom(WithPrincipal(context.Background(), Principal{Client: "client"}))
	require.ErrorIs(t, err, ErrNoPrincipal)
}

// Every use case must refuse a request without a principal, and must do so
// before it reaches a backend.
func TestNoPrincipalRefused(t *testing.T) {
	f := newFixture(t)
	a := f.addAccount(f.tenant, "me@example.com", offlineSettings(), "pw")
	backends := &fakeFactory{backend: &fakeBackend{}}
	svc := f.newService(backends, &fakeLinks{})

	noTenant := WithPrincipal(context.Background(), Principal{Client: "no tenant"})
	contexts := map[string]context.Context{
		"no principal": context.Background(),
		"no tenant":    noTenant,
	}
	id := a.ID
	uses := map[string]func(ctx context.Context) error{
		"ListAccounts": func(ctx context.Context) error { _, err := svc.ListAccounts(ctx); return err },
		"ListFolders":  func(ctx context.Context) error { _, err := svc.ListFolders(ctx, id); return err },
		"ListTags":     func(ctx context.Context) error { _, _, err := svc.ListTags(ctx, id); return err },
		"Search":       func(ctx context.Context) error { _, err := svc.Search(ctx, id, mailQuery(), ""); return err },
		"NewMessages":  func(ctx context.Context) error { _, _, _, err := svc.NewMessages(ctx, id, "@inbox", ""); return err },
		"GetMessage":   func(ctx context.Context) error { _, err := svc.GetMessage(ctx, id, "ref", bodyOptions()); return err },
		"GetAttachment": func(ctx context.Context) error {
			_, err := svc.GetAttachment(ctx, id, "ref", "2")
			return err
		},
	}
	for cname, ctx := range contexts {
		for uname, use := range uses {
			t.Run(cname+"/"+uname, func(t *testing.T) {
				require.ErrorIs(t, use(ctx), ErrNoPrincipal)
			})
		}
	}
	require.Zero(t, backends.calls(), "a request without a principal must not reach a backend")
}

// missingAccountID returns an account id that no tenant has.
func missingAccountID() account.AccountID { return account.NewAccountID() }
