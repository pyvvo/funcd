package oidc

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// ADR-0335 Decision 7: without addon the back channel reaches https at globally reachable addresses only, checked at
// each connection, redirects included.
func TestBackChannelReachesOnlyGloballyReachableHTTPS(t *testing.T) {
	t.Parallel()
	c := newBackChannel(nil).client("")
	for _, u := range []string{"http://93.184.216.34/x", "https://127.0.0.1:1/x", "https://10.0.0.5/x", "https://[::1]:1/x",
		"https://169.254.169.254/latest"} {
		_, err := get(context.Background(), c, u)
		require.Error(t, err, u)
		require.Contains(t, err.Error(), "oidc.backChannel", u)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") }))
	t.Cleanup(plain.Close)
	redirect := httptest.NewTLSServer(http.RedirectHandler(plain.URL, http.StatusFound))
	t.Cleanup(redirect.Close)
	_, err := get(context.Background(), &http.Client{Transport: httpsOnly{next: redirect.Client().Transport}}, redirect.URL)
	require.Error(t, err, "a redirect to http is refused")
	require.Contains(t, err.Error(), "is not https")
	require.Error(t, dialGlobal("tcp", "127.0.0.1:443", nil), "every connection's address is checked, a redirect's too")
	require.NoError(t, dialGlobal("tcp", "93.184.216.34:443", nil))
}

// ADR-0335 Decision 7: a call reads at most 1 MiB.
func TestBackChannelCapsAnAnswerAt1MiB(t *testing.T) {
	t.Parallel()
	for _, n := range []int{maxBackChannelBytes, maxBackChannelBytes + 1} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", n))
		}))
		t.Cleanup(srv.Close)
		body, err := get(context.Background(), &http.Client{Transport: capped{next: srv.Client().Transport}}, srv.URL)
		if n == maxBackChannelBytes {
			require.NoError(t, err)
			require.Len(t, body, n)
			continue
		}
		require.Error(t, err, "an answer over 1 MiB")
		require.Equal(t, fault.Unavailable, fault.KindOf(err))
	}
	require.Equal(t, backChannelTimeout, newBackChannel(nil).global.Timeout)
	require.Equal(t, backChannelTimeout, newBackChannel(nil).engine.Timeout)
}

// ADR-0336 Decision 3: a URI the provider names after its public host is sent to the engine's own address.
func TestRebaseSendsToTheEngine(t *testing.T) {
	t.Parallel()
	got, err := rebase("https://login.example.com/auth/v1/register/dyn%24a?x=1", "10.88.0.5:8080")
	require.NoError(t, err)
	require.Equal(t, "http://10.88.0.5:8080/auth/v1/register/dyn%24a?x=1", got)
	got, err = rebase("https://login.example.com/x", "")
	require.NoError(t, err)
	require.Equal(t, "https://login.example.com/x", got)
}

// ADR-0335 Decision 4: the first path starts with / but not //, /\ or /.auth/; else /.
func TestFirstPathKeepsOnlyAPathOfItsHost(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/orders/7?tab=items":    "/orders/7?tab=items",
		"/a%20b":                 "/a%20b",
		"//evil.example.com/x":   "/",
		`/\evil.example.com`:     "/",
		"/.auth/callback?code=x": "/",
		"/.auth":                 "/",
		"https://evil.example":   "/",
		"":                       "/",
		"/.authors":              "/.authors",
	} {
		require.Equal(t, want, firstPath(in), in)
	}
}
