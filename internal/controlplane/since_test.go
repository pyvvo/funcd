package controlplane_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/store"
)

// sinceLogs records the Since bound each logs read was given.
type sinceLogs struct{ since *time.Time }

func (s sinceLogs) Read(_ context.Context, q logread.Query) ([]logread.Line, error) {
	*s.since = q.Since
	return nil, nil
}

// The since parameter takes only a duration in ADR-0194's grammar or a timestamp in ADR-0196's form, on both
// logs routes; anything else is a 400 whose detail names both forms with an example.
func TestIssue824_SinceTakesOnlyDurationAndTimestamp(t *testing.T) {
	var got time.Time
	fnSrv := newLogsServer(t, sinceLogs{since: &got})
	runSrv, _ := newRunLogsServer(t, func(s store.Store) { seedRun(t, s, "team-a", "run-1", "wf", "trace-1") })
	routes := map[string]struct {
		srv  http.Handler
		path string
	}{
		"function": {fnSrv, logsPath("team-a")},
		"run":      {runSrv, runLogsPath("team-a", "run-1")},
	}
	get := func(srv http.Handler, path, since string) (int, string) {
		rec := do(t, srv, http.MethodGet, path+"?since="+url.QueryEscape(since), devToken, nil)
		return rec.Code, rec.Body.String()
	}

	for _, bad := range []string{
		"500us", "1.5h", "-15m", "15", "yesterday",
		"2026-10-07T22:00:00Z", "2026-10-07T22:00:00.0Z", "2026-10-07T22:00:00.000000Z",
		"2026-10-08T00:00:00.000+02:00", "2026-10-07 22:00:00.000Z",
	} {
		for name, r := range routes {
			code, body := get(r.srv, r.path, bad)
			require.Equal(t, http.StatusBadRequest, code, "%s route, since %q: %s", name, bad, body)
			require.Contains(t, body, "1h30m", "%s route, since %q: the detail names the duration form", name, bad)
			require.Contains(t, body, v1.TimestampForm, "%s route, since %q: the detail names the timestamp form", name, bad)
		}
	}

	for _, good := range []string{"15m", "1h30m", "500ms", "2026-10-07T22:00:00.000Z"} {
		for name, r := range routes {
			code, body := get(r.srv, r.path, good)
			require.Equal(t, http.StatusOK, code, "%s route, since %q: %s", name, good, body)
		}
	}

	before := time.Now()
	code, body := get(fnSrv, logsPath("team-a"), "1h30m")
	require.Equal(t, http.StatusOK, code, body)
	require.WithinRange(t, got, before.Add(-90*time.Minute), time.Now().Add(-90*time.Minute))
	code, body = get(fnSrv, logsPath("team-a"), "2026-10-07T22:00:00.000Z")
	require.Equal(t, http.StatusOK, code, body)
	require.True(t, got.Equal(time.Date(2026, 10, 7, 22, 0, 0, 0, time.UTC)), "since resolved to %s", got)
}
