// Command funcd is the thin shell over the pkg/funcd platform library: it selects
// drivers and runs the platform until a signal arrives (ADR-0014). It holds no
// business logic — all of that lives in pkg/funcd and the internal packages. It wires
// function execution (ADR-0036) so the standalone binary actually runs functions:
// process mode (default; the embedded Node shim) or containerd mode (FUNCD_RUNTIME).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"
	_ "time/tzdata" // ADR-0211: cron time zones resolve on a host without zoneinfo

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"net/netip"

	shimpython "github.com/pyvvo/funcd-python/shim"
	shimnode "github.com/pyvvo/funcd-typescript/shim"
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/edge/shape"
	edgetls "github.com/pyvvo/funcd/internal/edge/tls"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvbadger "github.com/pyvvo/funcd/internal/kvstore/badger"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/network"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/platform/stopsignal"
	"github.com/pyvvo/funcd/internal/platform/version"
	fnruntime "github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/containerd"
	"github.com/pyvvo/funcd/internal/runtime/ctrmanager"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/secrets/aesgcm"
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

func main() {
	if err := newRootCmd(os.Stdout).Execute(); err != nil {
		if !errors.As(err, new(loggedError)) {
			slog.Error("funcd", "error", err) // failed before the configured logger existed
		}
		os.Exit(1)
	}
}

// loggedError is a serve failure the configured logger already wrote, so main does not log it again.
type loggedError struct{ error }

// newRootCmd builds the funcd daemon command tree (ADR-0042): the root runs the platform; the
// `version` subcommand prints the stamped build identity (ADR-0026) to out (the test seam), and the
// daemon logs go to out too.
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
			return serve(cmd.Context(), configPath, memoryFlag, out)
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
	root.AddCommand(newRestoreCmd(out))
	root.AddCommand(newBenchCmd(out))
	root.AddCommand(newInstallCmd(out))
	root.AddCommand(newUninstallCmd(out))
	return root
}

