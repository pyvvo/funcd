package controlplane_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

type fixedStatus struct {
	st  runner.Status
	err error
}

func (s fixedStatus) Status(context.Context) (runner.Status, error) { return s.st, s.err }

// GET …/platformbackup authorizes get on WorkerNode, so a developer gets 403; without a backup stream it reads
// enabled: false; a failed listing of the target is 503.
func TestPlatformBackupRoute(t *testing.T) {
	t.Parallel()
	get := func(t *testing.T, s controlplane.BackupStatuser, token string) (int, runner.Status) {
		t.Helper()
		creds := middleware.NewStaticCredentials(map[string]auth.Identity{
			"admin": {Subject: "admin", Role: auth.RoleAdmin},
			"dev":   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"default"}},
		})
		h, err := controlplane.NewServer(controlplane.Deps{Store: store.New(memory.New()), Authorizer: rbac.New(),
			Credentials: creds, Backup: s})
		require.NoError(t, err)
		srv := httptest.NewServer(h)
		defer srv.Close()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/apis/funcd.io/v1alpha1/platformbackup", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var st runner.Status
		if resp.StatusCode == http.StatusOK {
			require.NoError(t, json.Unmarshal(body, &st))
		}
		return resp.StatusCode, st
	}

	code, st := get(t, nil, "admin")
	require.Equal(t, http.StatusOK, code)
	require.False(t, st.Enabled)
	code, st = get(t, fixedStatus{st: runner.Status{Enabled: true, RPORisk: true}}, "admin")
	require.Equal(t, http.StatusOK, code)
	require.True(t, st.Enabled && st.RPORisk)
	code, _ = get(t, fixedStatus{err: fault.Unavailablef("test", "list the backup target")}, "admin")
	require.Equal(t, http.StatusServiceUnavailable, code)
	code, _ = get(t, fixedStatus{}, "dev")
	require.Equal(t, http.StatusForbidden, code)
}
