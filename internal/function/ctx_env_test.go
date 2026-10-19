package function_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/function"
)

// scenario: ctx-pooled-member — two Functions in one pool share its config and secrets with their key lists (ADR-0245),
// in the pool's shared env, and each member's manifest row carries its own catalog token.
func TestScenarioCtxPooledMember(t *testing.T) {
	t.Parallel()
	proxies := cataloggw.NewManager("", cataloggw.NewCatalogKeys(nil, nil), nil, nil)
	t.Cleanup(proxies.Shutdown)
	_, _, err := proxies.Listen("default", "lake", 0)
	require.NoError(t, err)
	h := newShimHarness(t, http.StatusOK, false, withNodePool, func(d *function.Deps) {
		d.CatalogProxies = proxies
		d.CatalogMaster = []byte("ctx-pooled-member-master-secret!")
		d.PoolManifestDir = filepath.Join(t.TempDir(), "pool")
		d.Secrets = blackboxResolver{env: map[string]string{"STRIPE_KEY": "sk"}}
	})
	storeReadyCatalog(t, h.st)
	h.configMap(t, "app")
	for _, n := range []string{"a", "b"} {
		h.create(t, n, func(fn *v1.Function) {
			bindLake(fn)
			fn.Spec.Config = []v1.ObjectName{"app"}
			fn.Spec.Secrets = []v1.ObjectName{"api-keys"}
			fn.Spec.Pooling.Worker = "w1"
		})
	}
	h.pools(t, "a", "b")
	require.Equal(t, h.getFn(t, "a").Status.Pool, h.getFn(t, "b").Status.Pool, "both members share one pool")
	spec, ok := h.rt.specFor(h.poolWorkerOf(t, "a"))
	require.True(t, ok)
	require.Equal(t, "APP_MODE", spec.Env["FUNCD_CONFIG_KEYS"])
	require.Equal(t, "STRIPE_KEY", spec.Env["FUNCD_SECRET_KEYS"])
	require.Equal(t, "test", spec.Env["APP_MODE"])
	require.Equal(t, "sk", spec.Env["STRIPE_KEY"])
	require.NotEmpty(t, spec.Env["FUNCD_CATALOG_LAKE_URL"])
	require.NotContains(t, spec.Env, "FUNCD_CATALOG_LAKE_TOKEN", "no member's token is in the shared env")

	var rows []struct {
		Name string            `json:"name"`
		Env  map[string]string `json:"env"`
	}
	require.NoError(t, json.Unmarshal([]byte(h.poolManifest(t, h.poolWorkerOf(t, "a"))), &rows))
	require.Len(t, rows, 2)
	tokens := map[string]string{}
	for _, r := range rows {
		require.NotEmpty(t, r.Env["FUNCD_CATALOG_LAKE_TOKEN"], r.Name)
		tokens[r.Name] = r.Env["FUNCD_CATALOG_LAKE_TOKEN"]
	}
	require.NotEqual(t, tokens["a"], tokens["b"], "each member gets its own catalog token")
}
