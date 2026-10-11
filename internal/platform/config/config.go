// Package config is the funcd daemon config — funcdconfig.yaml (ADR-0061, ADR-0062). It is ONE
// struct populated from file + env + default and validated once:
//
//	defaults() → yaml.UnmarshalStrict (file) → env.Parse (FUNCD_* overlay) → --memory flag → Validate
//
// precedence flag > env > file > default. A key is defined in one place — a single Config field with
// three tags: `json` is the yaml key (sigs.k8s.io/yaml maps yaml→json→struct, reusing json tags as in
// ADR-0061), `env` is the FUNCD_* overlay var (caarlos0/env, which leaves a field untouched when its
// var is unset — no clobber), `validate` is the rule (go-playground/validator, the lib huma already
// pulls). Validation runs on the MERGED struct, so a bad value from any source is caught. It is a leaf
// (no driver imports); cmd/funcd maps Config → []funcd.Option.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/go-playground/validator/v10"
	"golang.org/x/net/http/httpguts"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"sigs.k8s.io/yaml"
)

// APIVersion / Kind are the optional funcdconfig.yaml envelope (validated only when present).
const (
	APIVersion = "funcd.io/v1alpha1"
	Kind       = "FuncdConfig"
)

// Config is the funcd daemon config — the single source of truth (ADR-0062). Each field's tags
// declare its yaml key (`json`), its FUNCD_* overlay var (`env`), and its validation rule (`validate`).
type Config struct {
	APIVersion string `json:"apiVersion,omitempty" validate:"omitempty,eq=funcd.io/v1alpha1"`
	Kind       string `json:"kind,omitempty" validate:"omitempty,eq=FuncdConfig"`
	Server     struct {
		ListenAddr    string `json:"listenAddr,omitempty" env:"FUNCD_LISTEN_ADDR"`
		DataPlaneAddr string `json:"dataPlaneAddr,omitempty" env:"FUNCD_DATA_PLANE_ADDR"`
		// TLS termination (ADR-0111, F74). When Enabled, both listeners serve HTTPS via ServeTLS.
		// Mode defaults to selfsigned (stdlib, offline). Empty/Enabled=false ⇒ plaintext (default).
		TLS struct {
			Enabled  bool     `json:"enabled,omitempty" env:"FUNCD_TLS_ENABLED"`
			Mode     string   `json:"mode,omitempty" env:"FUNCD_TLS_MODE" validate:"omitempty,oneof=selfsigned provided acme"`
			Hosts    []string `json:"hosts,omitempty" env:"FUNCD_TLS_HOSTS" envSeparator:","`
			CertFile string   `json:"certFile,omitempty" env:"FUNCD_TLS_CERT_FILE"`
			KeyFile  string   `json:"keyFile,omitempty" env:"FUNCD_TLS_KEY_FILE"`
			Email    string   `json:"email,omitempty" env:"FUNCD_TLS_EMAIL"`
			CADir    string   `json:"caDir,omitempty" env:"FUNCD_TLS_CA_DIR"`
		} `json:"tls,omitempty"`
		// Ingress protection limits (ADR-0112, F75). The rate, size and in-flight limits are off at 0; maxKeys is
		// a table size (min 1, default 4096) with no off switch (ADR-0164).
		Limits struct {
			RatePerMin   int    `json:"ratePerMin,omitempty" env:"FUNCD_LIMITS_RATE_PER_MIN" validate:"min=0"`
			Burst        int    `json:"burst,omitempty" env:"FUNCD_LIMITS_BURST" validate:"min=0"`
			Key          string `json:"key,omitempty" env:"FUNCD_LIMITS_KEY" validate:"omitempty,oneof=clientIP function"`
			MaxBodyBytes int64  `json:"maxBodyBytes,omitempty" env:"FUNCD_LIMITS_MAX_BODY_BYTES" validate:"min=0"`
			MaxInFlight  int    `json:"maxInFlight,omitempty" env:"FUNCD_LIMITS_MAX_IN_FLIGHT" validate:"min=0"`
			MaxKeys      int    `json:"maxKeys,omitempty" env:"FUNCD_LIMITS_MAX_KEYS" validate:"min=1"`
		} `json:"limits,omitempty"`
		// Edge authn PEP (ADR-0113, F77): enable per-target auth-stance enforcement on the data plane.
		Auth struct {
			Edge bool `json:"edge,omitempty" env:"FUNCD_AUTH_EDGE"`
		} `json:"auth,omitempty"`
		// Edge observability (ADR-0114, F76): RED metrics + edge trace span + access log. Off by default.
		Observability struct {
			Metrics   bool `json:"metrics,omitempty" env:"FUNCD_OBS_METRICS"`
			AccessLog bool `json:"accessLog,omitempty" env:"FUNCD_OBS_ACCESS_LOG"`
			Trace     bool `json:"trace,omitempty" env:"FUNCD_OBS_TRACE"`
		} `json:"observability,omitempty"`
		// Edge shaping (ADR-0114, F78): CORS / response headers / gzip compression. Off by default.
		Shaping struct {
			CORS struct {
				AllowOrigins  []string `json:"allowOrigins,omitempty" env:"FUNCD_SHAPING_CORS_ALLOW_ORIGINS" envSeparator:","`
				AllowMethods  []string `json:"allowMethods,omitempty" env:"FUNCD_SHAPING_CORS_ALLOW_METHODS" envSeparator:","`
				AllowHeaders  []string `json:"allowHeaders,omitempty" env:"FUNCD_SHAPING_CORS_ALLOW_HEADERS" envSeparator:","`
				MaxAgeSeconds int      `json:"maxAgeSeconds,omitempty" env:"FUNCD_SHAPING_CORS_MAX_AGE_SECONDS" validate:"min=0"`
			} `json:"cors,omitempty"`
			Headers struct {
				// The env form splits pairs on ",", so a header value that contains a comma needs the file.
				Set    map[string]string `json:"set,omitempty" env:"FUNCD_SHAPING_HEADERS_SET" envSeparator:"," envKeyValSeparator:"=" validate:"dive,keys,header_name,endkeys"`
				Remove []string          `json:"remove,omitempty" env:"FUNCD_SHAPING_HEADERS_REMOVE" envSeparator:","`
			} `json:"headers,omitempty"`
			Compression bool `json:"compression,omitempty" env:"FUNCD_SHAPING_COMPRESSION"`
		} `json:"shaping,omitempty"`
		// Egress network isolation (ADR-0115, FEAT-0007/F80): the worker-egress default-deny substrate.
		// Off by default (phased); Linux/containerd only (a no-op elsewhere). When enabled, funcd programs
		// an nftables policy over the funcd0 bridge that default-denies worker egress and redirects
		// remaining external TCP into the egress gateway (F81). DNSResolver / InternalAllow are the
		// worker-reachable resolver + node-service addresses (host:port) passed directly.
		Network struct {
			Egress            bool `json:"egress,omitempty" env:"FUNCD_NETWORK_EGRESS"`
			EgressGatewayPort int  `json:"egressGatewayPort,omitempty" env:"FUNCD_NETWORK_EGRESS_GATEWAY_PORT" validate:"min=0,max=65535"`
			// DNSForwarderPort is the funcd DNS forwarder host port (ADR-0117, F81): worker :53 is
			// REDIRECTed into it so it is the only reachable resolver (the domain trust anchor). Wired
			// with the gateway when Egress is on (Linux/containerd only).
			DNSForwarderPort int      `json:"dnsForwarderPort,omitempty" env:"FUNCD_NETWORK_DNS_FORWARDER_PORT" validate:"min=0,max=65535"`
			DNSResolver      string   `json:"dnsResolver,omitempty" env:"FUNCD_NETWORK_DNS_RESOLVER"`
			InternalAllow    []string `json:"internalAllow,omitempty" env:"FUNCD_NETWORK_INTERNAL_ALLOW" envSeparator:","`
			// WorkerSyncInterval is the egress WorkerIndex sync cadence (ADR-0163), a positive duration.
			WorkerSyncInterval string `json:"workerSyncInterval,omitempty" env:"FUNCD_NETWORK_WORKER_SYNC_INTERVAL"`
		} `json:"network,omitempty"`
		// ShutdownTimeout bounds the HTTP, Sensor and workflow-run drain at shutdown (ADR-0163), a positive duration.
		ShutdownTimeout string `json:"shutdownTimeout,omitempty" env:"FUNCD_SHUTDOWN_TIMEOUT"`
	} `json:"server,omitempty"`
	Storage struct {
		Mode    string `json:"mode,omitempty" env:"FUNCD_STORAGE_MODE" validate:"oneof=file memory"`
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_DATA_DIR" validate:"required"`
		// MetastoreDir is the control-plane metastore's dedicated Badger directory (ADR-0065). Empty ⇒
		// derived as <DataDir>/store in Load(); ignored (in-memory engine) when Mode is memory. Its own
		// instance, never shared — see the store-per-service rule (restore class + service ownership).
		MetastoreDir string `json:"metastoreDir,omitempty" env:"FUNCD_METASTORE_DIR"`
	} `json:"storage,omitempty"`
	Kvstore struct {
		// Engine for the function-facing KV service (ADR-0066/0069): memory (default, ephemeral) or
		// badger (durable). Its own dedicated instance. DataDir is empty ⇒ derived as <Storage.DataDir>/kv
		// in Load(); an explicit value overrides the default location. Ignored (in-memory) when Storage.Mode
		// is memory.
		Engine  string `json:"engine,omitempty" env:"FUNCD_KVSTORE_ENGINE" validate:"omitempty,oneof=memory badger"`
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_KVSTORE_DATA_DIR"`
		// MaxStoresPerNamespace is the per-namespace KVStore count cap enforced at admission (ADR-0072);
		// 0 ⇒ the default (100); a negative value disables the quota.
		MaxStoresPerNamespace int `json:"maxStoresPerNamespace,omitempty" env:"FUNCD_KVSTORE_MAX_STORES_PER_NAMESPACE"`
		// Backup is the opt-in DR export of the KV instance to object storage (ADR-0067), off by default.
		// Enabled without a Target, or with Engine memory ⇒ fault.Invalid at startup; ignored with a warning when
		// Storage.Mode is memory. Interval/Rebaseline/RebaselineRetry are durations ("30s"); RebaselineRetry is the
		// delay after a failed re-baseline (ADR-0195).
		Backup struct {
			Enabled         bool   `json:"enabled,omitempty" env:"FUNCD_KVSTORE_BACKUP_ENABLED"`
			Target          string `json:"target,omitempty" env:"FUNCD_KVSTORE_BACKUP_TARGET"`
			Interval        string `json:"interval,omitempty" env:"FUNCD_KVSTORE_BACKUP_INTERVAL"`
			Rebaseline      string `json:"rebaseline,omitempty" env:"FUNCD_KVSTORE_BACKUP_REBASELINE"`
			RebaselineRetry string `json:"rebaselineRetry,omitempty" env:"FUNCD_KVSTORE_BACKUP_REBASELINE_RETRY"`
			ChunkBytes      int    `json:"chunkBytes,omitempty" env:"FUNCD_KVSTORE_BACKUP_CHUNK_BYTES" validate:"min=0"`
		} `json:"backup,omitempty"`
		// Cdc is the opt-in change-feed of the KV instance to the bus (ADR-0068), off by default. Enabled
		// without a Sink, or with Engine memory ⇒ fault.Invalid at startup; ignored with a warning when Storage.Mode
		// is memory. Retention is a duration ("24h").
		Cdc struct {
			Enabled   bool   `json:"enabled,omitempty" env:"FUNCD_KVSTORE_CDC_ENABLED"`
			Sink      string `json:"sink,omitempty" env:"FUNCD_KVSTORE_CDC_SINK"`
			Retention string `json:"retention,omitempty" env:"FUNCD_KVSTORE_CDC_RETENTION"`
		} `json:"cdc,omitempty"`
	} `json:"kvstore,omitempty"`
	// Backup is the platform backup target (ADR-0203), run by ADR-0205. Target is s3:// or file:///<absolute dir>;
	// CredentialsFile (s3:// only) is an AWS shared credentials file whose [default] profile signs every request.
	// Retention is in hours (hourly), days (daily, verified) and weeks (weekly).
	Backup struct {
		Target          string `json:"target,omitempty" env:"FUNCD_BACKUP_TARGET"`
		CredentialsFile string `json:"credentialsFile,omitempty" env:"FUNCD_BACKUP_CREDENTIALS_FILE"`
		SingleWriter    bool   `json:"singleWriter,omitempty" env:"FUNCD_BACKUP_SINGLE_WRITER"`
		Retention       struct {
			Hourly   int `json:"hourly,omitempty" env:"FUNCD_BACKUP_RETENTION_HOURLY"`
			Daily    int `json:"daily,omitempty" env:"FUNCD_BACKUP_RETENTION_DAILY"`
			Weekly   int `json:"weekly,omitempty" env:"FUNCD_BACKUP_RETENTION_WEEKLY"`
			Verified int `json:"verified,omitempty" env:"FUNCD_BACKUP_RETENTION_VERIFIED"`
			// PreUpgrade is the complete pre-upgrade generations funcd upgrade keeps listing as kept (ADR-0207).
			PreUpgrade int `json:"preUpgrade,omitempty" env:"FUNCD_BACKUP_RETENTION_PRE_UPGRADE"`
		} `json:"retention,omitempty"`
		// Encryption seals every generation to age recipients (ADR-0204): Recipients are paths of recipients files;
		// None is the explicit choice to store the files unsealed.
		Encryption struct {
			Recipients []string `json:"recipients,omitempty" env:"FUNCD_BACKUP_ENCRYPTION_RECIPIENTS" envSeparator:","`
			None       bool     `json:"none,omitempty" env:"FUNCD_BACKUP_ENCRYPTION_NONE"`
		} `json:"encryption,omitempty"`
		// Interval, RetryInterval and Objectives.RPO time the runs and the rpoRisk alert (ADR-0205 Decision 1).
		Interval      string `json:"interval,omitempty" env:"FUNCD_BACKUP_INTERVAL"`
		RetryInterval string `json:"retryInterval,omitempty" env:"FUNCD_BACKUP_RETRY_INTERVAL"`
		Objectives    struct {
			RPO string `json:"rpo,omitempty" env:"FUNCD_BACKUP_OBJECTIVES_RPO"`
		} `json:"objectives,omitempty"`
	} `json:"backup,omitempty"`
	// Recovery is safe mode (ADR-0207): AfterCrashes unclean starts in a row start held, twice it stop with exit
	// status 70; StableAfter is the continuous run that makes a start clean. Both are checked in cmd/funcd.
	Recovery struct {
		SafeMode struct {
			AfterCrashes int    `json:"afterCrashes,omitempty" env:"FUNCD_RECOVERY_SAFE_MODE_AFTER_CRASHES"`
			StableAfter  string `json:"stableAfter,omitempty" env:"FUNCD_RECOVERY_SAFE_MODE_STABLE_AFTER"`
		} `json:"safeMode,omitempty"`
	} `json:"recovery,omitempty"`
	Auth struct {
		Token      string   `json:"token,omitempty" env:"FUNCD_TOKEN"`
		Namespaces []string `json:"namespaces,omitempty" env:"FUNCD_AUTH_NAMESPACES" envSeparator:","`
		// Credentials is file-only (ADR-0171 Decision 1): caarlos0/env walks a slice of structs under an
		// empty prefix, so stray 0_* variables would add zero-value entries.
		Credentials CredentialList `json:"credentials,omitempty" env:"-" validate:"dive"`
	} `json:"auth,omitempty"`
	Secrets struct {
		EncryptionKeyFile string `json:"encryptionKeyFile,omitempty" env:"FUNCD_SECRETS_ENCRYPTION_KEY_FILE"`
	} `json:"secrets,omitempty"`
	Runtime struct {
		Mode string `json:"mode,omitempty" env:"FUNCD_RUNTIME" validate:"oneof=process containerd"`
		// BootBackoffInitial and BootBackoffMax bound the wait before a worker that crashed while booting is created
		// again (ADR-0160): positive durations, the max at least the initial wait.
		BootBackoffInitial string `json:"bootBackoffInitial,omitempty" env:"FUNCD_RUNTIME_BOOT_BACKOFF_INITIAL"`
		BootBackoffMax     string `json:"bootBackoffMax,omitempty" env:"FUNCD_RUNTIME_BOOT_BACKOFF_MAX"`
		// The supervision, boot and drain times (ADR-0163): positive durations, bootTimeout above
		// invoke.activationTimeout and handOutSettle at most drainGrace.
		SupervisionPeriod string `json:"supervisionPeriod,omitempty" env:"FUNCD_RUNTIME_SUPERVISION_PERIOD"`
		BootTimeout       string `json:"bootTimeout,omitempty" env:"FUNCD_RUNTIME_BOOT_TIMEOUT"`
		DrainGrace        string `json:"drainGrace,omitempty" env:"FUNCD_RUNTIME_DRAIN_GRACE"`
		HandOutSettle     string `json:"handOutSettle,omitempty" env:"FUNCD_RUNTIME_HAND_OUT_SETTLE"`
		DrainPollInterval string `json:"drainPollInterval,omitempty" env:"FUNCD_RUNTIME_DRAIN_POLL_INTERVAL"`
		// LivenessTimeout is how long a listening replica may stay silent on /health/liveness before it is restarted
		// (ADR-0215): a positive duration, when set at least twice supervisionPeriod; empty ⇒ max(30s, three
		// supervisionPeriod), derived in cmd/funcd.
		LivenessTimeout string `json:"livenessTimeout,omitempty" env:"FUNCD_RUNTIME_LIVENESS_TIMEOUT"`
		// Process tunes the process driver (ADR-0167): StopGrace is the wait after SIGTERM before SIGKILL for a stop,
		// the shutdown close and the boot reap, a duration with 0 < d <= 10s.
		Process struct {
			StopGrace string `json:"stopGrace,omitempty" env:"FUNCD_PROCESS_STOP_GRACE"`
		} `json:"process,omitempty"`
		Containerd struct {
			Socket        string            `json:"socket,omitempty" env:"FUNCD_CONTAINERD_SOCKET"`
			Root          string            `json:"root,omitempty" env:"FUNCD_CONTAINERD_ROOT"`
			Snapshotter   string            `json:"snapshotter,omitempty" env:"FUNCD_SNAPSHOTTER"`
			CNIBinDir     string            `json:"cniBinDir,omitempty" env:"FUNCD_CNI_BIN_DIR"`
			CNIConfDir    string            `json:"cniConfDir,omitempty" env:"FUNCD_CNI_CONF_DIR"`
			StateDir      string            `json:"stateDir,omitempty" env:"FUNCD_CONTAINERD_STATE_DIR"`
			SubnetCIDR    string            `json:"subnetCIDR,omitempty" env:"FUNCD_SUBNET_CIDR"`
			ImagePrefix   string            `json:"imagePrefix,omitempty" env:"FUNCD_IMAGE_PREFIX"`
			ImageOverride map[string]string `json:"imageOverride,omitempty" env:"FUNCD_IMAGE_OVERRIDE" envSeparator:"," envKeyValSeparator:"="`
		} `json:"containerd,omitempty"`
	} `json:"runtime,omitempty"`
	// Controller tunes the platform controllers; GCSweepInterval is the owner garbage collector's sweep period
	// (ADR-0170), a positive duration.
	Controller struct {
		GCSweepInterval string `json:"gcSweepInterval,omitempty" env:"FUNCD_CONTROLLER_GC_SWEEP_INTERVAL"`
		// The retry and requeue times (ADR-0163): positive durations, retryBackoffMax at least 5ms.
		RetryBackoffMax      string `json:"retryBackoffMax,omitempty" env:"FUNCD_CONTROLLER_RETRY_BACKOFF_MAX"`
		ReferentPollInterval string `json:"referentPollInterval,omitempty" env:"FUNCD_CONTROLLER_REFERENT_POLL_INTERVAL"`
		RouteResyncInterval  string `json:"routeResyncInterval,omitempty" env:"FUNCD_CONTROLLER_ROUTE_RESYNC_INTERVAL"`
	} `json:"controller,omitempty"`
	Log struct {
		Format string `json:"format,omitempty" env:"FUNCD_LOG_FORMAT" validate:"oneof=json text"`
		Level  string `json:"level,omitempty" env:"FUNCD_LOG_LEVEL" validate:"oneof=debug info warn error"`
	} `json:"log,omitempty"`
	Telemetry struct {
		Endpoint string `json:"endpoint,omitempty" env:"FUNCD_TELEMETRY_ENDPOINT"`
		Insecure bool   `json:"insecure,omitempty" env:"FUNCD_TELEMETRY_INSECURE"`
	} `json:"telemetry,omitempty"`
	// Funclog tunes structured function-log capture (ADR-0081) and its traces signal (ADR-0101); both on by
	// default. A zero SegmentMaxBytes / empty SegmentMaxAge (a duration, "10s") keeps the sink default
	// (8 MiB / 10s). The sink writes to the blob substrate, so funcd-system is the only Bucket accepted.
	Funclog struct {
		Enabled         bool   `json:"enabled,omitempty" env:"FUNCD_FUNCLOG_ENABLED"`
		SegmentMaxBytes int    `json:"segmentMaxBytes,omitempty" env:"FUNCD_FUNCLOG_SEGMENT_MAX_BYTES" validate:"min=0"`
		SegmentMaxAge   string `json:"segmentMaxAge,omitempty" env:"FUNCD_FUNCLOG_SEGMENT_MAX_AGE"`
		Bucket          string `json:"bucket,omitempty" env:"FUNCD_FUNCLOG_BUCKET" validate:"omitempty,eq=funcd-system"`
		Traces          bool   `json:"traces,omitempty" env:"FUNCD_FUNCLOG_TRACES"`
		// MaxRecordBytes bounds one shim log or span record line (ADR-0168); 0 keeps the 65536 default, and funcd refuses
		// a nonzero value below 1024 or above the 1 MiB reader cap.
		MaxRecordBytes int `json:"maxRecordBytes,omitempty" env:"FUNCD_FUNCLOG_MAX_RECORD_BYTES" validate:"min=0"`
	} `json:"funclog,omitempty"`
	// S3Gateway is the opt-in S3-protocol frontend over the blob substrate (ADR-0080/0085).
	// Disabled by default ⇒ no listener, no IAM, no keypair injection.
	S3Gateway struct {
		Enabled    bool   `json:"enabled,omitempty" env:"FUNCD_S3GATEWAY_ENABLED"`
		ListenAddr string `json:"listenAddr,omitempty" env:"FUNCD_S3GATEWAY_LISTEN_ADDR"`
		// Endpoint is the sandbox-facing S3 URL injected into a spec.blob worker's AWS_ENDPOINT_URL_S3
		// (ADR-0085). Empty ⇒ derived as http://<ListenAddr>. Under containerd a worker is in its own
		// netns, so this MUST be a node address the sandbox can reach (the CNI bridge gateway IP, e.g.
		// http://10.63.0.1:9000), NOT a 127.0.0.1 ListenAddr.
		Endpoint         string `json:"endpoint,omitempty" env:"FUNCD_S3GATEWAY_ENDPOINT"`
		MaxUploadBytes   int64  `json:"maxUploadBytes,omitempty" env:"FUNCD_S3GATEWAY_MAX_UPLOAD_BYTES" validate:"min=0"`
		MasterSecretFile string `json:"masterSecretFile,omitempty" env:"FUNCD_S3GATEWAY_MASTER_SECRET_FILE"`
	} `json:"s3gateway,omitempty"`

	// Catalog tunes the per-CatalogService catalog::query PEP proxies (ADR-0137). ProxyHost is the
	// netns-reachable host each proxy publishes into a function's FUNCD_CATALOG_<ALIAS>_URL — the
	// query-path analog of S3Gateway.Endpoint's host. Under containerd a worker runs in its own netns
	// and cannot reach the daemon's 127.0.0.1, so this MUST be a node address the sandbox can reach (the
	// CNI bridge gateway IP, e.g. 10.63.0.1); the proxy then binds 0.0.0.0. Empty ⇒ 127.0.0.1 (the
	// process-runtime/dev default, shared loopback).
	Catalog struct {
		ProxyHost string `json:"proxyHost,omitempty" env:"FUNCD_CATALOG_PROXY_HOST"`
		// EnginePollInterval and EngineProbeTimeout pace the wait for an engine's readiness probe (ADR-0163).
		EnginePollInterval string `json:"enginePollInterval,omitempty" env:"FUNCD_CATALOG_ENGINE_POLL_INTERVAL"`
		EngineProbeTimeout string `json:"engineProbeTimeout,omitempty" env:"FUNCD_CATALOG_ENGINE_PROBE_TIMEOUT"`
	} `json:"catalog,omitempty"`

	// Workflow tunes the workflow engine (ADR-0094). Durable run state lives in its own dedicated Badger
	// instance at Workflow.DataDir (default <Storage.DataDir>/workflow; in-memory when Storage.Mode is
	// memory). DefaultStepTimeout + DefaultRetry feed the engine core. Retention is the terminal-run GC
	// horizon: a periodic sweep reclaims each closed run (engine record and WorkflowRun object) older than
	// it; 0 ⇒ no sweep. PayloadLimit caps the bytes of a run input (at WorkflowRun admission and run start)
	// and of each step output; 0 ⇒ unbounded.
	Workflow struct {
		DefaultStepTimeout string `json:"defaultStepTimeout,omitempty" env:"FUNCD_WORKFLOW_DEFAULT_STEP_TIMEOUT"`
		DefaultRetry       int    `json:"defaultRetry,omitempty" env:"FUNCD_WORKFLOW_DEFAULT_RETRY" validate:"min=0"`
		Retention          string `json:"retention,omitempty" env:"FUNCD_WORKFLOW_RETENTION"`
		PayloadLimit       int64  `json:"payloadLimit,omitempty" env:"FUNCD_WORKFLOW_PAYLOAD_LIMIT" validate:"min=0"`
		// MaxStepsInFlight bounds the function-step calls in flight across all runs (ADR-0146); 0 ⇒ no cap.
		MaxStepsInFlight int `json:"maxStepsInFlight,omitempty" env:"FUNCD_WORKFLOW_MAX_STEPS_IN_FLIGHT" validate:"min=0"`
		// ArtifactPollInterval re-checks a Workflow whose step artifact is not pushed; DefaultRetryBackoff is the first
		// retry gap of a step with no retry.backoff, doubled up to 1h; 0 ⇒ none (ADR-0163).
		ArtifactPollInterval string `json:"artifactPollInterval,omitempty" env:"FUNCD_WORKFLOW_ARTIFACT_POLL_INTERVAL"`
		DefaultRetryBackoff  string `json:"defaultRetryBackoff,omitempty" env:"FUNCD_WORKFLOW_DEFAULT_RETRY_BACKOFF"`
		// DataDir is the run-state Badger directory. Empty ⇒ derived as <Storage.DataDir>/workflow in
		// Load(); ignored (in-memory) when Storage.Mode is memory. An explicit value overrides it.
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_WORKFLOW_DATA_DIR"`
	} `json:"workflow,omitempty"`

	// Eventing tunes the Sensor action-delivery reliability path (ADR-0118, F85). DeliveryAttempts is the
	// bounded-retry cap before a failed workflow:/function: delivery is dead-lettered. The event store (ADR-0201),
	// one Badger instance holding the dead letters and the blob seen lists, is at Deadletter.DataDir (default
	// <Storage.DataDir>/deadletter; in-memory when Storage.Mode is memory); Deadletter.Retention (TTL) and
	// Deadletter.MaxEntries (per-namespace count cap) drive the periodic retention sweep of dead letters only. MaxDeliveriesInFlight (the delivery
	// workers), MaxInFlightPerTarget and MaxQueuedPerSensor size the Sensor delivery queue (ADR-0156); each is
	// at least 1 and the per-target cap at most the workers. MaxDeliveriesInFlight is declared before
	// MaxInFlightPerTarget so a bad worker count is the error reported, not the cap's cross-field check.
	Eventing struct {
		DeliveryAttempts      int `json:"deliveryAttempts,omitempty" env:"FUNCD_EVENTING_DELIVERY_ATTEMPTS" validate:"min=0"`
		MaxDeliveriesInFlight int `json:"maxDeliveriesInFlight,omitempty" env:"FUNCD_EVENTING_MAX_DELIVERIES_IN_FLIGHT" validate:"min=1"`
		MaxInFlightPerTarget  int `json:"maxInFlightPerTarget,omitempty" env:"FUNCD_EVENTING_MAX_IN_FLIGHT_PER_TARGET" validate:"min=1,ltefield=MaxDeliveriesInFlight"`
		MaxQueuedPerSensor    int `json:"maxQueuedPerSensor,omitempty" env:"FUNCD_EVENTING_MAX_QUEUED_PER_SENSOR" validate:"min=1"`
		Deadletter            struct {
			Retention  string `json:"retention,omitempty" env:"FUNCD_EVENTING_DEADLETTER_RETENTION"`
			MaxEntries int    `json:"maxEntries,omitempty" env:"FUNCD_EVENTING_DEADLETTER_MAX_ENTRIES" validate:"min=0"`
			// DataDir is the event store's Badger directory. Empty ⇒ derived as <Storage.DataDir>/deadletter in
			// Load(); ignored (in-memory) when Storage.Mode is memory. An explicit value overrides it.
			DataDir string `json:"dataDir,omitempty" env:"FUNCD_EVENTING_DEADLETTER_DATA_DIR"`
		} `json:"deadletter,omitempty"`
		// BlobPollInterval is the cadence a `blob:` EventSource's prefixes are List-polled for new objects
		// (ADR-0119, F83). A duration ("15s"); one cadence for all blob sources in V1.
		BlobPollInterval string `json:"blobPollInterval,omitempty" env:"FUNCD_EVENTING_BLOB_POLL_INTERVAL"`
		// BucketRecheckInterval re-checks every blob EventSource's Bucket; DeliveryBackoffInitial/Max pace the Sensor
		// delivery retry, an empty max following max(10s, initial) (ADR-0163).
		BucketRecheckInterval  string `json:"bucketRecheckInterval,omitempty" env:"FUNCD_EVENTING_BUCKET_RECHECK_INTERVAL"`
		DeliveryBackoffInitial string `json:"deliveryBackoffInitial,omitempty" env:"FUNCD_EVENTING_DELIVERY_BACKOFF_INITIAL"`
		DeliveryBackoffMax     string `json:"deliveryBackoffMax,omitempty" env:"FUNCD_EVENTING_DELIVERY_BACKOFF_MAX"`
	} `json:"eventing,omitempty"`

	// Invoke tunes function invocation. MaxNestedInFlight caps the nested (fn-to-fn) calls in flight to one
	// Function (ADR-0147); 0 ⇒ the default (10). It cannot be disabled. DefaultTimeout bounds external invokes
	// only (ADR-0151): how long one waits for its response to start when the Function sets no spec.timeout; a
	// link keeps links[].timeout (30 s default). A duration (ADR-0194); empty or 0s ⇒ 60s, at most 1h.
	Invoke struct {
		MaxNestedInFlight int    `json:"maxNestedInFlight,omitempty" env:"FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT" validate:"min=0"`
		DefaultTimeout    string `json:"defaultTimeout,omitempty" env:"FUNCD_INVOKE_DEFAULT_TIMEOUT"`
		// ActivationTimeout is the longest cold-start hold of a wake; ReclaimInterval the idle-reclaim cadence (ADR-0163).
		ActivationTimeout string `json:"activationTimeout,omitempty" env:"FUNCD_INVOKE_ACTIVATION_TIMEOUT"`
		ReclaimInterval   string `json:"reclaimInterval,omitempty" env:"FUNCD_INVOKE_RECLAIM_INTERVAL"`
	} `json:"invoke,omitempty"`

	// Site tunes the declarative static web app reconciler (ADR-0139, FEAT-0003/F103). DefaultIndex is
	// the document a Site serves for "/" — and asserts present before it reports Ready — when its
	// spec.index is empty. A relative path (no leading '/').
	Site struct {
		DefaultIndex string `json:"defaultIndex,omitempty" env:"FUNCD_SITE_DEFAULT_INDEX" validate:"omitempty,startsnotwith=/"`
	} `json:"site,omitempty"`

	// App tunes the App reconciler (ADR-0200). UpgradeTimeout bounds an upgrade, a duration more than
	// runtime.bootTimeout; empty ⇒ max(5m, twice runtime.bootTimeout), derived in cmd/funcd. RevisionHistory is the
	// number of AppRevisions an App keeps besides its current one.
	App struct {
		UpgradeTimeout  string `json:"upgradeTimeout,omitempty" env:"FUNCD_APP_UPGRADE_TIMEOUT"`
		RevisionHistory int    `json:"revisionHistory,omitempty" env:"FUNCD_APP_REVISION_HISTORY" validate:"min=1,max=100"`
	} `json:"app,omitempty"`

	// Health paces the storage probes behind the KVStore and Bucket status and the dependency check (ADR-0215):
	// positive durations, the timeout less than the interval.
	Health struct {
		StorageProbeInterval string `json:"storageProbeInterval,omitempty" env:"FUNCD_HEALTH_STORAGE_PROBE_INTERVAL"`
		StorageProbeTimeout  string `json:"storageProbeTimeout,omitempty" env:"FUNCD_HEALTH_STORAGE_PROBE_TIMEOUT"`
	} `json:"health,omitempty"`
}

