package main

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	shimpython "github.com/pyvvo/funcd-python/shim"
	shimnode "github.com/pyvvo/funcd-typescript/shim"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario: production-injects-substrate (ADR-0043) — Production() no longer wires blob/bus, so
// New requires them injected (else a missing-port error), like store/runtime.
func TestProductionRequiresSubstrate(t *testing.T) {
	t.Parallel()
	_, err := funcd.New(
		funcd.Production(),
		funcd.WithStore(store.New(memory.New())),
		funcd.WithRuntime(process.New()),
		funcd.WithDevAuth("t", "default"),
	)
	require.Error(t, err, "Production() requires WithBlob + WithBus injected (ADR-0043)")
}

// scenario: daemon-file-default + daemon-memory-flag (ADR-0043) — the daemon's substrate is
// file-backed (durable) by default and in-memory (no disk) with --memory.
func TestDaemonSubstrate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		memoryOnly bool
		label      string
	}{
		{"file-default", false, "file"},
		{"memory-flag", true, "memory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts, label, _, err := substrateOptions(context.Background(), tc.memoryOnly, dir)
			require.NoError(t, err)
			require.Equal(t, tc.label, label)

			// Assemble + shut down so the (file) NATS server is closed, not leaked.
			all := append([]funcd.Option{
				funcd.Production(),
				funcd.WithStore(store.New(memory.New())),
				funcd.WithRuntime(process.New()),
				funcd.WithDevAuth("t", "default"),
			}, opts...)
			p, err := funcd.New(all...)
			require.NoError(t, err)
			require.NoError(t, p.Shutdown(context.Background()))

			entries, _ := os.ReadDir(dir)
			if tc.memoryOnly {
				require.Empty(t, entries, "--memory writes nothing to disk")
			} else {
				require.NotEmpty(t, entries, "file substrate writes blob + nats under the data dir")
			}
		})
	}
}

// scenario: daemon-version-and-serve (ADR-0042) — `funcd version` prints the stamped build
// identity via the cobra root (the root's RunE serves; the version subcommand prints to out).
func TestDaemonVersion(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	root := newRootCmd(&out)
	root.SetArgs([]string{"version"})
	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), version.Get().Version)
}

// scenario: shim-is-self-contained — the Node shim is embedded in the binary (ADR-0036),
// so it ships with no sidecar file.
func TestShimEmbedded(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, shimnode.Shim, "the runtime shim is embedded")
	require.Contains(t, string(shimnode.Shim), "FUNCD_PORT", "it is the ADR-0030/0032 shim contract")
}

// scenario: shim-from-pinned-module (ADR-0141) — the binary embeds the pinned language modules'
// shims byte for byte (the Node shim + pool, and the Python package tree the daemon extracts), and
// funcd itself holds no shim source.
func TestScenarioShimFromPinnedModule(t *testing.T) {
	ts := langmod.Dir(t, langmod.TypeScript)
	for name, embedded := range map[string][]byte{"shim.mjs": shimnode.Shim, "pool.mjs": shimnode.Pool} {
		want, err := os.ReadFile(filepath.Join(ts, "shim", name))
		require.NoError(t, err)
		require.Equal(t, want, embedded, "the embedded %s is the pinned module's", name)
	}

	src := filepath.Join(langmod.Dir(t, langmod.Python), "shim", "src")
	dir := t.TempDir()
	_, _, err := shimpython.Extract(dir)
	require.NoError(t, err)
	extracted := 0
	require.NoError(t, filepath.WalkDir(filepath.Join(dir, "funcd_shim"), func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		rel, rerr := filepath.Rel(dir, p)
		require.NoError(t, rerr)
		got, gerr := os.ReadFile(p)
		require.NoError(t, gerr)
		want, rerr := os.ReadFile(filepath.Join(src, rel))
		require.NoError(t, rerr)
		require.Equal(t, want, got, "the extracted %s is the pinned module's", rel)
		extracted++
		return nil
	}))
	require.Positive(t, extracted, "the Python shim tree was extracted")

	// No shim source is left in funcd. Check the sources, not the dir: an old checkout can keep an
	// ignored shim/nodejs/node_modules behind.
	for _, src := range []string{"nodejs/shim.mjs", "nodejs/pool.mjs", "nodejs/src", "nodejs/embed.go", "python/embed.go", "python/src"} {
		_, err = os.Stat(filepath.Join("..", "..", "shim", src))
		require.ErrorIs(t, err, fs.ErrNotExist, "funcd holds no shim source (shim/%s)", src)
	}
}

