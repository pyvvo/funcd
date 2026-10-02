//go:build dev

package main

import (
	"bytes"
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

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// requireNode skips a test when node is not on PATH — the hermetic dev lane runs the REAL Node shim
// (no docker/colima), mirroring the existing pkg/funcd node-gated e2e tests.
func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH; skipping the funcdctl dev node lane")
	}
}

// devProject writes files into a fresh temp dir (its basename becomes the function name) and returns
// the dir. Every file's content is written verbatim.
func devProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	return dir
}

// runDev boots funcdctl dev over the project dir and returns the running instance; it is torn down
// (ctx cancelled, platform drained, contract dotfile removed) at test end.
func runDev(t *testing.T, dir string) *devInstance {
	t.Helper()
	a := &cli{out: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := a.startDev(ctx, dir, "", devConfig{})
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		_ = inst.stop()
	})
	return inst
}

// waitReady blocks until the (single) dev function reports Ready — proving reconcile → materialize →
// shim boot → readiness probe all passed from the working tree, with no push.
func waitReady(t *testing.T, inst *devInstance) {
	t.Helper()
	name := inst.functions[0]
	require.Eventually(t, func() bool {
		got, err := inst.client.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
		if err != nil {
			return false
		}
		return got.(*v1.Function).Status.Phase == v1.PhaseReady
	}, 20*time.Second, 50*time.Millisecond, "the from-source function reconciles to Ready")
}