// Flags are the top precedence tier (CLI flags with no env). MemoryOnly nil ⇒ --memory not set.
type Flags struct{ MemoryOnly *bool }

// DefaultImagePrefix is the built-in runtime.containerd.imagePrefix, the repository the embedded curated runtime
// images are tagged under. With it only the embedded images are used: a runtime image is pulled only from a registry
// the operator chose, an imageOverride entry or a custom imagePrefix.
const DefaultImagePrefix = "funcd/runtime-"

// defaults returns the Config with every built-in default set (the bottom precedence tier; identical
// to ADR-0061's defaults). The dataDir-relative containerd paths (Root, CNIConfDir) are derived in
// Load after the merge, when DataDir is final.
func defaults() Config {
	var c Config
	c.Server.ListenAddr = "0.0.0.0:8080"
	c.Server.DataPlaneAddr = "127.0.0.1:0"
	c.Server.Limits.MaxKeys = 4096
	c.Server.ShutdownTimeout = "15s"
	c.Server.Network.WorkerSyncInterval = "2s"
	c.Storage.Mode = "file"
	c.Storage.DataDir = "/var/lib/funcd"
	c.Auth.Namespaces = []string{"default"}
	c.Runtime.Mode = "process"
	c.Runtime.BootBackoffInitial = "10s"
	c.Runtime.SupervisionPeriod = "10s"
	c.Runtime.BootTimeout = "1m"
	c.Runtime.DrainGrace = "30s"
	c.Runtime.HandOutSettle = "2s"
	c.Runtime.DrainPollInterval = "1s"
	c.Runtime.Process.StopGrace = "3s"
	c.Runtime.Containerd.Snapshotter = "overlayfs"
	c.Controller.GCSweepInterval = "5m"
	c.Controller.RetryBackoffMax = "1s"
	c.Controller.ReferentPollInterval = "2s"
	c.Controller.RouteResyncInterval = "10s"
	c.Catalog.EnginePollInterval = "2s"
	c.Catalog.EngineProbeTimeout = "2s"
	c.Invoke.ActivationTimeout = "30s"
	c.Invoke.ReclaimInterval = "30s"
	c.Runtime.Containerd.CNIBinDir = "/opt/cni/bin"
	c.Runtime.Containerd.SubnetCIDR = "10.63.0.0/16"
	c.Runtime.Containerd.ImagePrefix = DefaultImagePrefix
	c.Log.Format = "json"
	c.Log.Level = "info"
	c.Funclog.Enabled = true
	c.Funclog.Bucket = "funcd-system"
	c.Funclog.Traces = true
	// Platform backup ladder (ADR-0203): 48 hours, 30 days, 12 weeks; a verified copy 2 days.
	c.Backup.Retention.Hourly = 48
	c.Backup.Retention.Daily = 30
	c.Backup.Retention.Weekly = 12
	c.Backup.Retention.Verified = 2
	// funcd upgrade's pre-upgrade pins kept (ADR-0207, Q10).
	c.Backup.Retention.PreUpgrade = 3
	// Safe mode (ADR-0207): held after 3 unclean starts, stopped after 6; a start is clean after 10 minutes.
	c.Recovery.SafeMode.AfterCrashes = 3
	c.Recovery.SafeMode.StableAfter = "10m"
	// Platform backup runs (ADR-0205): one an hour, a retry 5 minutes after a failure, rpoRisk past 2 hours.
	c.Backup.Interval = v1.Duration(defaultBackupInterval).String()
	c.Backup.RetryInterval = v1.Duration(defaultBackupRetryInterval).String()
	c.Backup.Objectives.RPO = v1.Duration(defaultBackupRPO).String()
	// S3 gateway (ADR-0080/0085): opt-in; node-private loopback; 1 GiB buffered-object cap.
	c.S3Gateway.Enabled = false
	c.S3Gateway.ListenAddr = "127.0.0.1:9000"
	c.S3Gateway.MaxUploadBytes = 1 << 30
	// Workflow engine (ADR-0094): one attempt by default (no retry), 300s per-step timeout,
	// 30-day terminal-run retention, 256 KiB run-payload cap.
	c.Workflow.DefaultRetry = 1
	c.Workflow.DefaultStepTimeout = "300s"
	c.Workflow.Retention = "720h"
	c.Workflow.PayloadLimit = 256 << 10
	c.Workflow.MaxStepsInFlight = 64
	c.Workflow.ArtifactPollInterval = "5s"
	c.Workflow.DefaultRetryBackoff = "0s"
	// Eventing DLQ (ADR-0118): 3 delivery attempts before dead-lettering; parked entries kept 720h with a
	// per-namespace cap of 1000, swept periodically.
	c.Eventing.DeliveryAttempts = 3
	// Sensor delivery queue (ADR-0156): 32 workers, 4 in flight per target, 4096 undelivered per Sensor.
	c.Eventing.MaxDeliveriesInFlight = 32
	c.Eventing.MaxInFlightPerTarget = 4
	c.Eventing.MaxQueuedPerSensor = 4096
	c.Eventing.Deadletter.Retention = "720h"
	c.Eventing.Deadletter.MaxEntries = 1000
	// Blob EventSource poll cadence (ADR-0119): 15s default.
	c.Eventing.BlobPollInterval = "15s"
	c.Eventing.BucketRecheckInterval = "15s"
	c.Eventing.DeliveryBackoffInitial = "100ms"
	// Site default index document (ADR-0139): the web convention.
	c.Site.DefaultIndex = "index.html"
	// App revisions (ADR-0200): app.upgradeTimeout stays empty, so cmd/funcd derives it from runtime.bootTimeout.
	c.App.RevisionHistory = 10
	// Health (ADR-0215): runtime.livenessTimeout stays empty, so cmd/funcd derives it from runtime.supervisionPeriod.
	c.Health.StorageProbeInterval = "10s"
	c.Health.StorageProbeTimeout = "2s"
	return c
}

