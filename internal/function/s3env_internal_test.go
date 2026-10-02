package function

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/scheduler/singlenode"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const s3TestListen = "127.0.0.1:9000"

//nolint:gochecknoglobals // a fixed test fixture for the injection tests
var s3TestMaster = []byte("function-injection-test-master-secret")

// newS3Reconciler builds a shim-mode reconciler with the S3 keypair injection enabled,
// deriving via the real s3gateway.DeriveKeypair (deterministic).
func newS3Reconciler(t *testing.T, enabled bool) *Reconciler {
	t.Helper()
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	inj := S3GatewayInjection{}
	if enabled {
		inj = S3GatewayInjection{
			Enabled:    true,
			ListenAddr: s3TestListen,
			Derive: func(ns, fn string) (string, string) {
				kp := s3gateway.DeriveKeypair(s3TestMaster, ns, fn)
				return kp.AccessKey, kp.SecretKey
			},
		}
	}
	r, err := NewReconciler(Deps{
		Store: store.New(memory.New()), Runtime: rt, Scheduler: sch,
		Gateway: embedded.New(), Validator: NewBasicValidator(),
		Materializer: fakeMat{}, ShimCommand: []string{"node", "shim.mjs"},
		S3Gateway: inj,
	})
	require.NoError(t, err)
	return r
}

func blobFn() *v1.Function {
	fn := sampleFn()
	fn.Spec.Blob = []v1.FunctionBlob{{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"}}
	return fn
}

// scenario: keypair-injected (ADR-0085) — a spec.blob function gets the four AWS_* env
// keys derived from the node master over its Ref; the values match DeriveKeypair.
func TestScenarioKeypairInjected(t *testing.T) {
	t.Parallel()
	r := newS3Reconciler(t, true)
	spec := r.workerSpec(blobFn(), 0, "/art/app.mjs", nil, nil)

	want := s3gateway.DeriveKeypair(s3TestMaster, "default", "echo")
	require.Equal(t, want.AccessKey, spec.Env["AWS_ACCESS_KEY_ID"])
	require.Equal(t, want.SecretKey, spec.Env["AWS_SECRET_ACCESS_KEY"])
	require.Equal(t, "us-east-1", spec.Env["AWS_REGION"])
	require.Equal(t, "http://"+s3TestListen, spec.Env["AWS_ENDPOINT_URL_S3"])
}

// scenario: endpoint overrides the bind addr (ADR-0085) — when Endpoint is set (the
// sandbox-reachable node address under containerd, e.g. the CNI bridge gateway IP),
// it is injected verbatim, NOT derived from the 127.0.0.1 bind ListenAddr.
func TestScenarioEndpointOverridesBindAddr(t *testing.T) {
	t.Parallel()
	sch, err := singlenode.New("local", v1.HostPlatform())
	require.NoError(t, err)
	rt := process.New()
	t.Cleanup(func() { _ = rt.Close() })
	r, err := NewReconciler(Deps{
		Store: store.New(memory.New()), Runtime: rt, Scheduler: sch,
		Gateway: embedded.New(), Validator: NewBasicValidator(),
		Materializer: fakeMat{}, ShimCommand: []string{"node", "shim.mjs"},
		S3Gateway: S3GatewayInjection{
			Enabled:    true,
			ListenAddr: "127.0.0.1:9000", // bind addr — NOT what a netns'd worker can reach
			Endpoint:   "http://10.63.0.1:9000",
			Derive:     func(ns, fn string) (string, string) { return "a", "s" },
		},
	})
	require.NoError(t, err)
	spec := r.workerSpec(blobFn(), 0, "/art/app.mjs", nil, nil)
	require.Equal(t, "http://10.63.0.1:9000", spec.Env["AWS_ENDPOINT_URL_S3"])
}

// scenario: no injection without spec.blob (ADR-0085) — a function without a blob
// binding gets none of the AWS_* keys even when the gateway is enabled.
func TestScenarioNoInjectionWithoutBlob(t *testing.T) {
	t.Parallel()
	r := newS3Reconciler(t, true)
	spec := r.workerSpec(sampleFn(), 0, "/art/app.mjs", nil, nil)

	require.NotContains(t, spec.Env, "AWS_ACCESS_KEY_ID")
	require.NotContains(t, spec.Env, "AWS_SECRET_ACCESS_KEY")
	require.NotContains(t, spec.Env, "AWS_ENDPOINT_URL_S3")
}

// scenario: disabled-no-injection (ADR-0085) — with the gateway disabled, even a
// spec.blob function gets no AWS_* keys.
func TestScenarioDisabledNoInjection(t *testing.T) {
	t.Parallel()
	r := newS3Reconciler(t, false)
	spec := r.workerSpec(blobFn(), 0, "/art/app.mjs", nil, nil)

	require.NotContains(t, spec.Env, "AWS_ACCESS_KEY_ID")
	require.NotContains(t, spec.Env, "AWS_ENDPOINT_URL_S3")
}
