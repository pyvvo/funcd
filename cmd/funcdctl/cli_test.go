package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const (
	devToken    = "dev-secret"
	viewerToken = "viewer-secret"
)

// lockedBuffer is an io.Writer a test reads while other goroutines write to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// execCLI drives the cobra root (ADR-0042) with an injected SDK client (nil for the artifact
// verbs), the test seam replacing the old run(ctx, args, out, c).
func execCLI(out io.Writer, c *sdk.Client, args ...string) error {
	root := newRootCmdWith(out, c)
	root.SetArgs(args)
	return root.Execute()
}

// newClient mounts the real control-plane on httptest and returns an SDK client.
func newClient(t *testing.T) *sdk.Client {
	t.Helper()
	c, _ := newClientURL(t)
	return c
}

// newClientURL is newClient that also returns the server URL, for a raw HTTP request beside the CLI.
func newClientURL(t *testing.T) (*sdk.Client, string) {
	t.Helper()
	c, url, _ := newTestServer(t)
	return c, url
}

// newTestServer is newClientURL that also returns the server's store, to seed what only a reconciler writes.
func newTestServer(t *testing.T) (*sdk.Client, string, store.Store) {
	t.Helper()
	return newTestServerVia(t, nil)
}

// newRacingServer is newTestServer whose first races PUTs to path each run bump first: a controller's status write
// landing between a command's read and its write. It also returns the count of PUTs to path.
func newRacingServer(t *testing.T, path string, races int32, bump func(store.Store)) (*sdk.Client, store.Store, *atomic.Int32) {
	t.Helper()
	var puts atomic.Int32
	c, _, st := newTestServerVia(t, func(next http.Handler, st store.Store) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut && r.URL.Path == path && puts.Add(1) <= races {
				bump(st)
			}
			next.ServeHTTP(w, r)
		})
	})
	return c, st, &puts
}

// newTestServerVia is newTestServer with the server's handler wrapped by wrap, when set.
func newTestServerVia(t *testing.T, wrap func(http.Handler, store.Store) http.Handler) (*sdk.Client, string, store.Store) {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:    {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		viewerToken: {Subject: "viewer", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	st := store.New(memory.New())
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       st,
		Authorizer:  rbac.New(),
		Credentials: creds,
	})
	require.NoError(t, err)
	if wrap != nil {
		h = wrap(h, st)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)
	return c, srv.URL, st
}

func mkfn(ctx context.Context, t *testing.T, c *sdk.Client) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "fn1", "team-a", "rg1"
	_, err := c.Apply(ctx, fn)
	require.NoError(t, err)
}

const goodManifest = `{"apiVersion":"funcd.io/v1alpha1","kind":"Function",` +
	`"metadata":{"name":"fn1","namespace":"team-a","resourceGroup":"rg1"},"spec":{"handler":"h1"}}`

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// scenario: cli-apply-then-get.
func TestScenarioCLIApplyThenGet(t *testing.T) {
	t.Parallel()
	c := newClient(t)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "apply", "-f", writeManifest(t, goodManifest)))
	require.Contains(t, out.String(), "applied Function/fn1")

	out.Reset()
	require.NoError(t, execCLI(&out, c, "get", "function", "fn1", "-n", "team-a"))
	require.Contains(t, out.String(), "fn1")
}

// scenario: cli-delete.
func TestScenarioCLIDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)
	mkfn(ctx, t, c)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "delete", "function", "fn1", "-n", "team-a"))
	require.Contains(t, out.String(), "deleted Function/fn1")

	out.Reset()
	err := execCLI(&out, c, "get", "function", "fn1", "-n", "team-a")
	require.Error(t, err)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}

