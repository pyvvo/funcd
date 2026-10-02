package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	shimpython "github.com/pyvvo/funcd-python/shim"
	shimnode "github.com/pyvvo/funcd-typescript/shim"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/version"
	fnruntime "github.com/pyvvo/funcd/internal/runtime"
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

// issue 189: a relative storage.dataDir must reach the file substrate as an absolute path under the
// working directory, not as a "file://data/blob" URL whose host swallows the first segment.
func TestIssue189_RelativeDataDirOpensFileSubstrate(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("funcdconfig.yaml", []byte("storage:\n  dataDir: data\n"), 0o600))

	cfg, err := config.Load("funcdconfig.yaml", config.Flags{})
	require.NoError(t, err)

	opts, label, _, err := substrateOptions(context.Background(), false, cfg.Storage.DataDir)
	require.NoError(t, err)
	require.Equal(t, "file", label)
	require.Equal(t, filepath.Join(cwd, "data"), cfg.Storage.DataDir)
	require.Equal(t, filepath.Join(cwd, "data", "store"), cfg.Storage.MetastoreDir)
	all := append([]funcd.Option{
		funcd.Production(),
		funcd.WithStore(store.New(memory.New())),
		funcd.WithRuntime(process.New()),
		funcd.WithDevAuth("t", "default"),
	}, opts...)
	p, err := funcd.New(all...)
	require.NoError(t, err)
	require.NoError(t, p.Shutdown(context.Background()))
	require.DirExists(t, filepath.Join(cwd, "data", "blob"))
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

	// No shim source is left in funcd. Check source FILES, not dirs: an old checkout can keep ignored
	// caches behind (shim/nodejs/node_modules, shim/python/src/funcd_shim/__pycache__).
	for _, src := range []string{"nodejs/shim.mjs", "nodejs/pool.mjs", "nodejs/embed.go", "nodejs/src/shim.ts", "python/embed.go", "python/src/funcd_shim/shim.py"} {
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

	opts, closeExec, err := executionOptions(context.Background(), cfgProcess(dir), slog.New(slog.DiscardHandler))
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

	opts, closeExec, err := executionOptions(context.Background(), cfgProcess(t.TempDir()), slog.New(slog.DiscardHandler))
	require.NoError(t, err, "missing node degrades, never errors")
	t.Cleanup(func() { _ = closeExec() })
	require.Len(t, opts, 1, "only the runtime driver is wired (no shim)")
}

