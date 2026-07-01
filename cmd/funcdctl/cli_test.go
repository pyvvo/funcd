package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/controlplane"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

const devToken = "dev-secret"

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
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       store.New(memory.New()),
		Authorizer:  rbac.New(),
		Credentials: creds,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)
	return c
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