// scenario: cli-validates-before-apply (missing resourceGroup AND unknown kind, both local).
func TestScenarioCLIValidatesBeforeApply(t *testing.T) {
	t.Parallel()
	c := newClient(t)

	// missing resourceGroup (required for a namespaced kind) → local fault.Invalid, no network.
	bad := `{"apiVersion":"funcd.io/v1alpha1","kind":"Function","metadata":{"name":"fn1","namespace":"team-a"},"spec":{}}`
	var out bytes.Buffer
	err := execCLI(&out, c, "apply", "-f", writeManifest(t, bad))
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))

	// unknown kind → fault.Invalid (never a panic).
	unk := `{"apiVersion":"funcd.io/v1alpha1","kind":"Frobnicate","metadata":{"name":"x"},"spec":{}}`
	err = execCLI(&out, c, "apply", "-f", writeManifest(t, unk))
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// scenario: cli-help-and-completion (ADR-0042) — cobra supplies --help (listing every verb)
// and a completion subcommand, both free, neither hand-written.
func TestScenarioCLIHelpAndCompletion(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	root := newRootCmdWith(&out, nil)
	root.SetArgs([]string{"--help"})
	require.NoError(t, root.Execute())
	help := out.String()
	for _, verb := range []string{"get", "describe", "apply", "delete", "push", "pull", "login", "logout", "completion"} {
		require.Contains(t, help, verb, "--help lists %q", verb)
	}
}

// scenario: cli-flag-compat (ADR-0042) — the short flags survive (as pflag shorthands) and gain
// long forms; an unknown verb is a cobra error.
func TestScenarioCLIFlagCompatAndUnknown(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	mkfn(context.Background(), t, c)

	// long-form --namespace works (the new pflag long flag alongside -n).
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "get", "function", "fn1", "--namespace", "team-a"))
	require.Contains(t, out.String(), "fn1")

	// unknown verb → error (cobra), never a panic.
	require.Error(t, execCLI(&bytes.Buffer{}, c, "frobnicate"))
}

// scenario: cli-push-pull-roundtrip (ADR-0031) — the push/pull verbs round-trip a bundle
// through a local OCI layout (no registry, no SDK client needed).
func TestScenarioCLIPushPullRoundtrip(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "bundle.js")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle() {}\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"

	// contracts are mandatory (ADR-0090): a --schema with a {input, output} document (void here).
	schema := writeSchemaFile(t, `{"input":{"type":"null"},"output":{"type":"null"}}`)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "push", bundle, ref, "--schema", schema))
	printed := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(printed, ref+"@sha256:"), "push prints <ref>@<digest>, got %q", printed)
	digest := strings.TrimPrefix(printed, ref+"@")

	dir := filepath.Join(t.TempDir(), "out")
	out.Reset()
	require.NoError(t, execCLI(&out, nil, "pull", ref, digest, dir))
	path := strings.TrimSpace(out.String())
	got, err := os.ReadFile(path) //nolint:gosec // path is test-owned
	require.NoError(t, err)
	require.Equal(t, "export function handle() {}\n", string(got))
}

// writeSchemaFile writes a single {input, output} contract document (ADR-0090) to a temp file and
// returns its path — the mandatory --schema source for `push`.
func writeSchemaFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// scenario: single-schema-surface (ADR-0090) — the single `--schema <file>` (one {input, output}
// document) is the sole contract surface; the removed `--contract-input`/`--contract-output` flags no
// longer exist; each side is still gated against the funcd profile (ADR-0058/0060) BEFORE packaging.
func TestScenario_single_schema_surface(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle.js")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle() {}\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(dir, "layout") + ":v1"

	inProfile := writeSchemaFile(t, `{"input":{"type":"object","properties":{"id":{"type":"string"}},`+
		`"required":["id"],"additionalProperties":false},"output":{"type":"null"}}`)
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "push", bundle, ref, "--schema", inProfile),
		"an in-profile {input, output} document passes the gate")
	require.True(t, strings.HasPrefix(strings.TrimSpace(out.String()), ref+"@sha256:"))

	// an out-of-profile side (open record) is rejected — nothing is pushed.
	outOfProfile := writeSchemaFile(t, `{"input":{"type":"object","properties":{"id":{"type":"string"}},`+
		`"additionalProperties":true},"output":{"type":"null"}}`)
	out.Reset()
	err := execCLI(&out, nil, "push", bundle, ref, "--schema", outOfProfile)
	require.Error(t, err, "an out-of-profile side is rejected at push")
	require.Contains(t, err.Error(), "profile", "the error names the profile violation")

	// the removed flags no longer exist (cobra rejects an unknown flag).
	out.Reset()
	require.Error(t, execCLI(&out, nil, "push", bundle, ref, "--contract-input", inProfile),
		"--contract-input is removed (ADR-0090)")

	// contracts are mandatory: a push with no --schema fails fault.Invalid.
	out.Reset()
	nerr := execCLI(&out, nil, "push", bundle, ref)
	require.Error(t, nerr, "a single-file push with no contract is refused (contracts are mandatory)")
	require.Equal(t, fault.Invalid, fault.KindOf(nerr), "no contract → fault.Invalid")

	// a document missing a key is rejected (both keys required).
	half := writeSchemaFile(t, `{"input":{"type":"null"}}`)
	out.Reset()
	herr := execCLI(&out, nil, "push", bundle, ref, "--schema", half)
	require.Error(t, herr, "a document missing the output key is rejected")
	require.Equal(t, fault.Invalid, fault.KindOf(herr))
}