// Issue #184: a python that cannot import the shim (too old for its syntax, or missing
// fastjsonschema) is not registered for the python* family; startup reports why instead. Counts are
// relative to the wiring with no python, so the host's interpreters and node options don't change them.
func TestIssue184_UnusablePythonNotRegistered(t *testing.T) {
	t.Setenv("FUNCD_RUNTIME", "")
	t.Setenv("FUNCD_NODE", "node")
	t.Setenv("PATH", "")
	wire := func(t *testing.T, python string) (int, string) {
		t.Helper()
		t.Setenv("FUNCD_PYTHON", python)
		var logs strings.Builder
		opts, closeExec, err := executionOptions(context.Background(), cfgProcess(t.TempDir()), slog.New(slog.NewTextHandler(&logs, nil)))
		require.NoError(t, err, "an unusable python degrades, never errors")
		t.Cleanup(func() { _ = closeExec() })
		return len(opts), logs.String()
	}
	noPython, _ := wire(t, "")

	for _, tc := range []struct {
		name      string
		script    string
		extraOpts int
		wantLog   string
	}{
		{"cannot-load", "echo \"ModuleNotFoundError: No module named 'fastjsonschema'\" >&2\nexit 1\n", 0, "fastjsonschema"},
		{"loads", "exit 0\n", 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			python := filepath.Join(t.TempDir(), "python3")
			require.NoError(t, os.WriteFile(python, []byte("#!/bin/sh\n"+tc.script), 0o700))
			n, logs := wire(t, python)
			require.Equal(t, noPython+tc.extraOpts, n, "the python shim and pool host are wired only when python can load the shim")
			require.Contains(t, logs, tc.wantLog, "startup names why python functions cannot run")
		})
	}
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
	_, closeExec, err := executionOptions(context.Background(), cfg, slog.New(slog.DiscardHandler))
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
	ctx := context.Background()
	c, dataPlane := startDaemonPlatform(t, funcd.WithArtifactStore(t.TempDir()))

	// push an OCI artifact + apply a Function with NO digest (the platform pins it, ADR-0035).
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle(_, e) { return { echoed: e }; }\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	_, err := artifact.Push(ctx, ref, bundle, nil, "", "")
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

	resp, err := http.Post(dataPlane+"/function/echo", "application/json", strings.NewReader(`{"hi":1}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "invoked over HTTP: %s", body)
	require.Contains(t, string(body), "echoed")
}

// startDaemonPlatform runs a platform assembled the way cmd/funcd assembles it (executionOptions), InMemory for
// ephemeral ports, and returns a client and the data-plane URL.
func startDaemonPlatform(t *testing.T, extra ...funcd.Option) (*sdk.Client, string) {
	t.Helper()
	execOpts, closeExec, err := executionOptions(context.Background(), cfgProcess(t.TempDir()), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeExec() })
	opts := append(append([]funcd.Option{funcd.InMemory()}, extra...), execOpts...)
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
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	return c, "http://" + p.DataPlaneAddr()
}

// The daemon's process mode wires the embedded node pool host (ADR-0046), so two node Functions naming one worker id
// run as handlers of one pool process, and each answers its calls.
func TestIssue36_DaemonPoolsNodeFunctions(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	ctx := context.Background()
	c, dataPlane := startDaemonPlatform(t)
	dir := t.TempDir()
	names := []string{"node-a", "node-b"}
	for _, name := range names {
		handler := filepath.Join(dir, name+".mjs")
		require.NoError(t, os.WriteFile(handler, []byte("export function handle() { return { pid: process.pid }; }\n"), 0o600))
		obj, _ := v1.NewObject(v1.KindFunction)
		fn := obj.(*v1.Function)
		fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
		fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file://"+handler
		fn.Spec.Replicas, fn.Spec.Scaling = 1, v1.Scaling{MinReplicas: 1}
		fn.Spec.Pooling.Worker = "agents"
		_, err := c.Apply(ctx, fn)
		require.NoError(t, err)
	}

	pids := map[string]int{}
	for _, name := range names {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			resp, err := http.Post(dataPlane+"/function/"+name, "application/json", strings.NewReader(`{}`))
			if !assert.NoError(c, err) {
				return
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if !assert.Equal(c, http.StatusOK, resp.StatusCode, "%s answers its calls: %s", name, body) {
				return
			}
			var out struct {
				PID int `json:"pid"`
			}
			if assert.NoError(c, json.Unmarshal(body, &out)) && assert.NotZero(c, out.PID) {
				pids[name] = out.PID
			}
		}, 20*time.Second, 100*time.Millisecond)
	}
	require.Equal(t, pids["node-a"], pids["node-b"], "both handlers run in one pool process")
}

// scenario: file-sets-addresses — a funcdconfig.yaml sets the (previously code-only) control-plane
// + data-plane addresses; the assembled platform binds them (the headline ADR-0061 gap closed).
func TestScenarioFileSetsAddresses(t *testing.T) {
	p := addressesFromConfigFile(t)
	// Production's default is 0.0.0.0:8080; the config set 127.0.0.1:0 → a loopback, ephemeral bind.
	require.True(t, strings.HasPrefix(p.Addr(), "127.0.0.1:"), "config listenAddr drove the control-plane bind, got %s", p.Addr())
	require.NotEqual(t, "0.0.0.0:8080", p.Addr(), "not the Production default")
	require.True(t, strings.HasPrefix(p.DataPlaneAddr(), "127.0.0.1:"), "config dataPlaneAddr drove the data-plane bind, got %s", p.DataPlaneAddr())
}

// Issue #313: the platform the file-sets-addresses scenario assembles is shut down when the test ends, so both
// of its listeners are released.
func TestIssue313_FileSetsAddressesReleasesListeners(t *testing.T) {
	var addrs []string
	t.Run("scenario", func(t *testing.T) {
		p := addressesFromConfigFile(t)
		addrs = []string{p.Addr(), p.DataPlaneAddr()}
	})
	require.Len(t, addrs, 2)
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		require.NoError(t, err, "%s is still bound after the test ended", addr)
		require.NoError(t, ln.Close())
	}
}

// addressesFromConfigFile assembles a platform from a funcdconfig.yaml that sets loopback, ephemeral control-plane
// and data-plane addresses.
func addressesFromConfigFile(t *testing.T) *funcd.Platform {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "funcdconfig.yaml")
	dataDir := shortDataDir(t)
	// loopback + ephemeral port ⇒ deterministic + conflict-free; memory substrate ⇒ zero-infra.
	require.NoError(t, os.WriteFile(path, []byte(
		"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \"127.0.0.1:0\"\n"+
			"storage:\n  mode: memory\n  dataDir: \""+dataDir+"\"\n"), 0o600))

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
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p
}

// TestIssue153_FunclogConfigBlockLoadsAndMaps: the funclog block of ADR-0081/ADR-0101 is a daemon config key. It
// loads (strict decode used to reject it as unknown) and reaches the platform: funclog.enabled: false installs no
// capture hook, and a bad segmentMaxAge fails startup instead of being ignored.
func TestIssue153_FunclogConfigBlockLoadsAndMaps(t *testing.T) {
	root := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name    string
		funclog string
		capture bool
		wantErr bool
	}{
		{"documented-keys", "  enabled: true\n  segmentMaxBytes: 1048576\n  segmentMaxAge: 2s\n  bucket: funcd-system\n  traces: false\n", true, false},
		{"disabled", "  enabled: false\n", false, false},
		{"bad-segment-max-age", "  segmentMaxAge: bogus\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortDataDir(t)
			path := filepath.Join(dir, "funcdconfig.yaml")
			require.NoError(t, os.WriteFile(path, []byte(
				"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \"127.0.0.1:0\"\n"+
					"storage:\n  mode: memory\n  dataDir: \""+dir+"\"\n"+
					"funclog:\n"+tc.funclog), 0o600))
			cfg, err := config.Load(path, config.Flags{})
			require.NoError(t, err, "the funclog block is a known config key")

			opts, closeExec, _, _, err := buildOptions(context.Background(), cfg, root)
			if tc.wantErr {
				require.ErrorContains(t, err, "funclog.segmentMaxAge")
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = closeExec() })

			spy := &captureSpy{Runtime: process.New()}
			p, err := funcd.New(append(opts, funcd.WithRuntime(spy))...)
			require.NoError(t, err)
			t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
			require.Equal(t, tc.capture, spy.installed, "funclog.enabled reaches the platform")
		})
	}
}

// captureSpy records whether the platform installed the funclog capture hook (runtime.LogCapturer, ADR-0081).
type captureSpy struct {
	fnruntime.Runtime
	installed bool
}

func (s *captureSpy) SetLogCapture(fn fnruntime.LogCaptureFunc) { s.installed = fn != nil }

// Issue 192: the fatal startup error and the runtime-detection lines go through the logger built
// from log.format/level (ADR-0061 §6) — none reaches the global slog default.
func TestIssue192_StartupLinesUseConfiguredLogger(t *testing.T) {
	var leaked bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&leaked, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	t.Setenv("FUNCD_RUNTIME", "")
	t.Setenv("FUNCD_NODE", "")
	t.Setenv("PATH", "") // node not found ⇒ the WARN detection line

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	dir := shortDataDir(t)
	path := filepath.Join(dir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \""+busy.Addr().String()+"\"\n"+
			"storage:\n  mode: memory\n  dataDir: \""+dir+"\"\n"+
			"log:\n  format: json\n  level: error\n"), 0o600))

	var out bytes.Buffer
	cmd := newRootCmd(&out)
	cmd.SetArgs([]string{"--config", path})
	require.ErrorContains(t, cmd.Execute(), "bind data-plane listener")

	require.Empty(t, leaked.String(), "a startup line bypassed the configured logger")
	var line struct{ Level, Msg, Error string }
	require.NoError(t, json.Unmarshal(out.Bytes(), &line), "want only the fatal error as one JSON line, got %q", out.String())
	require.Equal(t, "ERROR", line.Level)
	require.Contains(t, line.Error, "bind data-plane listener")
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

// Issue #93: a durable metastore whose stored Secrets do not decode with the configured
// secrets.encryptionKeyFile (a different key, a removed key, or a key added over plaintext Secrets)
// must fail buildStore at startup with an error naming the key setting, not break every Secret read.
func TestIssue93_KeyMismatchFailsAtStartup(t *testing.T) {
	ctx := context.Background()
	root := slog.New(slog.NewTextHandler(io.Discard, nil))
	keyFile := func(seed byte) string {
		key := make([]byte, 32)
		for i := range key {
			key[i] = seed + byte(i)
		}
		f := filepath.Join(t.TempDir(), "secret.key")
		require.NoError(t, os.WriteFile(f, key, 0o600))
		return f
	}
	keyA, keyB := keyFile(1), keyFile(2)
	fileCfg := func(dir, key string) config.Config {
		var c config.Config
		c.Storage.Mode = "file"
		c.Storage.MetastoreDir = dir
		c.Secrets.EncryptionKeyFile = key
		return c
	}
	seed := func(dir, key string) {
		st, err := buildStore(fileCfg(dir, key), root)
		require.NoError(t, err)
		obj, _ := v1.NewObject(v1.KindSecret)
		sec := obj.(*v1.Secret)
		sec.Namespace, sec.ResourceGroup, sec.Name = "default", "rg1", "creds"
		sec.Spec.Data = map[string][]byte{"API_KEY": []byte("s3cr3t")}
		_, err = st.Create(ctx, sec)
		require.NoError(t, err)
		require.NoError(t, st.Close())
	}

	encrypted := t.TempDir()
	seed(encrypted, keyA)
	for name, key := range map[string]string{"different key": keyB, "key removed": ""} {
		_, err := buildStore(fileCfg(encrypted, key), root)
		require.ErrorContains(t, err, "secrets.encryptionKeyFile", name)
	}
	st, err := buildStore(fileCfg(encrypted, keyA), root)
	require.NoError(t, err, "the original key still opens the store")
	require.NoError(t, st.Close())

	plaintext := t.TempDir()
	seed(plaintext, "")
	_, err = buildStore(fileCfg(plaintext, keyA), root)
	require.ErrorContains(t, err, "secrets.encryptionKeyFile", "key added over plaintext Secrets")
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

// shortDataDir is a data dir for an assembled platform. Not t.TempDir(): on macOS its path overruns the Unix socket
// path limit for <dataDir>/invoke, which startup rejects (issue #41).
func shortDataDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
