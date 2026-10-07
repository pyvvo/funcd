package controlplane_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const adminToken = "admin-secret"

// fakeLogs is a LogQuerier returning one line for whatever (authorized) namespace it is asked.
type fakeLogs struct{}

func (fakeLogs) Read(_ context.Context, q logread.Query) ([]logread.Line, error) {
	return []logread.Line{{
		Time: v1.NewTimestamp(time.Unix(0, 1)), Severity: "INFO", Body: "hello",
		Namespace: q.Namespace, Function: q.Function,
	}}, nil
}

func newLogsServer(t *testing.T, q controlplane.LogQuerier) http.Handler {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		adminToken: {Subject: "ops", Role: auth.RoleAdmin},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       store.New(memory.New()),
		Authorizer:  rbac.New(),
		Credentials: creds,
		Logs:        q,
	})
	require.NoError(t, err)
	return h
}

func logsPath(ns string) string {
	return "/apis/funcd.io/v1alpha1/namespaces/" + ns + "/functions/fn/logs"
}

// scenario: tenant-scoped-own-namespace — a developer bound to team-a reads team-a (200) but not team-b (403).
func TestScenarioTenantScopedOwnNamespace(t *testing.T) {
	srv := newLogsServer(t, fakeLogs{})
	ok := do(t, srv, http.MethodGet, logsPath("team-a"), devToken, nil)
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	denied := do(t, srv, http.MethodGet, logsPath("team-b"), devToken, nil)
	require.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
}

// scenario: operator-sees-any-namespace — an admin (operator) reads any namespace's logs (200).
func TestScenarioOperatorSeesAnyNamespace(t *testing.T) {
	srv := newLogsServer(t, fakeLogs{})
	for _, ns := range []string{"team-a", "team-b", "default"} {
		rec := do(t, srv, http.MethodGet, logsPath(ns), adminToken, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
}

// no-logs-route-when-unset: with Deps.Logs nil the route is absent (404), not a crash.
func TestLogsRouteAbsentWhenUnset(t *testing.T) {
	srv := newLogsServer(t, nil)
	rec := do(t, srv, http.MethodGet, logsPath("team-a"), adminToken, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
