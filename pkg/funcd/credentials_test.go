package funcd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const libAdminToken = "lib-admin-0123456789abcdef"

// ADR-0171 Decision 7: WithCredentials refuses each malformed list, naming the index and never the token.
func TestWithCredentialsRejects(t *testing.T) {
	t.Parallel()
	const tok, other = "secret-token-aaaa", "secret-token-bbbb"
	dev := []string{"team-a"}
	for name, tc := range map[string]struct {
		creds []funcd.Credential
		index string
	}{
		"none":               {nil, "no credential"},
		"empty-token":        {[]funcd.Credential{{Token: "", Role: "admin"}}, "credentials[0]"},
		"space-in-token":     {[]funcd.Credential{{Token: "secret token", Role: "admin"}}, "credentials[0]"},
		"newline-in-token":   {[]funcd.Credential{{Token: "secret\ntoken", Role: "admin"}}, "credentials[0]"},
		"non-ascii-token":    {[]funcd.Credential{{Token: "secret-tökén", Role: "admin"}}, "credentials[0]"},
		"duplicate":          {[]funcd.Credential{{Token: tok, Role: "admin"}, {Token: other, Role: "viewer", Namespaces: dev}, {Token: tok, Role: "developer", Namespaces: dev}}, "credentials[0] and credentials[2]"},
		"dev-token":          {[]funcd.Credential{{Token: funcd.DevToken, Role: "admin"}}, "credentials[0]"},
		"unknown-role":       {[]funcd.Credential{{Token: tok, Role: "root"}}, "credentials[0]"},
		"admin-namespaces":   {[]funcd.Credential{{Token: tok, Role: "admin", Namespaces: dev}}, "credentials[0]"},
		"developer-unscoped": {[]funcd.Credential{{Token: other, Role: "admin"}, {Token: tok, Role: "developer"}}, "credentials[1]"},
		"viewer-unscoped":    {[]funcd.Credential{{Token: tok, Role: "viewer"}}, "credentials[0]"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := funcd.New(funcd.InMemory(), funcd.WithCredentials(tc.creds...))
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.Contains(t, err.Error(), tc.index)
			require.NotContains(t, err.Error(), "secret", "no token in the error")
			require.NotContains(t, err.Error(), funcd.DevToken, "no token in the error")
		})
	}
}

// scenario: library-credentials-replace-dev-token — WithCredentials after InMemory replaces the dev token: DevToken
// gets 401 and the admin token lists Namespaces.
func TestScenarioLibraryCredentialsReplaceDevToken(t *testing.T) {
	t.Parallel()
	p, err := funcd.New(funcd.InMemory(), funcd.WithCredentials(funcd.Credential{Token: libAdminToken, Role: "admin"}))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})

	devClient, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	_, err = devClient.List(ctx, v1.KindNamespace, "")
	require.Equal(t, fault.Unauthorized, fault.KindOf(err), "the dev token is gone: %v", err)

	admin, err := sdk.New("http://"+p.Addr(), sdk.WithToken(libAdminToken))
	require.NoError(t, err)
	_, err = admin.List(ctx, v1.KindNamespace, "")
	require.NoError(t, err, "the admin lists the cluster-scoped Namespace kind")
}
