package function_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/function"
)

// pushHandler pushes a single-file node handler to a fresh OCI layout, with contract as its contract blob when it is
// set, and returns its ref and digest.
func pushHandler(t *testing.T, contract []byte) (ref, digest string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(file, []byte("export function handle() { return {}; }\n"), 0o600))
	ref = "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":fn"
	digest, err := artifact.Push(context.Background(), ref, file, contract, "nodejs22", "")
	require.NoError(t, err)
	return ref, digest
}

// Issue #1084: a cloudevent Function whose OCI artifact carries no I/O contract (ADR-0090) does not start; Ready names
// the missing contract and the artifact. An http Function (ADR-0243), which carries none by design, and a cloudevent
// Function whose artifact carries one still start, solo or pooled.
func TestIssue1084_OCIArtifactWithoutContractDoesNotStart(t *testing.T) {
	t.Parallel()
	contract, err := artifact.ContractBlob([]byte(`{}`), []byte(`{}`))
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		contract []byte
		protocol v1.FunctionProtocol
		pooled   bool
		fails    bool
	}{
		"cloudevent-without-contract":        {fails: true},
		"pooled-cloudevent-without-contract": {pooled: true, fails: true},
		"http-without-contract":              {protocol: v1.ProtocolHTTP},
		"cloudevent-with-contract":           {contract: contract},
		"pooled-cloudevent-with-contract":    {contract: contract, pooled: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ref, digest := pushHandler(t, tc.contract)
			oras := artifact.NewOrasMaterializer(t.TempDir(), "")
			h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.Materializer = oras })
			h.create(t, "reader", func(fn *v1.Function) {
				fn.Spec.Image, fn.Spec.ImageDigest, fn.Spec.Protocol = ref, digest, tc.protocol
				if tc.pooled {
					fn.Spec.Pooling.Worker = "w1"
				}
			})
			h.reconcile(t, "reader")

			if !tc.fails {
				require.Equal(t, v1.PhaseReady, h.getFn(t, "reader").Status.Phase)
				return
			}
			require.Equal(t, v1.PhaseFailed, h.getFn(t, "reader").Status.Phase)
			ready := h.condition(t, "reader", "Ready")
			require.Equal(t, "ContractMissing", ready.Reason)
			require.Equal(t, "artifact "+digest+" carries no I/O contract; a cloudevent Function needs one (ADR-0090)", ready.Message)
			creates, _ := h.rt.counts()
			require.Zero(t, creates, "no worker is created")
		})
	}
}

// scenario: an-http-function-with-a-contract-never-starts — an http Function whose artifact carries a contract,
// deployed with one replica, is ShapeValid=False ShapeInvalid with a message naming spec.protocol: http, and no worker
// starts (ADR-0304 Decision 3).
func TestScenarioAnHTTPFunctionWithAContractNeverStarts(t *testing.T) {
	t.Parallel()
	contract, err := artifact.ContractBlob([]byte(`{}`), []byte(`{}`))
	require.NoError(t, err)
	ref, digest := pushHandler(t, contract)
	oras := artifact.NewOrasMaterializer(t.TempDir(), "")
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.Materializer = oras })
	h.create(t, "api", func(fn *v1.Function) {
		fn.Spec.Image, fn.Spec.ImageDigest, fn.Spec.Protocol, fn.Spec.Replicas = ref, digest, v1.ProtocolHTTP, 1
	})
	h.reconcile(t, "api")

	shape := h.condition(t, "api", "ShapeValid")
	require.Equal(t, v1.ConditionFalse, shape.Status)
	require.Equal(t, "ShapeInvalid", shape.Reason)
	require.Contains(t, shape.Message, "spec.protocol: http")
	require.Equal(t, v1.PhaseFailed, h.getFn(t, "api").Status.Phase)
	creates, _ := h.rt.counts()
	require.Zero(t, creates, "no worker starts")
}