// scenario: cli-inspect-reads-contract (ADR-0059/0090) — `push --schema` embeds the mandatory I/O
// contract as OCI metadata; `inspect <ref>@<digest>` reads BOTH sides back (no bundle pull, no run).
func TestScenarioCLIInspectReadsContract(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle.js")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle() {}\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(dir, "layout") + ":v1"
	schema := writeSchemaFile(t, `{"input":{"type":"object","properties":{"name":{"type":"string"}},`+
		`"required":["name"],"additionalProperties":false},"output":{"type":"null"}}`)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "push", bundle, ref, "--schema", schema))
	digest := strings.TrimPrefix(strings.TrimSpace(out.String()), ref+"@")

	out.Reset()
	require.NoError(t, execCLI(&out, nil, "inspect", ref+"@"+digest))
	require.Contains(t, out.String(), `"name"`, "inspect renders the input schema")
	require.Contains(t, out.String(), `"output"`, "inspect always renders both sides (the void output present)")
	require.Contains(t, out.String(), "2020-12", "inspect renders the JSON Schema dialect")
}

// Issue #325: inspect splits off only a trailing @<digest>; an '@' inside a layout path stays in the path.
func TestIssue325_InspectKeepsAtSignInLayoutPath(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle.js")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle() {}\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(dir, "a@b") + ":tag"
	schema := writeSchemaFile(t, `{"input":{"type":"object","properties":{"name":{"type":"string"}},`+
		`"additionalProperties":false},"output":{"type":"null"}}`)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "push", bundle, ref, "--schema", schema))
	digest := strings.TrimPrefix(strings.TrimSpace(out.String()), ref+"@")

	for _, arg := range []string{ref, ref + "@" + digest} {
		out.Reset()
		require.NoError(t, execCLI(&out, nil, "inspect", arg), arg)
		require.Contains(t, out.String(), `"name"`, arg)
	}
	require.NoDirExists(t, filepath.Join(dir, "a"), "inspect creates no layout at the path's prefix")
}

// scenario: cli-push-site (ADR-0139) — `push --site <dir> <ref>` packs a prebuilt web app as a site
// artifact (no --schema needed) and prints <ref>@<digest>; the digest resolves as a site; and --site is
// mutually exclusive with the function flags.
func TestScenarioCLIPushSite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dist")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>bi</title>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":bi"

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "push", "--site", dir, ref))
	printed := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(printed, ref+"@sha256:"), "push --site prints <ref>@<digest>, got %q", printed)
	digest, err := artifact.ResolveSite(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, ref+"@"+digest, printed)

	for _, extra := range [][]string{{"--schema", "x.json"}, {"--runtime", "python314"}, {"--entry", "index.html"}} {
		args := append([]string{"push", "--site", dir, ref}, extra...)
		err := execCLI(&bytes.Buffer{}, nil, args...)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "--site with %v must be rejected", extra)
	}
}

// siteManifest is a Site manifest in the block-style YAML an operator writes (ADR-0139).
const siteManifest = `apiVersion: funcd.io/v1alpha1
kind: Site
metadata:
  name: bi
  namespace: team-a
  resourceGroup: rg1
spec:
  image: oci-layout:///mnt/registry:bi
  bucket:
    name: reports
  prefix: bi
  spa: true
  ingress:
    host: bi.example.com
    public: true
    rules:
      - path: /data
        prefix: gold
`

