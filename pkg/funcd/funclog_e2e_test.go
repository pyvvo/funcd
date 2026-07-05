package funcd_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// scenario (e2e): funclog-captures-burst (ADR-0081) — a REAL Node function emitting 100+ console.*
// logs has them captured via the Path B fd-3 side channel (the runtime-shim's console-intercept
// producer), batched host-side by the funclog pipeline, and persisted as OTLP-JSON-Lines into the
// blob substrate. End-to-end proof of producer (shim) + transport (fd 3) + pipeline on the process
// driver — fully darwin-runnable. The containerd/UDS variant runs on the Lima lane.
func TestScenarioE2EFunclogCapturesBurst(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the funclog capture lane")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	shim := filepath.Join(root, "shim", "nodejs", "shim.mjs")
	if _, serr := os.Stat(shim); serr != nil {
		t.Skipf("node shim not built at %s (run: just build-shim)", shim)
	}

	exDir := buildLogBurst(t, root, node)

	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)

	// Push the bare bundle (no I/O contract — the example's point is the log burst) to a local OCI layout.
	layout := t.TempDir()
	ref := "oci-layout://" + layout + ":log-burst"
	digest, err := artifact.Push(context.Background(), ref, filepath.Join(exDir, "burst.mjs"), nil, "")
	require.NoError(t, err)

	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)

	// Wire drivers explicitly (not InMemory) so the test holds the blob bucket to assert on. A short
	// funclog seal age makes the captured segment flush promptly for the assertion.
	p, err := funcd.New(
		funcd.WithBlob(bucket),
		funcd.WithBus(messaging),
		funcd.WithStore(store.New(memory.New())),
		funcd.WithRuntime(process.New()),
		funcd.WithGateway(embedded.New()),
		funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithRuntimeShim(node, shim),
		funcd.WithArtifactStore(t.TempDir()),
		funcd.WithFunclog(500*time.Millisecond, 0),
	)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	dpURL := "http://" + p.DataPlaneAddr()

	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "log-burst", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Artifact = v1.ArtifactRef{URI: ref, Digest: digest}
	fn.Spec.Replicas = 1
	fn.Spec.Scaling.MinReplicas = 1
	_, err = c.Apply(context.Background(), fn)
	require.NoError(t, err)
	waitReady(t, c, "log-burst")

	// Invoke → the handler emits 100+ console.* logs (captured over fd 3).
	resp, err := http.Post(dpURL+"/function/log-burst", "application/json", strings.NewReader(`{"data":{}}`))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equalf(t, http.StatusOK, resp.StatusCode, "invoke log-burst: %s", body)
	require.Contains(t, string(body), `"emitted"`)

	// The burst's logs land in blob as OTLP-JSONL — assert ≥100 captured records (after the age-flush).
	require.Eventually(t, func() bool {
		return countLogRecords(t, bucket) >= 100
	}, 15*time.Second, 300*time.Millisecond, "≥100 captured function-log records in blob")
}

// buildLogBurst runs the example's esbuild (build.ts → burst.mjs), reusing the shim's node_modules
// so it resolves esbuild offline (the kv-counter/fn-to-fn pattern). Node-gated.
func buildLogBurst(t *testing.T, root, node string) string {
	t.Helper()
	exDir := filepath.Join(root, "examples", "js", "log-burst")
	if _, serr := os.Stat(filepath.Join(exDir, "node_modules")); serr != nil {
		shimNM := filepath.Join(root, "shim", "nodejs", "node_modules")
		if _, e := os.Stat(shimNM); e != nil {
			t.Skip("shim node_modules absent (run: just build-shim)")
		}
		require.NoError(t, os.Symlink(shimNM, filepath.Join(exDir, "node_modules")))
	}
	cmd := exec.Command(node, "--experimental-strip-types", "build.ts")
	cmd.Dir = exDir
	if b, berr := cmd.CombinedOutput(); berr != nil {
		t.Fatalf("log-burst build: %v\n%s", berr, b)
	}
	return exDir
}

// countLogRecords sums the LogRecord count across every OTLP-JSONL object under logs/.
func countLogRecords(t *testing.T, b blob.Bucket) int {
	t.Helper()
	objs, err := b.List(context.Background(), "logs/")
	require.NoError(t, err)
	var u plog.JSONUnmarshaler
	total := 0
	for _, o := range objs {
		data, gerr := b.Get(context.Background(), o.Key)
		require.NoError(t, gerr)
		logs, perr := u.UnmarshalLogs([]byte(strings.TrimSpace(string(data))))
		require.NoError(t, perr)
		total += logs.LogRecordCount()
	}
	return total
}
