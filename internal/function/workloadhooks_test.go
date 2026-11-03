package function_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	"github.com/pyvvo/funcd/internal/runtime"
)

// withS3Gateway enables the S3 gateway, issuing each Function a key pair named after it.
func withS3Gateway(d *function.Deps) {
	d.S3Gateway = function.S3GatewayInjection{Enabled: true, ListenAddr: "127.0.0.1:9000", Derive: func(_ v1.Kind, _, name string) (string, string) {
		return "AK-" + name, "SK-" + name
	}}
}

// storeBucket stores Bucket k with prefix raw, which bindBucket binds.
func storeBucket(t *testing.T, h *shimHarness) {
	t.Helper()
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
}

func bindBucket(fn *v1.Function) {
	fn.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "raw", Bucket: "k", Prefix: "raw"}}
}

// memberEnvIn is the env member gets in its pool worker: the worker's env with the member's manifest row merged over
// it, as each pool host merges it (ADR-0306 Decision 4).
func (h *shimHarness) memberEnvIn(t *testing.T, member string) map[string]string {
	t.Helper()
	pool := h.poolWorkerOf(t, member)
	spec, ok := h.rt.specFor(pool)
	require.True(t, ok)
	var rows []struct {
		Name string            `json:"name"`
		Env  map[string]string `json:"env"`
	}
	require.NoError(t, json.Unmarshal([]byte(h.poolManifest(t, pool)), &rows))
	for _, r := range rows {
		if r.Name == member {
			env := maps.Clone(spec.Env)
			maps.Copy(env, r.Env)
			return env
		}
	}
	require.Failf(t, "no manifest row", "pool %s holds no row for %s", pool, member)
	return nil
}

// scenario: workload-hooks-hold-in-order
// The first hook that holds a Function stops its pass with that hook's reason, phase and requeue: a missing ConfigMap
// fails it before its Lake, which is not Ready, is waited for (ADR-0306 Decision 2).
func TestWorkloadHooks_HoldInListOrder(t *testing.T) {
	t.Parallel()
	const poll = 3 * time.Second
	h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) { d.ReferentPollInterval = poll })
	storeReadyLake(t, h.st)
	obj, err := h.st.Get(context.Background(), v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	lake := obj.(*v1.Lake)
	lake.Status.Phase = v1.PhasePending
	_, err = h.st.Update(context.Background(), lake)
	require.NoError(t, err)
	h.create(t, "reader", func(fn *v1.Function) {
		bindLake(fn)
		fn.Spec.Bindings.Config = []v1.ObjectName{"app"}
	})

	res := h.reconcile(t, "reader")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "reader").Status.Phase)
	h.requireCondition(t, "reader", "RevisionReady", v1.ConditionFalse, "ConfigResolveFailed")
	require.Equal(t, poll, res.RequeueAfter, "a ConfigMap applied later is found by a requeue")

	h.configMap(t, "app")
	res = h.reconcile(t, "reader")
	require.Equal(t, v1.PhasePending, h.getFn(t, "reader").Status.Phase)
	h.requireCondition(t, "reader", "RevisionReady", v1.ConditionFalse, "LakeNotReady")
	require.Equal(t, poll, res.RequeueAfter)
	creates, _ := h.rt.counts()
	require.Zero(t, creates, "no worker starts while a hook holds the Function")
}

