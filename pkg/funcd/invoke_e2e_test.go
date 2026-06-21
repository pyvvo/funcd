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
	"github.com/green-0-rabbit/funcd/internal/contract"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// shimPlatformOCI is the process-shim platform + the oras artifact materializer (ADR-0031), so
// functions are PULLED from an OCI layout by digest — the real `funcdcli push` → `apply` deploy path.
// Returns an SDK client + the data-plane base URL. Node-gated.
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

// buildFnToFnExample runs the example's CONTRACT build (build.ts, ADR-0058/0060): for greeter + front
// it generates the JSON Schema from FuncInput/FuncOutput and bakes the eval-free __funcdValidate*
// into the .mjs. Returns the example dir (the built .mjs + *-{input,output}.schema.json live there).
func buildFnToFnExample(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	exDir := filepath.Join(root, "examples", "js", "fn-to-fn")
	// The build imports the shim's buildContract + esbuild; ensure node_modules resolves (offline-safe).
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
		t.Fatalf("contract build: %v\n%s", berr, b)
	}
	return exDir
}

// pushExampleFn pushes a built handler + its generated contract to a local OCI layout — exactly what
// `funcdcli push <mjs> <ref> --contract-input … --contract-output …` does (gates the schemas against
// the funcd profile, then embeds them as OCI metadata, ADR-0058/0059). Returns the ref + digest.
func pushExampleFn(t *testing.T, layoutDir, exDir, name string) (ref, digest string) {
	t.Helper()
	read := func(suffix string) []byte {
		b, rerr := os.ReadFile(filepath.Join(exDir, name+suffix))
		require.NoError(t, rerr)
		return b
	}
	in, out := read("-input.schema.json"), read("-output.schema.json")
	require.NoError(t, contract.Check(in), "%s input schema is in the funcd profile", name)
	require.NoError(t, contract.Check(out), "%s output schema is in the funcd profile", name)
	blob, err := artifact.ContractBlob(in, out)
	require.NoError(t, err)
	ref = "oci-layout://" + layoutDir + ":" + name
	digest, err = artifact.Push(context.Background(), ref, filepath.Join(exDir, name+".mjs"), blob)
	require.NoError(t, err)
	return ref, digest
}

// loadFn reads an examples/js/fn-to-fn YAML manifest (the deployable unit — the link is declared
// there) and points its artifact at the pushed OCI ref+digest.
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

func waitReady(t *testing.T, c *sdk.Client, names ...string) {
	t.Helper()
	for _, n := range names {
		name := n
		require.Eventually(t, func() bool { return phaseOf(t, c, name) == v1.PhaseReady },
			20*time.Second, 50*time.Millisecond, "%s reconciles to Ready", name)
	}
}

// scenario: handler-invokes-linked-function (ADR-0064) — the REAL deploy path: build the TS handlers
// (with generated contracts), push them to an OCI layout, apply greeter.yaml + front.yaml (front
// declares the link), the daemon pulls by digest, then front calls context.invoke("greeter", …) and
// greeter's reply flows back through the broker.
func TestScenarioHandlerInvokesLinkedFunction(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := buildFnToFnExample(t)
	layout := t.TempDir()
	gRef, gDig := pushExampleFn(t, layout, exDir, "greeter")
	fRef, fDig := pushExampleFn(t, layout, exDir, "front")
	applyFnObj(t, c, loadFn(t, "greeter.yaml", gRef, gDig))
	applyFnObj(t, c, loadFn(t, "front.yaml", fRef, fDig))
	waitReady(t, c, "greeter", "front")

	resp, err := http.Post(dpURL+"/function/front", "application/json", strings.NewReader(`{"data":{"name":"funcd"}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "front invoked greeter and returned: %s", body)
	require.Contains(t, string(body), "Hello, funcd!", "greeter's reply flowed back through front")
	require.Contains(t, string(body), "front", "front wrapped greeter's reply")
}

// scenario: linked-input-contract-validated (ADR-0058/0064) — greeter's GENERATED, BAKED contract
// rejects a wrong-shaped input with 422 at the shim, before the handler; a valid one returns 200.
func TestScenarioContractRejectsBadInput(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := buildFnToFnExample(t)
	layout := t.TempDir()
	gRef, gDig := pushExampleFn(t, layout, exDir, "greeter")
	applyFnObj(t, c, loadFn(t, "greeter.yaml", gRef, gDig))
	waitReady(t, c, "greeter")

	ok, err := http.Post(dpURL+"/function/greeter", "application/json", strings.NewReader(`{"data":{"name":"funcd"}}`))
	require.NoError(t, err)
	defer func() { _ = ok.Body.Close() }()
	require.Equal(t, http.StatusOK, ok.StatusCode, "a valid input runs the handler")

	bad, err := http.Post(dpURL+"/function/greeter", "application/json", strings.NewReader(`{"data":{"name":123}}`))
	require.NoError(t, err)
	defer func() { _ = bad.Body.Close() }()
	badBody, _ := io.ReadAll(bad.Body)
	require.Equal(t, http.StatusUnprocessableEntity, bad.StatusCode, "name:number violates the contract → 422: %s", badBody)
}

// scenario: invoke-propagates-contract-422 (ADR-0064) — front (permissive: name optional) forwards a
// payload with no name to greeter (strict: name required); greeter's shim returns 422, the invoker
// propagates it, and front's awaited invoke throws → front fails (no greeting, no target output).
func TestScenarioInvokePropagatesContract422(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := buildFnToFnExample(t)
	layout := t.TempDir()
	gRef, gDig := pushExampleFn(t, layout, exDir, "greeter")
	fRef, fDig := pushExampleFn(t, layout, exDir, "front")
	applyFnObj(t, c, loadFn(t, "greeter.yaml", gRef, gDig))
	applyFnObj(t, c, loadFn(t, "front.yaml", fRef, fDig))
	waitReady(t, c, "greeter", "front")

	// front accepts the missing name, forwards it to greeter, whose contract rejects it (422).
	resp, err := http.Post(dpURL+"/function/front", "application/json", strings.NewReader(`{"data":{}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.GreaterOrEqual(t, resp.StatusCode, 400, "greeter's 422 propagates and fails front: %s", body)
	require.NotContains(t, string(body), "Hello", "greeter never produced a greeting (rejected at the contract)")
}

// scenario: unlinked-alias-denied (ADR-0064) — front's handler with the link STRIPPED: invoking an
// undeclared alias fails closed (no link is no grant, default-deny).
func TestScenarioUnlinkedAliasDeniedE2E(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := buildFnToFnExample(t)
	layout := t.TempDir()
	fRef, fDig := pushExampleFn(t, layout, exDir, "front")

	lonely := loadFn(t, "front.yaml", fRef, fDig)
	lonely.Name, lonely.Spec.Links = "lonely", nil // front's code, but NO declared link
	applyFnObj(t, c, lonely)
	waitReady(t, c, "lonely")

	resp, err := http.Post(dpURL+"/function/lonely", "application/json", strings.NewReader(`{"data":{"name":"x"}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.GreaterOrEqual(t, resp.StatusCode, 400, "an undeclared invoke fails closed: %s", body)
	require.NotContains(t, string(body), "Hello,", "no target was reached")
}
