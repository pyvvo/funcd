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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// requireNode skips a test when node is not on PATH — the hermetic dev lane runs the REAL Node shim
// (no docker/colima), mirroring the existing pkg/funcd node-gated e2e tests.
func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH; skipping the funcdctl dev node lane")
	}
}

// devProject writes files, keyed by slash paths, into a fresh temp folder and returns it.
func devProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeProject(t, dir, files)
	return dir
}

// writeProject writes files, keyed by slash paths, under dir.
func writeProject(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
}

// fnDoc is the Function document name on runtime, its spec lines (indented two spaces) appended.
func fnDoc(name, runtime, spec string) string {
	return "apiVersion: funcd.io/v1alpha1\nkind: Function\nmetadata:\n  name: " + name + "\nspec:\n  runtime: " + runtime +
		"\n  handler: handle\n  image: registry:" + name + "\n" + spec
}

// nodeFunction is the files of a node Function name: its resource file (spec lines appended), its build and its
// permissive contract file.
func nodeFunction(name, spec, build string) map[string]string {
	return map[string]string{
		name + ".yaml":          fnDoc(name, "nodejs22", spec),
		name + ".mjs":           build,
		name + ".contract.yaml": permissiveContract,
	}
}

// tryStartDev boots funcdctl dev over dir and returns the instance with the start error; whatever started is torn
// down at test end.
func tryStartDev(t *testing.T, dir string) (*devInstance, error) {
	t.Helper()
	return tryStartDevTo(t, dir, io.Discard)
}

// tryStartDevTo is tryStartDev with funcdctl's output, where funcdctl dev logs, written to out.
func tryStartDevTo(t *testing.T, dir string, out io.Writer) (*devInstance, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: out}).startDev(ctx, dir, devConfig{cacheDir: t.TempDir()})
	t.Cleanup(func() {
		cancel()
		if inst != nil {
			_ = inst.stop()
		}
	})
	return inst, err
}

// runDev boots funcdctl dev over dir and returns the running instance.
func runDev(t *testing.T, dir string) *devInstance {
	t.Helper()
	inst, err := tryStartDev(t, dir)
	require.NoError(t, err)
	return inst
}

// waitReady blocks until the first dev function reports Ready — proving reconcile → materialize → shim boot →
// readiness probe all passed from the local build, with no push.
func waitReady(t *testing.T, inst *devInstance) {
	t.Helper()
	waitFunction(t, inst, inst.functions[0])
}

// waitFunction blocks until Function name reports Ready.
func waitFunction(t *testing.T, inst *devInstance, name string) *v1.Function {
	t.Helper()
	var fn *v1.Function
	require.Eventually(t, func() bool {
		got, err := inst.client.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
		if err != nil {
			return false
		}
		fn = got.(*v1.Function)
		return fn.Status.Phase == v1.PhaseReady
	}, 20*time.Second, 50*time.Millisecond, "%s reconciles to Ready", name)
	return fn
}

func post(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

// eventuallyServes waits until url answers payload with status and a body holding want.
func eventuallyServes(t *testing.T, url, payload string, status int, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Post(url, "application/json", strings.NewReader(payload))
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode == status && strings.Contains(string(b), want)
	}, 20*time.Second, 100*time.Millisecond, "the running dev session serves %d %s", status, want)
}

const strictContract = `runtime: nodejs22
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

const permissiveContract = `runtime: nodejs22
input:
  type: object
output:
  type: object
