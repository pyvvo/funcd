package function

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
)

// FEAT-0002 decision 8: on a worker node a Function's credentials derive from its namespace's master, and while the
// node lacks that master the Function waits.
func TestAWorkerNodeHoldsAFunctionUntilItsNamespacesMasterArrives(t *testing.T) {
	t.Parallel()
	nsMaster := []byte("the master of default")
	r := newShimReconciler(t, fakeResolver{})
	r.lakeMaster = []byte("the node's own master")
	r.namespaceMasters = func(ns v1.NamespaceName) ([]byte, error) {
		if ns == "default" {
			return nsMaster, nil
		}
		return nil, fault.Unavailablef("test", "no master for namespace %s", ns)
	}
	fn := lakeConsumerFn("lake", "lake")
	want, err := lakegw.DeriveLakeToken(nsMaster, fn.Namespace, fn.Name)
	require.NoError(t, err)
	require.Equal(t, want, lakeOwn(t, r, fn)["FUNCD_LAKE_LAKE_TOKEN"])

	fn.Namespace = "e2"
	_, _, gate := lakeHook{r}.own(fn)
	require.NotNil(t, gate)
	require.Equal(t, reasonNamespaceKeyMissing, gate.reason)
	require.Equal(t, v1.PhasePending, gate.phase)

	r.s3Gateway = S3GatewayInjection{Enabled: true, ListenAddr: "127.0.0.1:9000", Derive: func(_ v1.Kind, ns, _ string) (string, string, error) {
		return "", "", fault.Unavailablef("test", "no master for namespace %s", ns)
	}}
	fn.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "raw", Bucket: "k", Prefix: "raw"}}
	_, _, gate = s3Hook{r}.own(fn)
	require.NotNil(t, gate)
	require.Equal(t, reasonNamespaceKeyMissing, gate.reason)
}
