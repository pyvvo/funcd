package function_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/pooling"
)

// createObj stores a namespaced object of kind k in default/rg1 after change fills it.
func (h *shimHarness) createObj(t *testing.T, k v1.Kind, name string, change func(v1.Object)) {
	t.Helper()
	obj, ok := v1.NewObject(k)
	require.True(t, ok)
	change(obj)
	switch o := obj.(type) {
	case *v1.KVStore:
		o.Name, o.Namespace, o.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	case *v1.Bucket:
		o.Name, o.Namespace, o.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	case *v1.RolesAssignment:
		o.Name, o.Namespace, o.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	case *v1.EgressPolicy:
		o.Name, o.Namespace, o.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	case *v1.Policy:
		o.Name, o.Namespace, o.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	}
	_, err := h.st.Create(context.Background(), obj)
	require.NoError(t, err)
}

// pools reconciles names in order and returns each one's status.pool.
func (h *shimHarness) pools(t *testing.T, names ...string) map[string]string {
	t.Helper()
	for _, n := range names {
		h.reconcile(t, n)
	}
	out := map[string]string{}
	for _, n := range names {
		out[n] = h.getFn(t, n).Status.Pool
	}
	return out
}

// scenario: pool-splits-by-access — a and b declare one worker id on nodejs22 and only a binds table t: both are
// Ready in two pool workers with distinct status.pool and no error condition; a solo Function's status.pool is empty.
func TestScenarioPoolSplitsByAccess(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
	h.createObj(t, v1.KindKVStore, "s", func(o v1.Object) { o.(*v1.KVStore).Spec.Tables = []v1.KVTable{{Name: "t"}} })
	h.create(t, "a", func(fn *v1.Function) {
		fn.Spec.Pooling.Worker = "agents"
		fn.Spec.KV = []v1.FunctionKV{{Alias: "t", Store: "s", Table: "t"}}
	})
	h.create(t, "b", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
	h.create(t, "solo", func(*v1.Function) {})
	got := h.pools(t, "a", "b", "solo")
	require.NotEqual(t, got["a"], got["b"])
	for _, n := range []string{"a", "b"} {
		require.Equal(t, v1.PhaseReady, h.getFn(t, n).Status.Phase, n)
		require.Empty(t, h.condition(t, n, "Ready").Reason, n)
		key, ok := pooling.ParsePool("default", got[n])
		require.True(t, ok, n)
		require.Equal(t, "nodejs22", key.Runtime)
		require.Equal(t, "agents", key.Worker)
	}
	require.Empty(t, got["solo"], "a solo Function has no pool")
	require.Contains(t, h.poolManifest(t, poolOf("agents")), `"b"`)
	require.NotContains(t, h.poolManifest(t, poolOf("agents")), `"a"`)
}

// scenario: pool-splits-by-grant — a grant outside the spec that names b moves b to its own pool, so a's pool no longer
// serves b (a call on a's pool socket naming b is refused); one grant of one role on one scope to both keeps them
// together. One subtest per grant kind, and the resource group.
func TestScenarioPoolSplitsByGrant(t *testing.T) {
	t.Parallel()
	role := v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "KV Data Reader"}
	scope := &v1.ScopeRef{Kind: v1.ScopeKindKVStore, Name: "s"}
	for _, tc := range []struct {
		name     string
		grant    func(t *testing.T, h *shimHarness)
		together bool
	}{
		{"rolesassignment", func(t *testing.T, h *shimHarness) {
			h.createObj(t, v1.KindRolesAssignment, "ra", func(o v1.Object) {
				o.(*v1.RolesAssignment).Spec.Assignments = []v1.AssignmentEntry{{Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "b"}, RoleRef: role, Scope: scope}}
			})
		}, false},
		{"egresspolicy", func(t *testing.T, h *shimHarness) {
			h.createObj(t, v1.KindEgressPolicy, "ep", func(o v1.Object) {
				o.(*v1.EgressPolicy).Spec = v1.EgressPolicySpec{Rules: []v1.EgressRule{{To: v1.EgressTo{Domains: []string{"example.com"}}}}, AppliesTo: []v1.ObjectName{"b"}}
			})
		}, false},
		{"policy", func(t *testing.T, h *shimHarness) {
			h.createObj(t, v1.KindPolicy, "pol", func(o v1.Object) {
				o.(*v1.Policy).Spec.Cedar = `permit (principal == Function::"default/b", action, resource);`
			})
		}, false},
		{"group", func(t *testing.T, h *shimHarness) {
			h.apply(t, "b", func(fn *v1.Function) { fn.ResourceGroup = "rg2" })
		}, false},
		{"one role to both", func(t *testing.T, h *shimHarness) {
			h.createObj(t, v1.KindRolesAssignment, "ra", func(o v1.Object) {
				ra := o.(*v1.RolesAssignment)
				ra.Spec.Scope = scope
				ra.Spec.Assignments = []v1.AssignmentEntry{
					{Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "a"}, RoleRef: role},
					{Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "b"}, RoleRef: role, Scope: scope},
				}
			})
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool)
			for _, n := range []string{"a", "b"} {
				h.create(t, n, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "agents" })
			}
			before := h.pools(t, "a", "b")
			require.Equal(t, before["a"], before["b"], "one access, one pool")

			tc.grant(t, h)
			after := h.pools(t, "a", "b", "a")
			key, ok := pooling.ParsePool("default", after["a"])
			require.True(t, ok)
			members, isMember, ok := h.r.PoolMembers("default", v1.ObjectName("__pool__nodejs22__agents__"+key.AccessHash))
			require.True(t, ok)
			if tc.together {
				require.Equal(t, after["a"], after["b"], "mates holding the same grant stay together")
				require.True(t, isMember("b"))
				return
			}
			require.NotEqual(t, after["a"], after["b"], "the granted Function pools apart")
			require.Equal(t, []v1.ObjectName{"a"}, members)
			require.False(t, isMember("b"), "a's pool no longer serves b")
			require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)
		})
	}
}

