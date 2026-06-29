// Command funcd is the thin shell over the pkg/funcd platform library: it selects
// drivers and runs the platform until a signal arrives (ADR-0014). It holds no
// business logic — all of that lives in pkg/funcd and the internal packages. It wires
// function execution (ADR-0036) so the standalone binary actually runs functions:
// process mode (default; the embedded Node shim) or containerd mode (FUNCD_RUNTIME).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/blob"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/kvstore"
	kvbadger "github.com/green-0-rabbit/funcd/internal/kvstore/badger"
	kvmemory "github.com/green-0-rabbit/funcd/internal/kvstore/memory"
	"github.com/green-0-rabbit/funcd/internal/platform/config"
	"github.com/green-0-rabbit/funcd/internal/platform/observability"
	"github.com/green-0-rabbit/funcd/internal/platform/version"
	"github.com/green-0-rabbit/funcd/internal/runtime/containerd"
	"github.com/green-0-rabbit/funcd/internal/runtime/ctrmanager"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/secrets/aesgcm"
	"github.com/green-0-rabbit/funcd/internal/store"
	badgerstore "github.com/green-0-rabbit/funcd/internal/store/badger"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	shimnode "github.com/green-0-rabbit/funcd/shim/nodejs"
	shimpython "github.com/green-0-rabbit/funcd/shim/python"
)

func main() {
	if err := newRootCmd(os.Stdout).Execute(); err != nil {
		slog.Error("funcd", "error", err)
		os.Exit(1)
	}
}

// newRootCmd builds the funcd daemon command tree (ADR-0042): the root runs the platform; the
// `version` subcommand prints the stamped build identity (ADR-0026) to out (the test seam).
func newRootCmd(out io.Writer) *cobra.Command {
	var memoryOnly bool
	var configPath string
	root := &cobra.Command{
		Use:           "funcd",
		Short:         "funcd — the single-binary serverless platform daemon",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var memoryFlag *bool // nil ⇒ --memory not set (config/default decides the substrate)
			if cmd.Flags().Changed("memory") {
				memoryFlag = &memoryOnly
			}
			return serve(cmd.Context(), configPath, memoryFlag)
		},
	}
	root.Flags().BoolVar(&memoryOnly, "memory", false,
		"run fully in memory (ephemeral — no disk); default is file-backed/durable (ADR-0043)")
	root.Flags().StringVar(&configPath, "config", "",
		"path to funcdconfig.yaml (else $FUNCD_CONFIG, ./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml; ADR-0061)")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the build identity and exit",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(out, version.Get().String())
			return err
		},
	})
	root.AddCommand(newBenchCmd(out))
	root.AddCommand(newInstallCmd(out))
	root.AddCommand(newUninstallCmd(out))
	return root
}

