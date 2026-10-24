package funcd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0279 Scope: a platform whose Lake runtime is overridden, as funcdctl dev's process engine is (ADR-0125), keeps
// its catalog local and serves no save endpoint, so a query through the Lake proxy is answered at once, with no
// barrier and no lake.saveInterval.
func TestLakeWithALocalCatalogAnswersAtOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "rows")
	}))
	t.Cleanup(engine.Close)
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	p, err := New(InMemory(), WithRuntime(rt), WithLakeProviderRuntime(readyEngine{addr: strings.TrimPrefix(engine.URL, "http://")}))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	st := p.cfg.store

	bucket := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	bucket.Name, bucket.Namespace, bucket.ResourceGroup = "lakehouse", "default", "rg1"
	bucket.Spec = v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw", Owner: v1.FunctionOwner("lake")}}}
	seedObjects(t, st, bucket, lakeResource("default"))
	var addr string
	require.Eventually(t, func() bool {
		var bound bool
		addr, bound = p.lakeProxy.ProxyURL("default", "lake")
		obj, gerr := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
		return bound && gerr == nil && obj.(*v1.Lake).Status.Phase == v1.PhaseReady
	}, 5*time.Second, 10*time.Millisecond, "lake lake is Ready")

	resp, err := http.Post("http://"+addr, "application/octet-stream", strings.NewReader("query"))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Equal(t, "rows", string(body))
}