func post(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

const strictContract = `contract:
  input:
    type: object
    additionalProperties: false
    properties:
      name:
        type: string
    required:
      - name
  output:
    type: object
`

const permissiveContract = `contract:
  input:
    type: object
  output:
    type: object
`

// scenario: dev-run-function (THE CRUX) — funcdctl dev runs a handler from the working tree (no push,
// no OCI artifact), the invoke returns the real handler result, and a payload violating contract.input
// returns 422 BEFORE the handler (ADR-0123 enforcement via the delivered FUNCD_CONTRACT_PATH).
func TestScenarioDevRunFunction(t *testing.T) {
	requireNode(t)
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + strictContract,
		"handler.mjs":   "export function handle(ctx, event) { return { greeting: `hi ${event.data.name}` }; }\n",
	})
	inst := runDev(t, dir)
	waitReady(t, inst)

	url := inst.gatewayURL + "/function/" + inst.functions[0]

	// The invoke body is a CloudEvent; the input contract validates event.data (ADR-0123).
	resp, body := post(t, url, `{"data":{"name":"ada"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, "good input runs the from-source handler: %s", body)
	require.Contains(t, body, "hi ada", "the real handler result comes back")

	// The delivered contract fails a bad payload closed at 422, BEFORE the handler executes.
	bad, badBody := post(t, url, `{"data":{"oops":1}}`)
	require.Equal(t, http.StatusUnprocessableEntity, bad.StatusCode,
		"a contract.input violation is 422 before the handler (ADR-0123): %s", badBody)
}

// scenario: dev-config-inline — dev.config.<name> values reach the handler as env, with no configmap.yaml.
func TestScenarioDevConfigInline(t *testing.T) {
	requireNode(t)
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n  config:\n    - app-config\n" +
			permissiveContract +
			"dev:\n  config:\n    app-config:\n      APP_MODE: prod\n",
		"handler.mjs": "export function handle() { return { mode: process.env.APP_MODE }; }\n",
	})
	inst := runDev(t, dir)
	waitReady(t, inst)

	_, body := post(t, inst.gatewayURL+"/function/"+inst.functions[0], `{"data":{}}`)
	require.Contains(t, body, `"mode":"prod"`, "the inline dev.config value reaches the handler env: %s", body)
}

// scenario: dev-secret-from-env — dev.secrets ${VAR} resolves from the process env into the handler env;
// a missing var fails fast (before any platform boot). No secret value is ever read from the file.
func TestScenarioDevSecretFromEnv(t *testing.T) {
	requireNode(t)
	t.Setenv("MY_DEV_TOKEN", "s3cr3t-from-env")
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n  secrets:\n    - app-secret\n" +
			permissiveContract +
			"dev:\n  secrets:\n    app-secret:\n      API_KEY: ${MY_DEV_TOKEN}\n",
		"handler.mjs": "export function handle() { return { key: process.env.API_KEY }; }\n",
	})
	inst := runDev(t, dir)
	waitReady(t, inst)

	_, body := post(t, inst.gatewayURL+"/function/"+inst.functions[0], `{"data":{}}`)
	require.Contains(t, body, `"key":"s3cr3t-from-env"`, "the ${ENV}-sourced secret reaches the handler env: %s", body)
}

// scenario: dev-secret-from-env (fail-fast facet) — a ${VAR} referencing an UNSET env var fails at boot,
// before the platform starts (this facet needs no runtime, so it is not node-gated).
func TestScenarioDevSecretMissingEnvFailsFast(t *testing.T) {
	_ = os.Unsetenv("DEFINITELY_UNSET_DEV_VAR")
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n  secrets:\n    - app-secret\n" +
			permissiveContract +
			"dev:\n  secrets:\n    app-secret:\n      API_KEY: ${DEFINITELY_UNSET_DEV_VAR}\n",
		"handler.mjs": "export function handle() { return {}; }\n",
	})
	a := &cli{out: io.Discard}
	_, err := a.startDev(context.Background(), dir, "", devConfig{})
	require.Error(t, err, "a missing env var referenced by a dev.secret fails fast")
	require.Contains(t, err.Error(), "DEFINITELY_UNSET_DEV_VAR")
}

// scenario: dev-auto-provisions-backends — kv/blob bindings with NO CRD on disk are synthesized (KVStore
// + Bucket), so the function's bindings resolve (it reaches Ready) — nothing hand-written.
func TestScenarioDevAutoProvisionsBackends(t *testing.T) {
	requireNode(t)
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n" +
			"  kv:\n    - alias: cache\n      store: cache-kv\n      table: entries\n" +
			"  blob:\n    - alias: bronze\n      bucket: releves\n      prefix: bronze\n" +
			permissiveContract,
		"handler.mjs": "export function handle() { return { ok: true }; }\n",
	})
	inst := runDev(t, dir)
	// Ready proves the reconcile-time binding-existence gate (ADR-0121) passed against the
	// auto-provisioned KVStore + Bucket — the bindings resolved with no CRD on disk.
	waitReady(t, inst)

	ks, err := inst.client.Get(context.Background(), v1.KindKVStore, "default", "cache-kv")
	require.NoError(t, err, "the kv binding auto-provisioned a KVStore")
	require.Equal(t, "entries", ks.(*v1.KVStore).Spec.Tables[0].Name)

	bk, err := inst.client.Get(context.Background(), v1.KindBucket, "default", "releves")
	require.NoError(t, err, "the blob binding auto-provisioned a Bucket")
	require.Equal(t, "bronze", bk.(*v1.Bucket).Spec.Prefixes[0].Name)
}

// scenario: dev-grants-blob-writer (ADR-0128 as amended 2026-07-14) — instead of dropping the S3
// single-writer forbid, `funcdctl dev` auto-provisions a `Blob Data Writer` RolesAssignment (ADR-0136)
// per function, so the dev principal is a real writer and its seeds/producer-writes pass the REAL forbid.
// Hermetic: asserts synthesizeResources emits the admission-valid grant (no node/boot needed).
func TestScenarioDevGrantsBlobWriterRole(t *testing.T) {
	pfs := []plannedFunc{{
		name: "ingest",
		m: &sdk.Manifest{
			Runtime: "python311",
			Handler: "handle",
			Bindings: sdk.Bindings{
				Blob: []v1.FunctionBlob{{Alias: "landing", Bucket: "releves", Prefix: "landing"}},
			},
		},
	}}
	objs, err := synthesizeResources("op", pfs)
	require.NoError(t, err)

	var role *v1.Role
	var ra *v1.RolesAssignment
	for _, o := range objs {
		switch v := o.(type) {
		case *v1.Role:
			role = v
		case *v1.RolesAssignment:
			ra = v
		}
	}
	// A WRITE-ONLY custom Role — not the built-in Blob Data Writer (read+write) — so dev reads stay
	// binding-gated (the "forgot to bind → read Forbidden" fidelity ADR-0125 keeps).
	require.NotNil(t, role, "dev auto-provisions the write-only dev-blob-writer Role")
	require.NoError(t, role.Validate())
	require.Equal(t, []string{"s3::write"}, role.Spec.Actions, "write only — reads NOT widened")

	require.NotNil(t, ra, "dev auto-provisions a RolesAssignment binding the function to that Role (non-bypass path)")
	require.NoError(t, ra.Validate(), "the provisioned grant is admission-valid")
	require.Equal(t, v1.ObjectName("dev-blob-writer-ingest"), ra.Name)
	require.Equal(t, &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "ingest"}, ra.Spec.Principal)
	require.Len(t, ra.Spec.Assignments, 1)
	require.Equal(t, v1.RoleRef{Kind: v1.RoleRefKindRole, Name: "dev-blob-writer"}, ra.Spec.Assignments[0].RoleRef)
	require.Equal(t, &v1.ScopeRef{Kind: v1.ScopeKindNamespace}, ra.Spec.Assignments[0].Scope)
}

// scenario: dev-egress-not-isolated — `funcdctl dev` does not reproduce egress isolation (process
// mode has no netns), so an outbound call from a handler is NOT blocked. The ADR requires that this
// boundary be DOCUMENTED, not hidden: the startup banner tells the author so, alongside the list of
// every localhost service it exposes. (The live outbound-call is inherently network-dependent — the
// deferred lane; the banner is the hermetic observable of the documented boundary.)
func TestScenarioDevEgressNotIsolated(t *testing.T) {
	var out bytes.Buffer
	a := &cli{out: &out}
	inst := &devInstance{
		gatewayURL:  "http://127.0.0.1:8080",
		functions:   []string{"ingest", "transform"},
		workflow:    "releve-lakehouse",
		catalogs:    []string{"lake"},
		s3Endpoint:  "http://127.0.0.1:54321",
		s3AccessKey: "AKIADEV",
		s3SecretKey: "devsecret",
		s3Region:    "us-east-1",
	}
	if err := a.printBanner(inst); err != nil {
		t.Fatalf("printBanner: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "egress is NOT isolated") {
		t.Errorf("banner does not document the egress fidelity boundary:\n%s", got)
	}
	// Every localhost service the ADR exposes is listed (Decision 6).
	for _, want := range []string{
		"SERVICES", "gateway", "http://127.0.0.1:8080",
		"s3", "http://127.0.0.1:54321",
		"catalog", "bound: lake",
		"POST  http://127.0.0.1:8080/function/ingest",
		"WORKFLOW", "releve-lakehouse",
		"AWS_ACCESS_KEY_ID=AKIADEV",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("services banner missing %q:\n%s", want, got)
		}
	}
}

// The catalog service line appears only when a catalog is actually bound.
func TestBannerOmitsCatalogWhenUnbound(t *testing.T) {
	var out bytes.Buffer
	a := &cli{out: &out}
	inst := &devInstance{
		gatewayURL: "http://127.0.0.1:8080",
		functions:  []string{"hello"},
		s3Endpoint: "http://127.0.0.1:5555",
		s3Region:   "us-east-1",
	}
	if err := a.printBanner(inst); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "catalog") {
		t.Errorf("banner shows a catalog line with no catalog bound:\n%s", out.String())
	}
}

// the dev log styler renders a streamed line compactly: time · function · severity · body, with
// structured attrs appended in sorted order (real-time dev log streaming). A non-TTY writer (this
// buffer) yields plain text — color only shows on a real terminal.
func TestPrintLogFormatsLine(t *testing.T) {
	var out bytes.Buffer
	s := newDevLogStyler(&out)
	got := s.format(funcd.LogLine{
		Function: "ingest",
		Severity: "INFO",
		Body:     "greeting",
		Time:     time.Date(2026, 7, 12, 9, 8, 7, 0, time.UTC),
		Attrs:    map[string]string{"name": "Ada", "attempt": "1"},
	})
	for _, want := range []string{"09:08:07", "ingest", "INFO", "greeting", "attempt=1", "name=Ada"} {
		if !strings.Contains(got, want) {
			t.Errorf("log line missing %q:\n%s", want, got)
		}
	}
	// attrs are sorted (attempt before name) for stable output.
	if strings.Index(got, "attempt=1") > strings.Index(got, "name=Ada") {
		t.Errorf("attrs not sorted: %s", got)
	}
	// A blank severity defaults to INFO.
	if !strings.Contains(s.format(funcd.LogLine{Function: "f", Body: "x", Time: time.Now()}), "INFO") {
		t.Error("blank severity should default to INFO")
	}
}

// TestPrintDevEnvDerivesDeterministicKeypair — `funcdctl dev --print-env` prints exactly the four AWS
// export lines with the S3 keypair DERIVED (fixed devS3Master over the resolved first function — the same
// keypair the banner shows, deterministic across calls), boots no server, and defaults the endpoint port
// to 3006.
func TestPrintDevEnvDerivesDeterministicKeypair(t *testing.T) {
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + strictContract,
	})
	var out bytes.Buffer
	a := &cli{out: &out}
	require.NoError(t, a.printDevEnv(dir, "", devConfig{printEnv: true, s3port: 3006}))
	s := out.String()

	pfs, err := resolveDevPlan("test", dir, "", devConfig{})
	require.NoError(t, err)
	kp := s3gateway.DeriveKeypair([]byte(devS3Master), devNamespace, string(pfs[0].name))
	require.Contains(t, s, "export AWS_ACCESS_KEY_ID="+kp.AccessKey)
	require.Contains(t, s, "export AWS_SECRET_ACCESS_KEY="+kp.SecretKey)
	require.Contains(t, s, "export AWS_REGION="+devS3Region)
	require.Contains(t, s, "export AWS_ENDPOINT_URL_S3=http://127.0.0.1:3006")
	require.Equal(t, 4, strings.Count(s, "export "), "exactly the four export lines, nothing else")

	// deterministic: a second call yields byte-identical output (fixed dev master).
	var out2 bytes.Buffer
	require.NoError(t, (&cli{out: &out2}).printDevEnv(dir, "", devConfig{printEnv: true, s3port: 3006}))
	require.Equal(t, s, out2.String(), "print-env is deterministic")

	// --s3port defaults to 3006 when unset.
	var out3 bytes.Buffer
	require.NoError(t, (&cli{out: &out3}).printDevEnv(dir, "", devConfig{printEnv: true}))
	require.Contains(t, out3.String(), "http://127.0.0.1:3006", "endpoint port defaults to 3006")
}

// TestIssue135_DevHotReloadsEditedHandler — ADR-0125 Decision 2: an edit to the handler source reaches the
// running dev session with no restart, for an in-place bundle (generic funcdctl.yaml) and an isolated
// single-file function (<stem>.funcdctl.yaml, whose bundle is a private copy).
func TestIssue135_DevHotReloadsEditedHandler(t *testing.T) {
	requireNode(t)
	for _, tc := range []struct{ name, manifest, handler string }{
		{"in-place", "funcdctl.yaml", "handler.mjs"},
		{"stem", "front.funcdctl.yaml", "front.mjs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := devProject(t, map[string]string{
				tc.manifest: "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
				tc.handler:  "export function handle() { return { v: 1 }; }\n",
			})
			inst := runDev(t, dir)
			waitReady(t, inst)
			url := inst.gatewayURL + "/function/" + inst.functions[0]
			_, body := post(t, url, `{"data":{}}`)
			require.Contains(t, body, `"v":1`)

			edited := "export function handle() { return { v: 999 }; }\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, tc.handler), []byte(edited), 0o600))
			require.Eventually(t, func() bool {
				resp, err := http.Post(url, "application/json", strings.NewReader(`{"data":{}}`))
				if err != nil {
					return false
				}
				defer func() { _ = resp.Body.Close() }()
				b, _ := io.ReadAll(resp.Body)
				return strings.Contains(string(b), `"v":999`)
			}, 20*time.Second, 100*time.Millisecond, "the running dev session serves the edited handler")
		})
	}
}

// TestIssue320_DevHotReloadsImportsAndManifest — ADR-0125 boot sequence ("watch files, re-apply on change"): an
// edit to a module the handler imports, or to the manifest's contract and dev block, reaches the running dev
// session with no restart.
func TestIssue320_DevHotReloadsImportsAndManifest(t *testing.T) {
	requireNode(t)
	eventuallyServes := func(t *testing.T, url, payload string, status int, want string) {
		t.Helper()
		require.Eventually(t, func() bool {
			resp, err := http.Post(url, "application/json", strings.NewReader(payload))
			if err != nil {
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode == status && strings.Contains(string(b), want)
		}, 20*time.Second, 100*time.Millisecond, "the running dev session serves the edit: %d %s", status, want)
	}

	t.Run("imported-module", func(t *testing.T) {
		dir := devProject(t, map[string]string{
			"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
			"handler.mjs":   "import { v } from './lib.mjs';\nexport function handle() { return { v }; }\n",
			"lib.mjs":       "export const v = 1;\n",
		})
		inst := runDev(t, dir)
		waitReady(t, inst)
		url := inst.gatewayURL + "/function/" + inst.functions[0]
		_, body := post(t, url, `{"data":{}}`)
		require.Contains(t, body, `"v":1`)

		require.NoError(t, os.WriteFile(filepath.Join(dir, "lib.mjs"), []byte("export const v = 7;\n"), 0o600))
		eventuallyServes(t, url, `{"data":{}}`, http.StatusOK, `"v":7`)
	})

	manifest := func(contract, mode string) string {
		return "runtime: nodejs22\nhandler: handle\nbindings:\n  config:\n    - app-config\n" + contract +
			"dev:\n  config:\n    app-config:\n      APP_MODE: " + mode + "\n"
	}
	for _, tc := range []struct{ name, manifest, handler string }{
		{"manifest-in-place", "funcdctl.yaml", "handler.mjs"},
		{"manifest-stem", "front.funcdctl.yaml", "front.mjs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := devProject(t, map[string]string{
				tc.manifest: manifest(strictContract, "one"),
				tc.handler:  "export function handle() { return { mode: process.env.APP_MODE }; }\n",
			})
			inst := runDev(t, dir)
			waitReady(t, inst)
			url := inst.gatewayURL + "/function/" + inst.functions[0]
			bad, body := post(t, url, `{"data":{"oops":1}}`)
			require.Equal(t, http.StatusUnprocessableEntity, bad.StatusCode, body)
			_, body = post(t, url, `{"data":{"name":"ada"}}`)
			require.Contains(t, body, `"mode":"one"`)

			require.NoError(t, os.WriteFile(filepath.Join(dir, tc.manifest), []byte(manifest(permissiveContract, "two")), 0o600))
			eventuallyServes(t, url, `{"data":{"oops":1}}`, http.StatusOK, `"mode":"two"`)
		})
	}
}
