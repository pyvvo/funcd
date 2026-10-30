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

// A pooled member's manifest row names the open contract funcd writes beside the delivered one, so its shim checks
// nothing (ADR-0303 Decision 7).
func TestPoolManifestNamesTheOpenContract(t *testing.T) {
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
	var rows []struct {
		Contract string `json:"contract"`
	}
	require.NoError(t, json.Unmarshal(data, &rows))
	require.Len(t, rows, 1)
	require.Equal(t, ".funcd-contract-open.json", filepath.Base(rows[0].Contract))
	open, err := os.ReadFile(rows[0].Contract)
	require.NoError(t, err)
	var sides map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(open, &sides))
	require.JSONEq(t, `{}`, string(sides["input"]))
	require.JSONEq(t, `{}`, string(sides["output"]))
}