// scenario: a-worker-gets-each-providers-env
// One pass gives a solo worker what the hook of each provider it binds gives it: the config keys with their list, the
// S3 gateway's region and endpoint and its own key pair, its Lake's URL and its lake token.
func TestWorkloadHooks_ASoloWorkerGetsEachProvidersEnv(t *testing.T) {
	t.Parallel()
	proxies := lakegw.NewManager("", lakegw.NewLakeKeys(nil, nil), nil, nil)
	t.Cleanup(proxies.Shutdown)
	_, _, err := proxies.Listen("default", "lake", 0)
	require.NoError(t, err)
	master := []byte("workload-hooks-master-secret")
	h := newShimHarness(t, http.StatusOK, false, withS3Gateway, func(d *function.Deps) {
		d.LakeProxies = proxies
		d.LakeMaster = master
	})
	storeReadyLake(t, h.st)
	storeBucket(t, h)
	h.configMap(t, "app")
	h.create(t, "s", func(fn *v1.Function) {
		bindLake(fn)
		bindBucket(fn)
		fn.Spec.Bindings.Config = []v1.ObjectName{"app"}
	})
	h.reconcile(t, "s")

	spec, ok := h.rt.specFor("s")
	require.True(t, ok)
	url, ok := proxies.ProxyURL("default", "lake")
	require.True(t, ok)
	token, err := lakegw.DeriveLakeToken(master, "default", "s")
	require.NoError(t, err)
	for k, want := range map[string]string{
		"APP_MODE":              "test",
		"FUNCD_CONFIG_KEYS":     "APP_MODE",
		"AWS_REGION":            "us-east-1",
		"AWS_ENDPOINT_URL_S3":   "http://127.0.0.1:9000",
		"AWS_ACCESS_KEY_ID":     "AK-s",
		"AWS_SECRET_ACCESS_KEY": "SK-s",
		"FUNCD_LAKE_LAKE_URL":   url,
		"FUNCD_LAKE_LAKE_TOKEN": token,
	} {
		require.Equal(t, want, spec.Env[k], k)
	}
}

// scenario: issued-keys-win
// A bound ConfigMap moves a provider's shared default (AWS_REGION) but never replaces the key pair issued with a
// worker, in a solo worker and a pool member alike (ADR-0306 Decision 3).
func TestWorkloadHooks_IssuedKeysWinOverConfig(t *testing.T) {
	t.Parallel()
	for name, pooled := range map[string]bool{"solo": false, "pooled": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withNodePool, withS3Gateway, func(d *function.Deps) {
				d.PoolManifestDir = filepath.Join(t.TempDir(), "pool")
			})
			storeBucket(t, h)
			h.createObj(t, v1.KindConfigMap, "app", func(o v1.Object) {
				o.(*v1.ConfigMap).Spec.Data = map[string]string{"AWS_REGION": "eu-west-3", "AWS_ACCESS_KEY_ID": "AK-config"}
			})
			h.create(t, "f", func(fn *v1.Function) {
				bindBucket(fn)
				fn.Spec.Bindings.Config = []v1.ObjectName{"app"}
				if pooled {
					fn.Spec.Pooling.Worker = "w1"
				}
			})
			h.reconcile(t, "f")

			var env map[string]string
			if pooled {
				env = h.memberEnvIn(t, "f")
			} else {
				spec, ok := h.rt.specFor("f")
				require.True(t, ok)
				env = spec.Env
			}
			require.Equal(t, "eu-west-3", env["AWS_REGION"], "the bound config key moves the S3 gateway's default")
			require.Equal(t, "AK-f", env["AWS_ACCESS_KEY_ID"], "the issued key wins over the config key")
			require.Equal(t, "SK-f", env["AWS_SECRET_ACCESS_KEY"])
		})
	}
}

// In container mode a worker dials its local API at the in-container path, where its host socket is bound in (ADR-0032),
// from the env builder process mode shares (ADR-0306 Decision 6).
func TestWorkloadHooks_AContainerWorkerDialsItsSocketInside(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, func(d *function.Deps) {
		d.EndpointMode = function.EndpointNetnsFixedPort
		d.ImageFor = func(rt string) string { return "funcd/runtime-" + rt + ":latest" }
		d.InvokeSockets = &brokenSockets{}
	})
	h.createFn(t, "boxed")
	h.reconcile(t, "boxed")

	spec, ok := h.rt.specFor("boxed")
	require.True(t, ok)
	require.Equal(t, "/run/funcd/invoke.sock", spec.Env["FUNCD_INVOKE_SOCKET"])
	require.Contains(t, spec.Mounts, runtime.Mount{Source: "/run/test/boxed.sock", Target: "/run/funcd/invoke.sock"})
}
