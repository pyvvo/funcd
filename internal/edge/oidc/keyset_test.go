package oidc

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/testkit/oidcprovider"
)

func newTestKeySet(t *testing.T) (*keySet, *oidcprovider.Provider, *clock.Manual) {
	t.Helper()
	idp := oidcprovider.New(t, oidcprovider.Options{})
	c := clock.NewManual(time.Now())
	return newKeySet(&http.Client{Transport: idp.Transport()}, idp.Issuer()+"/jwks", c), idp, c
}

// ADR-0335 Decision 7: keys older than a day are fetched again by the next call.
func TestKeySetFetchesKeysOlderThanADay(t *testing.T) {
	t.Parallel()
	ks, idp, c := newTestKeySet(t)
	token := idp.Token(oidcprovider.RS256, idp.Claims("app", ""))
	_, err := ks.VerifySignature(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, 1, idp.Calls(oidcprovider.OpKeys))
	c.Advance(keyMaxAge - time.Second)
	_, err = ks.VerifySignature(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, 1, idp.Calls(oidcprovider.OpKeys), "kept within a day")
	c.Advance(2 * time.Second)
	_, err = ks.VerifySignature(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, 2, idp.Calls(oidcprovider.OpKeys), "fetched again past a day")
}

// ADR-0335 Decision 7: a token signed with a new key passes within a minute, and no sooner fetch serves it.
func TestKeySetPassesANewKeyWithinAMinute(t *testing.T) {
	t.Parallel()
	ks, idp, c := newTestKeySet(t)
	_, err := ks.VerifySignature(context.Background(), idp.Token(oidcprovider.RS256, idp.Claims("app", "")))
	require.NoError(t, err)
	idp.AddKey(oidcprovider.RS256)
	fresh := idp.Token(oidcprovider.RS256, idp.Claims("app", ""))
	_, err = ks.VerifySignature(context.Background(), fresh)
	require.Error(t, err, "an unknown kid within a minute of the last fetch fetches nothing")
	require.Equal(t, 1, idp.Calls(oidcprovider.OpKeys))
	c.Advance(keyRefetchInterval)
	_, err = ks.VerifySignature(context.Background(), fresh)
	require.NoError(t, err, "a minute on, the unknown kid fetches the keys")
	require.Equal(t, 2, idp.Calls(oidcprovider.OpKeys))
	_, err = ks.VerifySignature(context.Background(), idp.Token(oidcprovider.EdDSA, idp.Claims("app", "")))
	require.NoError(t, err, "EdDSA verifies")
}

// ADR-0335 Decision 7: concurrent calls with unknown kids fetch the keys once.
func TestKeySetConcurrentUnknownKidsFetchOnce(t *testing.T) {
	t.Parallel()
	ks, idp, c := newTestKeySet(t)
	_, err := ks.VerifySignature(context.Background(), idp.Token(oidcprovider.RS256, idp.Claims("app", "")))
	require.NoError(t, err)
	idp.AddKey(oidcprovider.RS256)
	fresh := idp.Token(oidcprovider.RS256, idp.Claims("app", ""))
	c.Advance(keyRefetchInterval)
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Go(func() { _, errs[i] = ks.VerifySignature(context.Background(), fresh) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 2, idp.Calls(oidcprovider.OpKeys), "one fetch served every waiting call")
}

// A token signed with a key the provider does not publish, under a kid it does, fails.
func TestKeySetRefusesAForeignKey(t *testing.T) {
	t.Parallel()
	ks, idp, _ := newTestKeySet(t)
	_, err := ks.VerifySignature(context.Background(), idp.TokenWithForeignKey(idp.Claims("app", "")))
	require.Error(t, err)
}