// Locate returns the config file path to load: explicit (--config; must exist → else fault.NotFound)
// → $FUNCD_CONFIG (must exist) → first existing of ./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml →
// "" (none; zero-config). An explicit/env path that doesn't exist is an error; the implicit search
// paths may be absent.
func Locate(explicit string) (string, error) {
	const op = "config.Locate"
	if explicit != "" {
		if !fileExists(explicit) {
			return "", fault.NotFoundf(op, "config file %q does not exist", explicit)
		}
		return explicit, nil
	}
	if env := os.Getenv("FUNCD_CONFIG"); env != "" {
		if !fileExists(env) {
			return "", fault.NotFoundf(op, "FUNCD_CONFIG %q does not exist", env)
		}
		return env, nil
	}
	for _, p := range []string{"funcdconfig.yaml", "/etc/funcd/funcdconfig.yaml"} {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", nil
}

// Load builds the effective Config (ADR-0062): defaults() → strict-decode the file at path ("" ⇒
// skip) → overlay env (caarlos0/env; an unset FUNCD_* var leaves the field untouched) → apply the
// --memory flag → make storage.dataDir absolute → derive the dataDir-relative defaults → Validate. Any
// read/parse/unknown-key/enum error ⇒ fault.Invalid. The returned Config is fully populated + valid.
func Load(path string, flags Flags) (Config, error) {
	const op = "config.Load"
	c := defaults()
	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // operator-supplied config location
		if err != nil {
			return Config{}, fault.Invalidf(op, "read config %q: %v", path, err)
		}
		if err := yaml.UnmarshalStrict(data, &c); err != nil { // file overrides defaults; unknown key ⇒ error
			return Config{}, fault.Invalidf(op, "parse config %q: %v", path, err)
		}
	}
	if err := env.Parse(&c); err != nil { // env overrides file, only where set (no clobber)
		return Config{}, fault.Invalidf(op, "parse FUNCD_* env: %v", err)
	}
	if flags.MemoryOnly != nil && *flags.MemoryOnly { // the flag tier (top precedence)
		c.Storage.Mode = "memory"
	}
	// A relative dataDir resolves against the working directory once, here, so every derived path and
	// consumer sees one absolute root (the file:// blob URL reads a relative path's first segment as a host).
	if c.Storage.DataDir != "" {
		abs, err := filepath.Abs(c.Storage.DataDir)
		if err != nil {
			return Config{}, fault.Invalidf(op, "config key %q: resolve %q: %v", "storage.dataDir", c.Storage.DataDir, err)
		}
		c.Storage.DataDir = abs
	}
	// dataDir-relative containerd defaults — derived after the merge, when DataDir is final.
	if c.Runtime.Containerd.Root == "" {
		c.Runtime.Containerd.Root = filepath.Join(c.Storage.DataDir, "containerd")
	}
	if c.Runtime.Containerd.CNIConfDir == "" {
		c.Runtime.Containerd.CNIConfDir = filepath.Join(c.Storage.DataDir, "cni")
	}
	if c.Runtime.Containerd.StateDir == "" {
		// funcd-owned dir for runtime-generated files bind-mounted into workers (e.g. /etc/resolv.conf) —
		// under dataDir (not /tmp) so it survives a tmp sweep; funcd (re)writes its contents at startup.
		c.Runtime.Containerd.StateDir = filepath.Join(c.Storage.DataDir, "run")
	}
	// dataDir-relative store defaults — each service owns its dedicated Badger instance (store-per-service:
	// restore class + service ownership), so each has its own directory. Derived after the merge so an
	// explicit file/env override wins; else <DataDir>/<name>. Unused in memory mode (in-memory engines).
	if c.Storage.MetastoreDir == "" {
		c.Storage.MetastoreDir = filepath.Join(c.Storage.DataDir, "store")
	}
	if c.Kvstore.DataDir == "" {
		c.Kvstore.DataDir = filepath.Join(c.Storage.DataDir, "kv")
	}
	if c.Workflow.DataDir == "" {
		c.Workflow.DataDir = filepath.Join(c.Storage.DataDir, "workflow")
	}
	if c.Eventing.Deadletter.DataDir == "" {
		c.Eventing.Deadletter.DataDir = filepath.Join(c.Storage.DataDir, "deadletter")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate runs the struct's `validate` tags (go-playground/validator) over the merged values,
// returning a fault.Invalid naming the offending yaml key + the allowed set. Load calls it on the
// effective config (so a bad value from the file, a FUNCD_* env, or the flag is all caught — the
// ADR-0061 env edge is closed); it is exported so a caller can validate a hand-built Config.
func (c Config) Validate() error {
	const op = "config.Config.Validate"
	v := validator.New()
	// net/http silently drops a header whose name is not a token when it writes the response (#509).
	if err := v.RegisterValidation("header_name", func(fl validator.FieldLevel) bool {
		return httpguts.ValidHeaderFieldName(fl.Field().String())
	}); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "register header_name validation")
	}
	// Report yaml keys ("storage.mode") in errors, not Go field names (the json tag is the yaml key).
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name, _, _ := strings.Cut(fld.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
	err := v.Struct(c)
	if err == nil {
		if err := c.validateCredentials(op); err != nil {
			return err
		}
		if fs := c.CheckBackup(); len(fs) > 0 && fs[0].Error {
			return fault.Invalidf(op, "%s", fs[0].Message)
		}
		return nil
	}
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) && len(verrs) > 0 {
		fe := verrs[0]
		want := fe.Tag()
		if fe.Param() != "" {
			want += "=" + fe.Param()
		}
		key := strings.TrimPrefix(fe.Namespace(), "Config.")
		return fault.Invalidf(op, "config key %q has invalid value %q (want %s)", key, fmt.Sprint(fe.Value()), want)
	}
	return fault.Wrapf(err, fault.Invalid, op, "invalid config")
}

