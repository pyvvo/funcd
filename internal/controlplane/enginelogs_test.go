package controlplane_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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

// queries records each read the logs routes make.
type queries struct{ got []logread.Query }

func (q *queries) Read(_ context.Context, query logread.Query) ([]logread.Line, error) {
	q.got = append(q.got, query)
	return []logread.Line{}, nil
}

// scenario: engine-logs-no-engine — the logs read of a settings kind's plural, whose objects run no engine, and of a
// plural no Addon registers answer 404; the Addon's own kind reads under its kind, and Lakes under Lake.
func TestScenarioEngineLogsNoEngine(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddon.GVK().APIVersion(), Kind: v1.KindAddon}}
	a.Name = "rauthy"
	a.Spec = v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider", Image: "ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5edcb27399e23f8386fecb03b13f909966d2c3c4708095e2c31ccfe2d", Workloads: []v1.Workload{v1.WorkloadEngine},
		Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080, BasePath: "/auth/v1", Readiness: v1.AddonReadiness{Path: "/auth/v1/ping"}, Settings: []v1.AddonSettings{{Kind: "RauthyPasswordPolicy", Target: "spec.config.provider",
			Function: v1.AddonFunction{Ref: "rauthy-settings"}}}}}}}
	_, err := st.Create(context.Background(), a)
	require.NoError(t, err)
	q := &queries{}
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		"dev": {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"default"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{Store: st, Authorizer: rbac.New(), Credentials: creds, Logs: q})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	get := func(path string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
			srv.URL+"/apis/funcd.io/v1alpha1/namespaces/default/"+path+"/logs", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer dev")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}

	code, body := get("rauthypasswordpolicys/strict")
	require.Equal(t, http.StatusNotFound, code, body)
	require.Contains(t, body, "settings kind")
	code, body = get("gadgets/x")
	require.Equal(t, http.StatusNotFound, code, body)
	code, body = get("identityproviders/login")
	require.Equal(t, http.StatusOK, code, body)
	code, body = get("lakes/sales")
	require.Equal(t, http.StatusOK, code, body)
	var out struct{ Items []json.RawMessage }
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.Equal(t, []logread.Query{
		{Namespace: "default", Kind: "IdentityProvider", Name: "login"},
		{Namespace: "default", Kind: "Lake", Name: "sales"},
	}, q.got, "only the engine kinds' reads reach the reader, each under its kind and name")
}
