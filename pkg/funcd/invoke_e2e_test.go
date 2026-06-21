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
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// shimPlatformOCI is the process-shim platform + the oras artifact materializer (ADR-0031), so
// functions are PULLED from an OCI layout by digest — the real `funcdcli push` → `apply` deploy path,
// not a file:// stand-in. Returns an SDK client + the data-plane base URL. Node-gated.
func shimPlatformOCI(t *testing.T) (*sdk.Client, string) {
	t.Helper()
	shim, err := filepath.Abs(filepath.Join("..", "..", "shim", "nodejs", "shim.mjs"))
	require.NoError(t, err)
	if _, serr := os.Stat(shim); serr != nil {
		t.Skipf("shim not found at %s", shim)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the OCI deploy lane")
	}
	p, err := funcd.New(funcd.InMemory(), funcd.WithRuntimeShim(node, shim), funcd.WithArtifactStore(t.TempDir()))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	return c, "http://" + p.DataPlaneAddr()
}

// buildFnToFnExample bundles the TypeScript examples/js/fn-to-fn handlers to .mjs with esbuild — the
// example's `npm run build`, here using the shim's already-installed esbuild (type-only imports are
// erased, so no example node_modules needed). Returns the two built .mjs file paths.
func buildFnToFnExample(t *testing.T) (greeterMjs, frontMjs string) {
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
	return filepath.Join(out, "greeter.mjs"), filepath.Join(out, "front.mjs")
}

// pushToLayout pushes a built .mjs to a local OCI layout — exactly what `funcdcli push <mjs> <ref>`
// does (ADR-0031) — and returns the ref + digest for spec.artifact.
func pushToLayout(t *testing.T, layoutDir, tag, mjs string) (ref, digest string) {
	t.Helper()
	ref = "oci-layout://" + layoutDir + ":" + tag
	d, err := artifact.Push(context.Background(), ref, mjs, nil)
	require.NoError(t, err)
	return ref, d
}

// loadFn reads an examples/js/fn-to-fn YAML manifest (the deployable unit — the link is declared
// there) and points its artifact at the pushed OCI ref+digest (the YAML's illustrative uri is
// replaced with the layout we just pushed to).
func loadFn(t *testing.T, manifest, ref, digest string) *v1.Function {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, "examples", "js", "fn-to-fn", manifest))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn), "parse %s", manifest)
	fn.Spec.Artifact = v1.ArtifactRef{URI: ref, Digest: digest}
	return &fn
}

func applyFnObj(t *testing.T, c *sdk.Client, fn *v1.Function) {
	t.Helper()
	_, err := c.Apply(context.Background(), fn)
	require.NoError(t, err)
}

// scenario: handler-invokes-linked-function (ADR-0064) — the REAL deploy path: push the TS example
// handlers to an OCI layout (funcdcli push), apply the example greeter.yaml + front.yaml manifests
// (front declares the link), the daemon PULLS the artifacts by digest and runs them, then front calls
// context.invoke("greeter", …) and greeter's reply flows back through the broker.
func TestScenarioHandlerInvokesLinkedFunction(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	greeterMjs, frontMjs := buildFnToFnExample(t)
	layout := t.TempDir()
	gRef, gDig := pushToLayout(t, layout, "greeter", greeterMjs)
	fRef, fDig := pushToLayout(t, layout, "front", frontMjs)

	applyFnObj(t, c, loadFn(t, "greeter.yaml", gRef, gDig))
	applyFnObj(t, c, loadFn(t, "front.yaml", fRef, fDig))

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
	c, dpURL := shimPlatformOCI(t)
	_, frontMjs := buildFnToFnExample(t)
	layout := t.TempDir()
	fRef, fDig := pushToLayout(t, layout, "front", frontMjs)

	lonely := loadFn(t, "front.yaml", fRef, fDig)
	lonely.Name, lonely.Spec.Links = "lonely", nil // front's code, but NO declared link
	applyFnObj(t, c, lonely)
	require.Eventually(t, func() bool { return phaseOf(t, c, "lonely") == v1.PhaseReady },
		20*time.Second, 50*time.Millisecond, "lonely reconciles to Ready")

	resp, err := http.Post(dpURL+"/function/lonely", "application/json", strings.NewReader(`{"data":{"name":"x"}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.GreaterOrEqual(t, resp.StatusCode, 400, "an undeclared invoke fails closed: %s", body)
	require.NotContains(t, string(body), "Hello,", "no target was reached")
}