// serve resolves the operator config (funcdconfig.yaml, ADR-0061; precedence flag > env > file >
// default), assembles the platform from it, and runs until a signal arrives. memoryFlag is the
// --memory flag value (nil ⇒ the flag was not set; the config/default decides the substrate).
func serve(parent context.Context, configPath string, memoryFlag *bool) error {
	// Locate + load funcdconfig.yaml into the effective config (file + env + default, validated; ADR-0062).
	path, err := config.Locate(configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path, config.Flags{MemoryOnly: memoryFlag})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.Storage.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", cfg.Storage.DataDir, err)
	}

	// Logger from log.format/level (overrides the preset's logger, ADR-0061 §6).
	logger, err := buildLogger(cfg)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	root := logger.Root()

	opts, closeExec, startKV, substrate, err := buildOptions(parent, cfg, root)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeExec(); cerr != nil {
			root.Warn("funcd: closing execution runtime", "error", cerr)
		}
	}()

	platform, err := funcd.New(opts...)
	if err != nil {
		return fmt.Errorf("assemble platform: %w", err)
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	root.InfoContext(ctx, "funcd starting",
		"version", version.Get().Version, "commit", version.Get().Commit, "substrate", substrate, "config", configSource(path))

	startKV(ctx) // launch the opt-in KV DR export loop (ADR-0067), if enabled — stops when ctx is cancelled

	if err := platform.Run(ctx); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

// buildOptions assembles the daemon's []funcd.Option from the resolved config (ADR-0061): the
// production drivers, the substrate, the store (+ optional at-rest encryptor), the credential, the
// bind addresses, the logger, telemetry, and the execution wiring. It returns the options, the
// execution closer the caller must defer, and the substrate label. The platform owns the drivers.
func buildOptions(ctx context.Context, cfg config.Config, root *slog.Logger) ([]funcd.Option, func() error, func(context.Context), string, error) {
	// Control-plane credential: auth.token / FUNCD_TOKEN, or the built-in dev token + a warning
	// (Production() ships no default token — ADR-0028).
	token := cfg.Auth.Token
	if token == "" {
		token = funcd.DevToken
		root.Warn("funcd: no auth.token / FUNCD_TOKEN — using the built-in dev token (not for production)")
	}

	st, err := buildStore(cfg, root)
	if err != nil {
		return nil, noopClose, nil, "", err
	}

	// Substrate: file-backed (durable) by default, in-memory (ephemeral) with storage.mode: memory (ADR-0043).
	// Built before the KV driver so the opt-in KV CDC (ADR-0068) can publish to the same bus.
	substrateOpts, substrate, theBus, err := substrateOptions(ctx, cfg.Storage.Mode == "memory", cfg.Storage.DataDir)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	kvDriver, startKV, err := buildKVStore(ctx, cfg, theBus, root)
	if err != nil {
		return nil, noopClose, nil, "", err
	}

	// Production() wires the fixed production drivers + the data-plane listener (ADR-0028/0033); the
	// store, substrate, addresses, logger, and execution wiring are resolved from the config.
	opts := []funcd.Option{funcd.Production()}
	opts = append(opts, substrateOpts...)
	opts = append(opts,
		funcd.WithStore(st),
		funcd.WithKVStore(kvDriver),
		funcd.WithKVStoreQuota(cfg.Kvstore.MaxStoresPerNamespace),
		funcd.WithDevAuth(token, cfg.Auth.Namespaces...),
		funcd.WithArtifactStore(filepath.Join(cfg.Storage.DataDir, "artifacts")),
		funcd.WithInvokeSocketDir(filepath.Join(cfg.Storage.DataDir, "invoke")),
		funcd.WithListenAddr(cfg.Server.ListenAddr),
		funcd.WithDataPlaneAddr(cfg.Server.DataPlaneAddr),
		funcd.WithLogger(root),
	)
	// Telemetry: override the preset's no-op pipeline only when an OTLP endpoint is configured.
	if cfg.Telemetry.Endpoint != "" {
		tel, terr := observability.NewTelemetry(ctx,
			observability.TelemetryConfig{Endpoint: cfg.Telemetry.Endpoint, Insecure: cfg.Telemetry.Insecure})
		if terr != nil {
			return nil, noopClose, nil, "", fmt.Errorf("build telemetry: %w", terr)
		}
		opts = append(opts, funcd.WithTelemetry(tel))
	}

	// S3 gateway (ADR-0080/0085): opt-in S3-protocol frontend over the blob substrate.
	if cfg.S3Gateway.Enabled {
		opts = append(opts, funcd.WithS3Gateway(
			cfg.S3Gateway.ListenAddr, cfg.S3Gateway.Endpoint, cfg.S3Gateway.MaxUploadBytes,
			cfg.S3Gateway.MasterSecretFile, cfg.Storage.DataDir))
	}

	execOpts, closeExec, err := executionOptions(ctx, cfg)
	if err != nil {
		return nil, noopClose, nil, "", fmt.Errorf("wire execution: %w", err)
	}
	opts = append(opts, execOpts...)
	return opts, closeExec, startKV, substrate, nil
}

// buildLogger builds the root logger from the resolved log.format/level (ADR-0061 §6).
func buildLogger(cfg config.Config) (*observability.Logger, error) {
	format := observability.FormatJSON
	if cfg.Log.Format == "text" {
		format = observability.FormatText
	}
	return observability.NewLogger(observability.Config{Format: format, Level: parseLevel(cfg.Log.Level)}, os.Stdout)
}

// parseLevel maps a validated level string to a slog.Level (config already rejected bad values).
func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// buildStore constructs the metastore, activating ADR-0022's at-rest encryptor for Secret values
// when secrets.encryptionKeyFile is set. Absent ⇒ no encryptor + a warning that Secret values are
// unencrypted in the durable-store lane (the default in-memory store is ephemeral, ADR-0061 §5).
// buildKVStore selects the function-facing KV driver (ADR-0066/0069): in-memory by default (ephemeral),
// or durable pure-Go Badger at <kvstore.dataDir|<storage.dataDir>/kv> when kvstore.engine: badger. When
// kvstore.backup (ADR-0067) and/or kvstore.cdc (ADR-0068) are enabled it wires those opt-in seams behind
// the driver — DR export to an object-storage target, and a transactional-outbox change-feed to the bus.
// The returned start func launches their loops (a no-op otherwise). Enable-without-target / enable-without-
// sink ⇒ fault.Invalid at startup.
func buildKVStore(ctx context.Context, cfg config.Config, theBus bus.Bus, logger *slog.Logger) (kvstore.KV, func(context.Context), error) {
	noop := func(context.Context) {}
	if cfg.Kvstore.Engine != "badger" {
		return kvmemory.New(), noop, nil
	}
	dir := cfg.Kvstore.DataDir
	if dir == "" {
		dir = filepath.Join(cfg.Storage.DataDir, "kv")
	}
	if !cfg.Kvstore.Backup.Enabled && !cfg.Kvstore.Cdc.Enabled {
		kv, err := kvbadger.Open(dir)
		return kv, noop, err
	}

	var bucket blob.Bucket
	var bcfg kvbadger.BackupConfig
	if cfg.Kvstore.Backup.Enabled {
		if cfg.Kvstore.Backup.Target == "" {
			return nil, noop, fault.Invalidf("buildKVStore", "kvstore.backup.enabled but kvstore.backup.target is empty")
		}
		b, err := gocloud.Open(ctx, cfg.Kvstore.Backup.Target)
		if err != nil {
			return nil, noop, fmt.Errorf("open kv backup target %q: %w", cfg.Kvstore.Backup.Target, err)
		}
		bucket = b
		bcfg = kvbadger.BackupConfig{
			Interval:   parseDurationOr(cfg.Kvstore.Backup.Interval, 30*time.Second),
			Rebaseline: parseDurationOr(cfg.Kvstore.Backup.Rebaseline, 24*time.Hour),
			ChunkBytes: cfg.Kvstore.Backup.ChunkBytes,
		}
	}

	var sink bus.Bus
	var ccfg kvbadger.CDCConfig
	if cfg.Kvstore.Cdc.Enabled {
		if cfg.Kvstore.Cdc.Sink == "" {
			return nil, noop, fault.Invalidf("buildKVStore", "kvstore.cdc.enabled but kvstore.cdc.sink is empty")
		}
		if theBus == nil {
			return nil, noop, fault.Invalidf("buildKVStore", "kvstore.cdc.enabled but no bus is configured")
		}
		sink = theBus
		ccfg = kvbadger.CDCConfig{
			Subject:   bus.Subject(cfg.Kvstore.Cdc.Sink),
			Retention: parseDurationOr(cfg.Kvstore.Cdc.Retention, 24*time.Hour),
		}
	}

	kv, seams, err := kvbadger.OpenWithSeamsFor(dir, bucket, bcfg, sink, ccfg)
	if err != nil {
		if bucket != nil {
			_ = bucket.Close()
		}
		return nil, noop, err
	}
	start := func(runCtx context.Context) {
		if seams.Backup != nil {
			go kvbadger.RunBackup(runCtx, seams.Backup, logger)
		}
		if seams.CDC != nil {
			go kvbadger.RunCDC(runCtx, seams.CDC, logger)
		}
	}
	return kv, start, nil
}

// parseDurationOr parses a Go duration string, falling back to def on empty or invalid input (config
// already validated the surface; this is a defensive default for the optional cadence fields).
func parseDurationOr(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func buildStore(cfg config.Config, log *slog.Logger) (store.Store, error) {
	enc, err := secretEncryptor(cfg)
	if err != nil {
		return nil, err
	}
	var opts []store.Option
	if enc != nil {
		opts = append(opts, store.WithEncryptor([]v1.Kind{v1.KindSecret}, enc))
	} else if cfg.Storage.Mode != "memory" {
		log.Warn("funcd: no secrets.encryptionKeyFile — Secret values are NOT encrypted in the durable-store lane (set a 32-byte key file)")
	}
	// memory = ephemeral (ADR-0043); file = durable pure-Go Badger metastore at <dataDir>/store (ADR-0065).
	if cfg.Storage.Mode == "memory" {
		return store.New(memory.New(), opts...), nil
	}
	eng, err := badgerstore.Open(filepath.Join(cfg.Storage.DataDir, "store"))
	if err != nil {
		return nil, err
	}
	return store.New(eng, opts...), nil
}

// secretEncryptor builds the at-rest Secret encryptor from secrets.encryptionKeyFile (ADR-0022): a
// 32-byte key file → an AES-256-GCM encryptor; "" ⇒ nil (no encryption); a non-32-byte key ⇒ a
// fault.Invalid (never silently weak crypto).
func secretEncryptor(cfg config.Config) (store.Encryptor, error) {
	if cfg.Secrets.EncryptionKeyFile == "" {
		return nil, nil
	}
	key, err := os.ReadFile(cfg.Secrets.EncryptionKeyFile) //nolint:gosec // operator-supplied key path
	if err != nil {
		return nil, fmt.Errorf("read secrets.encryptionKeyFile %s: %w", cfg.Secrets.EncryptionKeyFile, err)
	}
	enc, err := aesgcm.NewAESEncryptor(key) // validates exactly 32 bytes (AES-256)
	if err != nil {
		return nil, fmt.Errorf("secrets encryptor: %w", err)
	}
	return enc, nil
}

// configSource labels where the config came from, for the startup log.
func configSource(path string) string {
	if path == "" {
		return "defaults (no funcdconfig.yaml)"
	}
	return path
}

// substrateOptions builds the blob + bus drivers for the daemon (ADR-0043): in-memory (ephemeral,
// no disk) when memoryOnly, else file-backed under dataDir (durable). It returns the options, the active
// substrate label for the startup log, and the bus (so the opt-in KV CDC can publish to it, ADR-0068).
// The platform owns + closes the drivers.
func substrateOptions(ctx context.Context, memoryOnly bool, dataDir string) ([]funcd.Option, string, bus.Bus, error) {
	if memoryOnly {
		bucket, err := gocloud.Open(ctx, "mem://")
		if err != nil {
			return nil, "", nil, fmt.Errorf("open in-memory blob: %w", err)
		}
		messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
		if err != nil {
			return nil, "", nil, fmt.Errorf("open in-memory bus: %w", err)
		}
		return []funcd.Option{funcd.WithBlob(bucket), funcd.WithBus(messaging)}, "memory", messaging, nil
	}
	blobDir, natsDir := filepath.Join(dataDir, "blob"), filepath.Join(dataDir, "nats")
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return nil, "", nil, fmt.Errorf("create blob dir %s: %w", blobDir, err)
	}
	if err := os.MkdirAll(natsDir, 0o700); err != nil {
		return nil, "", nil, fmt.Errorf("create nats dir %s: %w", natsDir, err)
	}
	bucket, err := gocloud.Open(ctx, "file://"+blobDir)
	if err != nil {
		return nil, "", nil, fmt.Errorf("open file blob: %w", err)
	}
	messaging, err := nats.Open(ctx, nats.Options{Storage: nats.FileStorage, StoreDir: natsDir})
	if err != nil {
		return nil, "", nil, fmt.Errorf("open file bus: %w", err)
	}
	return []funcd.Option{funcd.WithBlob(bucket), funcd.WithBus(messaging)}, "file", messaging, nil
}

// noopClose is the execution closer for the process lane (nothing to tear down).
func noopClose() error { return nil }

// executionOptions selects the runtime driver + function-execution wiring from the resolved
// runtime.mode (ADR-0036/0061): "containerd" → the containerd/crun worker running curated images
// (its lane settings from cfg.Runtime.Containerd); else (default) → the process driver running the embedded
// Node shim. It returns a closer the caller must defer — for containerd mode it stops the
// ctrmanager-supervised private containerd (ADR-0054); for process mode it is a no-op.
func executionOptions(ctx context.Context, cfg config.Config) ([]funcd.Option, func() error, error) {
	if cfg.Runtime.Mode == "containerd" {
		c := cfg.Runtime.Containerd
		// ADR-0054: bring the container runtime up through the Manager. By default it starts +
		// supervises a PRIVATE containerd and imports the embedded curated images; with an external
		// socket set it returns that socket and starts no child. The driver dials whatever Ensure yields.
		mgr, err := ctrmanager.New(ctrmanager.Config{
			ExternalSocket: c.Socket,
			DataRoot:       c.Root,
			ImageOverride:  c.ImageOverride,
		})
		if err != nil {
			return nil, noopClose, fmt.Errorf("build container manager: %w", err)
		}
		socket, err := mgr.Ensure(ctx)
		if err != nil {
			_ = mgr.Close()
			return nil, noopClose, fmt.Errorf("ensure container runtime (private containerd is Linux+root; set runtime.containerd.socket otherwise): %w", err)
		}
		cd, err := containerd.New(containerd.Config{
			Socket:      socket,
			Snapshotter: c.Snapshotter,
			CNIBinDir:   c.CNIBinDir,
			CNIConfDir:  c.CNIConfDir,
			SubnetCIDR:  c.SubnetCIDR,
		})
		if err != nil {
			_ = mgr.Close()
			return nil, noopClose, fmt.Errorf("containerd runtime (runtime.mode: containerd is Linux-only): %w", err)
		}
		imageFor := func(rt string) string { return c.ImagePrefix + rt + ":latest" }
		return []funcd.Option{funcd.WithRuntime(cd), funcd.WithContainerExecution(imageFor)}, mgr.Close, nil
	}

	// process mode (default, cross-platform): run the embedded Node shim on the process driver.
	opts := []funcd.Option{funcd.WithRuntime(process.New())}
	node := envOr("FUNCD_NODE", "")
	if node == "" {
		if p, lerr := exec.LookPath("node"); lerr == nil {
			node = p
		}
	}
	if node == "" {
		slog.Warn("funcd: node not found — functions will NOT execute (control plane only); set FUNCD_NODE or FUNCD_RUNTIME=containerd")
		return opts, noopClose, nil
	}
	shimPath := filepath.Join(cfg.Storage.DataDir, "shim.mjs")
	if werr := os.WriteFile(shimPath, shimnode.Shim, 0o600); werr != nil {
		return nil, noopClose, fmt.Errorf("extract runtime shim to %s: %w", shimPath, werr)
	}
	opts = append(opts, funcd.WithRuntimeShim(node, shimPath))

	// Optional second curated language — the Python shim (ADR-0049). If a python3 is present,
	// extract the embedded (stdlib-only, no pip) shim package and register it for the `python*`
	// runtime family; node functions are unaffected when it is absent.
	python := envOr("FUNCD_PYTHON", "")
	if python == "" {
		if p, lerr := exec.LookPath("python3"); lerr == nil {
			python = p
		}
	}
	if python == "" {
		slog.Info("funcd: python3 not found — python functions will not execute in process mode (set FUNCD_PYTHON); node functions unaffected")
		return opts, noopClose, nil
	}
	shimEntry, poolEntry, perr := shimpython.Extract(filepath.Join(cfg.Storage.DataDir, "shim-python"))
	if perr != nil {
		return nil, noopClose, fmt.Errorf("extract python runtime shim: %w", perr)
	}
	opts = append(opts, funcd.WithRuntimeShimFor("python", python, shimEntry))

	// Python worker pooling (ADR-0050) needs `concurrent.interpreters` (Python ≥3.14). Register the
	// subinterpreter pool host for the python* family only when the interpreter supports it; below
	// 3.14, python functions still run solo (the runtime shim above), they just don't co-pool.
	if pythonAtLeast314(python) {
		opts = append(opts, funcd.WithPoolShimFor("python", python, poolEntry))
	} else {
		slog.Info("funcd: python < 3.14 — python worker pooling disabled (needs concurrent.interpreters); python functions run solo")
	}
	return opts, noopClose, nil
}

// pythonAtLeast314 reports whether the interpreter at path is Python ≥3.14 (the floor for the
// subinterpreter pool host, ADR-0050). Best-effort: a non-zero exit / missing interpreter ⇒ false.
func pythonAtLeast314(python string) bool {
	cmd := exec.Command(python, "-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 14) else 1)")
	return cmd.Run() == nil
}

// envOr returns the env var value or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
