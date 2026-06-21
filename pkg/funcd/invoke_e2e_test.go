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
	"sigs.k8s.io/yaml"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// buildFnToFnExample bundles the TypeScript examples/js/fn-to-fn handlers (greeter + front, ADR-0064)
// to .mjs with esbuild — the exact build the example's `npm run build` runs, here using the shim's
// already-installed esbuild so the test is hermetic (type-only imports are erased, so no example
// node_modules are needed). Returns the two file:// artifact URIs.
func buildFnToFnExample(t *testing.T) (greeterURI, frontURI string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	esbuild := filepath.Join(root, "shim", "nodejs", "node_modules", ".bin", "esbuild")
	if _, statErr := os.Stat(esbuild); statErr != nil {
		t.Skipf("esbuild not found at %s (run: just build-shim)", esbuild)
	}
	src := filepath.Join(root, "examples", "js", "fn-to-fn", "src")
	out := t.TempDir()
	cmd := exec.Command(esbuild,
		filepath.Join(src, "greeter.ts"), filepath.Join(src, "front.ts"),
		"--bundle", "--platform=node", "--format=esm", "--target=node22",
		"--outdir="+out, "--out-extension:.js=.mjs")
	if b, berr := cmd.CombinedOutput(); berr != nil {
		t.Fatalf("esbuild fn-to-fn example: %v\n%s", berr, b)
	}
	return "file://" + filepath.Join(out, "greeter.mjs"), "file://" + filepath.Join(out, "front.mjs")
}

// loadFn reads an examples/js/fn-to-fn Function manifest (the YAML the example ships — the link is
// declared there, not in Go) and points its artifact at the freshly-built .mjs.
func loadFn(t *testing.T, manifest, artifactURI string) *v1.Function {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, "examples", "js", "fn-to-fn", manifest))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn), "parse %s", manifest)
	fn.Spec.Artifact.URI = artifactURI // the YAML uri is illustrative; point at the built artifact
	return &fn
}

func applyFnObj(t *testing.T, c *sdk.Client, fn *v1.Function) {
	t.Helper()
	_, err := c.Apply(context.Background(), fn)
	require.NoError(t, err)
}

// scenario: handler-invokes-linked-function (ADR-0064) — the REAL cross-process round-trip on the
// process-shim lane, applying the example's greeter.yaml + front.yaml (front declares the link to
// greeter) and exercising the TypeScript handlers. front calls context.invoke("greeter", …); the
// platform provisions front's worker-node local API (FUNCD_INVOKE_SOCKET), front's shim dials it, the
// resolver+invoker broker the call to greeter through the in-process data plane, and greeter's reply
// flows back into front's response.
func TestScenarioHandlerInvokesLinkedFunction(t *testing.T) {
	c, dpURL, _ := shimPlatform(t)
	greeterURI, frontURI := buildFnToFnExample(t)

	applyFnObj(t, c, loadFn(t, "greeter.yaml", greeterURI))
	applyFnObj(t, c, loadFn(t, "front.yaml", frontURI))

	for _, n := range []string{"greeter", "front"} {
		name := n
		require.Eventually(t, func() bool { return phaseOf(t, c, name) == v1.PhaseReady },
			20*time.Second, 50*time.Millisecond, "%s reconciles to Ready", name)
	}

	resp, err := http.Post(dpURL+"/function/front", "application/json", strings.NewReader(`{"data":{"name":"funcd"}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "front invoked greeter and returned: %s", body)
	require.Contains(t, string(body), "Hello, funcd!", "greeter's reply flowed back through front (the broker round-trip)")
	require.Contains(t, string(body), "front", "front wrapped greeter's reply")
}

// scenario: unlinked-alias-denied (ADR-0064) — front's handler with the link STRIPPED: invoking an
// undeclared alias fails closed (no link is no grant, default-deny).
func TestScenarioUnlinkedAliasDeniedE2E(t *testing.T) {
	c, dpURL, _ := shimPlatform(t)
	_, frontURI := buildFnToFnExample(t)

	lonely := loadFn(t, "front.yaml", frontURI)
	lonely.Name, lonely.Spec.Links = "lonely", nil // front's code, but NO declared link
	applyFnObj(t, c, lonely)
	require.Eventually(t, func() bool { return phaseOf(t, c, "lonely") == v1.PhaseReady },
		20*time.Second, 50*time.Millisecond, "lonely reconciles to Ready")

	resp, err := http.Post(dpURL+"/function/lonely", "application/json", strings.NewReader(`{"data":{"name":"x"}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	// front awaits the invoke; the rejection (default-deny) surfaces as the handler throwing → 5xx.
	require.GreaterOrEqual(t, resp.StatusCode, 400, "an undeclared invoke fails closed: %s", body)
	require.NotContains(t, string(body), "Hello,", "no target was reached")
}
