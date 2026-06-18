package kv_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/kvstore/memory"
	"github.com/green-0-rabbit/funcd/internal/services/kv"
)

func newFacade(t *testing.T) *kv.Facade {
	t.Helper()
	f, err := kv.NewFacade(kv.FacadeDeps{KV: memory.New(), Authorizer: rbac.New()})
	require.NoError(t, err)
	return f
}

func dev(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{ns}}
}

// scenario: facade-prefixes-by-namespace-and-binding — same key in two namespaces is independent.
func TestScenarioFacadePrefixesByNamespaceAndBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t)

	require.NoError(t, f.Put(ctx, dev("team-a"), "team-a", "cache", "k", []byte("a-val")))
	require.NoError(t, f.Put(ctx, dev("team-b"), "team-b", "cache", "k", []byte("b-val")))

	va, found, err := f.Get(ctx, dev("team-a"), "team-a", "cache", "k")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []byte("a-val"), va, "team-a sees its own value, not team-b's")

	vb, _, err := f.Get(ctx, dev("team-b"), "team-b", "cache", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("b-val"), vb)

	// List returns the caller's key space (tenant prefix stripped).
	keys, err := f.List(ctx, dev("team-a"), "team-a", "cache", "")
	require.NoError(t, err)
	require.Equal(t, []string{"k"}, keys, "returned keys are stripped of <ns>/<binding>/")
}

// scenario: facade-authorizes-each-access — an identity not permitted in the namespace is denied.
func TestScenarioFacadeAuthorizesEachAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFacade(t)

	// A developer scoped to team-a accessing team-b → Forbidden, before any store touch.
	err := f.Put(ctx, dev("team-a"), "team-b", "cache", "k", []byte("x"))
	require.Error(t, err)
	require.Equal(t, fault.Forbidden, fault.KindOf(err))

	_, _, err = f.Get(ctx, dev("team-a"), "team-b", "cache", "k")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))

	// A viewer may read but not write in its own namespace.
	viewer := auth.Identity{Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}}
	require.Equal(t, fault.Forbidden, fault.KindOf(f.Put(ctx, viewer, "team-a", "cache", "k", []byte("x"))))
}