// serve resolves the operator config (funcdconfig.yaml, ADR-0061; precedence flag > env > file >
// default), assembles the platform from it, and runs until a signal arrives. memoryFlag is the
// --memory flag value (nil ⇒ the flag was not set; the config/default decides the substrate). The
// logger writes to out; once it exists, serve logs its own failure and returns it as a loggedError.
func serve(parent context.Context, configPath string, memoryFlag *bool, out io.Writer) (err error) {
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
	// ADR-0206 Decision 6: the hold travels with the data; an interrupted restore refuses the start.
	held, err := hold.Open(cfg.Storage.DataDir)
	if err != nil {
		return err
	}

	// Logger from log.format/level (overrides the preset's logger, ADR-0061 §6).
	logger, err := buildLogger(cfg, out)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	root := logger.Root()
	defer func() {
		if err != nil {
			root.ErrorContext(parent, "funcd", "error", err)
			err = loggedError{err}
		}
	}()

	opts, closeExec, startKV, substrate, err := buildOptions(parent, cfg, root)
	if err != nil {
		return err
	}
	opts = append(opts, funcd.WithHold(held))
	defer func() {
		if cerr := closeExec(); cerr != nil {
			root.Warn("funcd: closing execution runtime", "error", cerr)
		}
	}()

	platform, err := funcd.New(opts...)
	if err != nil {
		return fmt.Errorf("assemble platform: %w", err)
	}

	ctx, stop := signal.NotifyContext(parent, stopsignal.Signals()...)
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
// execution closer the caller must defer, and the substrate label. The platform owns the drivers; a failed
// call closes the ones it already opened (issue #437).
func buildOptions(ctx context.Context, cfg config.Config, root *slog.Logger) (_ []funcd.Option, _ func() error, _ func(context.Context), _ string, err error) {
	// Credentials first: a bad token file or entry refuses startup before anything opens (ADR-0171 Decision 3).
	credOpt, err := credentialOption(cfg, root)
	if err != nil {
		return nil, noopClose, nil, "", err
	}

	// The node master secret, loaded once for the platform and the backup envelope (ADR-0204 Decisions 2, 7).
	master, err := loadMaster(cfg, root)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	logBackupFindings(cfg, root)
	sealer, err := backupSealer(cfg, master, root)
	if err != nil {
		return nil, noopClose, nil, "", err
	}

	var opened []io.Closer
	defer func() {
		if err != nil {
			for i := len(opened) - 1; i >= 0; i-- {
				_ = opened[i].Close()
			}
		}
	}()

	st, err := buildStore(cfg, root)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	opened = append(opened, st)

	// Telemetry: override the preset's no-op pipeline only when an OTLP endpoint is configured. Built before the
	// backup runner, whose funcd.backup meter it provides (ADR-0205).
	var tel *observability.Telemetry
	if cfg.Telemetry.Endpoint != "" {
		tel, err = observability.NewTelemetry(ctx,
			observability.TelemetryConfig{Endpoint: cfg.Telemetry.Endpoint, Insecure: cfg.Telemetry.Insecure})
		if err != nil {
			return nil, noopClose, nil, "", fmt.Errorf("build telemetry: %w", err)
		}
		opened = append(opened, telemetryCloser{tel})
	}
	// The backup runner, before the blob and KV wiring that take its Recorder (ADR-0205 Decision 2).
	backups, target, err := backupRunner(ctx, cfg, sealer, backupMeter(tel), root)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	if target != nil {
		opened = append(opened, target)
	}

	// Substrate: file-backed (durable) by default, in-memory (ephemeral) with storage.mode: memory (ADR-0043).
	// Built before the KV driver so the opt-in KV CDC (ADR-0068) can publish to the same bus.
	substrateOpts, substrate, bucket, theBus, err := substrateOptions(ctx, cfg.Storage.Mode == "memory", cfg.Storage.DataDir)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	opened = append(opened, bucket, theBus)
	kvDriver, startKV, err := buildKVStore(ctx, cfg, theBus, root)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	if c, ok := kvDriver.(io.Closer); ok {
		opened = append(opened, c)
	}

	// Production() wires the fixed production drivers + the data-plane listener (ADR-0028/0033); the
	// store, substrate, addresses, logger, and execution wiring are resolved from the config.
	opts := []funcd.Option{funcd.Production()}
	opts = append(opts, substrateOpts...)
	opts = append(opts,
		funcd.WithStore(st),
		funcd.WithKVStore(kvDriver),
		funcd.WithKVStoreQuota(cfg.Kvstore.MaxStoresPerNamespace),
		funcd.WithNestedInFlightCap(cfg.Invoke.MaxNestedInFlight),
		credOpt,
		funcd.WithArtifactStore(filepath.Join(cfg.Storage.DataDir, "artifacts")),
		funcd.WithInvokeSocketDir(filepath.Join(cfg.Storage.DataDir, "invoke")),
		funcd.WithListenAddr(cfg.Server.ListenAddr),
		funcd.WithDataPlaneAddr(cfg.Server.DataPlaneAddr),
		funcd.WithLogger(root),
	)
	if tel != nil {
		opts = append(opts, funcd.WithTelemetry(tel))
	}
	if backups != nil {
		opts = append(opts, funcd.WithPlatformBackup(backups))
	}

	// TLS termination (ADR-0111, F74): opt-in HTTPS on both listeners. The daemon keeps TLS state in
	// <storage.dataDir>/tls.
	if cfg.Server.TLS.Enabled {
		opts = append(opts, funcd.WithTLS(edgetls.Spec{
			Mode:       edgetls.Mode(cfg.Server.TLS.Mode),
			Hosts:      cfg.Server.TLS.Hosts,
			CertFile:   cfg.Server.TLS.CertFile,
			KeyFile:    cfg.Server.TLS.KeyFile,
			Email:      cfg.Server.TLS.Email,
			CADir:      cfg.Server.TLS.CADir,
			StorageDir: filepath.Join(cfg.Storage.DataDir, "tls"),
		}))
	}

	// Ingress protection (ADR-0112, F75): opt-in rate/size/concurrency limits on the data-plane chain.
	if l := cfg.Server.Limits; l.RatePerMin > 0 || l.MaxBodyBytes > 0 || l.MaxInFlight > 0 {
		opts = append(opts, funcd.WithLimits(limitsConfig(cfg)))
	}

	// Edge authn PEP (ADR-0113, F77): opt-in per-target auth-stance enforcement on the data plane.
	if cfg.Server.Auth.Edge {
		opts = append(opts, funcd.WithEdgeAuth())
	}

	// Egress network isolation (ADR-0115, FEAT-0007/F80): opt-in worker-egress default-deny + redirect to
	// the egress gateway (F81). Linux/containerd only (network.New is a no-op elsewhere). The subnet comes
	// from the containerd runtime; the gateway port / resolver / internal allowlist from config.
	if n := cfg.Server.Network; n.Egress {
		subnet, err := netip.ParsePrefix(cfg.Runtime.Containerd.SubnetCIDR)
		if err != nil {
			return nil, noopClose, nil, "", fmt.Errorf("egress isolation: parse worker subnet %q: %w", cfg.Runtime.Containerd.SubnetCIDR, err)
		}
		pol := network.Policy{WorkerSubnet: subnet, GatewayPort: uint16(n.EgressGatewayPort), DNSForwarderPort: uint16(n.DNSForwarderPort)}
		if n.DNSResolver != "" {
			if pol.DNSResolver, err = netip.ParseAddrPort(n.DNSResolver); err != nil {
				return nil, noopClose, nil, "", fmt.Errorf("egress isolation: parse dnsResolver %q: %w", n.DNSResolver, err)
			}
		}
		for _, s := range n.InternalAllow {
			ap, err := netip.ParseAddrPort(s)
			if err != nil {
				return nil, noopClose, nil, "", fmt.Errorf("egress isolation: parse internalAllow %q: %w", s, err)
			}
			pol.InternalAllow = append(pol.InternalAllow, ap)
		}
		opts = append(opts, funcd.WithEgressIsolation(network.New(true, root), pol))
		// Egress gateway + DNS forwarder (ADR-0117, F81): the enforcement point F80 redirects into. Only
		// wired when a forwarder port is configured (the forwarder is mandatory for domain policy).
		if n.DNSForwarderPort != 0 {
			opts = append(opts, funcd.WithEgressGateway(uint16(n.EgressGatewayPort), uint16(n.DNSForwarderPort), pol.DNSResolver))
		}
	}

	// Edge observability (ADR-0114, F76): opt-in RED metrics + edge trace span + access log.
	if o := cfg.Server.Observability; o.Metrics || o.AccessLog || o.Trace {
		opts = append(opts, funcd.WithEdgeObservability(observ.Config{Metrics: o.Metrics, AccessLog: o.AccessLog, Trace: o.Trace}))
	}
	// Edge shaping (ADR-0114, F78): opt-in CORS / response headers / gzip compression.
	if sh := cfg.Server.Shaping; len(sh.CORS.AllowOrigins) > 0 || len(sh.Headers.Set) > 0 || len(sh.Headers.Remove) > 0 || sh.Compression {
		shCfg := shape.Config{Compression: sh.Compression}
		if len(sh.CORS.AllowOrigins) > 0 {
			shCfg.CORS = &shape.CORS{AllowOrigins: sh.CORS.AllowOrigins, AllowMethods: sh.CORS.AllowMethods, AllowHeaders: sh.CORS.AllowHeaders, MaxAgeSeconds: sh.CORS.MaxAgeSeconds}
		}
		if len(sh.Headers.Set) > 0 || len(sh.Headers.Remove) > 0 {
			shCfg.Headers = &shape.Headers{Set: sh.Headers.Set, Remove: sh.Headers.Remove}
		}
		opts = append(opts, funcd.WithEdgeShaping(shCfg))
	}

	opts = append(opts, funcd.WithMasterSecret(master))
	// S3 gateway (ADR-0080/0085): opt-in S3-protocol frontend over the blob substrate.
	if cfg.S3Gateway.Enabled {
		opts = append(opts, funcd.WithS3Gateway(
			cfg.S3Gateway.ListenAddr, cfg.S3Gateway.Endpoint, cfg.S3Gateway.MaxUploadBytes,
			cfg.S3Gateway.MasterSecretFile, cfg.Storage.DataDir))
	}

	// Catalog PEP proxy (ADR-0137): under containerd, publish the proxy on a netns-reachable host (the
	// CNI bridge gateway IP) so a worker in its own netns can reach it; empty ⇒ 127.0.0.1 (process/dev).
	if cfg.Catalog.ProxyHost != "" {
		opts = append(opts, funcd.WithCatalogProxyHost(cfg.Catalog.ProxyHost))
	}

	// Workflow engine (ADR-0094): durable run state in its own Badger instance at Workflow.DataDir
	// (default <dataDir>/workflow; in-memory when the substrate is memory), plus the workflow.* tunables.
	stepTimeout, err := parseDuration("workflow.defaultStepTimeout", cfg.Workflow.DefaultStepTimeout, 0, 0, v1.MaxDuration)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	retention, err := parseDuration("workflow.retention", cfg.Workflow.Retention, 0, 0, v1.MaxDuration)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	workflowDir := ""
	if cfg.Storage.Mode != "memory" {
		workflowDir = cfg.Workflow.DataDir
	}
	opts = append(opts, funcd.WithWorkflow(workflowDir, stepTimeout, retention, cfg.Workflow.DefaultRetry, cfg.Workflow.PayloadLimit),
		funcd.WithWorkflowMaxStepsInFlight(cfg.Workflow.MaxStepsInFlight))
	// ADR-0151: the response deadline of an external invoke whose Function sets no spec.timeout.
	invokeTimeout, err := parseDuration("invoke.defaultTimeout", cfg.Invoke.DefaultTimeout, 0, 0, v1.Duration(v1.MaxInvokeTimeout))
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	opts = append(opts, funcd.WithDefaultInvokeTimeout(invokeTimeout))
	bootInitial, bootMax, err := bootBackoff(cfg)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	opts = append(opts, funcd.WithBootBackoff(bootInitial, bootMax))
	pace, err := pacing(cfg)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	opts = append(opts, funcd.WithPacing(pace), funcd.WithAppRevisionHistory(cfg.App.RevisionHistory))

	// Eventing DLQ + bounded action-delivery retry (ADR-0118, F85): its own dedicated Badger store at
	// Eventing.Deadletter.DataDir (default <dataDir>/deadletter; in-memory when the substrate is memory).
	dlRetention, err := parseDuration("eventing.deadletter.retention", cfg.Eventing.Deadletter.Retention, 0, 0, v1.MaxDuration)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	deadletterDir := ""
	if cfg.Storage.Mode != "memory" {
		deadletterDir = cfg.Eventing.Deadletter.DataDir
	}
	opts = append(opts, funcd.WithDeadLetterQueue(deadletterDir, cfg.Eventing.DeliveryAttempts, dlRetention, cfg.Eventing.Deadletter.MaxEntries),
		funcd.WithSensorDelivery(cfg.Eventing.MaxDeliveriesInFlight, cfg.Eventing.MaxInFlightPerTarget, cfg.Eventing.MaxQueuedPerSensor))

	gcSweep, err := parseDuration("controller.gcSweepInterval", cfg.Controller.GCSweepInterval, 0, minPositive, v1.MaxDuration)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	opts = append(opts, funcd.WithGCSweepInterval(gcSweep))

	// Blob EventSource poll cadence (ADR-0119, F83): the List-poll interval for `blob:` sources.
	blobPoll, err := parseDuration("eventing.blobPollInterval", cfg.Eventing.BlobPollInterval, 0, 0, v1.MaxDuration)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	opts = append(opts, funcd.WithBlobPollInterval(blobPoll))
	// Site default index document (ADR-0139, F103).
	opts = append(opts, funcd.WithSiteDefaultIndex(cfg.Site.DefaultIndex))

	// Function-log capture (ADR-0081) and its traces signal (ADR-0101); a zero segment size/age keeps the sink default.
	segmentMaxAge, err := parseDuration("funclog.segmentMaxAge", cfg.Funclog.SegmentMaxAge, 0, 0, v1.MaxDuration)
	if err != nil {
		return nil, noopClose, nil, "", err
	}
	opts = append(opts, funcd.WithFunclog(segmentMaxAge, cfg.Funclog.SegmentMaxBytes),
		funcd.WithFunclogMaxRecordBytes(cfg.Funclog.MaxRecordBytes))
	if !cfg.Funclog.Enabled {
		opts = append(opts, funcd.WithoutFunclog())
	}
	if !cfg.Funclog.Traces {
		opts = append(opts, funcd.WithoutFunclogTraces())
	}

	execOpts, closeExec, err := executionOptions(ctx, cfg, root)
	if err != nil {
		return nil, noopClose, nil, "", fmt.Errorf("wire execution: %w", err)
	}
	opts = append(opts, execOpts...)
	if target != nil {
		closeRuntime := closeExec
		closeExec = func() error { return errors.Join(closeRuntime(), target.Close()) }
	}
	return opts, closeExec, startKV, substrate, nil
}

// buildLogger builds the root logger from the resolved log.format/level (ADR-0061 §6).
func buildLogger(cfg config.Config, w io.Writer) (*observability.Logger, error) {
	format := observability.FormatJSON
	if cfg.Log.Format == "text" {
		format = observability.FormatText
	}
	return observability.NewLogger(observability.Config{Format: format, Level: parseLevel(cfg.Log.Level)}, w)
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

// buildKVStore selects the function-facing KV driver (ADR-0066/0069): in-memory by default (ephemeral),
// or durable pure-Go Badger at <kvstore.dataDir|<storage.dataDir>/kv> when kvstore.engine: badger and
// storage.mode is file (storage.mode: memory keeps the KV in memory, ADR-0043). When
// kvstore.backup (ADR-0067) and/or kvstore.cdc (ADR-0068) are enabled it wires those opt-in seams behind
// the driver — DR export to an object-storage target, and a transactional-outbox change-feed to the bus.
// The returned start func launches their loops (a no-op otherwise). Enable-without-target / enable-without-
// sink, or either enabled on the memory engine ⇒ fault.Invalid at startup; storage.mode memory ignores them
// with a warning.
func buildKVStore(ctx context.Context, cfg config.Config, theBus bus.Bus, logger *slog.Logger) (kvstore.KV, func(context.Context), error) {
	noop := func(context.Context) {}
	memory, err := checkKVStoreConfig(cfg, logger)
	if err != nil {
		return nil, noop, err
	}
	if memory {
		return kvmemory.New(), noop, nil
	}
	dir := cfg.Kvstore.DataDir // its own dedicated instance; default <dataDir>/kv derived in config.Load
	if !cfg.Kvstore.Backup.Enabled && !cfg.Kvstore.Cdc.Enabled {
		kv, err := kvbadger.Open(dir)
		return kv, noop, err
	}
	bucket, bcfg, err := kvBackup(ctx, cfg, logger)
	if err != nil {
		return nil, noop, err
	}
	sink, ccfg, err := kvCDC(cfg, theBus)
	if err != nil {
		return nil, noop, err
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

// checkKVStoreConfig rejects a kvstore config buildKVStore cannot honor and reports whether the KV runs in
// memory: storage.mode memory (which warns about what it overrides) or an engine other than badger.
func checkKVStoreConfig(cfg config.Config, logger *slog.Logger) (memory bool, err error) {
	if cfg.Kvstore.Backup.Enabled && cfg.Kvstore.Backup.Target == "" {
		return false, fault.Invalidf("buildKVStore", "kvstore.backup.enabled but kvstore.backup.target is empty")
	}
	if cfg.Kvstore.Cdc.Enabled && cfg.Kvstore.Cdc.Sink == "" {
		return false, fault.Invalidf("buildKVStore", "kvstore.cdc.enabled but kvstore.cdc.sink is empty")
	}
	if cfg.Storage.Mode == "memory" {
		if cfg.Kvstore.Engine == "badger" {
			logger.Warn("funcd: storage.mode memory overrides kvstore.engine badger — KV data is in memory and lost on restart")
		}
		if cfg.Kvstore.Backup.Enabled || cfg.Kvstore.Cdc.Enabled {
			logger.Warn("funcd: storage.mode memory ignores kvstore.backup and kvstore.cdc — no KV backup or change feed runs")
		}
		return true, nil
	}
	if cfg.Kvstore.Engine != "badger" {
		if cfg.Kvstore.Backup.Enabled {
			return false, fault.Invalidf("buildKVStore", "kvstore.backup.enabled requires kvstore.engine: badger")
		}
		if cfg.Kvstore.Cdc.Enabled {
			return false, fault.Invalidf("buildKVStore", "kvstore.cdc.enabled requires kvstore.engine: badger")
		}
		return true, nil
	}
	return false, nil
}

// kvBackup opens the kvstore.backup target and resolves its intervals (ADR-0067, ADR-0195); disabled ⇒ no bucket.
func kvBackup(ctx context.Context, cfg config.Config, logger *slog.Logger) (blob.Bucket, kvbadger.BackupConfig, error) {
	if !cfg.Kvstore.Backup.Enabled {
		return nil, kvbadger.BackupConfig{}, nil
	}
	interval, err := parseDuration("kvstore.backup.interval", cfg.Kvstore.Backup.Interval, 30*time.Second, minPositive, v1.MaxDuration)
	if err != nil {
		return nil, kvbadger.BackupConfig{}, err
	}
	rebaseline, err := parseDuration("kvstore.backup.rebaseline", cfg.Kvstore.Backup.Rebaseline, 24*time.Hour, minPositive, v1.MaxDuration)
	if err != nil {
		return nil, kvbadger.BackupConfig{}, err
	}
	retry, err := parseDuration("kvstore.backup.rebaselineRetry", cfg.Kvstore.Backup.RebaselineRetry, time.Hour, minPositive, v1.MaxDuration)
	if err != nil {
		return nil, kvbadger.BackupConfig{}, err
	}
	b, err := gocloud.Open(ctx, cfg.Kvstore.Backup.Target)
	if err != nil {
		return nil, kvbadger.BackupConfig{}, fmt.Errorf("open kv backup target %q: %w", cfg.Kvstore.Backup.Target, err)
	}
	return b, kvbadger.BackupConfig{
		Interval:        interval,
		Rebaseline:      rebaseline,
		RebaselineRetry: retry,
		ChunkBytes:      cfg.Kvstore.Backup.ChunkBytes,
		Logger:          logger,
	}, nil
}

// kvCDC resolves the kvstore.cdc change feed onto the bus (ADR-0068); disabled ⇒ no sink.
func kvCDC(cfg config.Config, theBus bus.Bus) (bus.Bus, kvbadger.CDCConfig, error) {
	if !cfg.Kvstore.Cdc.Enabled {
		return nil, kvbadger.CDCConfig{}, nil
	}
	if theBus == nil {
		return nil, kvbadger.CDCConfig{}, fault.Invalidf("buildKVStore", "kvstore.cdc.enabled but no bus is configured")
	}
	retention, err := parseDuration("kvstore.cdc.retention", cfg.Kvstore.Cdc.Retention, 24*time.Hour, minPositive, v1.MaxDuration)
	if err != nil {
		return nil, kvbadger.CDCConfig{}, err
	}
	return theBus, kvbadger.CDCConfig{
		Subject:   bus.Subject(cfg.Kvstore.Cdc.Sink),
		Retention: retention,
	}, nil
}

// bootBackoff parses runtime.bootBackoffInitial and runtime.bootBackoffMax (ADR-0160): positive durations, the max
// defaulting to max(5m, initial) and, when set, at least the initial wait.
func bootBackoff(cfg config.Config) (initial, limit time.Duration, err error) {
	initial, err = parseDuration("runtime.bootBackoffInitial", cfg.Runtime.BootBackoffInitial, 10*time.Second, minPositive, v1.MaxDuration)
	if err != nil {
		return 0, 0, err
	}
	limit, err = parseDuration("runtime.bootBackoffMax", cfg.Runtime.BootBackoffMax, max(5*time.Minute, initial), minPositive, v1.MaxDuration)
	if err != nil {
		return 0, 0, err
	}
	if limit < initial {
		return 0, 0, fault.Invalidf("buildOptions", "config key %q has value %s, below runtime.bootBackoffInitial %s", "runtime.bootBackoffMax", limit, initial)
	}
	return initial, limit, nil
}

// pacingKey is one ADR-0163 key: its name, raw value, default, where its parsed value goes and its bounds.
type pacingKey struct {
	key, value string
	def        time.Duration
	dst        *time.Duration
	lo, hi     v1.Duration
}

// pacing parses the ADR-0163 keys, app.upgradeTimeout (ADR-0200), runtime.livenessTimeout and the health keys
// (ADR-0215) with parseDuration and their bounds (ADR-0194), then checks ADR-0163 Decision 5's orderings, that a set
// app.upgradeTimeout is more than runtime.bootTimeout, that a set runtime.livenessTimeout is at least twice
// runtime.supervisionPeriod and that health.storageProbeTimeout is less than health.storageProbeInterval, each failure a
// fault.Invalid naming the first key with the other bound. An unset app.upgradeTimeout is max(5m, twice
// runtime.bootTimeout), so one boot retry fits; an unset runtime.livenessTimeout is max(30s, three
// runtime.supervisionPeriod).
func pacing(cfg config.Config) (funcd.Pacing, error) {
	var p funcd.Pacing
	keys := []pacingKey{
		{"controller.retryBackoffMax", cfg.Controller.RetryBackoffMax, time.Second, &p.RetryBackoffMax, minRetryBackoffMax, v1.MaxDuration},
		{"controller.referentPollInterval", cfg.Controller.ReferentPollInterval, 2 * time.Second, &p.ReferentPollInterval, minPositive, v1.MaxDuration},
		{"controller.routeResyncInterval", cfg.Controller.RouteResyncInterval, 10 * time.Second, &p.RouteResyncInterval, minPositive, v1.MaxDuration},
		{"runtime.supervisionPeriod", cfg.Runtime.SupervisionPeriod, 10 * time.Second, &p.SupervisionPeriod, minPositive, v1.MaxDuration},
		{"runtime.bootTimeout", cfg.Runtime.BootTimeout, time.Minute, &p.BootTimeout, minPositive, v1.MaxDuration},
		{"runtime.drainGrace", cfg.Runtime.DrainGrace, 30 * time.Second, &p.DrainGrace, minPositive, v1.MaxDuration},
		{"runtime.handOutSettle", cfg.Runtime.HandOutSettle, 2 * time.Second, &p.HandOutSettle, minPositive, v1.MaxDuration},
		{"runtime.drainPollInterval", cfg.Runtime.DrainPollInterval, time.Second, &p.DrainPollInterval, minPositive, v1.MaxDuration},
		{"catalog.enginePollInterval", cfg.Catalog.EnginePollInterval, 2 * time.Second, &p.EnginePollInterval, minPositive, v1.MaxDuration},
		{"catalog.engineProbeTimeout", cfg.Catalog.EngineProbeTimeout, 2 * time.Second, &p.EngineProbeTimeout, minPositive, v1.MaxDuration},
		{"workflow.artifactPollInterval", cfg.Workflow.ArtifactPollInterval, 5 * time.Second, &p.ArtifactPollInterval, minPositive, v1.MaxDuration},
		{"workflow.defaultRetryBackoff", cfg.Workflow.DefaultRetryBackoff, 0, &p.DefaultRetryBackoff, 0, v1.MaxRetryBackoff},
		{"eventing.bucketRecheckInterval", cfg.Eventing.BucketRecheckInterval, 15 * time.Second, &p.BucketRecheckInterval, minPositive, v1.MaxDuration},
		{"eventing.deliveryBackoffInitial", cfg.Eventing.DeliveryBackoffInitial, 100 * time.Millisecond, &p.DeliveryBackoffInitial, minPositive, v1.MaxDuration},
		{"eventing.deliveryBackoffMax", cfg.Eventing.DeliveryBackoffMax, 0, &p.DeliveryBackoffMax, minPositive, v1.MaxDuration},
		{"invoke.activationTimeout", cfg.Invoke.ActivationTimeout, 30 * time.Second, &p.ActivationTimeout, minPositive, v1.MaxDuration},
		{"invoke.reclaimInterval", cfg.Invoke.ReclaimInterval, 30 * time.Second, &p.ReclaimInterval, minPositive, v1.MaxDuration},
		{"server.shutdownTimeout", cfg.Server.ShutdownTimeout, 15 * time.Second, &p.ShutdownTimeout, minPositive, v1.MaxDuration},
		{"server.network.workerSyncInterval", cfg.Server.Network.WorkerSyncInterval, 2 * time.Second, &p.WorkerSyncInterval, minPositive, v1.MaxDuration},
		{"app.upgradeTimeout", cfg.App.UpgradeTimeout, 0, &p.AppUpgradeTimeout, minPositive, v1.MaxDuration},
		{"runtime.livenessTimeout", cfg.Runtime.LivenessTimeout, 0, &p.LivenessTimeout, minPositive, v1.MaxDuration},
		{"health.storageProbeInterval", cfg.Health.StorageProbeInterval, 10 * time.Second, &p.StorageProbeInterval, minPositive, v1.MaxDuration},
		{"health.storageProbeTimeout", cfg.Health.StorageProbeTimeout, 2 * time.Second, &p.StorageProbeTimeout, minPositive, v1.MaxDuration},
	}
	for _, k := range keys {
		d, err := parseDuration(k.key, k.value, k.def, k.lo, k.hi)
		if err != nil {
			return funcd.Pacing{}, err
		}
		*k.dst = d
	}
	maxSet := p.DeliveryBackoffMax > 0
	if !maxSet {
		p.DeliveryBackoffMax = max(10*time.Second, p.DeliveryBackoffInitial)
	}
	upgradeSet := p.AppUpgradeTimeout > 0
	if !upgradeSet {
		p.AppUpgradeTimeout = math.MaxInt64 // twice a bootTimeout above half the range saturates
		if p.BootTimeout <= math.MaxInt64/2 {
			p.AppUpgradeTimeout = max(5*time.Minute, 2*p.BootTimeout)
		}
	}
	livenessSet := p.LivenessTimeout > 0
	if !livenessSet {
		p.LivenessTimeout = funcd.DefaultLivenessTimeout(p.SupervisionPeriod)
	}
	refuse := func(key string, d time.Duration, want string) error {
		return fault.Invalidf("buildOptions", "config key %q has invalid value %q (want %s)", key, d.String(), want)
	}
	switch {
	case p.BootTimeout <= p.ActivationTimeout:
		return funcd.Pacing{}, refuse("runtime.bootTimeout", p.BootTimeout, "more than invoke.activationTimeout, "+p.ActivationTimeout.String())
	case p.HandOutSettle > p.DrainGrace:
		return funcd.Pacing{}, refuse("runtime.handOutSettle", p.HandOutSettle, "at most runtime.drainGrace, "+p.DrainGrace.String())
	case maxSet && p.DeliveryBackoffMax < p.DeliveryBackoffInitial:
		return funcd.Pacing{}, refuse("eventing.deliveryBackoffMax", p.DeliveryBackoffMax, "at least eventing.deliveryBackoffInitial, "+p.DeliveryBackoffInitial.String())
	case upgradeSet && p.AppUpgradeTimeout <= p.BootTimeout:
		return funcd.Pacing{}, refuse("app.upgradeTimeout", p.AppUpgradeTimeout, "more than runtime.bootTimeout, "+p.BootTimeout.String())
	case livenessSet && p.LivenessTimeout/2 < p.SupervisionPeriod:
		return funcd.Pacing{}, refuse("runtime.livenessTimeout", p.LivenessTimeout, "at least twice runtime.supervisionPeriod, "+p.SupervisionPeriod.String())
	case p.StorageProbeTimeout >= p.StorageProbeInterval:
		return funcd.Pacing{}, refuse("health.storageProbeTimeout", p.StorageProbeTimeout, "less than health.storageProbeInterval, "+p.StorageProbeInterval.String())
	}
	return p, nil
}

// The config duration bounds (ADR-0194): minPositive for a key that must be positive, maxStopGrace for
// runtime.process.stopGrace (the containerd driver's stop grace, ADR-0167) and minRetryBackoffMax for
// controller.retryBackoffMax (ADR-0163, as funcd.WithPacing checks it).
const (
	minPositive        = v1.Duration(time.Millisecond)
	maxStopGrace       = v1.Duration(10 * time.Second)
	minRetryBackoffMax = v1.Duration(5 * time.Millisecond)
)

// processStopGrace parses runtime.process.stopGrace (ADR-0167): 1ms to 10s, default 3s.
func processStopGrace(cfg config.Config) (time.Duration, error) {
	return parseDuration("runtime.process.stopGrace", cfg.Runtime.Process.StopGrace, 3*time.Second, minPositive, maxStopGrace)
}

// parseDuration parses the optional duration at config key with the API grammar and bounds it to [lo, hi]
// (ADR-0194): empty ⇒ def; a malformed or out-of-bounds value ⇒ fault.Invalid naming the key (ADR-0061), never a
// silent fall back to def.
func parseDuration(key, s string, def time.Duration, lo, hi v1.Duration) (time.Duration, error) {
	const op = "buildOptions"
	if s == "" {
		return def, nil
	}
	d, err := v1.ParseDuration(s)
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, op, "config key %q", key)
	}
	if err := v1.CheckDuration(op, fmt.Sprintf("config key %q", key), d, lo, hi); err != nil {
		return 0, err
	}
	return time.Duration(d), nil
}

// limitsConfig maps server.limits to the data-plane limiter's Config (ADR-0112, ADR-0164).
func limitsConfig(cfg config.Config) limit.Config {
	l := cfg.Server.Limits
	return limit.Config{
		RatePerMin:   l.RatePerMin,
		Burst:        l.Burst,
		Key:          limit.Key(l.Key),
		MaxBodyBytes: l.MaxBodyBytes,
		MaxInFlight:  l.MaxInFlight,
		MaxKeys:      l.MaxKeys,
	}
}

// buildStore constructs the metastore, activating ADR-0022's at-rest encryptor for Secret values
// when secrets.encryptionKeyFile is set. Absent ⇒ no encryptor + a warning that Secret values are
// unencrypted in the durable-store lane (the default in-memory store is ephemeral, ADR-0061 §5).
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
	// memory = ephemeral (ADR-0043); file = durable pure-Go Badger metastore at Storage.MetastoreDir
	// (default <dataDir>/store, ADR-0065) — its own dedicated instance.
	if cfg.Storage.Mode == "memory" {
		return store.New(memory.New(), opts...), nil
	}
	eng, err := badgerstore.Open(cfg.Storage.MetastoreDir)
	if err != nil {
		return nil, err
	}
	st := store.New(eng, opts...)
	if err := checkSecretsDecode(st, enc != nil); err != nil {
		_ = st.Close()
		return nil, err
	}
	return st, nil
}

// checkSecretsDecode reads every stored Secret once at startup, so a secrets.encryptionKeyFile that
// does not match how the durable store's Secrets were written (issue #93) stops funcd with a clear
// error instead of failing every Secret read, write and delete later.
func checkSecretsDecode(st store.Store, keyed bool) error {
	_, err := st.List(context.Background(), v1.KindSecret.GVK(), store.ListOptions{})
	switch {
	case err == nil:
		return nil
	case keyed:
		return fmt.Errorf("stored Secrets do not decrypt with secrets.encryptionKeyFile: they were written with a different key or with none — restore the setting they were written with: %w", err)
	default:
		return fmt.Errorf("stored Secrets are encrypted but secrets.encryptionKeyFile is not set — set it to the key they were written with: %w", err)
	}
}

// loadMaster migrates a working-directory master and loads the node master secret from its place, gateway on or off
// (ADR-0204 Decision 7).
func loadMaster(cfg config.Config, log *slog.Logger) ([]byte, error) {
	file, dataDir := cfg.S3Gateway.MasterSecretFile, cfg.Storage.DataDir
	if err := s3gateway.MigrateMaster(file, dataDir, cfg.S3Gateway.Enabled, log); err != nil {
		return nil, err
	}
	return s3gateway.LoadOrCreateMaster(file, dataDir)
}

// logBackupFindings logs config.CheckBackup's warnings, one line each (ADR-0205 Decision 3); Validate refused its
// errors. With a target, envelope.New logs ADR-0204 Decision 2's two encryption warnings itself, so they are not
// repeated here.
func logBackupFindings(cfg config.Config, log *slog.Logger) {
	sealed := cfg.Backup.Target != ""
	for _, f := range cfg.CheckBackup() {
		if f.Error || sealed && (f.Key == "backup.encryption.none" || f.Key == "backup.encryption.recipients") {
			continue
		}
		log.Warn(f.Message, "key", f.Key)
	}
}

// backupSealer applies ADR-0204 Decision 2's start rules when backup.target is set: a violation refuses the start,
// and the keys' fingerprints are logged. nil without a target.
func backupSealer(cfg config.Config, master []byte, log *slog.Logger) (*envelope.Sealer, error) {
	if cfg.Backup.Target == "" {
		return nil, nil
	}
	key, err := readSecretsKey(cfg)
	if err != nil {
		return nil, err
	}
	return envelope.New(envelope.Config{
		Recipients: cfg.Backup.Encryption.Recipients,
		None:       cfg.Backup.Encryption.None,
		SecretsKey: key,
		Master:     master,
		Logger:     log,
	})
}

// backupRunner builds the backup runner when the platform backup (backup.target with storage.mode file) or the KV
// export is on (ADR-0205 Decision 2), nil otherwise; with the platform backup it opens the target, which the caller
// closes. A backup.Open error refuses the start.
func backupRunner(ctx context.Context, cfg config.Config, sealer *envelope.Sealer, meter metric.Meter, log *slog.Logger) (*runner.Runner, backup.Target, error) {
	file := cfg.Storage.Mode == "file"
	platform, kv := file && cfg.Backup.Target != "", file && cfg.Kvstore.Backup.Enabled
	if !platform && !kv {
		return nil, nil, nil
	}
	times, err := cfg.BackupTimes()
	if err != nil {
		return nil, nil, err
	}
	rc := runner.Config{Times: times, Meter: meter, Logger: log}
	if platform {
		r := cfg.Backup.Retention
		rc.Target, err = backup.Open(ctx, backup.Config{
			Target:          cfg.Backup.Target,
			CredentialsFile: cfg.Backup.CredentialsFile,
			DataDir:         cfg.Storage.DataDir,
			SingleWriter:    cfg.Backup.SingleWriter,
			Retention:       backup.Retention{Hourly: r.Hourly, Daily: r.Daily, Weekly: r.Weekly, Verified: r.Verified},
			Logger:          log,
		})
		if err != nil {
			return nil, nil, err
		}
		rc.Sealer = sealer
	}
	runs, err := runner.New(rc)
	if err != nil {
		if rc.Target != nil {
			_ = rc.Target.Close()
		}
		return nil, nil, err
	}
	return runs, rc.Target, nil
}

// backupMeter is the backup runner's meter (ADR-0205 Decision 5); a no-op one without telemetry.
func backupMeter(t *observability.Telemetry) metric.Meter {
	if t == nil {
		return metricnoop.NewMeterProvider().Meter("funcd.backup")
	}
	return t.MeterProvider().Meter("funcd.backup")
}

// readSecretsKey reads secrets.encryptionKeyFile; "" ⇒ nil.
func readSecretsKey(cfg config.Config) ([]byte, error) {
	if cfg.Secrets.EncryptionKeyFile == "" {
		return nil, nil
	}
	key, err := os.ReadFile(cfg.Secrets.EncryptionKeyFile) //nolint:gosec // operator-supplied key path
	if err != nil {
		return nil, fmt.Errorf("read secrets.encryptionKeyFile %s: %w", cfg.Secrets.EncryptionKeyFile, err)
	}
	return key, nil
}

// secretEncryptor builds the at-rest Secret encryptor from secrets.encryptionKeyFile (ADR-0022): a
// 32-byte key file → an AES-256-GCM encryptor; "" ⇒ nil (no encryption); a non-32-byte key ⇒ a
// fault.Invalid (never silently weak crypto).
func secretEncryptor(cfg config.Config) (store.Encryptor, error) {
	key, err := readSecretsKey(cfg)
	if err != nil || key == nil {
		return nil, err
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
// substrate label for the startup log, and the bucket and bus (so the opt-in KV CDC can publish to the bus,
// ADR-0068, and a failed buildOptions can close both). The platform owns + closes the drivers.
func substrateOptions(ctx context.Context, memoryOnly bool, dataDir string) ([]funcd.Option, string, blob.Bucket, bus.Bus, error) {
	if memoryOnly {
		bucket, err := gocloud.Open(ctx, "mem://")
		if err != nil {
			return nil, "", nil, nil, fmt.Errorf("open in-memory blob: %w", err)
		}
		messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
		if err != nil {
			return nil, "", nil, nil, fmt.Errorf("open in-memory bus: %w", err)
		}
		return []funcd.Option{funcd.WithBlob(bucket), funcd.WithBus(messaging)}, "memory", bucket, messaging, nil
	}
	blobDir, natsDir := filepath.Join(dataDir, "blob"), filepath.Join(dataDir, "nats")
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return nil, "", nil, nil, fmt.Errorf("create blob dir %s: %w", blobDir, err)
	}
	if err := os.MkdirAll(natsDir, 0o700); err != nil {
		return nil, "", nil, nil, fmt.Errorf("create nats dir %s: %w", natsDir, err)
	}
	bucket, err := gocloud.Open(ctx, gocloud.FileURL(blobDir))
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("open file blob: %w", err)
	}
	messaging, err := nats.Open(ctx, nats.Options{Storage: nats.FileStorage, StoreDir: natsDir})
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("open file bus: %w", err)
	}
	return []funcd.Option{funcd.WithBlob(bucket), funcd.WithBus(messaging)}, "file", bucket, messaging, nil
}

