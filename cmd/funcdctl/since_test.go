package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/pkg/sdk"
)

// funcdctl logs and workflow logs send a duration as typed and any RFC3339 time in ADR-0196's form, and refuse
// anything else on the client with a message naming both forms.
func TestIssue824_LogsSinceConvertsToWireForm(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.URL.Query().Get("since"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)

	verbs := map[string][]string{
		"logs":          {"logs", "fn", "-n", "team-a"},
		"workflow logs": {"workflow", "logs", "run-1", "-n", "team-a"},
	}
	for name, verb := range verbs {
		for in, want := range map[string]string{
			"15m":                       "15m",
			"1h30m":                     "1h30m",
			"2026-10-07T22:00:00Z":      "2026-10-07T22:00:00.000Z",
			"2026-10-08T00:00:00+02:00": "2026-10-07T22:00:00.000Z",
			"2026-10-07T22:00:00.1239Z": "2026-10-07T22:00:00.123Z",
		} {
			sent = nil
			var out bytes.Buffer
			require.NoError(t, execCLI(&out, c, append(verb, "--since", in)...), "%s --since %s", name, in)
			require.Equal(t, []string{want}, sent, "%s --since %s", name, in)
		}

		for _, bad := range []string{"1.5h", "500us", "-15m", "yesterday"} {
			sent = nil
			var out bytes.Buffer
			err := execCLI(&out, c, append(verb, "--since", bad)...)
			require.Error(t, err, "%s --since %s", name, bad)
			require.Contains(t, err.Error(), "1h30m", "%s --since %s", name, bad)
			require.Contains(t, err.Error(), "2026-10-07T22:00:00.000Z", "%s --since %s", name, bad)
			require.Empty(t, sent, "%s --since %s is refused before any request", name, bad)
		}
	}
}