`

// scenario: dev-runs-resources — given counter.yaml (a Function declaring its KV store), counter.mjs and
// counter.contract.yaml, `funcdctl dev .` serves counter from the build with its contract enforced (a violating
// payload is 422 before the handler, ADR-0123), its store exists, and a rebuild rolls a new revision.
func TestScenarioDevRunsResources(t *testing.T) {
	requireNode(t)
	dir := devProject(t, map[string]string{
		"counter.yaml": fnDoc("counter", "nodejs22", "  kv:\n    - name: counters\n      tables:\n        - name: hits\n"+
			"  bindings:\n    kv:\n      - alias: hits\n        store: counters\n        table: hits\n"),
		"counter.mjs":           "export function handle(ctx, event) { return { greeting: `hi ${event.data.name}` }; }\n",
		"counter.contract.yaml": strictContract,
	})
	inst := runDev(t, dir)
	first := waitFunction(t, inst, "counter")
	url := inst.gatewayURL + "/function/counter"

	resp, body := post(t, url, `{"data":{"name":"ada"}}`)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, body, "hi ada", "the build serves")
	bad, badBody := post(t, url, `{"data":{"oops":1}}`)
	require.Equal(t, http.StatusUnprocessableEntity, bad.StatusCode, "the contract file is enforced: %s", badBody)

	ks, err := inst.client.Get(context.Background(), v1.KindKVStore, "default", "counters")
	require.NoError(t, err, "the store the Function declares exists")
	require.Equal(t, "hits", ks.(*v1.KVStore).Spec.Tables[0].Name)

	writeProject(t, dir, map[string]string{"counter.mjs": "export function handle() { return { greeting: 'rebuilt' }; }\n"})
	eventuallyServes(t, url, `{"data":{"name":"ada"}}`, http.StatusOK, "rebuilt")
	got, err := inst.client.Get(context.Background(), v1.KindFunction, "default", "counter")
	require.NoError(t, err)
	require.NotEqual(t, first.Status.CurrentRevision, got.(*v1.Function).Status.CurrentRevision, "a rebuild rolls a new revision")
}

// TestDevReloadSeesANewFileFixedAfterAFailedLoad: a resource file added with an error fails the reload, and saving
// it fixed reloads the folder, though the folder was never read with it (the stamps of a failed load cover it).
func TestDevReloadSeesANewFileFixedAfterAFailedLoad(t *testing.T) {
	requireNode(t)
	dir := devProject(t, nodeFunction("a", "", "export function handle() { return {}; }\n"))
	out := &lockedBuffer{}
	inst, err := tryStartDevTo(t, dir, out)
	require.NoError(t, err)
	waitFunction(t, inst, "a")
	extra := "apiVersion: funcd.io/v1alpha1\nkind: ConfigMap\nmetadata:\n  name: extra\nspec:\n  data:\n    a: "
	writeProject(t, dir, map[string]string{"extra.yaml": extra + "[\n"})
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "reload failed") }, 20*time.Second,
		50*time.Millisecond, "the new file's error fails the reload")
	writeProject(t, dir, map[string]string{"extra.yaml": extra + "b\n"})
	require.Eventually(t, func() bool {
		_, gerr := inst.client.Get(context.Background(), v1.KindConfigMap, "default", "extra")
		return gerr == nil
	}, 20*time.Second, 50*time.Millisecond, "saving the new file fixed reloads the folder; dev printed:\n%s", out)
}

// scenario: dev-skips-and-refuses and old-manifest-refused (dev half) — dev skips a FuncdConfig file and documents that
// are not funcd's, and stops on an App (naming the template directory), on two Functions named counter (naming both
// files), on a namespace other than default, on a file that does not parse, and on a funcdctl.yaml (naming what
// replaces it).
func TestScenarioDevSkipsAndRefuses(t *testing.T) {
	t.Parallel()
	counter := nodeFunction("counter", "", "export function handle() {}\n")
	const funcdConfig = "apiVersion: funcd.io/v1alpha1\nkind: FuncdConfig\nserver:\n  listen: 127.0.0.1:8080\n"
	const other = "apiVersion: example.com/v1\nkind: Thing\nmetadata:\n  name: x\n"
	f, err := loadDevFolder("test", devProject(t, merge(counter, map[string]string{"funcdconfig.yaml": funcdConfig, "other.yaml": other})))
	require.NoError(t, err)
	require.Equal(t, []string{"counter"}, f.names(), "the FuncdConfig and foreign documents are skipped")

	for name, tc := range map[string]struct {
		files map[string]string
		want  []string
	}{
		"app": {files: map[string]string{"app.yaml": "apiVersion: funcd.io/v1alpha1\nkind: App\nmetadata:\n  name: todo\nspec:\n  version: 1.0.0\n"},
			want: []string{"template directory"}},
		"duplicate": {files: map[string]string{"again.yaml": fnDoc("counter", "nodejs22", "")},
			want: []string{"counter.yaml", "again.yaml"}},
		"namespace": {files: map[string]string{"ns.yaml": "apiVersion: funcd.io/v1alpha1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: prod\nspec:\n  data:\n    A: b\n"},
			want: []string{`namespace "prod"`}},
		"unparsable":   {files: map[string]string{"bad.yaml": "apiVersion: funcd.io/v1alpha1\nkind: Function\n  bad: [indent\n"}, want: []string{"bad.yaml"}},
		"old-manifest": {files: map[string]string{"funcdctl.yaml": "runtime: nodejs22\n"}, want: []string{"funcdctl.yaml is read nowhere", ".contract.yaml"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := devProject(t, merge(counter, tc.files))
			_, err := loadDevFolder("test", dir)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			for _, w := range tc.want {
				require.Contains(t, err.Error(), w)
			}
		})
	}
	_, err = loadDevFolder("test", filepath.Join(devProject(t, counter), "counter.yaml"))
	require.ErrorContains(t, err, "is not a folder", "a file target is refused")
}

// merge returns the union of maps, later ones winning.
func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// scenario: dev-build-checks — a Function with no build, with builds in both the folder and dist/, or with a runtime
// its contract file does not share stops dev, naming the paths it looked at or both files.
func TestScenarioDevBuildChecks(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		files map[string]string
		want  []string
	}{
		"no-build": {files: map[string]string{"counter.yaml": fnDoc("counter", "nodejs22", ""), "counter.contract.yaml": permissiveContract},
			want: []string{"counter.mjs", filepath.Join("dist", "counter.mjs")}},
		"two-builds": {files: merge(nodeFunction("counter", "", "x\n"), map[string]string{"dist/counter.mjs": "x\n"}),
			want: []string{"two builds", filepath.Join("dist", "counter.mjs")}},
		"runtime-mismatch": {files: map[string]string{"counter.yaml": fnDoc("counter", "nodejs22", ""), "counter.mjs": "x\n",
			"counter.contract.yaml": strings.Replace(permissiveContract, "nodejs22", "python314", 1)},
			want: []string{"counter.yaml", "counter.contract.yaml", "disagree"}},
		"python-no-build": {files: map[string]string{"intake.yaml": fnDoc("intake", "python314", ""),
			"intake.contract.yaml": strings.Replace(permissiveContract, "nodejs22", "python314", 1)},
			want: []string{"intake.py", filepath.Join("src", "main.py")}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := devProject(t, tc.files)
			_, err := loadDevFolder("test", dir)
			require.Error(t, err)
			for _, w := range tc.want {
				require.Contains(t, err.Error(), w)
			}
		})
	}
}

// scenario: dev-secret-from-env — a Function declares Secret api-keys with key STRIPE_KEY and no document defines it:
// `STRIPE_KEY=sk funcdctl dev .` serves sk through ctx.secret; unset, dev stops before booting, naming STRIPE_KEY.
func TestScenarioDevSecretFromEnv(t *testing.T) {
	requireNode(t)
	files := nodeFunction("counter", "  secrets:\n    - name: api-keys\n      keys:\n        - STRIPE_KEY\n"+
		"  bindings:\n    secret:\n      - api-keys\n",
		"export async function handle(ctx) { return { key: await ctx.secret.get('STRIPE_KEY'), env: process.env.STRIPE_KEY ?? null }; }\n")

	t.Run("unset", func(t *testing.T) {
		t.Setenv("STRIPE_KEY", "")
		require.NoError(t, os.Unsetenv("STRIPE_KEY"))
		_, err := (&cli{out: io.Discard}).startDev(context.Background(), devProject(t, files), devConfig{cacheDir: t.TempDir()})
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.Contains(t, err.Error(), "STRIPE_KEY")
	})
	t.Run("set", func(t *testing.T) {
		t.Setenv("STRIPE_KEY", "sk")
		inst := runDev(t, devProject(t, files))
		waitReady(t, inst)
		_, body := post(t, inst.gatewayURL+"/function/counter", `{"data":{}}`)
		require.Contains(t, body, `"key":"sk"`, body)
		require.Contains(t, body, `"env":null`, "secrets are read from funcd, never the env (ADR-0249): %s", body)
	})
}

// scenario: ctx-dev (ADR-0245) on resource files — ctx.config reads the ConfigMap the Function declares at its root,
// and ctx.secret the Secret dev creates from the environment.
func TestScenarioCtxDev(t *testing.T) {
	requireNode(t)
	t.Setenv("API_KEY", "s3cr3t-from-env")
	dir := devProject(t, nodeFunction("counter",
		"  configMaps:\n    - name: app-config\n      data:\n        APP_MODE: prod\n"+
			"  secrets:\n    - name: app-secret\n      keys:\n        - API_KEY\n"+
			"  bindings:\n    config:\n      - app-config\n    secret:\n      - app-secret\n",
		"export async function handle(ctx) {\n  return { mode: ctx.config.get('APP_MODE'), key: await ctx.secret.get('API_KEY') };\n}\n"))
	inst := runDev(t, dir)
	waitReady(t, inst)
	_, body := post(t, inst.gatewayURL+"/function/counter", `{"data":{}}`)
	require.Contains(t, body, `"mode":"prod"`, body)
	require.Contains(t, body, `"key":"s3cr3t-from-env"`, body)
}

// scenario: dev-reload-resources — with dev running, a table added to counter.yaml is applied, and a removed
// ConfigMap file prunes the ConfigMap.
func TestScenarioDevReloadResources(t *testing.T) {
	requireNode(t)
	spec := func(tables string) string {
		return "  kv:\n    - name: counters\n      tables:\n" + tables + "  bindings:\n    kv:\n      - alias: hits\n        store: counters\n        table: hits\n"
	}
	const configMap = "apiVersion: funcd.io/v1alpha1\nkind: ConfigMap\nmetadata:\n  name: settings\nspec:\n  data:\n    MODE: one\n"
	dir := devProject(t, merge(nodeFunction("counter", spec("        - name: hits\n"), "export function handle() { return {}; }\n"),
		map[string]string{"settings.yaml": configMap}))
	inst := runDev(t, dir)
	waitReady(t, inst)
	ctx := context.Background()
	_, err := inst.client.Get(ctx, v1.KindConfigMap, "default", "settings")
	require.NoError(t, err)

	writeProject(t, dir, map[string]string{"counter.yaml": fnDoc("counter", "nodejs22", spec("        - name: hits\n        - name: misses\n"))})
	require.NoError(t, os.Remove(filepath.Join(dir, "settings.yaml")))
	require.Eventually(t, func() bool {
		ks, kerr := inst.client.Get(ctx, v1.KindKVStore, "default", "counters")
		_, cerr := inst.client.Get(ctx, v1.KindConfigMap, "default", "settings")
		return kerr == nil && len(ks.(*v1.KVStore).Spec.Tables) == 2 && fault.KindOf(cerr) == fault.NotFound
	}, 20*time.Second, 100*time.Millisecond, "the added table is applied and the removed ConfigMap pruned")
}

// scenario: dev-writer-grant — dev provisions the write-only dev-blob-writer Role and one RolesAssignment per Function
// and step it runs (ADR-0128 Decision 2, as amended), admission-valid, so the developer writes an ownerless prefix
// through the real forbid (TestScenarioDevInspectBlobViaS3 writes one live), while reads stay binding-gated.
func TestScenarioDevWriterGrant(t *testing.T) {
	t.Parallel()
	objs := writerGrants([]string{"ingest", "orders-score"})
	require.Len(t, objs, 3)
	role := objs[0].(*v1.Role)
	require.NoError(t, role.Validate())
	require.Equal(t, []string{"s3::write"}, role.Spec.Actions, "write only — reads NOT widened")
	for i, name := range []v1.ObjectName{"ingest", "orders-score"} {
		ra := objs[i+1].(*v1.RolesAssignment)
		require.NoError(t, ra.Validate())
		require.Equal(t, "dev-blob-writer-"+name, ra.Name)
		require.Equal(t, &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: name}, ra.Spec.Principal)
		require.Equal(t, v1.RoleRef{Kind: v1.RoleRefKindRole, Name: "dev-blob-writer"}, ra.Spec.Assignments[0].RoleRef)
		require.Equal(t, &v1.ScopeRef{Kind: v1.ScopeKindNamespace}, ra.Spec.Assignments[0].Scope)
	}
}

// scenario: dev-egress-not-isolated — `funcdctl dev` does not reproduce egress isolation (process mode has no netns),
// so an outbound call is NOT blocked; the banner documents that boundary beside every localhost service it exposes.
func TestScenarioDevEgressNotIsolated(t *testing.T) {
	var out bytes.Buffer
	a := &cli{out: &out}
	inst := &devInstance{
		gatewayURL:  "http://127.0.0.1:8080",
		functions:   []string{"ingest", "transform"},
		workflows:   []string{"releve-lakehouse"},
		lakes:       []string{"lake"},
		s3Endpoint:  "http://127.0.0.1:54321",
		s3AccessKey: "AKIADEV",
		s3SecretKey: "devsecret",
		s3Region:    "us-east-1",
	}
	require.NoError(t, a.printBanner(inst))
	got := out.String()
	for _, want := range []string{
		"egress is NOT isolated",
		"SERVICES", "gateway", "http://127.0.0.1:8080",
		"s3", "http://127.0.0.1:54321",
		"lake", "serves: lake",
		"POST  http://127.0.0.1:8080/function/ingest",
		"WORKFLOWS", "releve-lakehouse",
		"AWS_ACCESS_KEY_ID=AKIADEV",
	} {
		require.Contains(t, got, want)
	}
}

// The lake service line appears only when a lake is served.
func TestBannerOmitsLakeWhenUnbound(t *testing.T) {
	var out bytes.Buffer
	inst := &devInstance{gatewayURL: "http://127.0.0.1:8080", functions: []string{"hello"}, s3Endpoint: "http://127.0.0.1:5555", s3Region: "us-east-1"}
	require.NoError(t, (&cli{out: &out}).printBanner(inst))
	require.NotContains(t, out.String(), "lake")
}

// the dev log styler renders a streamed line compactly: time · function · severity · body, with structured attrs
// appended in sorted order. A non-TTY writer (this buffer) yields plain text — color only shows on a real terminal.
func TestPrintLogFormatsLine(t *testing.T) {
	var out bytes.Buffer
	s := newDevLogStyler(&out)
	s.loc = time.UTC
	got := s.format(funcd.LogLine{
		Function: "ingest",
		Severity: "INFO",
		Body:     "greeting",
		Time:     time.Date(2026, 7, 12, 9, 8, 7, 0, time.UTC),
		Attrs:    map[string]string{"name": "Ada", "attempt": "1"},
	})
	for _, want := range []string{"09:08:07", "ingest", "INFO", "greeting", "attempt=1", "name=Ada"} {
		require.Contains(t, got, want)
	}
	require.Less(t, strings.Index(got, "attempt=1"), strings.Index(got, "name=Ada"), "attrs are sorted")
	require.Contains(t, s.format(funcd.LogLine{Function: "f", Body: "x", Time: time.Now()}), "INFO", "a blank severity is INFO")
}

// Issue #1134: a structured record's time is UTC and a plain line's local, and the dev log tee printed each in its own
// zone, so two lines of the same moment read 01:40:55 and 03:40:55 on a UTC+2 host. Both now show in the host's zone.
func TestIssue1134_DevLogTimesShowInOneZone(t *testing.T) {
	at := time.Date(2026, 10, 21, 1, 40, 55, 0, time.UTC)
	s := newDevLogStyler(&bytes.Buffer{})
	s.loc = time.FixedZone("UTC+2", 2*60*60)
	structured := s.format(funcd.LogLine{Function: "fleet-migrate", Body: "x", Time: at})
	plain := s.format(funcd.LogLine{Function: "fleet-api", Body: "y", Time: at.In(s.loc)})
	require.Contains(t, structured, "03:40:55", "a structured record's UTC time shows in the host's zone")
	require.Contains(t, plain, "03:40:55", "a plain line's local time too")
	require.Equal(t, time.Local, newDevLogStyler(&bytes.Buffer{}).loc, "the host's zone is the default")
}

// A streamed record's fields come from the function, so the dev log tee escapes what a terminal would act on and keeps
// each record on one line, also for an unknown severity or a name wider than its column.
func TestDevLogStylerEscapesControlBytes(t *testing.T) {
	got := newDevLogStyler(&bytes.Buffer{}).format(funcd.LogLine{
		Function: "a-function-name-wider-than-its-column",
		Severity: "BOGUS\x1b[2J",
		Body:     "multi\n  09:08:07  ingest       ERROR  forged\x1b]0;title\x07\x00\u009b\u2028end",
		Time:     time.Date(2026, 7, 12, 9, 8, 7, 0, time.UTC),
		Attrs:    map[string]string{"k\x1b": "a\x1b[31mb\nc"},
	})
	require.NotContains(t, got, "\n", "one line per record")
	for _, r := range got {
		require.False(t, r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028, "raw control %U in %q", r, got)
	}
	require.Contains(t, got, "a-function-name-wider-than-its-column")
	require.Contains(t, got, `BOGUS\x1b[2J`)
	require.Contains(t, got, `multi\n  09:08:07  ingest       ERROR  forged\x1b]0;title\a\x00\u009b\u2028end`)
	require.Contains(t, got, `k\x1b=a\x1b[31mb\nc`)
}

// `funcdctl dev --print-env` prints exactly the four AWS export lines with the S3 keypair derived over the folder's
// first Function (the banner's, deterministic across calls), boots no server, and defaults the endpoint port to 3006.
func TestPrintDevEnvDerivesDeterministicKeypair(t *testing.T) {
	dir := devProject(t, nodeFunction("counter", "", "x\n"))
	var out bytes.Buffer
	require.NoError(t, (&cli{out: &out}).printDevEnv(dir, devConfig{printEnv: true, s3port: 3006}))
	s := out.String()
	kp := s3gateway.DeriveKeypair([]byte(devS3Master), v1.KindFunction, devNamespace, "counter")
	require.Contains(t, s, "export AWS_ACCESS_KEY_ID="+kp.AccessKey)
	require.Contains(t, s, "export AWS_SECRET_ACCESS_KEY="+kp.SecretKey)
	require.Contains(t, s, "export AWS_REGION="+devS3Region)
	require.Contains(t, s, "export AWS_ENDPOINT_URL_S3=http://127.0.0.1:3006")
	require.Equal(t, 4, strings.Count(s, "export "))

	var out2 bytes.Buffer
	require.NoError(t, (&cli{out: &out2}).printDevEnv(dir, devConfig{printEnv: true}))
	require.Equal(t, s, out2.String(), "deterministic, with the port defaulting to 3006")
}

// TestIssue320_DevReloadsContract — ADR-0247 Decision 8: an edit to a build's contract file reaches the running session
// with no restart. A python entry's imports are TestScenarioDevPythonInPlace's.
func TestIssue320_DevReloadsContract(t *testing.T) {
	requireNode(t)
	t.Run("contract", func(t *testing.T) {
		dir := devProject(t, merge(nodeFunction("counter", "", "export function handle() { return { ok: true }; }\n"),
			map[string]string{"counter.contract.yaml": strictContract}))
		inst := runDev(t, dir)
		waitReady(t, inst)
		url := inst.gatewayURL + "/function/counter"
		bad, body := post(t, url, `{"data":{"oops":1}}`)
		require.Equal(t, http.StatusUnprocessableEntity, bad.StatusCode, body)

		writeProject(t, dir, map[string]string{"counter.contract.yaml": permissiveContract})
		eventuallyServes(t, url, `{"data":{"oops":1}}`, http.StatusOK, `"ok":true`)
	})
}

// deleteStatus passes every request through and records the response status of a DELETE of one path.
type deleteStatus struct {
	next http.RoundTripper
	path string
	code atomic.Int64
}

func (d *deleteStatus) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := d.next.RoundTrip(r)
	if err == nil && r.Method == http.MethodDelete && r.URL.Path == d.path {
		d.code.Store(int64(resp.StatusCode))
	}
	return resp, err
}

// kvStoreDoc is a KVStore document a with table entries, owned by owner.
func kvStoreDoc(tables ...string) string {
	s := "apiVersion: funcd.io/v1alpha1\nkind: KVStore\nmetadata:\n  name: a\nspec:\n  tables:\n"
	for _, tb := range tables {
		s += "    - name: " + tb + "\n      owner: counter\n"
	}
	return s
}

// TestIssue429_DevReloadDeletesRemovedResources — ADR-0247 Decision 8: a KVStore file removed from the folder deletes
// the store the session applied, and one that still holds keys is kept by its deletion protection (ADR-0073), so a
// reload never drops data.
func TestIssue429_DevReloadDeletesRemovedResources(t *testing.T) {
	requireNode(t)
	bound := nodeFunction("counter", "  kv:\n    - ref: a\n  bindings:\n    kv:\n      - alias: cache\n        store: a\n        table: entries\n",
		"export function handle() { return { ok: true }; }\n")
	unbound := nodeFunction("counter", "", "export function handle() { return { ok: true }; }\n")
	gone := func(inst *devInstance, kind v1.Kind, name v1.ObjectName) bool {
		_, err := inst.client.Get(context.Background(), kind, "default", name)
		return fault.KindOf(err) == fault.NotFound
	}
	unbind := func(t *testing.T, dir string) {
		t.Helper()
		writeProject(t, dir, unbound)
		require.NoError(t, os.Remove(filepath.Join(dir, "store.yaml")))
	}

	t.Run("unused", func(t *testing.T) {
		dir := devProject(t, merge(bound, map[string]string{"store.yaml": kvStoreDoc("entries")}))
		inst := runDev(t, dir)
		waitReady(t, inst)
		require.False(t, gone(inst, v1.KindKVStore, "a"))
		unbind(t, dir)
		require.Eventually(t, func() bool { return gone(inst, v1.KindKVStore, "a") }, 20*time.Second, 100*time.Millisecond,
			"the reload deletes the KVStore the folder no longer holds")
	})

	t.Run("holds-keys", func(t *testing.T) {
		dir := devProject(t, merge(bound, map[string]string{"store.yaml": kvStoreDoc("entries")}))
		root, err := os.MkdirTemp("", "funcd")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		// startDev builds its SDK client from http.DefaultClient, so its transport is the seam on the reload's delete.
		prev := http.DefaultClient.Transport
		next := prev
		if next == nil {
			next = http.DefaultTransport
		}
		del := &deleteStatus{next: next, path: "/apis/funcd.io/v1alpha1/namespaces/default/kvstores/a"}
		http.DefaultClient.Transport = del
		t.Cleanup(func() { http.DefaultClient.Transport = prev })

		ctx, cancel := context.WithCancel(context.Background())
		inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, devConfig{persist: true, persistTo: root})
		require.NoError(t, err)
		t.Cleanup(func() { cancel(); _ = inst.stop() })
		const key = "default/a/entries/k"
		require.NoError(t, inst.kv.Put(ctx, key, []byte("v")))

		unbind(t, dir)
		require.Eventually(t, func() bool { return del.code.Load() != 0 }, 20*time.Second, 100*time.Millisecond,
			"the reload deletes the KVStore the folder no longer holds")
		require.Equal(t, int64(http.StatusConflict), del.code.Load(), "deletion protection refuses a store that holds keys")
		require.False(t, gone(inst, v1.KindKVStore, "a"))
		_, found, err := inst.kv.Get(ctx, key)
		require.NoError(t, err)
		require.True(t, found, "the store's data survives the reload")
	})
}

// TestIssue490_DevReloadDropsBoundTableAndPrefix — ADR-0247 Decision 8: an edit that drops one kv table and renames
// one blob prefix while the store and bucket keep another reaches the running session, and a --persist restart after
// it boots. Admission refuses to drop a table or prefix a Function still binds, so the Function must stop binding it
// first.
func TestIssue490_DevReloadDropsBoundTableAndPrefix(t *testing.T) {
	requireNode(t)
	folder := func(tables, prefixes, kv, blob string) map[string]string {
		return merge(nodeFunction("counter",
			"  kv:\n    - ref: cache\n  buckets:\n    - ref: media\n  bindings:\n    kv:\n      - alias: hot\n        store: cache\n        table: hot\n"+kv+
				"    blob:\n      - alias: raw\n        bucket: media\n        prefix: raw\n"+blob,
			"export function handle() { return { ok: true }; }\n"),
			map[string]string{
				"cache.yaml": "apiVersion: funcd.io/v1alpha1\nkind: KVStore\nmetadata:\n  name: cache\nspec:\n  tables:\n" + tables,
				"media.yaml": "apiVersion: funcd.io/v1alpha1\nkind: Bucket\nmetadata:\n  name: media\nspec:\n  prefixes:\n" + prefixes,
			})
	}
	before := folder("    - name: hot\n      owner: counter\n    - name: cold\n      owner: counter\n",
		"    - name: raw\n      owner:\n        kind: Function\n        name: counter\n    - name: old\n      owner:\n        kind: Function\n        name: counter\n",
		"      - alias: cold\n        store: cache\n        table: cold\n", "      - alias: old\n        bucket: media\n        prefix: old\n")
	after := folder("    - name: hot\n      owner: counter\n", "    - name: raw\n      owner:\n        kind: Function\n        name: counter\n    - name: fresh\n      owner:\n        kind: Function\n        name: counter\n",
		"", "      - alias: fresh\n        bucket: media\n        prefix: fresh\n")
	settled := func(inst *devInstance) bool {
		ctx := context.Background()
		var got []string
		ks, err := inst.client.Get(ctx, v1.KindKVStore, "default", "cache")
		if err != nil {
			return false
		}
		for _, tb := range ks.(*v1.KVStore).Spec.Tables {
			got = append(got, "table "+tb.Name)
		}
		bk, err := inst.client.Get(ctx, v1.KindBucket, "default", "media")
		if err != nil {
			return false
		}
		for _, p := range bk.(*v1.Bucket).Spec.Prefixes {
			got = append(got, "prefix "+p.Name)
		}
		fn, err := inst.client.Get(ctx, v1.KindFunction, "default", "counter")
		if err != nil {
			return false
		}
		for _, b := range fn.(*v1.Function).Spec.Bindings.KV {
			got = append(got, "kv "+b.Table)
		}
		for _, b := range fn.(*v1.Function).Spec.Bindings.Blob {
			got = append(got, "blob "+b.Prefix)
		}
		return slices.Equal(got, []string{"table hot", "prefix raw", "prefix fresh", "kv hot", "blob raw", "blob fresh"})
	}

	t.Run("reload", func(t *testing.T) {
		dir := devProject(t, before)
		inst := runDev(t, dir)
		waitReady(t, inst)
		writeProject(t, dir, after)
		require.Eventually(t, func() bool { return settled(inst) }, 20*time.Second, 100*time.Millisecond,
			"the reload drops the table and the prefix from the Function, the KVStore and the Bucket")
	})

	t.Run("persist-restart", func(t *testing.T) {
		dir := devProject(t, before)
		root, err := os.MkdirTemp("", "funcd")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		cfg := devConfig{persist: true, persistTo: root}

		ctx1, cancel1 := context.WithCancel(context.Background())
		inst1, err := (&cli{out: io.Discard}).startDev(ctx1, dir, cfg)
		require.NoError(t, err)
		cancel1()
		require.NoError(t, inst1.stop())

		writeProject(t, dir, after)
		ctx2, cancel2 := context.WithCancel(context.Background())
		inst2, err := (&cli{out: io.Discard}).startDev(ctx2, dir, cfg)
		require.NoError(t, err, "a restart applies the Function before the KVStore and Bucket drop what it bound")
		t.Cleanup(func() { cancel2(); _ = inst2.stop() })
		require.True(t, settled(inst2), "the restart drops the table and the prefix")
	})
}