// bootSweeper is the containerd driver's boot sweep of every funcd namespace (ADR-0167, ADR-0186); off Linux the driver
// is a stub.
type bootSweeper interface {
	BootSweep(ctx context.Context) error
}

// noopClose is the execution closer for the process lane (nothing to tear down).
func noopClose() error { return nil }

// telemetryCloseTimeout bounds the telemetry shutdown of a failed buildOptions: nothing was recorded yet, so an
// unreachable collector must not delay the error (issue #507).
const telemetryCloseTimeout = time.Second

// telemetryCloser adapts the Telemetry's context-bounded Shutdown to buildOptions' io.Closer cleanup.
type telemetryCloser struct{ tel *observability.Telemetry }

func (c telemetryCloser) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), telemetryCloseTimeout)
	defer cancel()
	return c.tel.Shutdown(ctx)
}

// executionOptions selects the runtime driver + function-execution wiring from the resolved
// runtime.mode (ADR-0036/0061): "containerd" → the containerd/crun worker running curated images
// (its lane settings from cfg.Runtime.Containerd); else (default) → the process driver running the embedded
// Node shim. It returns a closer the caller must defer — for containerd mode it stops the
// ctrmanager-supervised private containerd (ADR-0054); for process mode it is a no-op.
func executionOptions(ctx context.Context, cfg config.Config, logger *slog.Logger) ([]funcd.Option, func() error, error) {
	grace, err := processStopGrace(cfg)
	if err != nil {
		return nil, noopClose, err
	}
	if cfg.Runtime.Mode == "containerd" {
		return containerdOptions(ctx, cfg, logger, containerd.New)
	}

	// process mode (default, cross-platform): run the embedded Node shim and pool host on the process driver, which
	// reaps the workers a crashed run left in <dataDir>/process before the controllers start (ADR-0167).
	logger.WarnContext(ctx, "funcd: runtime.mode process — functions run as the daemon's OS user with no isolation (dev/test only, ADR-0011); set runtime.mode: containerd for untrusted functions")
	rt, err := process.Open(ctx, filepath.Join(cfg.Storage.DataDir, "process"), grace, logger)
	if err != nil {
		return nil, noopClose, fmt.Errorf("process runtime: %w", err)
	}
	opts, err := processShimOptions(ctx, cfg, logger)
	if err != nil {
		_ = rt.Close()
		return nil, noopClose, err
	}
	return append([]funcd.Option{funcd.WithRuntime(rt)}, opts...), rt.Close, nil
}

