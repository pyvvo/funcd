package apitoken_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/apitoken"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func declare(t *testing.T, st store.Store, name, role string, nss ...v1.NamespaceName) {
	t.Helper()
	tok := &v1.APIToken{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAPIToken.GVK().APIVersion(), Kind: v1.KindAPIToken}}
	tok.Name, tok.Spec.Role, tok.Spec.Namespaces = v1.ObjectName(name), role, nss
	_, err := st.Create(context.Background(), tok)
	require.NoError(t, err)
}

func TestAnAPITokenWorksUntilItExpiresOrIsMintedAgain(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	declare(t, st, "ci", "developer", "shop")
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m, creds := apitoken.NewMinter(st, clock), apitoken.NewCredentials(st, clock)

	_, _, err := m.MintAPIToken(ctx, "nope", 0)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	_, _, err = m.MintAPIToken(ctx, "ci", 366*24*time.Hour)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "at most a year")

	token, expires, err := m.MintAPIToken(ctx, "ci", time.Hour)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(token, "funcd.ci."))
	require.Equal(t, now.Add(time.Hour), expires)
	obj, err := st.Get(ctx, v1.KindAPIToken.GVK(), "", "ci")
	require.NoError(t, err)
	require.NotContains(t, obj.(*v1.APIToken).Status.TokenHash, strings.TrimPrefix(token, "funcd.ci."), "only a hash is kept")

	id, err := creds.Lookup(ctx, token)
	require.NoError(t, err)
	require.Equal(t, auth.Identity{Subject: "apitoken/ci", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"shop"}}, id)
	for _, bad := range []string{token + "x", "funcd.ci", "funcd..secret", "ci.secret", "funcd.other." + strings.TrimPrefix(token, "funcd.ci.")} {
		_, err := creds.Lookup(ctx, bad)
		require.Equal(t, fault.Unauthorized, fault.KindOf(err), bad)
	}

	again, _, err := m.MintAPIToken(ctx, "ci", 0)
	require.NoError(t, err)
	_, err = creds.Lookup(ctx, token)
	require.Equal(t, fault.Unauthorized, fault.KindOf(err), "a token minted again ends the one before")
	_, err = creds.Lookup(ctx, again)
	require.NoError(t, err)

	now = now.Add(31 * 24 * time.Hour)
	_, err = creds.Lookup(ctx, again)
	require.ErrorContains(t, err, "expired")
	require.Equal(t, fault.Unauthorized, fault.KindOf(err))
}

func TestTheConfigsCredentialsComeBeforeTheStoredTokens(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	declare(t, st, "ops", "admin")
	token, _, err := apitoken.NewMinter(st, nil).MintAPIToken(ctx, "ops", 0)
	require.NoError(t, err)
	chain := middleware.ChainCredentials(
		middleware.NewStaticCredentials(map[string]auth.Identity{"from-config": {Subject: "credentials[0]", Role: auth.RoleViewer}}),
		apitoken.NewCredentials(st, nil))
	id, err := chain.Lookup(ctx, "from-config")
	require.NoError(t, err)
	require.Equal(t, "credentials[0]", id.Subject)
	id, err = chain.Lookup(ctx, token)
	require.NoError(t, err)
	require.Equal(t, auth.RoleAdmin, id.Role)
	_, err = chain.Lookup(ctx, "neither")
	require.Equal(t, fault.Unauthorized, fault.KindOf(err))
}
