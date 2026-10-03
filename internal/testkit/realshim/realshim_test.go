package realshim_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/testkit/realshim"
)

// recordingMaterializer resolves file images and records each image it was asked for.
type recordingMaterializer struct {
	function.FileMaterializer
	mu     sync.Mutex
	images []string
}

func (m *recordingMaterializer) Materialize(ctx context.Context, fn *v1.Function) (string, error) {
	m.mu.Lock()
	m.images = append(m.images, fn.Spec.Image)
	m.mu.Unlock()
	return m.FileMaterializer.Materialize(ctx, fn)
}

// One setup brings the real Node shim up for every package that needs it: the caller supplies only the materializer
// and the image, so a fix to the setup reaches every node-gated test (issues #393 and #452 had to fix two copies).
func TestIssue506_OneSetupRunsTheCallersMaterializerAndImage(t *testing.T) {
	handler := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(handler, []byte("export function handle() {}\n"), 0o600))
	image := "file://" + handler
	mat := &recordingMaterializer{}

	gw := realshim.Ready(t, mat, image, "")

	mat.mu.Lock()
	require.Contains(t, mat.images, image, "the setup materialized the caller's image through the caller's materializer")
	mat.mu.Unlock()
	rs, err := gw.Routes(context.Background())
	require.NoError(t, err)
	require.Len(t, rs, 1, "the gateway routes to the ready shim")
}