// scenario: cli-apply-site + prefix-is-immutable at the API (ADR-0139) — `funcdctl apply -f site.yaml`
// creates a Site through the REAL control plane (stampTypeMeta + validate admission + the store), `get`
// reads it back, a re-apply of the same manifest is accepted, and a re-apply that changes spec.prefix is
// refused by the site-prefix-immutable Update admission.
func TestScenarioCLIApplySite(t *testing.T) {
	t.Parallel()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:    {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		viewerToken: {Subject: "viewer", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       store.New(memory.New()),
		Authorizer:  rbac.New(),
		Credentials: creds,
		Admissions:  []admission.Admission{admission.NewSitePrefixImmutableAdmission()},
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "apply", "-f", writeManifest(t, siteManifest)))
	require.Contains(t, out.String(), "applied Site/bi")

	out.Reset()
	require.NoError(t, execCLI(&out, c, "get", "site", "bi", "-n", "team-a", "-o", "json"))
	require.Contains(t, out.String(), `"prefix": "bi"`)
	require.Contains(t, out.String(), `"apiVersion": "funcd.io/v1alpha1"`, "the server stamps TypeMeta for a Site")

	out.Reset()
	require.NoError(t, execCLI(&out, c, "apply", "-f", writeManifest(t, siteManifest)), "re-applying the same manifest is fine")

	moved := strings.Replace(siteManifest, "  prefix: bi\n", "  prefix: reports\n", 1)
	err = execCLI(&bytes.Buffer{}, c, "apply", "-f", writeManifest(t, moved))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a changed spec.prefix is refused at the API")
	require.Contains(t, err.Error(), "spec.prefix is immutable")
}

// appManifest is the ADR-0199 Scenarios fixture App todo.
const appManifest = `apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: team-a
  resourceGroup: rg1
spec:
  version: 1.0.0
  kv:
    - name: todo-store
      tables:
        - name: todos
          owner: todo-api
    - name: todo-cache
      deletion: delete
      tables:
        - name: entries
          owner: todo-api
  buckets:
    - name: todo-files
      prefixes:
        - name: attachments
          owner: todo-api
    - name: todo-tmp
      deletion: delete
  functions:
    - name: todo-api
      runtime: nodejs22
      handler: index.handler
      image: oci-layout://todo-api:1
      scaling:
        minReplicas: 1
      kv:
        - alias: store
          store: todo-store
          table: todos
        - alias: cache
          store: todo-cache
          table: entries
      blob:
        - alias: files
          bucket: todo-files
          prefix: attachments
    - ref: mailer
  workflows:
    - name: todo-plan
      steps:
        - name: due
          function:
            image: oci-layout://todo-due:1
  routes:
    - name: todo-api
      rules:
        - path: /api
          backend:
            function: todo-api
`

// The CLI apply test of ADR-0199 (Implementation plan step 4): `funcdctl apply -f app.yaml` creates an App through the
// real control plane (stampTypeMeta, the validate admission and the store) and a re-apply is accepted; a manifest with
// a bad part or an unknown section is refused before anything is stored.
func TestScenarioCLIApplyApp(t *testing.T) {
	t.Parallel()
	c := newClient(t)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "apply", "-f", writeManifest(t, appManifest)))
	require.Contains(t, out.String(), "applied App/todo")

	out.Reset()
	require.NoError(t, execCLI(&out, c, "get", "app", "todo", "-n", "team-a", "-o", "json"))
	require.Contains(t, out.String(), `"apiVersion": "funcd.io/v1alpha1"`, "the server stamps TypeMeta for an App")
	require.Contains(t, out.String(), `"ref": "mailer"`)
	require.Contains(t, out.String(), `"deletion": "delete"`)

	require.NoError(t, execCLI(&bytes.Buffer{}, c, "apply", "-f", writeManifest(t, appManifest)), "re-applying the same manifest is fine")

	renamed := strings.Replace(appManifest, "  name: todo\n", "  name: todo-bad\n", 1)
	for field, manifest := range map[string]string{
		"spec.kv[0].tables[0].name": strings.Replace(renamed, "- name: todos\n", "- name: Bad_Name\n", 1),
		"spec.functions[2]":         strings.Replace(renamed, "    - ref: mailer\n", "    - ref: mailer\n    - ref: mailer\n", 1),
		"deployments":               strings.Replace(renamed, "spec:\n", "spec:\n  deployments:\n    - name: web\n", 1),
	} {
		err := execCLI(&bytes.Buffer{}, c, "apply", "-f", writeManifest(t, manifest))
		require.Error(t, err, field)
		require.Contains(t, err.Error(), field)
		_, err = c.Get(context.Background(), v1.KindApp, "team-a", "todo-bad")
		require.Equal(t, fault.NotFound, fault.KindOf(err), "%s: nothing is stored", field)
	}
}

