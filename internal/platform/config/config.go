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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/go-playground/validator/v10"

	"github.com/pyvvo/funcd/api/fault"
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
			Hosts    []string `json:"hosts,omitempty"`
			CertFile string   `json:"certFile,omitempty" env:"FUNCD_TLS_CERT_FILE"`
			KeyFile  string   `json:"keyFile,omitempty" env:"FUNCD_TLS_KEY_FILE"`
			Email    string   `json:"email,omitempty" env:"FUNCD_TLS_EMAIL"`
			CADir    string   `json:"caDir,omitempty" env:"FUNCD_TLS_CA_DIR"`
		} `json:"tls,omitempty"`
		// Ingress protection limits (ADR-0112, F75). Absent/zero ⇒ off (pass-through).
		Limits struct {
			RatePerMin   int    `json:"ratePerMin,omitempty" env:"FUNCD_LIMITS_RATE_PER_MIN" validate:"min=0"`
			Burst        int    `json:"burst,omitempty" env:"FUNCD_LIMITS_BURST" validate:"min=0"`
			Key          string `json:"key,omitempty" env:"FUNCD_LIMITS_KEY" validate:"omitempty,oneof=clientIP function"`
			MaxBodyBytes int64  `json:"maxBodyBytes,omitempty" env:"FUNCD_LIMITS_MAX_BODY_BYTES" validate:"min=0"`
			MaxInFlight  int    `json:"maxInFlight,omitempty" env:"FUNCD_LIMITS_MAX_IN_FLIGHT" validate:"min=0"`
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
				AllowOrigins  []string `json:"allowOrigins,omitempty"`
				AllowMethods  []string `json:"allowMethods,omitempty"`
				AllowHeaders  []string `json:"allowHeaders,omitempty"`
				MaxAgeSeconds int      `json:"maxAgeSeconds,omitempty" validate:"min=0"`
			} `json:"cors,omitempty"`
			Headers struct {
				Set    map[string]string `json:"set,omitempty"`
				Remove []string          `json:"remove,omitempty"`
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
			InternalAllow    []string `json:"internalAllow,omitempty"`
		} `json:"network,omitempty"`
	} `json:"server,omitempty"`
	Storage struct {
		Mode    string `json:"mode,omitempty" env:"FUNCD_STORAGE_MODE" validate:"oneof=file memory"`
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_DATA_DIR"`
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
		// Storage.Mode is memory. Interval/Rebaseline are Go durations ("30s").
		Backup struct {
			Enabled    bool   `json:"enabled,omitempty" env:"FUNCD_KVSTORE_BACKUP_ENABLED"`
			Target     string `json:"target,omitempty" env:"FUNCD_KVSTORE_BACKUP_TARGET"`
			Interval   string `json:"interval,omitempty" env:"FUNCD_KVSTORE_BACKUP_INTERVAL"`
			Rebaseline string `json:"rebaseline,omitempty" env:"FUNCD_KVSTORE_BACKUP_REBASELINE"`
			ChunkBytes int    `json:"chunkBytes,omitempty" env:"FUNCD_KVSTORE_BACKUP_CHUNK_BYTES" validate:"min=0"`
		} `json:"backup,omitempty"`
		// Cdc is the opt-in change-feed of the KV instance to the bus (ADR-0068), off by default. Enabled
		// without a Sink, or with Engine memory ⇒ fault.Invalid at startup; ignored with a warning when Storage.Mode
		// is memory. Retention is a Go duration ("24h").
		Cdc struct {
			Enabled   bool   `json:"enabled,omitempty" env:"FUNCD_KVSTORE_CDC_ENABLED"`
			Sink      string `json:"sink,omitempty" env:"FUNCD_KVSTORE_CDC_SINK"`
			Retention string `json:"retention,omitempty" env:"FUNCD_KVSTORE_CDC_RETENTION"`
		} `json:"cdc,omitempty"`
	} `json:"kvstore,omitempty"`
	Auth struct {
		Token      string   `json:"token,omitempty" env:"FUNCD_TOKEN"`
		Namespaces []string `json:"namespaces,omitempty" env:"FUNCD_AUTH_NAMESPACES" envSeparator:","`
	} `json:"auth,omitempty"`
	Secrets struct {
		EncryptionKeyFile string `json:"encryptionKeyFile,omitempty" env:"FUNCD_SECRETS_ENCRYPTION_KEY_FILE"`
	} `json:"secrets,omitempty"`
	Runtime struct {
		Mode       string `json:"mode,omitempty" env:"FUNCD_RUNTIME" validate:"oneof=process containerd"`
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
	Log struct {
		Format string `json:"format,omitempty" env:"FUNCD_LOG_FORMAT" validate:"oneof=json text"`
		Level  string `json:"level,omitempty" env:"FUNCD_LOG_LEVEL" validate:"oneof=debug info warn error"`
	} `json:"log,omitempty"`
	Telemetry struct {
		Endpoint string `json:"endpoint,omitempty" env:"FUNCD_TELEMETRY_ENDPOINT"`
		Insecure bool   `json:"insecure,omitempty" env:"FUNCD_TELEMETRY_INSECURE"`
	} `json:"telemetry,omitempty"`
	// Funclog tunes structured function-log capture (ADR-0081) and its traces signal (ADR-0101); both on by
	// default. A zero SegmentMaxBytes / empty SegmentMaxAge (a Go duration, "10s") keeps the sink default
	// (8 MiB / 10s). The sink writes to the blob substrate, so funcd-system is the only Bucket accepted.
	Funclog struct {
		Enabled         bool   `json:"enabled,omitempty" env:"FUNCD_FUNCLOG_ENABLED"`
		SegmentMaxBytes int    `json:"segmentMaxBytes,omitempty" env:"FUNCD_FUNCLOG_SEGMENT_MAX_BYTES" validate:"min=0"`
		SegmentMaxAge   string `json:"segmentMaxAge,omitempty" env:"FUNCD_FUNCLOG_SEGMENT_MAX_AGE"`
		Bucket          string `json:"bucket,omitempty" env:"FUNCD_FUNCLOG_BUCKET" validate:"omitempty,eq=funcd-system"`
		Traces          bool   `json:"traces,omitempty" env:"FUNCD_FUNCLOG_TRACES"`
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
	} `json:"catalog,omitempty"`

	// Workflow tunes the workflow engine (ADR-0094). Durable run state lives in its own dedicated Badger
	// instance at Workflow.DataDir (default <Storage.DataDir>/workflow; in-memory when Storage.Mode is
	// memory). DefaultStepTimeout + DefaultRetry feed the engine core; Retention is the terminal-run GC
	// horizon and PayloadLimit caps a run's input and a step's output in bytes (0 ⇒ unbounded).
	Workflow struct {
		DefaultStepTimeout string `json:"defaultStepTimeout,omitempty" env:"FUNCD_WORKFLOW_DEFAULT_STEP_TIMEOUT"`
		DefaultRetry       int    `json:"defaultRetry,omitempty" env:"FUNCD_WORKFLOW_DEFAULT_RETRY" validate:"min=0"`
		Retention          string `json:"retention,omitempty" env:"FUNCD_WORKFLOW_RETENTION"`
		PayloadLimit       int64  `json:"payloadLimit,omitempty" env:"FUNCD_WORKFLOW_PAYLOAD_LIMIT" validate:"min=0"`
		// DataDir is the run-state Badger directory. Empty ⇒ derived as <Storage.DataDir>/workflow in
		// Load(); ignored (in-memory) when Storage.Mode is memory. An explicit value overrides it.
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_WORKFLOW_DATA_DIR"`
	} `json:"workflow,omitempty"`

	// Eventing tunes the Sensor action-delivery reliability path (ADR-0118, F85). DeliveryAttempts is the
	// bounded-retry cap before a failed workflow:/function: delivery is dead-lettered. The dead-letter
	// queue is its own dedicated Badger instance at Deadletter.DataDir (default <Storage.DataDir>/deadletter;
	// in-memory when Storage.Mode is memory); Deadletter.Retention (TTL) and Deadletter.MaxEntries
	// (per-namespace count cap) drive the periodic retention sweep.
	Eventing struct {
		DeliveryAttempts int `json:"deliveryAttempts,omitempty" env:"FUNCD_EVENTING_DELIVERY_ATTEMPTS" validate:"min=0"`
		Deadletter       struct {
			Retention  string `json:"retention,omitempty" env:"FUNCD_EVENTING_DEADLETTER_RETENTION"`
			MaxEntries int    `json:"maxEntries,omitempty" env:"FUNCD_EVENTING_DEADLETTER_MAX_ENTRIES" validate:"min=0"`
			// DataDir is the DLQ's Badger directory. Empty ⇒ derived as <Storage.DataDir>/deadletter in
			// Load(); ignored (in-memory) when Storage.Mode is memory. An explicit value overrides it.
			DataDir string `json:"dataDir,omitempty" env:"FUNCD_EVENTING_DEADLETTER_DATA_DIR"`
		} `json:"deadletter,omitempty"`
		// BlobPollInterval is the cadence a `blob:` EventSource's prefixes are List-polled for new objects
		// (ADR-0119, F83). A Go duration ("15s"); one cadence for all blob sources in V1.
		BlobPollInterval string `json:"blobPollInterval,omitempty" env:"FUNCD_EVENTING_BLOB_POLL_INTERVAL"`
	} `json:"eventing,omitempty"`

	// Site tunes the declarative static web app reconciler (ADR-0139, FEAT-0003/F103). DefaultIndex is
	// the document a Site serves for "/" — and asserts present before it reports Ready — when its
	// spec.index is empty. A relative path (no leading '/').
	Site struct {
		DefaultIndex string `json:"defaultIndex,omitempty" env:"FUNCD_SITE_DEFAULT_INDEX" validate:"omitempty,startsnotwith=/"`
	} `json:"site,omitempty"`
}