// containerdOptions brings the containerd driver up through newRuntime (containerd.New; a test passes a fake) and
// sweeps the funcd namespaces before any controller starts.
func containerdOptions(ctx context.Context, cfg config.Config, logger *slog.Logger, newRuntime func(containerd.Config) (fnruntime.Runtime, error)) ([]funcd.Option, func() error, error) {
	c := cfg.Runtime.Containerd
	// ADR-0054: bring the container runtime up through the Manager. By default it starts +
	// supervises a PRIVATE containerd and imports the embedded curated images; with an external
	// socket set it returns that socket and starts no child. The driver dials whatever Ensure yields.
	mgrCfg := ctrmanager.Config{
		ExternalSocket: c.Socket,
		DataRoot:       c.Root,
		ImageOverride:  c.ImageOverride,
	}
	mgr, err := ctrmanager.New(mgrCfg)
	if err != nil {
		return nil, noopClose, fmt.Errorf("build container manager: %w", err)
	}
	socket, err := mgr.Ensure(ctx)
	if err != nil {
		_ = mgr.Close()
		return nil, noopClose, fmt.Errorf("ensure container runtime (private containerd is Linux+root; set runtime.containerd.socket otherwise): %w", err)
	}
	cd, err := newRuntime(containerd.Config{
		Socket:      socket,
		Snapshotter: c.Snapshotter,
		CNIBinDir:   c.CNIBinDir,
		CNIConfDir:  c.CNIConfDir,
		StateDir:    c.StateDir,
		SubnetCIDR:  c.SubnetCIDR,
		Logger:      logger,
		Pullable:    mgrCfg.Pullable(c.ImagePrefix),
		Private:     c.Socket == "",
	})
	if err != nil {
		_ = mgr.Close()
		return nil, noopClose, fmt.Errorf("containerd runtime (runtime.mode: containerd is Linux-only): %w", err)
	}
	// What a funcd namespace holds is a leftover of an earlier run: this driver runs none yet, so the sweep removes it
	// before any controller starts (ADR-0167, ADR-0186).
	if sw, ok := cd.(bootSweeper); ok {
		if serr := sw.BootSweep(ctx); serr != nil {
			logger.WarnContext(ctx, "funcd: could not clear everything an earlier run left in containerd", "error", serr)
		}
	}
	return []funcd.Option{funcd.WithRuntime(cd), funcd.WithContainerExecution(mgrCfg.ImageFor(c.ImagePrefix))}, mgr.Close, nil
}