// CredentialList is auth.credentials (ADR-0171): nil means the key is absent. UnmarshalJSON makes a
// present key non-nil, null included, and refuses an unknown entry key, which yaml.UnmarshalStrict
// does not check below a custom unmarshaler.
type CredentialList []Credential

// Credential is one auth.credentials entry: the token lives in TokenFile, never in the config.
type Credential struct {
	TokenFile  string   `json:"tokenFile" validate:"required"`
	Role       string   `json:"role" validate:"oneof=admin developer viewer"`
	Namespaces []string `json:"namespaces,omitempty"`
}

// UnmarshalJSON decodes the list strictly, naming the entry index in an error.
func (l *CredentialList) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("auth.credentials: %w", err)
	}
	out := make(CredentialList, 0, len(raw))
	for i, r := range raw {
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.DisallowUnknownFields()
		var e Credential
		if err := dec.Decode(&e); err != nil {
			return fmt.Errorf("auth.credentials[%d]: %w", i, err)
		}
		out = append(out, e)
	}
	*l = out
	return nil
}

// validateCredentials applies ADR-0171 Decision 4: a present auth.credentials lists at least one entry
// and never merges with the auth.token shorthand. Errors name keys, never a value.
func (c Config) validateCredentials(op string) error {
	switch {
	case c.Auth.Credentials == nil:
		return nil
	case len(c.Auth.Credentials) == 0:
		return fault.Invalidf(op, "config key %q is present but lists no entry: add an entry or remove the key", "auth.credentials")
	case c.Auth.Token != "":
		return fault.Invalidf(op, "config key %q is set beside auth.credentials: remove it (funcdctl reads FUNCD_TOKEN too, so unset it in the daemon's environment)", "auth.token (FUNCD_TOKEN)")
	case len(c.Auth.Namespaces) != 1 || c.Auth.Namespaces[0] != "default":
		return fault.Invalidf(op, "config key %q is set beside auth.credentials: remove it and scope each entry with its own namespaces", "auth.namespaces (FUNCD_AUTH_NAMESPACES)")
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