// Flags are the top precedence tier (CLI flags with no env). MemoryOnly nil ⇒ --memory not set.
type Flags struct{ MemoryOnly *bool }

// defaults returns the Config with every built-in default set (the bottom precedence tier; identical
// to ADR-0061's defaults). The dataDir-relative containerd paths (Root, CNIConfDir) are derived in
// Load after the merge, when DataDir is final.
func defaults() Config {
	var c Config
	c.Server.ListenAddr = "0.0.0.0:8080"
	c.Server.DataPlaneAddr = "127.0.0.1:0"
	c.Storage.Mode = "file"
	c.Storage.DataDir = "/var/lib/funcd"
	c.Auth.Namespaces = []string{"default"}
	c.Runtime.Mode = "process"
	c.Runtime.Containerd.Snapshotter = "overlayfs"
	c.Runtime.Containerd.CNIBinDir = "/opt/cni/bin"
	c.Runtime.Containerd.SubnetCIDR = "10.63.0.0/16"
	c.Runtime.Containerd.ImagePrefix = "funcd/runtime-"
	c.Log.Format = "json"
	c.Log.Level = "info"
	c.Funclog.Enabled = true
	c.Funclog.Bucket = "funcd-system"
	c.Funclog.Traces = true
	// S3 gateway (ADR-0080/0085): opt-in; node-private loopback; 1 GiB buffered-object cap.
	c.S3Gateway.Enabled = false
	c.S3Gateway.ListenAddr = "127.0.0.1:9000"
	c.S3Gateway.MaxUploadBytes = 1 << 30
	// Workflow engine (ADR-0094): one attempt by default (no retry), 300s per-step timeout,
	// 30-day terminal-run retention, 1 MiB run-payload cap.
	c.Workflow.DefaultRetry = 1
	c.Workflow.DefaultStepTimeout = "300s"
	c.Workflow.Retention = "720h"
	c.Workflow.PayloadLimit = 1 << 20
	// Eventing DLQ (ADR-0118): 3 delivery attempts before dead-lettering; parked entries kept 720h with a
	// per-namespace cap of 1000, swept periodically.
	c.Eventing.DeliveryAttempts = 3
	c.Eventing.Deadletter.Retention = "720h"
	c.Eventing.Deadletter.MaxEntries = 1000
	// Blob EventSource poll cadence (ADR-0119): 15s default.
	c.Eventing.BlobPollInterval = "15s"
	// Site default index document (ADR-0139): the web convention.
	c.Site.DefaultIndex = "index.html"
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
