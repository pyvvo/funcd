package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/pkg/funcd"
)

// TestE2EUserJourney walks the platform exactly as an end user would, through the public
// surface only: the user pushes a source artifact with `funcdctl push` (OCI, ADR-0031),
// applies a Function manifest with `funcdctl apply`, watches it reconcile to Ready with
// `funcdctl get`, and invokes it over HTTP on the data plane (ADR-0033) — then does the
// same for a scale-to-zero function and proves a cold HTTP request wakes it.
//
// The "server" is an embedded funcd configured for real execution (the ADR-0014 embed
// path); the client side is the REAL `funcdctl` binary + plain HTTP — no internal/ import.
// Node-gated (the process-driver shim runs the JS handler), like the other execution e2e.
func TestE2EUserJourney(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the end-user execution journey")
	}
	shim := e2eNodeShim(t)

	// --- the platform the user deploys to (embedded; real execution + OCI artifacts) ---
	artifactCache := t.TempDir() // platform-side per-digest pull cache (ADR-0031)
	p, err := funcd.New(
		funcd.InMemory(),
		funcd.WithRuntimeShim(node, shim),      // run functions on the process-driver shim (ADR-0030)
		funcd.WithArtifactStore(artifactCache), // pull artifacts by digest via oras (ADR-0031)
	)
	require.NoError(t, err)
	require.NotEmpty(t, p.Addr())
	require.NotEmpty(t, p.DataPlaneAddr())

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

	server := "http://" + p.Addr()
	dataPlane := "http://" + p.DataPlaneAddr()

	// --- the user's CLI: build the real funcdctl binary and drive it ---
	cli := buildFuncdcli(t)
	runCLI := func(args ...string) string {
		t.Helper()
		full := append([]string{"--server", server, "--token", funcd.DevToken}, args...)
		out, cerr := exec.Command(cli, full...).CombinedOutput()
		require.NoError(t, cerr, "funcdctl %s:\n%s", strings.Join(args, " "), out)
		return string(out)
	}

	// 1. the user writes a handler and pushes it as an OCI artifact (no registry — a local layout).
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle,
		[]byte("export function handle(_, event) { return { echoed: event }; }\n"), 0o600))
	layout := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"

	// contracts are mandatory (ADR-0090): a --schema with a {input, output} document (Json in / any out).
	schema := writeE2ESchema(t, `{"input":{},"output":{}}`)
	pushed := strings.TrimSpace(runCLI("push", bundle, layout, "--schema", schema))
	require.True(t, strings.HasPrefix(pushed, layout+"@sha256:"),
		"`funcdctl push` prints <ref>@<digest>, got %q", pushed)

	// 2. the user applies a Function referencing only the ref — NO digest. The platform
	// resolves the tag → digest and pins it into the Revision at stamp time (ADR-0035).
	applyFunction(t, runCLI, "echo", layout, "", `"scaling":{"minReplicas":1},"replicas":1`)

	// 3. the user watches it reconcile to Ready via the CLI.
	requireCLIPhase(t, runCLI, "echo", "Ready")

	// 4. the user invokes it over HTTP — the data plane serves it through the running shim.
	resp, err := http.Post(dataPlane+"/function/echo", "application/json", strings.NewReader(`{"hello":"world"}`))
	require.NoError(t, err)
	body := readClose(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "HTTP invocation reaches the function: %s", body)
	require.Contains(t, body, "echoed", "the user's handler ran and returned its body")

	// 5. scale-to-zero: a min-replicas-0 function settles Idle, and a cold HTTP request wakes it.
	applyFunction(t, runCLI, "cold", layout, "", `"scaling":{"minReplicas":0,"idleTimeout":3600000000000},"replicas":0`)
	require.Eventually(t, func() bool {
		ph := cliPhase(t, runCLI, "cold")
		return ph == "Idle" || ph == "Pending"
	}, 10*time.Second, 100*time.Millisecond, "a scale-to-zero function settles Idle (0 running)")

	wakeResp, err := http.Post(dataPlane+"/function/cold", "application/json", strings.NewReader(`{"wake":true}`))
	require.NoError(t, err)
	wakeBody := readClose(t, wakeResp)
	require.Equal(t, http.StatusOK, wakeResp.StatusCode, "a cold HTTP request wakes the function: %s", wakeBody)
	require.Contains(t, wakeBody, "echoed")
}