// scenario: process-mode extracts the shim — executionOptions (default mode) extracts the
// embedded shim to the data dir and wires the process driver + shim.
func TestExecutionOptionsProcessExtractsShim(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	t.Setenv("FUNCD_RUNTIME", "")
	t.Setenv("FUNCD_NODE", node)
	dir := t.TempDir()

	opts, closeExec, err := executionOptions(context.Background(), cfgProcess(dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeExec() })
	require.NotEmpty(t, opts, "process mode wires the runtime + shim")

	got, err := os.ReadFile(filepath.Join(dir, "shim.mjs"))
	require.NoError(t, err)
	require.Equal(t, shimnode.Shim, got, "the embedded shim was extracted to the data dir")
}

// scenario: node-absent-degrades — with no node the daemon wires no shim (control plane
// only), returning a runtime-only option set without error (no crash).
func TestExecutionOptionsNodeAbsentDegrades(t *testing.T) {
	t.Setenv("FUNCD_RUNTIME", "")
	t.Setenv("FUNCD_NODE", "") // no explicit node
	t.Setenv("PATH", "")       // and none on PATH

	opts, closeExec, err := executionOptions(context.Background(), cfgProcess(t.TempDir()))
	require.NoError(t, err, "missing node degrades, never errors")
	t.Cleanup(func() { _ = closeExec() })
	require.Len(t, opts, 1, "only the runtime driver is wired (no shim)")
}

// scenario: container-mode-selected — FUNCD_RUNTIME=containerd routes through the
// ctrmanager (ADR-0054): with no --containerd it takes the private-managed path, which off
// Linux reports "Linux-only" and on Linux-non-root reports "needs root" → executionOptions
// fails fast either way (no silent no-op).
func TestExecutionOptionsContainerdMode(t *testing.T) {
	// runtime.mode: containerd with no external socket forces the private-managed path (ADR-0054).
	var cfg config.Config
	cfg.Runtime.Mode = "containerd"
	cfg.Storage.DataDir = t.TempDir()
	cfg.Runtime.Containerd.Root = filepath.Join(t.TempDir(), "containerd")
	cfg.Runtime.Containerd.Snapshotter = "overlayfs"
	cfg.Runtime.Containerd.CNIBinDir = "/opt/cni/bin"
	cfg.Runtime.Containerd.CNIConfDir = filepath.Join(t.TempDir(), "cni")
	cfg.Runtime.Containerd.SubnetCIDR = "10.63.0.0/16"
	cfg.Runtime.Containerd.ImagePrefix = "funcd/runtime-"
	_, closeExec, err := executionOptions(context.Background(), cfg)
	if closeExec != nil {
		t.Cleanup(func() { _ = closeExec() })
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		require.Error(t, err, "private containerd fails fast off Linux / without root")
		return
	}
	_ = err // Linux+root: depends on a bundled containerd being present; not asserted in unit
}