// Issue #193: a verb whose only machine format is json must reject any other -o value, not fall
// back to the table with exit 0 (the logs verbs already do).
func TestIssue193_UnknownOutputIsRejected(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	mkfn(context.Background(), t, c)

	for _, args := range [][]string{
		{"get", "function", "fn1", "-n", "team-a", "-o", "yaml"},
		{"get", "function", "-n", "team-a", "-o", "jsn"},
		{"workflow", "runs", "-n", "team-a", "-o", "yaml"},
		{"workflow", "describe", "run1", "-n", "team-a", "-o", "garbage"},
		{"eventing", "dlq", "list", "-n", "team-a", "-o", "yaml"},
	} {
		var out bytes.Buffer
		err := execCLI(&out, c, args...)
		require.Error(t, err, "%v", args)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v: %v", args, err)
		require.Contains(t, err.Error(), "unknown output", "%v", args)
		require.Empty(t, out.String(), "%v prints nothing", args)
	}

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "get", "function", "fn1", "-n", "team-a", "-o", "json"))
	require.Contains(t, out.String(), `"name": "fn1"`)
}

func configMapDoc(name string) string {
	return "apiVersion: funcd.io/v1alpha1\nkind: ConfigMap\nmetadata:\n  name: " + name +
		"\n  namespace: team-a\n  resourceGroup: rg1\nspec:\n  data:\n    k: v\n"
}

// Issue #62: a multi-document YAML manifest applies every document, not only the first; an invalid
// document fails the pre-flight before any document reaches the server.
func TestIssue62_ApplyMultiDocumentAppliesEveryDocument(t *testing.T) {
	t.Parallel()
	c := newClient(t)

	names := []string{"md-first", "md-second", "md-third"}
	multi := "---\n" + configMapDoc(names[0]) + "---\n# a comment-only document is skipped\n---\n" +
		configMapDoc(names[1]) + "---\n" + configMapDoc(names[2]) + "---\n"
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "apply", "-f", writeManifest(t, multi)))
	for _, name := range names {
		require.Contains(t, out.String(), "applied ConfigMap/"+name)
		_, err := c.Get(context.Background(), v1.KindConfigMap, "team-a", v1.ObjectName(name))
		require.NoError(t, err, "ConfigMap %s was applied", name)
	}

	bad := configMapDoc("md-valid") + "---\n" + strings.Replace(configMapDoc("md-bad"), "  resourceGroup: rg1\n", "", 1)
	err := execCLI(&bytes.Buffer{}, c, "apply", "-f", writeManifest(t, bad))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), `"md-bad"`)
	_, err = c.Get(context.Background(), v1.KindConfigMap, "team-a", "md-valid")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no document is applied when one fails the pre-flight")
}

// Issue #316: a server rejection during a multi-document apply names the document (counted as the
// decode errors count it) and the object; apply's help says it stops there and keeps what it applied.
func TestIssue316_ApplyRejectionNamesTheDocument(t *testing.T) {
	t.Parallel()
	c := newClient(t)

	otherNS := strings.Replace(configMapDoc("md-rejected"), "namespace: team-a", "namespace: team-b", 1)
	multi := configMapDoc("md-kept") + "---\n# a comment-only document still counts\n---\n" + otherNS
	var out bytes.Buffer
	err := execCLI(&out, c, "apply", "-f", writeManifest(t, multi))
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "%v", err)
	require.Contains(t, err.Error(), `document 3 (ConfigMap "md-rejected")`)
	require.Contains(t, out.String(), "applied ConfigMap/md-kept")
	_, err = c.Get(context.Background(), v1.KindConfigMap, "team-a", "md-kept")
	require.NoError(t, err, "a document before the rejected one stays applied")

	out.Reset()
	require.NoError(t, execCLI(&out, nil, "apply", "--help"))
	require.Contains(t, out.String(), "stops at the first error")
}

