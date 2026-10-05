//go:build e2e

package funcd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	bstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// tagMaterializer resolves every tag to its current digest and records each digest it materializes; it serves one
// handler file whatever the digest.
type tagMaterializer struct {
	path string
	mu   sync.Mutex
	tag  string
	seen []string
}

func (m *tagMaterializer) Resolve(context.Context, string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tag, nil
}

func (m *tagMaterializer) Materialize(_ context.Context, fn *v1.Function) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, fn.Spec.ImageDigest)
	return m.path, nil
}

func (m *tagMaterializer) moveTo(d string) { m.mu.Lock(); defer m.mu.Unlock(); m.tag = d }

func (m *tagMaterializer) digests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.seen)
}

// scenario: refused-edit-keeps-digest (ADR-0172) — a PUT of greeter-1 with another digest is refused, the tag moves
// and funcd restarts: the worker still runs the digest greeter-1 was stamped with.
func TestScenarioRefusedEditKeepsDigest(t *testing.T) {
	a, b, e := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("e", 64)
	handler := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(handler, []byte("export async function handle(event) { return event; }\n"), 0o600))
	mat := &tagMaterializer{path: handler, tag: a}
	dir := filepath.Join(shortDataDir(t), "store")
	open := func() store.Store {
		eng, err := bstore.Open(dir, bstore.WithValueLogGCInterval(0))
		require.NoError(t, err)
		return store.New(eng)
	}
	env := startGC(t, funcd.WithStore(open()), funcd.WithMaterializer(mat))
	env.apply(t, &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "greeter", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "oci-layout://greeter:v1", Replicas: 1},
	})
	env.waitExists(t, v1.KindRevision, "greeter-1")
	require.Eventually(t, func() bool { return slices.Contains(mat.digests(), a) }, 30*time.Second, 50*time.Millisecond, "the worker runs A")

	obj, err := env.c.Get(env.ctx, v1.KindRevision, "default", "greeter-1")
	require.NoError(t, err)
	rev := obj.(*v1.Revision)
	rev.Spec.ImageDigest = e
	body, err := json.Marshal(rev)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(env.ctx, http.MethodPut, env.api+"/apis/funcd.io/v1alpha1/namespaces/default/revisions/greeter-1", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+funcd.DevToken)
	req.Header.Set("Content-Type", "application/json")
	require.Equal(t, http.StatusMethodNotAllowed, env.do(t, req), "the edit is refused")

	mat.moveTo(b)
	env.stop()
	before := len(mat.digests())
	env = startGC(t, funcd.WithStore(open()), funcd.WithMaterializer(mat))
	require.Eventually(t, func() bool { return len(mat.digests()) > before }, 30*time.Second, 50*time.Millisecond, "the restarted funcd runs greeter")
	obj, err = env.c.Get(env.ctx, v1.KindRevision, "default", "greeter-1")
	require.NoError(t, err)
	require.Equal(t, a, obj.(*v1.Revision).Spec.ImageDigest)
	for _, d := range mat.digests() {
		require.Equal(t, a, d, "the worker runs A, never the refused E or the moved tag's B")
	}
}
