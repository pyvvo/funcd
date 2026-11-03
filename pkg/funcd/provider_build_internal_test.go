package funcd

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
)

// scenario: a-lake-engine-signs-with-blobs-keys
// A provider's build uses the parts of the providers before it in the list: with the S3 gateway on, a Lake engine is
// issued the key pair of the deriver blob built, over its Lake identity (ADR-0175).
func TestBuild_TheLakeEngineGetsTheKeysOfBlobsDeriver(t *testing.T) {
	t.Parallel()
	engine := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(engine.Close)
	eng := &issuedEngine{readyEngine: readyEngine{addr: strings.TrimPrefix(engine.URL, "http://")}}
	logs, serve := platformRun()
	dataDir := t.TempDir()
	p, _ := startS3Gateway(t, freeLoopbackAddr, func(addr string) (*Platform, error) {
		return New(InMemory(), logs, WithRuntime(&recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}),
			WithLakeProviderRuntime(eng), WithS3Gateway(addr, "", 0, "", dataDir))
	}, serve)
	bucket := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	bucket.Name, bucket.Namespace, bucket.ResourceGroup = "lakehouse", "default", "rg1"
	bucket.Spec = v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw", Owner: v1.FunctionOwner("lake")}}}
	seedObjects(t, p.cfg.store, bucket, lakeResource("default"))

	want := p.S3Keys().Keypair(v1.KindLake, "default", "lake")
	require.NotEmpty(t, want.AccessKey)
	require.Eventually(t, func() bool {
		env := eng.lastIssued()
		return env["AWS_ACCESS_KEY_ID"] == want.AccessKey && env["AWS_SECRET_ACCESS_KEY"] == want.SecretKey
	}, 5*time.Second, 10*time.Millisecond, "the Lake engine is issued the key pair of blob's deriver")
}

// scenario: a-stopping-worker-keeps-its-local-api
// The Executor closes the workers' local API listeners when the daemon stops (ADR-0064): closing a Unix listener
// unlinks its path, so after Shutdown a worker's socket in a kept dir accepts no connection.
func TestShutdown_ClosesTheLocalAPIListeners(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p, err := New(InMemory(), WithoutLogCompaction(), WithInvokeSocketDir(dir))
	require.NoError(t, err)
	sock, err := p.exec.sockets.SocketFor("default", "fn")
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(context.Background()))
	conn, err := net.Dial("unix", sock)
	if err == nil {
		_ = conn.Close()
	}
	require.Error(t, err, "a worker's local API still accepts connections after Shutdown")
}

// issuedEngine is a Lake engine runtime that keeps the env the last engine it converged was issued.
type issuedEngine struct {
	readyEngine
	mu     sync.Mutex
	issued map[string]string
}

func (e *issuedEngine) Converge(ctx context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	if spec.IssueEnv != nil {
		env := spec.IssueEnv()
		e.mu.Lock()
		e.issued = env
		e.mu.Unlock()
	}
	return e.readyEngine.Converge(ctx, spec)
}

func (e *issuedEngine) lastIssued() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.issued
}