// TestE2EEventDataContract validates the I/O contract (ADR-0058/0123) through the public surface:
// a SCHEMA-ONLY artifact (no baked validator) whose delivered input schema requires {hello: string}
// rejects a wrong-shaped event with 422 *before* the handler runs, and runs normally on a matching
// one. Under ADR-0123 the shim compiles the validator from the delivered digest-pinned schema at
// warm-up (advertised == enforced) — the artifact bakes no __funcdValidate* callable. Node-gated.
func TestE2EEventDataContract(t *testing.T) {
	dataPlane, runCLI := execPlatform(t)

	// the artifact is schema-only (ADR-0123): just the handler, NO baked __funcdValidate*. The shim
	// compiles the validator from the delivered schema and validates event.data before invoking
	// (mismatch → 422). The output side is Json ({}), so the handler's object return is accepted.
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte(
		`export function handle(_, event) { return { echoed: event.data }; }`+"\n"), 0o600))
	layout := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	// mandatory contract (ADR-0090): the enforced constraint lives IN the schema (ADR-0123) — a
	// closed record requiring hello:string on input; any JSON out (the handler echoes an object).
	schema := writeE2ESchema(t,
		`{"input":{"type":"object","properties":{"hello":{"type":"string"}},"required":["hello"],"additionalProperties":false},"output":{}}`)
	pushed := strings.TrimSpace(runCLI("push", bundle, layout, "--schema", schema))
	require.True(t, strings.HasPrefix(pushed, layout+"@sha256:"), "push prints <ref>@<digest>, got %q", pushed)

	applyFunction(t, runCLI, "contracted", layout, "", `"scaling":{"minReplicas":1},"replicas":1`)
	requireCLIPhase(t, runCLI, "contracted", "Ready")

	// matching event.data → the handler runs (200).
	okResp, err := http.Post(dataPlane+"/function/contracted", "application/json",
		strings.NewReader(`{"data":{"hello":"world"}}`))
	require.NoError(t, err)
	okBody := readClose(t, okResp)
	require.Equal(t, http.StatusOK, okResp.StatusCode, "matching event.data invokes the handler: %s", okBody)
	require.Contains(t, okBody, "echoed")

	// wrong-shaped event.data → 422, the handler never runs (the contract gate).
	badResp, err := http.Post(dataPlane+"/function/contracted", "application/json",
		strings.NewReader(`{"data":{"hello":123}}`))
	require.NoError(t, err)
	badBody := readClose(t, badResp)
	require.Equal(t, http.StatusUnprocessableEntity, badResp.StatusCode,
		"a contract mismatch is rejected before the handler: %s", badBody)
	require.Contains(t, badBody, "contract", "the 422 explains the contract violation: %s", badBody)
}

// execPlatform boots an embedded, real-execution funcd (node-gated process shim + OCI
// artifacts) and the real funcdctl driver — the shared rig for the execution e2e tests.
func execPlatform(t *testing.T) (dataPlane string, runCLI func(...string) string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the execution e2e")
	}
	shim := e2eNodeShim(t)
	p, err := funcd.New(
		funcd.InMemory(),
		funcd.WithRuntimeShim(node, shim),
		funcd.WithArtifactStore(t.TempDir()),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-doneCh:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})

	server := "http://" + p.Addr()
	dataPlane = "http://" + p.DataPlaneAddr()
	cli := buildFuncdcli(t)
	runCLI = func(args ...string) string {
		t.Helper()
		full := append([]string{"--server", server, "--token", funcd.DevToken}, args...)
		out, cerr := exec.Command(cli, full...).CombinedOutput()
		require.NoError(t, cerr, "funcdctl %s:\n%s", strings.Join(args, " "), out)
		return string(out)
	}
	return dataPlane, runCLI
}

// buildFuncdcli compiles the real CLI binary from this repo and returns its path.
func buildFuncdcli(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "funcdctl")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/funcdctl")
	cmd.Dir = filepath.Join("..", "..") // repo root from tests/e2e/
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build funcdctl: %v\n%s", err, out)
	}
	return bin
}

// applyFunction writes a Function manifest and applies it through the CLI.
// writeE2ESchema writes the mandatory {input, output} contract document (ADR-0090) to a temp file
// and returns its path — the --schema source funcdctl push now requires.
func writeE2ESchema(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(f, []byte(body), 0o600))
	return f
}

func applyFunction(t *testing.T, runCLI func(...string) string, name, ref, digest, scalingJSON string) {
	t.Helper()
	manifest := fmt.Sprintf(`{"apiVersion":"funcd.io/v1alpha1","kind":"Function",`+
		`"metadata":{"name":%q,"namespace":"default","resourceGroup":"rg1"},`+
		`"spec":{"runtime":"nodejs22","handler":"handle",`+
		`"image":%q,"imageDigest":%q,%s}}`, name, ref, digest, scalingJSON)
	f := filepath.Join(t.TempDir(), name+".json")
	require.NoError(t, os.WriteFile(f, []byte(manifest), 0o600))
	out := runCLI("apply", "-f", f)
	require.Contains(t, out, "applied", "funcdctl apply confirms: %s", out)
}

// cliPhase reads a function's Status.Phase via `funcdctl get ... -o json`.
func cliPhase(t *testing.T, runCLI func(...string) string, name string) string {
	t.Helper()
	out := runCLI("get", "function", name, "-n", "default", "-o", "json")
	var obj struct {
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &obj), "parse `funcdctl get -o json`: %s", out)
	return obj.Status.Phase
}

func requireCLIPhase(t *testing.T, runCLI func(...string) string, name, want string) {
	t.Helper()
	require.Eventually(t, func() bool { return cliPhase(t, runCLI, name) == want },
		20*time.Second, 100*time.Millisecond, "function %s should reach %s via the CLI", name, want)
}

func readClose(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