// Each manifest row carries only its member's credentials and bundle dir; the shared env comes from the member being
// reconciled; the load bound and the pool socket are set; the manifest is 0600 in a 0700 dir.
func TestPoolManifestCarriesMemberEnv(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "pool")
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) {
		d.PoolManifestDir = dir
		d.S3Gateway = function.S3GatewayInjection{Enabled: true, ListenAddr: "127.0.0.1:9000", Derive: func(_ v1.Kind, _, name string) (string, string) {
			return "AK-" + name, "SK-" + name
		}}
	})
	h.createObj(t, v1.KindBucket, "k", func(o v1.Object) { o.(*v1.Bucket).Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}} })
	for _, n := range []string{"a", "b"} {
		h.create(t, n, func(fn *v1.Function) {
			fn.Spec.Pooling.Worker = "s3"
			fn.Spec.Blob = []v1.FunctionBlob{{Alias: "raw", Bucket: "k", Prefix: "raw"}}
		})
	}
	h.pools(t, "a", "b")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "b").Status.Phase)
	key, ok := pooling.ParsePool("default", h.getFn(t, "a").Status.Pool)
	require.True(t, ok)
	spec, ok := h.rt.specFor(v1.ObjectName("__pool__nodejs22__s3__" + key.AccessHash))
	require.True(t, ok)
	require.Equal(t, "60000", spec.Env["FUNCD_POOL_LOAD_TIMEOUT_MS"])
	require.Equal(t, "us-east-1", spec.Env["AWS_REGION"], "the shared env comes from self")
	require.NotContains(t, spec.Env, "AWS_ACCESS_KEY_ID", "no member's credential is in the process env")

	path := spec.Env["FUNCD_POOL_MANIFEST"]
	require.Equal(t, dir, filepath.Dir(path))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	di, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var rows []struct {
		Name string            `json:"name"`
		Env  map[string]string `json:"env"`
	}
	require.NoError(t, json.Unmarshal(data, &rows))
	require.Len(t, rows, 2)
	for _, r := range rows {
		require.Equal(t, "AK-"+r.Name, r.Env["AWS_ACCESS_KEY_ID"], r.Name)
		require.Equal(t, "SK-"+r.Name, r.Env["AWS_SECRET_ACCESS_KEY"], r.Name)
		require.NotEmpty(t, r.Env["FUNCD_BUNDLE_DIR"], r.Name)
	}

	before := string(data)
	h.reconcile(t, "a")
	again, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, string(again), "deterministic credentials keep the manifest and its signature stable")
	creates, _ := h.rt.counts()
	h.reconcile(t, "b")
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "an unchanged manifest restarts nothing")
}