// processShimOptions extracts the embedded Node shim and pool host, and the Python shim when a usable python3 is
// present, for the process driver.
func processShimOptions(ctx context.Context, cfg config.Config, logger *slog.Logger) ([]funcd.Option, error) {
	var opts []funcd.Option
	node := envOr("FUNCD_NODE", "")
	if node == "" {
		if p, lerr := exec.LookPath("node"); lerr == nil {
			node = p
		}
	}
	if node == "" {
		logger.WarnContext(ctx, "funcd: node not found — functions will NOT execute (control plane only); set FUNCD_NODE or FUNCD_RUNTIME=containerd")
		return opts, nil
	}
	shimPath := filepath.Join(cfg.Storage.DataDir, "shim.mjs")
	if werr := os.WriteFile(shimPath, shimnode.Shim, 0o600); werr != nil {
		return nil, fmt.Errorf("extract runtime shim to %s: %w", shimPath, werr)
	}
	// The node pool host (ADR-0046): node functions that name one spec.pooling.worker share it.
	poolPath := filepath.Join(cfg.Storage.DataDir, "pool.mjs")
	if werr := os.WriteFile(poolPath, shimnode.Pool, 0o600); werr != nil {
		return nil, fmt.Errorf("extract pool shim to %s: %w", poolPath, werr)
	}
	opts = append(opts, funcd.WithRuntimeShim(node, shimPath), funcd.WithPoolShim(node, poolPath),
		funcd.WithPoolManifestDir(filepath.Join(cfg.Storage.DataDir, "pool")))

	// Optional second curated language — the Python shim (ADR-0049). If a python3 is present and can
	// import the extracted shim package (it needs Python ≥3.12 and fastjsonschema, ADR-0123), register
	// it for the `python*` runtime family; node functions are unaffected when it is absent or unusable.
	python := envOr("FUNCD_PYTHON", "")
	if python == "" {
		if p, lerr := exec.LookPath("python3"); lerr == nil {
			python = p
		}
	}
	if python == "" {
		logger.InfoContext(ctx, "funcd: python3 not found — python functions will not execute in process mode (set FUNCD_PYTHON); node functions unaffected")
		return opts, nil
	}
	shimEntry, poolEntry, perr := shimpython.Extract(filepath.Join(cfg.Storage.DataDir, "shim-python"))
	if perr != nil {
		return nil, fmt.Errorf("extract python runtime shim: %w", perr)
	}
	if reason := process.PythonShimLoadError(ctx, python, filepath.Dir(shimEntry)); reason != "" {
		logger.WarnContext(ctx, "funcd: python cannot load the runtime shim — python functions will not execute in process mode (set FUNCD_PYTHON to a Python ≥3.12 with fastjsonschema); node functions unaffected",
			"python", python, "reason", reason)
		return opts, nil
	}
	opts = append(opts, funcd.WithRuntimeShimFor("python", python, shimEntry))

	// Python worker pooling (ADR-0050) needs `concurrent.interpreters` (Python ≥3.14). Register the
	// subinterpreter pool host for the python* family only when the interpreter supports it; below
	// 3.14, python functions still run solo (the runtime shim above), they just don't co-pool.
	if pythonAtLeast314(python) {
		opts = append(opts, funcd.WithPoolShimFor("python", python, poolEntry))
	} else {
		logger.InfoContext(ctx, "funcd: python < 3.14 — python worker pooling disabled (needs concurrent.interpreters); python functions run solo")
	}
	return opts, nil
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
