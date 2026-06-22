package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// buildKVExample runs the kv-counter example's esbuild (→ counter.mjs), reusing the shim's node_modules
// so it resolves esbuild offline (same pattern as buildFnToFnExample). Node-gated.
func buildKVExample(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	exDir := filepath.Join(root, "examples", "js", "kv-counter")
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
		t.Fatalf("kv-counter build: %v\n%s", berr, b)
	}
	return exDir
}

// scenario (e2e): kv-counter-via-context-kv (ADR-0069) — the REAL path: build the kv-counter handler,
// push it to an OCI layout, apply it, then POST twice. The handler reads+increments a per-name counter
// through context.kv (→ worker-node local API UDS → PDP Facade → durable driver), so the count goes 1
// then 2 across invocations — proving functions can use durable KV end-to-end.
func TestScenarioE2EKVCounterViaContextKV(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := buildKVExample(t)
	layout := t.TempDir()
	// push WITH the I/O contract (counter-{input,output}.schema.json baked by build.ts) — the kv-counter
	// function is contract-validated exactly like fn-to-fn, so the artifact carries its schemas.
	ref, digest := pushExampleFn(t, layout, exDir, "counter")

	root, _ := filepath.Abs(filepath.Join("..", ".."))
	exYAML := filepath.Join(root, "examples", "js", "kv-counter")
	data, err := os.ReadFile(filepath.Join(exYAML, "counter.yaml"))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn), "parse counter.yaml")
	fn.Spec.Artifact = v1.ArtifactRef{URI: ref, Digest: digest}

	// ADR-0072: the function needs an owned KVStore + an rw Grant. Apply the store first (so the Grant
	// admission's referenced-store check passes), then the function, then the Grant.
	applyKVStore(t, c, filepath.Join(exYAML, "store.yaml"))
	applyFnObj(t, c, &fn)
	waitReady(t, c, "counter")

	call := func() (int, []byte) {
		resp, err := http.Post(dpURL+"/function/counter", "application/json", strings.NewReader(`{"data":{"name":"alice"}}`))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return -1, body
		}
		var out struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		require.NoError(t, json.Unmarshal(body, &out))
		return out.Count, body
	}

	// scenario (e2e): ungranted-access-denied — with NO Grant yet, context.kv is Forbidden (default-deny):
	// the handler's kv call fails, so the invocation does not return a 200 count.
	count, body := call()
	require.NotEqual(t, 1, count, "without a Grant, the kv-counter call is denied (default-deny): %s", body)

	// Apply the rw Grant — now the function is the store's writer and the counter works.
	applyGrant(t, c, filepath.Join(exYAML, "grant.yaml"))

	// scenario (e2e): grant-gated 1→2 — the granted handler increments across invocations.
	got1, body1 := call()
	require.Equal(t, 1, got1, "first granted invoke → count 1: %s", body1)
	got2, _ := call()
	require.Equal(t, 2, got2, "second granted invoke → count 2 (KV persisted across invocations)")
}

// applyKVStore parses a KVStore manifest and applies it through the control-plane client (ADR-0072).
func applyKVStore(t *testing.T, c *sdk.Client, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var ks v1.KVStore
	require.NoError(t, yaml.Unmarshal(data, &ks), "parse %s", path)
	_, err = c.Apply(context.Background(), &ks)
	require.NoError(t, err)
}

// applyGrant parses a Grant manifest and applies it through the control-plane client (ADR-0072).
func applyGrant(t *testing.T, c *sdk.Client, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var g v1.Grant
	require.NoError(t, yaml.Unmarshal(data, &g), "parse %s", path)
	_, err = c.Apply(context.Background(), &g)
	require.NoError(t, err)
}