// scenario: daemon-executes-process (node-gated) — a platform assembled the way the daemon
// assembles it (the EMBEDDED shim via executionOptions + the artifact store) deploys an OCI
// function with no digest and serves it over HTTP.
func TestDaemonExecutesFunction(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	// Build the platform like cmd/funcd does, but InMemory for ephemeral ports.
	execOpts, closeExec, err := executionOptions(context.Background(), cfgProcess(t.TempDir())) // extracts the embedded shim + WithRuntimeShim
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeExec() })
	opts := append([]funcd.Option{funcd.InMemory(), funcd.WithArtifactStore(t.TempDir())}, execOpts...)
	p, err := funcd.New(opts...)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})

	// push an OCI artifact + apply a Function with NO digest (the platform pins it, ADR-0035).
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle(_, e) { return { echoed: e }; }\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	_, err = artifact.Push(ctx, ref, bundle, nil, "")
	require.NoError(t, err)

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "echo", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image = ref // no digest
	fn.Spec.Replicas, fn.Spec.Scaling = 1, v1.Scaling{MinReplicas: 1}
	_, err = c.Apply(ctx, fn)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		got, gerr := c.Get(ctx, v1.KindFunction, "default", "echo")
		return gerr == nil && got.(*v1.Function).Status.Phase == v1.PhaseReady
	}, 15*time.Second, 50*time.Millisecond, "the daemon-wired platform runs the embedded shim to Ready")

	resp, err := http.Post("http://"+p.DataPlaneAddr()+"/function/echo", "application/json", strings.NewReader(`{"hi":1}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "invoked over HTTP: %s", body)
	require.Contains(t, string(body), "echoed")
}

// scenario: file-sets-addresses — a funcdconfig.yaml sets the (previously code-only) control-plane
// + data-plane addresses; the assembled platform binds them (the headline ADR-0061 gap closed).
func TestScenarioFileSetsAddresses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "funcdconfig.yaml")
	// loopback + ephemeral port ⇒ deterministic + conflict-free; memory substrate ⇒ zero-infra.
	require.NoError(t, os.WriteFile(path, []byte(
		"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \"127.0.0.1:0\"\n"+
			"storage:\n  mode: memory\n  dataDir: \""+dir+"\"\n"), 0o600))

	loc, err := config.Locate(path)
	require.NoError(t, err)
	cfg, err := config.Load(loc, config.Flags{})
	require.NoError(t, err)

	root := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts, closeExec, _, _, err := buildOptions(context.Background(), cfg, root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeExec() })

	p, err := funcd.New(opts...)
	require.NoError(t, err)
	// Production's default is 0.0.0.0:8080; the config set 127.0.0.1:0 → a loopback, ephemeral bind.
	require.True(t, strings.HasPrefix(p.Addr(), "127.0.0.1:"), "config listenAddr drove the control-plane bind, got %s", p.Addr())
	require.NotEqual(t, "0.0.0.0:8080", p.Addr(), "not the Production default")
	require.True(t, strings.HasPrefix(p.DataPlaneAddr(), "127.0.0.1:"), "config dataPlaneAddr drove the data-plane bind, got %s", p.DataPlaneAddr())
}

// scenario: secrets-keyfile-activates-encryption — a 32-byte secrets.encryptionKeyFile wires the
// store's at-rest encryptor (ADR-0022): the value bytes are ciphertext. No key ⇒ unencrypted (+ a
// warning); a non-32-byte key ⇒ rejected (never silently weak).
func TestScenarioSecretsKeyfileActivatesEncryption(t *testing.T) {
	root := slog.New(slog.NewTextHandler(io.Discard, nil))

	// no key ⇒ nil encryptor (unencrypted); buildStore still succeeds (with a warning).
	enc, err := secretEncryptor(config.Config{})
	require.NoError(t, err)
	require.Nil(t, enc, "no keyfile ⇒ no encryptor")
	_, err = buildStore(memCfg(), root)
	require.NoError(t, err)

	// a 32-byte key ⇒ an encryptor whose output is ciphertext (Secret value bytes encrypted at rest).
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	keyFile := filepath.Join(t.TempDir(), "secret.key")
	require.NoError(t, os.WriteFile(keyFile, key, 0o600))
	enc, err = secretEncryptor(cfgWithKeyFile(keyFile))
	require.NoError(t, err)
	require.NotNil(t, enc)
	ct, err := enc.Encrypt(context.Background(), []byte("super-secret-value"))
	require.NoError(t, err)
	require.NotEqual(t, []byte("super-secret-value"), ct, "the stored Secret value bytes are ciphertext")
	_, err = buildStore(cfgWithKeyFile(keyFile), root)
	require.NoError(t, err, "a valid 32-byte key wires the store encryptor")

	// a non-32-byte key ⇒ an error (never silently weak crypto).
	badFile := filepath.Join(t.TempDir(), "bad.key")
	require.NoError(t, os.WriteFile(badFile, []byte("too-short"), 0o600))
	_, err = buildStore(cfgWithKeyFile(badFile), root)
	require.Error(t, err, "a non-32-byte key is rejected")
}

// cfgWithKeyFile builds a Config with only secrets.encryptionKeyFile set (the nested struct can't be
// a flat literal). Memory mode keeps this encryptor-wiring test engine-agnostic — buildStore must not
// open a real Badger directory (ADR-0065) for a test that only checks encryptor selection.
func cfgWithKeyFile(f string) config.Config {
	var c config.Config
	c.Storage.Mode = "memory"
	c.Secrets.EncryptionKeyFile = f
	return c
}

// memCfg is a minimal memory-mode Config (so buildStore uses the in-memory engine, not a Badger dir).
func memCfg() config.Config {
	var c config.Config
	c.Storage.Mode = "memory"
	return c
}

// cfgProcess / cfgContainerd build a minimal Config for the executionOptions tests.
func cfgProcess(dataDir string) config.Config {
	var c config.Config
	c.Storage.DataDir = dataDir
	c.Runtime.Mode = "process"
	return c
}

// the shipped example funcdconfig.yaml loads + resolves cleanly (guards it against drifting).
func TestExampleConfigResolves(t *testing.T) {
	cfg, err := config.Load("../../examples/funcdconfig.yaml", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "memory", cfg.Storage.Mode) // file value (no env tier set) ⇒ deterministic
	require.Equal(t, "127.0.0.1:8080", cfg.Server.ListenAddr)
	require.Equal(t, "text", cfg.Log.Format)
}
