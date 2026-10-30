package function_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/function"
)

// scenario: a-contract-that-cannot-compile-stops-the-revision (ADR-0303 Decision 3) — a fixture artifact whose contract
// has a pattern funcd cannot compile, which push refuses since ADR-0301, fails its Function with ContractInvalid and
// starts no worker. artifact.Push does not gate the contract, as funcdctl push and the bundle gate do.
func TestScenarioAContractThatCannotCompileStopsTheRevision(t *testing.T) {
	t.Parallel()
	bad, err := artifact.ContractBlob([]byte(`{"type": "string", "pattern": "[z-a]"}`), []byte(`{}`))
	require.NoError(t, err)
	for name, pooled := range map[string]bool{"solo": false, "pooled": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ref, digest := pushHandler(t, bad)
			oras := artifact.NewOrasMaterializer(t.TempDir(), "")
			h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) { d.Materializer = oras })
			h.create(t, "reader", func(fn *v1.Function) {
				fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
				if pooled {
					fn.Spec.Pooling.Worker = "w1"
				}
			})
			h.reconcile(t, "reader")
			require.Equal(t, v1.PhaseFailed, h.getFn(t, "reader").Status.Phase)
			ready := h.condition(t, "reader", "Ready")
			require.Equal(t, "ContractInvalid", ready.Reason)
			require.True(t, strings.HasPrefix(ready.Message, "artifact "+digest+" carries a contract funcd cannot compile"), ready.Message)
			creates, _ := h.rt.counts()
			require.Zero(t, creates, "no worker is created")
		})
	}
}
