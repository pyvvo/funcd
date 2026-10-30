package function_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/pooling"
)

// scenario: funcd-names-no-contract (pooled) — a contracted cloudevent Function's pool manifest row has no contract,
// and funcd writes no open contract beside the blob, which the materializer still delivers (ADR-0304).
func TestScenarioFuncdNamesNoContract(t *testing.T) {
	t.Parallel()
	blob, err := artifact.ContractBlob([]byte(`{"type": "object", "required": ["a"]}`), []byte(`{}`))
	require.NoError(t, err)
	ref, digest := pushHandler(t, blob)
	dir := filepath.Join(t.TempDir(), "pool")
	oras := artifact.NewOrasMaterializer(t.TempDir(), "")
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withNodePool, func(d *function.Deps) {
		d.Materializer, d.PoolManifestDir = oras, dir
	})
	h.create(t, "reader", func(fn *v1.Function) {
		fn.Spec.Image, fn.Spec.ImageDigest, fn.Spec.Pooling.Worker = ref, digest, "w1"
	})
	h.pools(t, "reader")
	key, ok := pooling.ParsePool("default", h.getFn(t, "reader").Status.Pool)
	require.True(t, ok)
	spec, ok := h.rt.specFor(v1.ObjectName("__pool__nodejs22__w1__" + key.AccessHash))
	require.True(t, ok)
	data, err := os.ReadFile(spec.Env["FUNCD_POOL_MANIFEST"])
	require.NoError(t, err)
	var rows []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &rows))
	require.Len(t, rows, 1)
	require.NotContains(t, rows[0], "contract", "the member's row names no contract")
	var path string
	require.NoError(t, json.Unmarshal(rows[0]["artifact"], &path))
	require.FileExists(t, filepath.Join(filepath.Dir(path), ".funcd-contract.json"), "the materializer delivers the blob")
	require.NoFileExists(t, filepath.Join(filepath.Dir(path), ".funcd-contract-open.json"), "funcd writes no open contract")
}