// scenario: force-only-on-resourcegroup — `delete function a --force` fails before any request and a stays;
// `delete rg <name> --force` reaches the force route (here answered 503: the test server wires no collector).
func TestScenarioForceOnlyOnResourceGroup(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	mkfn(ctx, t, c)
	err := execCLI(io.Discard, c, "delete", "function", "fn1", "-n", "team-a", "--force")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = c.Get(ctx, v1.KindFunction, "team-a", "fn1")
	require.NoError(t, err, "fn1 stays")

	rg := &v1.ResourceGroup{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindResourceGroup.GVK().APIVersion(), Kind: v1.KindResourceGroup},
		ObjectMeta: v1.ObjectMeta{Name: "rg1", Namespace: "team-a", ResourceGroup: "rg1"},
	}
	_, err = c.Apply(ctx, rg)
	require.NoError(t, err)
	err = execCLI(io.Discard, c, "delete", "rg", "rg1", "-n", "team-a", "--force")
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "the force query reached the route")
}

// ADR-0172 Decision 2: funcdctl refuses a Revision write before any request. apply -f checks every document offline
// first, so a file with a Revision applies none of its documents.
func TestCLIRevisionIsReadOnly(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	const readOnly = "Revision is read-only: the Function reconciler writes it"
	manifest := `{"apiVersion":"funcd.io/v1alpha1","kind":"ConfigMap",` +
		`"metadata":{"name":"cm1","namespace":"team-a","resourceGroup":"rg1"}}` + "\n---\n" +
		`{"apiVersion":"funcd.io/v1alpha1","kind":"Revision",` +
		`"metadata":{"name":"greeter-1","namespace":"team-a","resourceGroup":"rg1"}}`

	var out bytes.Buffer
	err := execCLI(&out, c, "apply", "-f", writeManifest(t, manifest))
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), readOnly)
	_, gerr := c.Get(context.Background(), v1.KindConfigMap, "team-a", "cm1")
	require.Equal(t, fault.NotFound, fault.KindOf(gerr), "the ConfigMap before the Revision is not applied")

	err = execCLI(&out, c, "delete", "revision", "x", "-n", "team-a")
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), readOnly)
}

// scenario: app-revision-read-only (ADR-0200 Decision 2) — apply -f of a file holding an AppRevision, or delete
// apprevision, fails naming the App reconciler before any request; the documents before it are not applied.
func TestCLIAppRevisionIsReadOnly(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	const readOnly = "AppRevision is read-only: the App reconciler writes it"
	manifest := `{"apiVersion":"funcd.io/v1alpha1","kind":"ConfigMap",` +
		`"metadata":{"name":"cm1","namespace":"team-a","resourceGroup":"rg1"}}` + "\n---\n" +
		`{"apiVersion":"funcd.io/v1alpha1","kind":"AppRevision",` +
		`"metadata":{"name":"todo-1","namespace":"team-a","resourceGroup":"rg1"},` +
		`"spec":{"app":{"kind":"App","name":"todo"},"number":1,"spec":{"version":"1.0.0"}}}`

	var out bytes.Buffer
	err := execCLI(&out, c, "apply", "-f", writeManifest(t, manifest))
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), readOnly)
	_, gerr := c.Get(context.Background(), v1.KindConfigMap, "team-a", "cm1")
	require.Equal(t, fault.NotFound, fault.KindOf(gerr), "the ConfigMap before the AppRevision is not applied")

	err = execCLI(&out, c, "delete", "apprevision", "todo-1", "-n", "team-a")
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), readOnly)
}

// funcdctl delete of a name that is not a DNS label must fail, not delete the object the name's prefix names and
// report success.
func TestIssue698_DeleteRefusesNameThatIsNotALabel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)
	mkfn(ctx, t, c)
	var out bytes.Buffer
	err := execCLI(&out, c, "delete", "function", "fn1#zzz", "-n", "team-a")
	_, gerr := c.Get(ctx, v1.KindFunction, "team-a", "fn1")
	require.NoError(t, gerr, "delete fn1#zzz deleted fn1: err=%v out=%q", err, out.String())
	require.Equal(t, fault.Invalid, fault.KindOf(err), "delete fn1#zzz: err=%v out=%q", err, out.String())
	require.NotContains(t, out.String(), "deleted")
}
