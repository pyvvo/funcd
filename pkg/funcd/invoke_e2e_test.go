//go:build e2e

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
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// shimPlatformOCI is the process-shim platform + the oras artifact materializer (ADR-0031), so
// functions are PULLED from an OCI layout by digest — the real `funcdctl push` → `apply` deploy path.
// Returns an SDK client + the data-plane base URL. Node-gated.
func shimPlatformOCI(t *testing.T) (*sdk.Client, string) {
	t.Helper()
	shim := langmod.NodeShim(t)
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

// tsExample returns a TypeScript example's dir in the pinned funcd-typescript module (ADR-0141). Its
// bundles and {input, output} contracts are committed there and kept fresh by that repo's CI, so nothing
// is built here. Read-only.
func tsExample(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(langmod.Dir(t, langmod.TypeScript), "examples", name)
}

// pushExampleFn pushes a built handler + its generated contract to a local OCI layout — exactly what
// `funcdctl push <mjs> <ref> --schema <name>.schema.json` does (gates the schemas against the funcd
// profile, then embeds them as OCI metadata, ADR-0058/0059/0090). Returns the ref + digest.
func pushExampleFn(t *testing.T, layoutDir, exDir, name string) (ref, digest string) {
	t.Helper()
	// The build emits ONE combined {input, output} contract file (the ADR-0090 --schema surface).
	docBytes, rerr := os.ReadFile(filepath.Join(exDir, name+".schema.json"))
	require.NoError(t, rerr)
	var doc struct {
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	}
	require.NoError(t, json.Unmarshal(docBytes, &doc), "%s schema is a {input, output} document", name)
	in, out := []byte(doc.Input), []byte(doc.Output)
	require.NoError(t, contract.Check(in), "%s input schema is in the funcd profile", name)
	require.NoError(t, contract.Check(out), "%s output schema is in the funcd profile", name)
	blob, err := artifact.ContractBlob(in, out)
	require.NoError(t, err)
	ref = "oci-layout://" + layoutDir + ":" + name
	digest, err = artifact.Push(context.Background(), ref, filepath.Join(exDir, name+".mjs"), blob, "")
	require.NoError(t, err)
	return ref, digest
}

// loadFn reads a fn-to-fn example YAML manifest (the deployable unit — the link is declared
// there) and points its artifact at the pushed OCI ref+digest.
func loadFn(t *testing.T, manifest, ref, digest string) *v1.Function {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tsExample(t, "fn-to-fn"), manifest))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn), "parse %s", manifest)
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
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
	exDir := tsExample(t, "fn-to-fn")
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
	exDir := tsExample(t, "fn-to-fn")
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
	exDir := tsExample(t, "fn-to-fn")
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

// scenario: policy-revokes-invoke (ADR-0075) — front declares the greeter link (so the built-in
// permit allows the invoke) and the call succeeds; then an operator Policy forbid(link::invoke) on
// Function::"default/greeter" is applied, REVOKING the declared link without editing front.spec.links
// — the next invoke is denied (the daemon's PDP gate returns Forbidden, front never reaches greeter).
func TestScenarioPolicyRevokesInvokeE2E(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := tsExample(t, "fn-to-fn")
	layout := t.TempDir()
	gRef, gDig := pushExampleFn(t, layout, exDir, "greeter")
	fRef, fDig := pushExampleFn(t, layout, exDir, "front")
	applyFnObj(t, c, loadFn(t, "greeter.yaml", gRef, gDig))
	applyFnObj(t, c, loadFn(t, "front.yaml", fRef, fDig))
	waitReady(t, c, "greeter", "front")

	// 1) the declared link invokes (built-in permit, no Policy).
	resp, err := http.Post(dpURL+"/function/front", "application/json", strings.NewReader(`{"data":{"name":"funcd"}}`))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "a declared link invokes by default: %s", body)
	require.Contains(t, string(body), "Hello, funcd!", "greeter's reply flowed back")

	// 2) apply a forbid Policy revoking the declared link; the invoke is then denied.
	revoke := &v1.Policy{
		ObjectMeta: v1.ObjectMeta{Name: "revoke-greeter", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.PolicySpec{
			Cedar: `forbid(principal == Function::"default/front", action == Action::"link::invoke", resource == Function::"default/greeter");`,
		},
	}
	_, err = c.Apply(context.Background(), revoke)
	require.NoError(t, err)

	// The PolicySource recompiles on the store-revision change; the next invoke sees the forbid.
	require.Eventually(t, func() bool {
		r, perr := http.Post(dpURL+"/function/front", "application/json", strings.NewReader(`{"data":{"name":"funcd"}}`))
		if perr != nil {
			return false
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		return r.StatusCode >= 400 && !strings.Contains(string(b), "Hello, funcd!")
	}, 10*time.Second, 100*time.Millisecond, "a forbid Policy revokes the declared invoke (Forbidden, greeter never reached)")
}

// scenario: unlinked-alias-denied (ADR-0064) — front's handler with the link STRIPPED: invoking an
// undeclared alias fails closed (no link is no grant, default-deny).
func TestScenarioUnlinkedAliasDeniedE2E(t *testing.T) {
	c, dpURL := shimPlatformOCI(t)
	exDir := tsExample(t, "fn-to-fn")
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
