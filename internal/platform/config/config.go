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

const (
	defaultS3MaxUpload          = 1 << 30
	defaultWorkflowPayloadLimit = 256 << 10
)

// APIVersion / Kind are the optional funcdconfig.yaml envelope (validated only when present).
const (
	APIVersion = "funcd.io/v1alpha1"
	Kind       = "FuncdConfig"
)

// Config is the funcd daemon config — the single source of truth (ADR-0062). Each field's tags
// declare its yaml key (`json`), its FUNCD_* overlay var (`env`), and its validation rule (`validate`).
type Config struct {
	APIVersion string `json:"apiVersion,omitempty" validate:"omitempty,eq=funcd.io/v1alpha1" doc:"The version of this file's format. It may be left out: funcd only checks its value, and only when it is set."`
	Kind       string `json:"kind,omitempty" validate:"omitempty,eq=FuncdConfig" doc:"The kind of this document, which marks it as funcd's server configuration. It may be left out: funcd only checks its value, and only when it is set. funcdctl skips a document of this kind when it reads a folder's resource files."`
	Server     struct {
		ListenAddr    string `json:"listenAddr,omitempty" env:"FUNCD_LISTEN_ADDR" doc:"The address that the control plane's API listens on. An address off loopback needs auth.credentials or a token of your own in auth.token."`
		DataPlaneAddr string `json:"dataPlaneAddr,omitempty" env:"FUNCD_DATA_PLANE_ADDR" doc:"The address that the data plane listens on, where functions, Routes and Sites answer. Port 0 takes a free port. With server.auth.edge on, an address off loopback needs a credential of your own too."`
		// BaseDomain is the domain under which every namespace but default answers, on <namespace>.<baseDomain>
		// (ADR-0240). funcd validates it at start.
		BaseDomain string `json:"baseDomain,omitempty" env:"FUNCD_BASE_DOMAIN" doc:"The domain under which every namespace but default answers, as <namespace>.<baseDomain>. funcd checks it at start."`
		// TLS termination (ADR-0111, F74). When Enabled, both listeners serve HTTPS via ServeTLS.
		// Mode defaults to selfsigned (stdlib, offline). Empty/Enabled=false ⇒ plaintext (default).
		TLS struct {
			Enabled  bool     `json:"enabled,omitempty" env:"FUNCD_TLS_ENABLED" doc:"When true, both listeners serve HTTPS."`
			Mode     string   `json:"mode,omitempty" env:"FUNCD_TLS_MODE" validate:"omitempty,oneof=selfsigned provided acme" doc:"Where the certificate comes from: selfsigned, made by funcd with no network; provided, the files certFile and keyFile; or acme, from an ACME server. Empty means selfsigned."`
			Hosts    []string `json:"hosts,omitempty" env:"FUNCD_TLS_HOSTS" envSeparator:"," doc:"The host names that the certificate covers."`
			CertFile string   `json:"certFile,omitempty" env:"FUNCD_TLS_CERT_FILE" doc:"The certificate file, in provided mode."`
			KeyFile  string   `json:"keyFile,omitempty" env:"FUNCD_TLS_KEY_FILE" doc:"The private key file, in provided mode."`
			Email    string   `json:"email,omitempty" env:"FUNCD_TLS_EMAIL" doc:"The contact email of the ACME account, in acme mode."`
			CADir    string   `json:"caDir,omitempty" env:"FUNCD_TLS_CA_DIR" doc:"The directory URL of the ACME server, in acme mode. Empty means Let's Encrypt."`
		} `json:"tls,omitempty"`
		// Ingress protection limits (ADR-0112, F75). The rate, size and in-flight limits are off at 0; maxKeys is
		// a table size (min 1, default 4096) with no off switch (ADR-0164).
		Limits struct {
			RatePerMin   int    `json:"ratePerMin,omitempty" env:"FUNCD_LIMITS_RATE_PER_MIN" validate:"min=0" doc:"The requests per minute that the data plane lets each key make. 0 turns the rate limit off."`
			Burst        int    `json:"burst,omitempty" env:"FUNCD_LIMITS_BURST" validate:"min=0" doc:"How many requests a key may make in a row before its rate applies. 0 means as many as server.limits.ratePerMin."`
			Key          string `json:"key,omitempty" env:"FUNCD_LIMITS_KEY" validate:"omitempty,oneof=clientIP function" doc:"What the rate limit counts per: clientIP, each client address, or function, each function called. Empty means clientIP."`
			MaxBodyBytes int64  `json:"maxBodyBytes,omitempty" env:"FUNCD_LIMITS_MAX_BODY_BYTES" validate:"min=0" doc:"The largest request body, in bytes, that the data plane accepts; a larger one gets 413. 0 turns the cap off."`
			MaxInFlight  int    `json:"maxInFlight,omitempty" env:"FUNCD_LIMITS_MAX_IN_FLIGHT" validate:"min=0" doc:"The most requests that the data plane serves at once; one more gets 503. 0 turns the cap off."`
			MaxKeys      int    `json:"maxKeys,omitempty" env:"FUNCD_LIMITS_MAX_KEYS" validate:"min=1" doc:"The most keys whose rate the data plane tracks at once. It has no off switch."`
		} `json:"limits,omitempty"`
		// Edge authn PEP (ADR-0113, F77): enable per-target auth-stance enforcement on the data plane.
		Auth struct {
			Edge bool `json:"edge,omitempty" env:"FUNCD_AUTH_EDGE" doc:"When true, the data plane authenticates a call to a target whose stance is authenticated with the control plane's credentials, and authorizes it with its policies. When false, such a call gets 401. An open target answers either way."`
		} `json:"auth,omitempty"`
		// Edge observability (ADR-0114, F76): RED metrics + edge trace span + access log. Off by default.
		Observability struct {
			Metrics   bool `json:"metrics,omitempty" env:"FUNCD_OBS_METRICS" doc:"When true, the data plane counts each target's requests, errors and durations."`
			AccessLog bool `json:"accessLog,omitempty" env:"FUNCD_OBS_ACCESS_LOG" doc:"When true, the data plane logs each request."`
			Trace     bool `json:"trace,omitempty" env:"FUNCD_OBS_TRACE" doc:"When true, the data plane starts a trace span for each request."`
		} `json:"observability,omitempty"`
		// Edge shaping (ADR-0114, F78): CORS / response headers / gzip compression. Off by default.
		Shaping struct {
			CORS struct {
				AllowOrigins  []string `json:"allowOrigins,omitempty" env:"FUNCD_SHAPING_CORS_ALLOW_ORIGINS" envSeparator:"," doc:"The origins that a browser may call the data plane from."`
				AllowMethods  []string `json:"allowMethods,omitempty" env:"FUNCD_SHAPING_CORS_ALLOW_METHODS" envSeparator:"," doc:"The methods that a cross-origin call may use."`
				AllowHeaders  []string `json:"allowHeaders,omitempty" env:"FUNCD_SHAPING_CORS_ALLOW_HEADERS" envSeparator:"," doc:"The request headers that a cross-origin call may send."`
				MaxAgeSeconds int      `json:"maxAgeSeconds,omitempty" env:"FUNCD_SHAPING_CORS_MAX_AGE_SECONDS" validate:"min=0" doc:"How long, in seconds, a browser may keep the answer to a CORS preflight request."`
			} `json:"cors,omitempty"`
			Headers struct {
				// The env form splits pairs on ",", so a header value that contains a comma needs the file.
				Set    map[string]string `json:"set,omitempty" env:"FUNCD_SHAPING_HEADERS_SET" envSeparator:"," envKeyValSeparator:"=" validate:"dive,keys,header_name,endkeys" doc:"The response headers that the data plane sets, by name. Each name must be a valid header name. The variable splits pairs at commas, so a value that holds a comma needs the file."`
				Remove []string          `json:"remove,omitempty" env:"FUNCD_SHAPING_HEADERS_REMOVE" envSeparator:"," doc:"The response headers that the data plane removes."`
			} `json:"headers,omitempty"`
			Compression bool `json:"compression,omitempty" env:"FUNCD_SHAPING_COMPRESSION" doc:"When true, the data plane compresses responses with gzip."`
		} `json:"shaping,omitempty"`
		// Egress network isolation (ADR-0115, FEAT-0007/F80): the worker-egress default-deny substrate.
		// Off by default (phased); Linux/containerd only (a no-op elsewhere). When enabled, funcd programs
		// an nftables policy over the funcd0 bridge that default-denies worker egress and redirects
		// remaining external TCP into the egress gateway (F81). InternalAllow are the worker-reachable
		// node-service addresses (host:port) passed directly; DNSResolver is the DNS forwarder's upstream, read
		// only with a forwarder.
		Network struct {
			Egress            bool `json:"egress,omitempty" env:"FUNCD_NETWORK_EGRESS" doc:"When true, workers' outbound traffic is denied by default, and what their EgressPolicies allow goes through the egress gateway. Linux with containerd only; elsewhere it does nothing."`
			EgressGatewayPort int  `json:"egressGatewayPort,omitempty" env:"FUNCD_NETWORK_EGRESS_GATEWAY_PORT" validate:"min=0,max=65535" doc:"The port of the egress gateway on the workers' bridge, where their outbound TCP is redirected. Required when server.network.egress is on."`
			// DNSForwarderPort is the funcd DNS forwarder's port on the bridge address (ADR-0117, F81, ADR-0315): worker
			// :53 is REDIRECTed into it so it is the only reachable resolver (the domain trust anchor). It answers known
			// workers only, and forwards only the names an EgressPolicy domain rule allows them. Wired with the gateway
			// when Egress is on (Linux/containerd only). Zero ⇒ neither runs, and nothing goes out, DNS included.
			DNSForwarderPort int      `json:"dnsForwarderPort,omitempty" env:"FUNCD_NETWORK_DNS_FORWARDER_PORT" validate:"min=0,max=65535" doc:"The port of the DNS forwarder on the bridge address. A worker's DNS goes only to it, and it answers only the names that an EgressPolicy domain rule allows. 0 runs neither the forwarder nor the gateway, and nothing goes out, DNS included."`
			DNSResolver      string   `json:"dnsResolver,omitempty" env:"FUNCD_NETWORK_DNS_RESOLVER" doc:"The upstream resolver that the DNS forwarder asks."`
			InternalAllow    []string `json:"internalAllow,omitempty" env:"FUNCD_NETWORK_INTERNAL_ALLOW" envSeparator:"," doc:"The addresses on the node, as host:port, that workers may reach directly."`
			// WorkerSyncInterval is the egress WorkerIndex sync cadence (ADR-0163), a positive duration.
			WorkerSyncInterval string `json:"workerSyncInterval,omitempty" env:"FUNCD_NETWORK_WORKER_SYNC_INTERVAL" doc:"How often the egress rules follow the workers that start and stop, as a positive duration such as 2s."`
		} `json:"network,omitempty"`
		// ShutdownTimeout bounds the HTTP, Sensor and workflow-run drain at shutdown (ADR-0163), a positive duration.
		ShutdownTimeout string `json:"shutdownTimeout,omitempty" env:"FUNCD_SHUTDOWN_TIMEOUT" doc:"How long funcd waits at shutdown for HTTP requests, Sensors and workflow runs to finish, as a positive duration such as 15s."`
	} `json:"server,omitempty"`
	Storage struct {
		Mode    string `json:"mode,omitempty" env:"FUNCD_STORAGE_MODE" validate:"oneof=file memory" doc:"Where funcd keeps its data: file, on disk under storage.dataDir, or memory, where the data is lost when funcd stops. Under memory, funcd still writes some files to storage.dataDir, such as its sockets and the node master secret; it runs no platform or KV backup and no safe mode, and storage.minFreeSize does not apply. The --memory flag of funcd sets memory."`
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_DATA_DIR" validate:"required" doc:"The directory of funcd's data and working files, created when it is missing; a relative path resolves against funcd's working directory. Each store directory left empty, such as storage.metastoreDir, sits under it. A path too long for the Unix sockets that funcd creates under it refuses the start. The systemd unit that funcd install writes sets it through the variable, which wins over the file."`
		// MetastoreDir is the control-plane metastore's dedicated Badger directory (ADR-0065). Empty ⇒
		// derived as <DataDir>/store in Load(); ignored (in-memory engine) when Mode is memory. Its own
		// instance, never shared — see the store-per-service rule (restore class + service ownership).
		MetastoreDir string `json:"metastoreDir,omitempty" env:"FUNCD_METASTORE_DIR" doc:"The directory of the store that holds every object of the API, such as Functions and Secrets. Empty means <storage.dataDir>/store. Under storage.mode memory, funcd does not read it."`
		// QuarantineRetention is how long the start-up reclaims keep the data no object names before the sweep deletes
		// it (ADR-0288): an ADR-0194 duration of at least 1h; empty ⇒ 168h.
		QuarantineRetention string `json:"quarantineRetention,omitempty" env:"FUNCD_STORAGE_QUARANTINE_RETENTION" doc:"How long the quarantine keeps the data that no object names, which a start moves there instead of deleting it, before a sweep deletes it: the keys of a KVStore, the objects of a Bucket, a SQL database or a Function volume. funcdctl hold status lists the quarantine. Once the object exists again and holds no data, funcdctl quarantine return brings a KVStore's, a Bucket's or a volume's entry back into it; a SQL database's directory goes back by hand, with funcd stopped. A duration of at least 1h; empty means 168h."`
		// MinFreeSize is the free space funcd keeps on DataDir's filesystem for its own stores: workload writes that
		// would take it lower get 507 (ADR-0296). A ByteSize; empty ⇒ 2GiB; "0" turns the floor off.
		MinFreeSize string `json:"minFreeSize,omitempty" env:"FUNCD_STORAGE_MIN_FREE_SIZE" doc:"The free space that funcd keeps on the filesystem of storage.dataDir: a workload write that would take the free space below it gets 507, and function log records are dropped, until space is freed. A size such as 512Mi or 2GiB; empty means 2GiB, and 0 turns the floor off. Under storage.mode memory, it does not apply."`
	} `json:"storage,omitempty"`
	Kvstore struct {
		// Engine for the function-facing KV service (ADR-0066/0069): memory (default, ephemeral) or
		// badger (durable). Its own dedicated instance. DataDir is empty ⇒ derived as <Storage.DataDir>/kv
		// in Load(); an explicit value overrides the default location. Ignored (in-memory) when Storage.Mode
		// is memory.
		Engine  string `json:"engine,omitempty" env:"FUNCD_KVSTORE_ENGINE" validate:"omitempty,oneof=memory badger" doc:"Where the data of every KVStore lives: memory, lost when funcd stops, or badger, on disk in kvstore.dataDir. Empty means memory. Under storage.mode memory, the data stays in memory, and badger logs a warning."`
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_KVSTORE_DATA_DIR" doc:"The directory where kvstore.engine badger keeps the KV data, and where funcd restore kv loads a backup. Empty means <storage.dataDir>/kv. funcd does not read it with the memory engine or under storage.mode memory."`
		// MaxStoresPerNamespace is the per-namespace KVStore count cap enforced at admission (ADR-0072);
		// 0 ⇒ the default (100); a negative value disables the quota.
		MaxStoresPerNamespace int `json:"maxStoresPerNamespace,omitempty" env:"FUNCD_KVSTORE_MAX_STORES_PER_NAMESPACE" doc:"The most KVStores that one namespace may hold: creating one more gets 400. 0 means 100, and a negative value lifts the cap."`
		// SweepInterval is how often the KV service deletes the keys that expired (ADR-0320), so the change feed, the
		// backup and the quota see each expiry: an ADR-0194 duration of at least 100ms; empty ⇒ 1s.
		SweepInterval string `json:"sweepInterval,omitempty" env:"FUNCD_KVSTORE_SWEEP_INTERVAL" doc:"How often funcd deletes the KV keys whose time to live has passed. Reads miss such a key at once; the deletion lets the change feed, the KV backup and the quotas count it. A duration of at least 100ms; empty means 1s."`
		// Backup is the opt-in DR export of the KV instance (ADR-0067) on the common backup format (ADR-0209), off
		// by default. The target group (Target, CredentialsFile, SingleWriter) and the encryption group each come
		// whole from here when their lead key is set, else from backup: (ResolveKVBackup); ignored with a warning when
		// Storage.Mode is memory. Interval/Rebaseline/RebaselineRetry are durations ("30s"); RebaselineRetry is the
		// delay after a failed re-baseline (ADR-0195). Retention is the bases' ladder in hours, days and weeks.
		Backup struct {
			Enabled         bool   `json:"enabled,omitempty" env:"FUNCD_KVSTORE_BACKUP_ENABLED" doc:"When true, funcd backs up the KV data to kvstore.backup.target, or to backup.target when that key is empty, for funcd restore kv; one of the two must be set. Under storage.mode memory, funcd runs no KV backup and logs a warning; elsewhere it also needs kvstore.engine badger, and recipients or none in kvstore.backup.encryption or backup.encryption."`
			Target          string `json:"target,omitempty" env:"FUNCD_KVSTORE_BACKUP_TARGET" doc:"The KV backup's own target, an s3:// URL or file:///<absolute dir>, under whose kv/ prefix it writes. Empty uses backup.target, with backup.credentialsFile and backup.singleWriter. It must differ from backup.target and lie outside the blob store."`
			CredentialsFile string `json:"credentialsFile,omitempty" env:"FUNCD_KVSTORE_BACKUP_CREDENTIALS_FILE" doc:"An AWS shared credentials file whose default profile alone signs the KV backup's requests to an s3:// kvstore.backup.target. It needs kvstore.backup.target; empty uses the AWS SDK's default credential chain."`
			SingleWriter    bool   `json:"singleWriter,omitempty" env:"FUNCD_KVSTORE_BACKUP_SINGLE_WRITER" doc:"When true, the KV backup writes to a kvstore.backup.target that ignores or refuses a create-if-absent put (If-None-Match), on the promise that one funcd alone writes there, and logs a warning; when false, such a target fails every run. It needs kvstore.backup.target; a shared backup.target follows backup.singleWriter."`
			Interval        string `json:"interval,omitempty" env:"FUNCD_KVSTORE_BACKUP_INTERVAL" doc:"How often the KV backup ships the changes since its last point, as a positive duration; a tick with no change writes nothing. Empty means 30s."`
			Rebaseline      string `json:"rebaseline,omitempty" env:"FUNCD_KVSTORE_BACKUP_REBASELINE" doc:"How often the KV backup writes a new base, a full copy that starts a new chain, as a positive duration counted from the newest complete base, so a restart keeps the pace. It must stay below kvstore.backup.retention.hourly rounded up to whole days. Empty means 24h."`
			RebaselineRetry string `json:"rebaselineRetry,omitempty" env:"FUNCD_KVSTORE_BACKUP_REBASELINE_RETRY" doc:"How long the KV backup waits to try a base again after one failed, or while the platform is held, as a positive duration. Empty means 1h."`
			ChunkBytes      int    `json:"chunkBytes,omitempty" env:"FUNCD_KVSTORE_BACKUP_CHUNK_BYTES" validate:"min=0" doc:"The most bytes that the KV backup puts in one object, so a larger base or increment goes up in several parts. At most 8388608 (8 MiB); 0 means 8 MiB."`
			Encryption      struct {
				Recipients []string `json:"recipients,omitempty" env:"FUNCD_KVSTORE_BACKUP_ENCRYPTION_RECIPIENTS" envSeparator:"," doc:"The age recipients files that seal the KV backup, with at least 2 distinct X25519 or hybrid recipients in all, the two types not mixed. Setting it or kvstore.backup.encryption.none takes the KV backup's encryption from this block; else backup.encryption applies whole."`
				None       bool     `json:"none,omitempty" env:"FUNCD_KVSTORE_BACKUP_ENCRYPTION_NONE" doc:"When true, the KV backup is stored unsealed, with a warning at start, and kvstore.backup.encryption.recipients must be empty. Setting it takes the KV backup's encryption from this block instead of backup.encryption."`
			} `json:"encryption,omitempty"`
			Retention struct {
				Hourly int `json:"hourly,omitempty" env:"FUNCD_KVSTORE_BACKUP_RETENTION_HOURLY" doc:"How long, in hours, the hourly bases and the increments of the KV backup stay, by the lifecycle rule that funcd logs at start for the operator to set on the target: this many hours rounded up to whole days. funcd deletes nothing on the target itself. At least 1 while the KV backup is on."`
				Daily  int `json:"daily,omitempty" env:"FUNCD_KVSTORE_BACKUP_RETENTION_DAILY" doc:"How long, in days, the daily bases of the KV backup stay, by the lifecycle rule that funcd logs at start for the operator to set on the target. 0 makes no daily bases; a negative value is refused while the KV backup is on."`
				Weekly int `json:"weekly,omitempty" env:"FUNCD_KVSTORE_BACKUP_RETENTION_WEEKLY" doc:"How long, in weeks, the weekly bases of the KV backup stay, by the lifecycle rule that funcd logs at start for the operator to set on the target. 0 makes no weekly bases; a negative value is refused while the KV backup is on."`
			} `json:"retention,omitempty"`
		} `json:"backup,omitempty"`
		// Cdc is the opt-in change-feed of the KV instance to the bus (ADR-0068), off by default. Enabled
		// without a Sink, or with Engine memory ⇒ fault.Invalid at startup; ignored with a warning when Storage.Mode
		// is memory. Retention is a duration ("24h").
		Cdc struct {
			Enabled   bool   `json:"enabled,omitempty" env:"FUNCD_KVSTORE_CDC_ENABLED" doc:"When true, funcd publishes each write and delete of the KV data, in order, to the subject kvstore.cdc.sink of its own message bus, and a restart loses no change. Nothing in funcd reads that subject, and the bus takes no outside connection. It needs kvstore.cdc.sink; under storage.mode memory funcd runs no feed and logs a warning, and elsewhere it also needs kvstore.engine badger."`
			Sink      string `json:"sink,omitempty" env:"FUNCD_KVSTORE_CDC_SINK" doc:"The subject of funcd's message bus that the KV change feed publishes to; kvstore.cdc.enabled needs it."`
			Retention string `json:"retention,omitempty" env:"FUNCD_KVSTORE_CDC_RETENTION" doc:"How long the message bus keeps each change of the KV change feed, as a positive duration. Empty means 24h."`
		} `json:"cdc,omitempty"`
	} `json:"kvstore,omitempty"`
	// Backup is the platform backup target (ADR-0203), run by ADR-0205. Target is s3:// or file:///<absolute dir>;
	// CredentialsFile (s3:// only) is an AWS shared credentials file whose [default] profile signs every request.
	// Retention is in hours (hourly), days (daily, verified) and weeks (weekly).
	Backup struct {
		Target          string `json:"target,omitempty" env:"FUNCD_BACKUP_TARGET" doc:"Where the platform backup writes its generations: s3://<bucket>?region=<region> for an S3-compatible store, with endpoint and prefix parameters when needed, or file:///<absolute dir>. Empty, or with storage.mode memory, no platform backup runs. Blob's mirror goes here too while blob.target is empty, and so does the KV backup when kvstore.backup.enabled is true and kvstore.backup.target is empty. A target in the blob store's own bucket, or a directory in or around blob.dir, refuses the start. funcd deletes nothing on a target: it logs at start the lifecycle rules to set there."`
		CredentialsFile string `json:"credentialsFile,omitempty" env:"FUNCD_BACKUP_CREDENTIALS_FILE" doc:"The AWS shared credentials file whose [default] profile signs every request to an s3:// backup.target, from each backup that writes there. A file:// target refuses it. Empty, the default AWS credential chain signs them."`
		SingleWriter    bool   `json:"singleWriter,omitempty" env:"FUNCD_BACKUP_SINGLE_WRITER" doc:"When true, funcd writes on a target that ignores or refuses a create-if-absent put (If-None-Match: *), on the promise that no other funcd writes there, and warns. When false, every write to such a target fails, since nothing would fence a second writer."`
		Retention       struct {
			Hourly   int `json:"hourly,omitempty" env:"FUNCD_BACKUP_RETENTION_HOURLY" doc:"How many hours the lifecycle rule that funcd logs at start keeps an hourly generation, one that is neither daily nor weekly; the rule rounds it up to whole days. It must be at least 1."`
			Daily    int `json:"daily,omitempty" env:"FUNCD_BACKUP_RETENTION_DAILY" doc:"How many days the lifecycle rule that funcd logs at start keeps a daily generation, the first of each UTC day that has no weekly one. 0 writes no daily generations. A negative value refuses the start while backup.target is set."`
			Weekly   int `json:"weekly,omitempty" env:"FUNCD_BACKUP_RETENTION_WEEKLY" doc:"How many weeks the lifecycle rule that funcd logs at start keeps a weekly generation, the first of each ISO week in UTC. 0 writes no weekly generations. A negative value refuses the start while backup.target is set."`
			Verified int `json:"verified,omitempty" env:"FUNCD_BACKUP_RETENTION_VERIFIED" doc:"How many days the lifecycle rule that funcd logs at start keeps a verified copy, which funcdctl backup verify writes. It must be at least 1, and funcd warns when it is below backup.objectives.rpo minus backup.interval."`
			// PreUpgrade is the complete pre-upgrade generations funcd upgrade keeps listing as kept (ADR-0207).
			PreUpgrade int `json:"preUpgrade,omitempty" env:"FUNCD_BACKUP_RETENTION_PRE_UPGRADE" doc:"How many complete pre-upgrade generations funcd upgrade counts as kept. funcd deletes none: funcd upgrade lists the older ones past that number for the operator to prune. It must be at least 1."`
		} `json:"retention,omitempty"`
		// Encryption seals every generation to age recipients (ADR-0204): Recipients are paths of recipients files;
		// None is the explicit choice to store the files unsealed.
		Encryption struct {
			Recipients []string `json:"recipients,omitempty" env:"FUNCD_BACKUP_ENCRYPTION_RECIPIENTS" envSeparator:"," doc:"The files of age recipients, X25519 ones that start with age1 or hybrid ones that start with age1pq1, to which funcd seals every generation on backup.target. Together they must hold at least 2 distinct recipients, such as an operator's and a recovery one, and a hybrid one cannot sit beside an X25519 one. With backup.target set, it is required unless backup.encryption.none is true. The KV backup takes this group too while kvstore.backup.encryption sets neither of its keys."`
			None       bool     `json:"none,omitempty" env:"FUNCD_BACKUP_ENCRYPTION_NONE" doc:"When true, funcd stores the generations unsealed and warns that every record but Secret values leaves in plaintext. It needs backup.encryption.recipients empty and secrets.encryptionKeyFile set. The KV backup takes this group too while kvstore.backup.encryption sets neither of its keys."`
		} `json:"encryption,omitempty"`
		// Interval, RetryInterval and Objectives.RPO time the runs and the rpoRisk alert (ADR-0205 Decision 1).
		Interval      string `json:"interval,omitempty" env:"FUNCD_BACKUP_INTERVAL" doc:"How often the platform backup runs, as a positive duration: each run starts one interval after the previous one. It must not exceed backup.objectives.rpo, and funcd warns when it passes half of that objective. The longest of the hourly, daily and weekly retention terms must reach it."`
		RetryInterval string `json:"retryInterval,omitempty" env:"FUNCD_BACKUP_RETRY_INTERVAL" doc:"How long after a failed run the platform backup tries again, as a positive duration, but no later than one backup.interval after that run started. Blob's mirror retries after it too, and a held platform checks again after it."`
		Objectives    struct {
			RPO string `json:"rpo,omitempty" env:"FUNCD_BACKUP_OBJECTIVES_RPO" doc:"The recovery point objective of the platform backup, as a positive duration. While the newest verified generation, one that funcdctl backup verify checked, is at least this old, or there is none, funcdctl backup status shows rpoRisk and funcd logs backup rpo at risk, once per this time. It must be at least backup.interval."`
		} `json:"objectives,omitempty"`
	} `json:"backup,omitempty"`
	// WorkloadBackup bounds what a BackupSchedule's target may hold (ADR-0222), with or without backup.target: the keys and
	// the bytes of keys one listing reads, the latter a ByteSize, and the members of one copy.
	WorkloadBackup struct {
		MaxListedKeys    int    `json:"maxListedKeys,omitempty" env:"FUNCD_WORKLOAD_BACKUP_MAX_LISTED_KEYS" validate:"min=1" doc:"The most keys that one listing of a BackupSchedule's own target may read, with or without backup.target. A listing past it fails, and a failed listing of the schedule's copies turns its TargetSynced condition False with ListFailed."`
		MaxListedKeySize string `json:"maxListedKeySize,omitempty" env:"FUNCD_WORKLOAD_BACKUP_MAX_LISTED_KEY_SIZE" doc:"The most bytes of key names that one listing of a BackupSchedule's own target may read, as a size such as 64MiB. A listing past it fails, as one past workloadBackup.maxListedKeys does. It must be above zero."`
		MaxCopyMembers   int    `json:"maxCopyMembers,omitempty" env:"FUNCD_WORKLOAD_BACKUP_MAX_COPY_MEMBERS" validate:"min=1" doc:"The most members, the KVStores, Buckets, Lakes, SQL databases and volumes, that one Backup of a BackupSchedule may copy. A Backup whose scope resolves to more fails with no copy written, and a listing that finds a copy with more fails."`
	} `json:"workloadBackup,omitempty"`
	// Blob is the blob store (ADR-0208): Target an S3-compatible bucket funcd protects by its versioning, else the
	// local store at Dir (derived as <Storage.DataDir>/blob in Load), which Backup mirrors to backup.target while the
	// platform backup is on. VersionRetention is the store rule's noncurrent-version expiry, the window of a restore
	// to a time.
	Blob struct {
		Target           string `json:"target,omitempty" env:"FUNCD_BLOB_TARGET" doc:"A versioned S3-compatible bucket, as s3://<bucket>?region=…&endpoint=…, that holds the blob store, where Buckets keep their objects, instead of the local blob.dir. funcd refuses to start when the bucket keeps no versions, unless blob.allowUnversioned is true, and warns when it has no Object Lock. Under storage.mode memory, the blob store is in memory, and funcd ignores every blob key with a warning."`
		CredentialsFile  string `json:"credentialsFile,omitempty" env:"FUNCD_BLOB_CREDENTIALS_FILE" doc:"An AWS shared credentials file whose default profile alone signs the requests to blob.target, and the default credential of funcd restore blob. It needs blob.target; empty uses the AWS SDK's default credential chain."`
		Dir              string `json:"dir,omitempty" env:"FUNCD_BLOB_DIR" doc:"The directory of the local blob store, used while blob.target is empty; a relative path resolves against funcd's working directory. Empty means <storage.dataDir>/blob. It must stay empty beside blob.target, and must be neither storage.dataDir nor a directory above it."`
		AllowUnversioned bool   `json:"allowUnversioned,omitempty" env:"FUNCD_BLOB_ALLOW_UNVERSIONED" doc:"When true, funcd serves a blob.target that keeps no versions, with a warning, instead of refusing to start; a deleted or overwritten object is then gone. funcd reads it only with blob.target."`
		VersionRetention string `json:"versionRetention,omitempty" env:"FUNCD_BLOB_VERSION_RETENTION" doc:"How long the operator's lifecycle rule on blob.target keeps a noncurrent version, as a positive duration. funcd restore blob --at takes only a time within it, and refuses --at while it is empty."`
		Backup           struct {
			Interval   string `json:"interval,omitempty" env:"FUNCD_BLOB_BACKUP_INTERVAL" doc:"How often the mirror of the local blob store to backup.target writes a generation for funcd restore blob, as a positive duration from the start of one run to the next; after a failed run, the next comes after backup.retryInterval when that is sooner. The mirror runs while backup.target is set, storage.mode is file and blob.target is empty."`
			Rebaseline string `json:"rebaseline,omitempty" env:"FUNCD_BLOB_BACKUP_REBASELINE" doc:"How old the mirror's current epoch may grow before a run starts a new one, which uploads every object again; within an epoch, a run uploads only the objects that changed. At least blob.backup.interval."`
			Retention  string `json:"retention,omitempty" env:"FUNCD_BLOB_BACKUP_RETENTION" doc:"The least time that each mirror generation stays restorable. funcd deletes nothing on the target: it logs at start the lifecycle expiry to set on its blob/ prefix, blob.backup.rebaseline plus this time rounded up to whole days. At least blob.backup.interval."`
		} `json:"backup,omitempty"`
	} `json:"blob,omitempty"`
	// Recovery is safe mode (ADR-0207): AfterCrashes unclean starts in a row start held, twice it stop with exit
	// status 70; StableAfter is the continuous run that makes a start clean. Both are checked in cmd/funcd.
	Recovery struct {
		SafeMode struct {
			AfterCrashes int    `json:"afterCrashes,omitempty" env:"FUNCD_RECOVERY_SAFE_MODE_AFTER_CRASHES" doc:"How many unclean starts in a row make the next start held: the platform then waits for funcdctl hold release. After twice as many, funcd exits with status 70 and opens no store until funcd safe-mode reset. It must be at least 1, and storage.mode memory counts no starts."`
			StableAfter  string `json:"stableAfter,omitempty" env:"FUNCD_RECOVERY_SAFE_MODE_STABLE_AFTER" doc:"How long funcd must run, as a positive duration, before its start counts as clean and the count of unclean starts goes back to 0. A stop without an error counts as clean too."`
		} `json:"safeMode,omitempty"`
	} `json:"recovery,omitempty"`
	Auth struct {
		Token      string   `json:"token,omitempty" env:"FUNCD_TOKEN" doc:"The token of the one developer credential, which acts in auth.namespaces: one token of visible ASCII characters. Empty, or set to the built-in dev token, it is that token, with a warning, and funcd then refuses to start with server.listenAddr off loopback, or with server.dataPlaneAddr off loopback while server.auth.edge is on. It cannot be set beside auth.credentials."`
		Namespaces []string `json:"namespaces,omitempty" env:"FUNCD_AUTH_NAMESPACES" envSeparator:"," doc:"The namespaces in which the auth.token credential acts, or the built-in dev token when auth.token is empty. An empty list or entry refuses the start, and beside auth.credentials it must keep its default: each entry there names its own namespaces."`
		// Credentials is file-only (ADR-0171 Decision 1): caarlos0/env walks a slice of structs under an
		// empty prefix, so stray 0_* variables would add zero-value entries.
		Credentials CredentialList `json:"credentials,omitempty" env:"-" validate:"dive"`
	} `json:"auth,omitempty"`
	Secrets struct {
		EncryptionKeyFile string `json:"encryptionKeyFile,omitempty" env:"FUNCD_SECRETS_ENCRYPTION_KEY_FILE" doc:"The file of the key that encrypts Secret values at rest: exactly 32 raw bytes, an AES-256 key. Empty, the metastore holds Secret values unencrypted, and with storage.mode file funcd warns at start. funcd refuses to start when the key does not match how the stored Secrets were written."`
	} `json:"secrets,omitempty"`
	Runtime struct {
		Mode string `json:"mode,omitempty" env:"FUNCD_RUNTIME" validate:"oneof=process containerd" doc:"Where workers run: process, as child processes of funcd under its own OS user with no isolation, for development and tests; or containerd, in containers, on Linux only. Function volumes need containerd. The systemd unit that funcd install writes sets containerd through the variable, which wins over the file."`
		// BootBackoffInitial and BootBackoffMax bound the wait before a worker that crashed while booting is created
		// again (ADR-0160): positive durations, the max at least the initial wait.
		BootBackoffInitial string `json:"bootBackoffInitial,omitempty" env:"FUNCD_RUNTIME_BOOT_BACKOFF_INITIAL" doc:"The first wait before funcd creates again a worker that crashed while booting: one that failed to start, ended before it listened or did not listen within runtime.bootTimeout. Each further crash in a row doubles the wait, up to runtime.bootBackoffMax. It is a positive duration, at most a set runtime.bootBackoffMax."`
		BootBackoffMax     string `json:"bootBackoffMax,omitempty" env:"FUNCD_RUNTIME_BOOT_BACKOFF_MAX" doc:"The longest wait before funcd creates again a worker that keeps crashing while booting: the doubling from runtime.bootBackoffInitial stops there. Empty means 5m, or runtime.bootBackoffInitial when that is longer; a set value must be at least runtime.bootBackoffInitial."`
		// The supervision, boot and drain times (ADR-0163): positive durations, bootTimeout above
		// invoke.activationTimeout and handOutSettle at most drainGrace.
		SupervisionPeriod string `json:"supervisionPeriod,omitempty" env:"FUNCD_RUNTIME_SUPERVISION_PERIOD" doc:"How often funcd checks again each object that owns running instances, such as a Function's workers or a Lake's engine, so that it replaces one that died; a worker that exited after it listened is replaced this long after its last start. It is a positive duration; a set runtime.livenessTimeout must be at least twice it, and with s3gateway.enabled it counts toward the minimum of s3gateway.keyRotationPeriod."`
		BootTimeout       string `json:"bootTimeout,omitempty" env:"FUNCD_RUNTIME_BOOT_TIMEOUT" doc:"How long a worker has after its start to listen and to become ready. One that does not listen in time is stopped and counts as a boot crash, and on a Function's first boot one that is not ready in time turns the Function Failed. It is a positive duration, more than invoke.activationTimeout and less than a set app.upgradeTimeout."`
		DrainGrace        string `json:"drainGrace,omitempty" env:"FUNCD_RUNTIME_DRAIN_GRACE" doc:"How long a worker that funcd retires, such as one of a Function's old revision after the switch, may go on serving its calls: it stops once idle, else at the end of this grace with its calls cut. A revision that an open workflow run pins stays until the run ends. It is a positive duration of at least runtime.handOutSettle; with s3gateway.enabled, it counts toward the minimum of s3gateway.keyRotationPeriod."`
		HandOutSettle     string `json:"handOutSettle,omitempty" env:"FUNCD_RUNTIME_HAND_OUT_SETTLE" doc:"How long a call that funcd has handed to a worker may take to reach it. Within runtime.drainGrace, a worker of a Function's old revision stops no sooner than this after the switch and after the last call handed to it. It is a positive duration, at most runtime.drainGrace."`
		DrainPollInterval string `json:"drainPollInterval,omitempty" env:"FUNCD_RUNTIME_DRAIN_POLL_INTERVAL" doc:"The longest gap between two checks of whether the workers that funcd retires have gone idle, so that it can stop them. It is a positive duration; with s3gateway.enabled, it counts toward the minimum of s3gateway.keyRotationPeriod."`
		// LivenessTimeout is how long a listening replica may stay silent on /health/liveness before it is restarted
		// (ADR-0215): a positive duration, when set at least twice supervisionPeriod; empty ⇒ max(30s, three
		// supervisionPeriod), derived in cmd/funcd.
		LivenessTimeout string `json:"livenessTimeout,omitempty" env:"FUNCD_RUNTIME_LIVENESS_TIMEOUT" doc:"How long a worker that listened may go without answering its /health/liveness check before funcd stops it and creates it again. Empty means 30s, or three times runtime.supervisionPeriod when that is longer; a set value is a positive duration of at least twice runtime.supervisionPeriod."`
		// ReadinessTimeout is the window in which a worker whose readiness reads run late keeps its last answer, not the
		// bound of one probe (ADR-0259): a positive duration; empty ⇒ 5s.
		ReadinessTimeout string `json:"readinessTimeout,omitempty" env:"FUNCD_RUNTIME_READINESS_TIMEOUT" doc:"How long a worker whose readiness checks go unanswered is still judged by its last answer, so that calls can still reach it; it does not bound one check. Past it, with every worker late, the Function is Degraded with the reason NotAnswering. Empty means 5s; a set value is a positive duration."`
		// Process tunes the process driver (ADR-0167): StopGrace is the wait after SIGTERM before SIGKILL for a stop,
		// the shutdown close and the boot reap, a duration with 0 < d <= 10s.
		Process struct {
			StopGrace string `json:"stopGrace,omitempty" env:"FUNCD_PROCESS_STOP_GRACE" doc:"How long a worker on the process runtime has after SIGTERM to end before it gets SIGKILL: when funcd stops it, at shutdown, and at start for the workers that an earlier run left. It is a positive duration of at most 10s."`
		} `json:"process,omitempty"`
		// Containerd tunes the containerd driver; StopGrace is the wait after SIGTERM before SIGKILL for a single stop
		// and the daemon stop (ADR-0277), a duration with 0 < d <= 10s.
		Containerd struct {
			StopGrace   string `json:"stopGrace,omitempty" env:"FUNCD_CONTAINERD_STOP_GRACE" doc:"How long a container that funcd stops, a Function's worker or a Lake's or SQL database's engine, has after SIGTERM to end before it gets SIGKILL, at shutdown too. It is a positive duration of at most 10s. A Lake engine waits up to 5s at a stop for its running queries before it saves its catalog a last time, so a grace near or below that can kill it before that save."`
			Socket      string `json:"socket,omitempty" env:"FUNCD_CONTAINERD_SOCKET" doc:"The socket of a containerd that already runs, where funcd then runs its containers. Empty means funcd starts a private containerd of its own in runtime.containerd.root, which needs funcd to run as root, and stops it at shutdown; funcd does not start it again if it dies."`
			Root        string `json:"root,omitempty" env:"FUNCD_CONTAINERD_ROOT" doc:"The directory of the private containerd that funcd starts when runtime.containerd.socket is empty, which holds its socket, images and state. Empty means <storage.dataDir>/containerd."`
			Snapshotter string `json:"snapshotter,omitempty" env:"FUNCD_SNAPSHOTTER" doc:"The containerd snapshotter that unpacks the images and holds the root filesystem of each container."`
			CNIBinDir   string `json:"cniBinDir,omitempty" env:"FUNCD_CNI_BIN_DIR" doc:"The directory of the CNI plugins that attach each container to the workers' bridge. funcd install lays them down in the default directory, whatever this key holds."`
			CNIConfDir  string `json:"cniConfDir,omitempty" env:"FUNCD_CNI_CONF_DIR" doc:"The directory of the containers' CNI network list. When funcd's own list is missing there, funcd writes one, a bridge over runtime.containerd.subnetCIDR; it never rewrites a list that it finds. Empty means <storage.dataDir>/cni."`
			StateDir    string `json:"stateDir,omitempty" env:"FUNCD_CONTAINERD_STATE_DIR" doc:"The directory where funcd keeps the files that it makes for its running containers, such as their DNS settings and network namespaces. Empty means <storage.dataDir>/run."`
			SubnetCIDR  string `json:"subnetCIDR,omitempty" env:"FUNCD_SUBNET_CIDR" doc:"The subnet of the workers' bridge: each container gets an address in it, and the bridge takes its network address plus one, which the containers use as gateway and DNS server. funcd writes it into its CNI network list only when it creates that list in runtime.containerd.cniConfDir."`
			// BridgeAddress is funcd's address on its container bridge (ADR-0238); empty ⇒ the subnet's network address + 1.
			BridgeAddress string            `json:"bridgeAddress,omitempty" env:"FUNCD_BRIDGE_ADDRESS" doc:"funcd's address on the workers' bridge, where its containers reach the DNS forwarder, the Lake gateways and the SQL change feed, and which a derived s3gateway.endpoint names. It must lie in runtime.containerd.subnetCIDR. With server.network.egress on, an address other than the bridge's first one leaves the workers' DNS lookups unanswered. Empty means the subnet's network address plus one, the first address of the bridge that funcd sets up; the process runtime does not read it."`
			ImagePrefix   string            `json:"imagePrefix,omitempty" env:"FUNCD_IMAGE_PREFIX" doc:"The start of each runtime's image name, for a Function's spec.runtime and for the Lake and SQL engines: funcd runs <imagePrefix><runtime>:latest unless runtime.containerd.imageOverride names another image. Under the default, such an image comes only from those built into funcd; under another prefix, funcd pulls one that it does not hold."`
			ImageOverride map[string]string `json:"imageOverride,omitempty" env:"FUNCD_IMAGE_OVERRIDE" envSeparator:"," envKeyValSeparator:"=" doc:"The image to run in place of <imagePrefix><runtime>:latest, by runtime name: a Function's spec.runtime such as nodejs22, duckdb for a Lake's engine or rqlite for a SQL database's. funcd pulls such an image when it does not hold it."`
		} `json:"containerd,omitempty"`
	} `json:"runtime,omitempty"`
	// Controller tunes the platform controllers; GCSweepInterval is the owner garbage collector's sweep period
	// (ADR-0170), a positive duration.
	Controller struct {
		GCSweepInterval string `json:"gcSweepInterval,omitempty" env:"FUNCD_CONTROLLER_GC_SWEEP_INTERVAL" doc:"How often the owner collector checks every child kind for objects whose owner is gone, and deletes them, as a positive duration. Its watch on the owners deletes most of them at once; this check, also run at start, finds what a crash or a dropped watch missed."`
		// The retry and requeue times (ADR-0163): positive durations, retryBackoffMax at least 5ms.
		RetryBackoffMax      string `json:"retryBackoffMax,omitempty" env:"FUNCD_CONTROLLER_RETRY_BACKOFF_MAX" doc:"The longest wait before the controller retries a failed pass or reopens a broken watch. The wait starts at 5ms and doubles after each failure, so this must be at least 5ms."`
		ReferentPollInterval string `json:"referentPollInterval,omitempty" env:"FUNCD_CONTROLLER_REFERENT_POLL_INTERVAL" doc:"How often funcd checks again an object that waits for another one it names, as a positive duration. Such an object is, for example, a Function or a Lake whose Secret, ConfigMap or Bucket is missing, a Route whose backend is missing, or a WorkflowRun that waits for its Workflow. A waiting Function is checked at least once per runtime.supervisionPeriod."`
		RouteResyncInterval  string `json:"routeResyncInterval,omitempty" env:"FUNCD_CONTROLLER_ROUTE_RESYNC_INTERVAL" doc:"How often funcd checks again a Route whose backend exists, so that it notices when the backend is deleted, as a positive duration. A Route whose backend is missing is checked every controller.referentPollInterval."`
	} `json:"controller,omitempty"`
	Log struct {
		Format string `json:"format,omitempty" env:"FUNCD_LOG_FORMAT" validate:"oneof=json text" doc:"The format of the log that funcd writes to standard output: json writes one JSON object per line, and text writes key=value pairs."`
		Level  string `json:"level,omitempty" env:"FUNCD_LOG_LEVEL" validate:"oneof=debug info warn error" doc:"The lowest level of the records that funcd writes to its log; it drops the records below it."`
	} `json:"log,omitempty"`
	Telemetry struct {
		Endpoint string `json:"endpoint,omitempty" env:"FUNCD_TELEMETRY_ENDPOINT" doc:"The host:port of an OTLP gRPC collector, such as localhost:4317, to which funcd exports its own metrics and traces. Empty exports nothing."`
		Insecure bool   `json:"insecure,omitempty" env:"FUNCD_TELEMETRY_INSECURE" doc:"When true, funcd connects to telemetry.endpoint without TLS."`
	} `json:"telemetry,omitempty"`
	// Funclog tunes structured function-log capture (ADR-0081) and its traces signal (ADR-0101); both on by
	// default. A zero SegmentMaxBytes / empty SegmentMaxAge (a duration, "10s") keeps the sink default
	// (8 MiB / 10s). The sink writes to the blob substrate, so funcd-system is the only Bucket accepted.
	Funclog struct {
		Enabled         bool   `json:"enabled,omitempty" env:"FUNCD_FUNCLOG_ENABLED" doc:"When false, funcd stores neither the logs nor the spans of the Functions' workers, so funcdctl logs has nothing new to show."`
		SegmentMaxBytes int    `json:"segmentMaxBytes,omitempty" env:"FUNCD_FUNCLOG_SEGMENT_MAX_BYTES" validate:"min=0" doc:"The size, in bytes, at which funcd writes the log or span records that it holds for one worker to the funcd-system bucket as a segment, unless funclog.segmentMaxAge comes first. 0 means 8 MiB."`
		SegmentMaxAge   string `json:"segmentMaxAge,omitempty" env:"FUNCD_FUNCLOG_SEGMENT_MAX_AGE" doc:"How long funcd holds a worker's log or span records before it writes them to the funcd-system bucket as a segment, unless funclog.segmentMaxBytes comes first. funcdctl logs shows a record only once its segment is written. Empty or 0s means 10s."`
		Bucket          string `json:"bucket,omitempty" env:"FUNCD_FUNCLOG_BUCKET" validate:"omitempty,eq=funcd-system" doc:"The bucket of the Functions' logs and spans. funcd does not read it: they always go to the funcd-system bucket."`
		Traces          bool   `json:"traces,omitempty" env:"FUNCD_FUNCLOG_TRACES" doc:"When false, funcd stores no spans, of Function calls or of workflow runs, and still stores the logs. While funclog.enabled is false, funcd stores no spans either way."`
		// MaxRecordBytes bounds one shim log or span record line (ADR-0168); 0 keeps the 65536 default, and funcd refuses
		// a nonzero value below 1024 or above the 1 MiB reader cap.
		MaxRecordBytes int `json:"maxRecordBytes,omitempty" env:"FUNCD_FUNCLOG_MAX_RECORD_BYTES" validate:"min=0" doc:"The longest line, in bytes, that a Function's shim writes for one log or span record; the shim cuts a longer record and marks it as truncated. 0 means 65536. A value other than 0 must be from 1024 to 1048576."`
	} `json:"funclog,omitempty"`
	// S3Gateway is the opt-in S3-protocol frontend over the blob substrate (ADR-0080/0085).
	// Disabled by default ⇒ no listener, no IAM, no keypair injection.
	S3Gateway struct {
		Enabled    bool   `json:"enabled,omitempty" env:"FUNCD_S3GATEWAY_ENABLED" doc:"When true, funcd serves an S3 API over the blob store at s3gateway.listenAddr, and gives each Function and Lake that binds a Bucket in spec.bindings.blob the gateway's endpoint and a key pair of its own. When false, nothing listens, and a Lake engine gets no S3 endpoint."`
		ListenAddr string `json:"listenAddr,omitempty" env:"FUNCD_S3GATEWAY_LISTEN_ADDR" doc:"The address that the S3 gateway listens on while s3gateway.enabled is true; a port that is taken fails the start. While s3gateway.endpoint is empty, it also sets the URL that workers get."`
		// Endpoint is the sandbox-facing S3 URL injected into a spec.bindings.blob worker's AWS_ENDPOINT_URL_S3
		// (ADR-0085). Empty ⇒ derived: under containerd, http://<bridge address>:<port> when ListenAddr's host is
		// empty, unspecified or the bridge address (ADR-0238), else http://<ListenAddr>.
		Endpoint         string `json:"endpoint,omitempty" env:"FUNCD_S3GATEWAY_ENDPOINT" doc:"The S3 URL that funcd gives the Lake engines and the workers of the Functions with spec.bindings.blob; a Lake needs an IP address in it, not a host name. Empty, it is http://<bridge address>:<port> under the containerd runtime when the host of s3gateway.listenAddr is empty, unspecified or the bridge address; otherwise a Function gets http://<s3gateway.listenAddr> and a Lake engine none."`
		MaxUploadBytes   int64  `json:"maxUploadBytes,omitempty" env:"FUNCD_S3GATEWAY_MAX_UPLOAD_BYTES" validate:"min=0" doc:"The largest object, in bytes, that the S3 gateway takes in one PUT or multipart upload, which it holds in memory; a larger one gets EntityTooLarge. The multipart uploads in flight hold at most three times it in all, and one principal's parts at most it, past which a part gets SlowDown. 0 means 1 GiB; lake.catalogMaxSize must not exceed it."`
		MasterSecretFile string `json:"masterSecretFile,omitempty" env:"FUNCD_S3GATEWAY_MASTER_SECRET_FILE" doc:"The file of the node master secret, from which funcd derives every S3 key pair and the tokens it gives Functions and engines; funcd reads it with the gateway on or off. Empty means <storage.dataDir>/s3gateway/master.key, which funcd creates when it is missing; a file set here must exist and not be empty."`
		// KeyRotationPeriod is how long a derived S3 key pair's window lasts (ADR-0270): a pair is valid for one to
		// two periods and never past a restart. At least 1h.
		KeyRotationPeriod string `json:"keyRotationPeriod,omitempty" env:"FUNCD_S3GATEWAY_KEY_ROTATION_PERIOD" doc:"How long a window of the derived S3 key pairs lasts: a pair is valid for one to two windows and never past a restart, and the workers that carry one are replaced before it expires. At least 1h, checked with the gateway off too; with it on, more than 20 times the sum of runtime.supervisionPeriod, runtime.drainGrace, twice runtime.drainPollInterval and 10s."`
	} `json:"s3gateway,omitempty"`

	// Lake tunes the Lake engines (ADR-0274) and the per-Lake lake::query PEP proxies (ADR-0137), which bind the
	// bridge address (runtime.containerd.bridgeAddress, ADR-0238).
	Lake struct {
		// SaveInterval is how often a Lake engine saves its catalog after a commit, the Lake's recovery point
		// (ADR-0274): an ADR-0194 duration from 500ms to 1h, an opt-in; empty, every answer waits for a save that
		// covers it (ADR-0279). It is set by hand: funcdctl backup plan sets no Lake key (ADR-0286).
		SaveInterval string `json:"saveInterval,omitempty" env:"FUNCD_LAKE_SAVE_INTERVAL" doc:"How often a Lake engine saves its catalog after a commit, as a duration from 500ms to 1h. Set, the engine answers each commit at once, and a crash can lose the commits of one interval and of the two saves around it. Empty, each answer waits for a save that covers it."`
		// CatalogMaxSize caps one Lake's catalog in the catalog store (ADR-0293): a ByteSize, 256MiB when empty
		// (ADR-0294), at most s3gateway.maxUploadBytes. A save past it fails, and the Lake shows it (ADR-0276).
		CatalogMaxSize string `json:"catalogMaxSize,omitempty" env:"FUNCD_LAKE_CATALOG_MAX_SIZE" doc:"The largest catalog that one Lake may keep in the catalog store, as a size such as 256MiB: a save past it fails, and the Lake shows CatalogSaved False. Empty means 256MiB, or s3gateway.maxUploadBytes when that is smaller. A value of 0, or one above s3gateway.maxUploadBytes, refuses the start."`
		// ScratchSize sizes the Lake engine's scratch filesystem (ADR-0294): a ByteSize, at least twice
		// CatalogMaxSize plus 512MiB; empty, the larger of 1GiB and that sum.
		ScratchSize string `json:"scratchSize,omitempty" env:"FUNCD_LAKE_SCRATCH_SIZE" doc:"The size of the scratch filesystem that each running Lake engine gets for its catalog copy, its checkpoint copy and its spills, preallocated on the filesystem of storage.dataDir, as a size such as 1GiB. It must be at least twice lake.catalogMaxSize plus 512MiB; empty, it is that sum or 1GiB, whichever is larger. A Lake whose scratch does not fit above storage.minFreeSize stays Pending with ScratchSpaceLacking."`
		// EnginePollInterval and EngineProbeTimeout pace the wait for an engine's readiness probe (ADR-0163).
		EnginePollInterval string `json:"enginePollInterval,omitempty" env:"FUNCD_LAKE_ENGINE_POLL_INTERVAL" doc:"How often funcd checks again on a Lake whose engine does not answer its readiness probe yet, or whose scratch does not fit yet, as a positive duration. A value above runtime.supervisionPeriod waits only that period."`
		EngineProbeTimeout string `json:"engineProbeTimeout,omitempty" env:"FUNCD_LAKE_ENGINE_PROBE_TIMEOUT" doc:"How long one readiness probe of an engine may take, a Lake's engine or a SQL database's, as a positive duration. An engine that answers later counts as not ready."`
	} `json:"lake,omitempty"`

	// Workflow tunes the workflow engine (ADR-0094). Durable run state lives in its own dedicated Badger
	// instance at Workflow.DataDir (default <Storage.DataDir>/workflow; in-memory when Storage.Mode is
	// memory). DefaultStepTimeout + DefaultRetry feed the engine core. Retention is the terminal-run GC
	// horizon: a periodic sweep reclaims each closed run (engine record and WorkflowRun object) older than
	// it; 0 ⇒ no sweep. PayloadLimit caps the bytes of a run input (at WorkflowRun admission and run start)
	// and of each step output; 0 ⇒ unbounded.
	Workflow struct {
		DefaultStepTimeout string `json:"defaultStepTimeout,omitempty" env:"FUNCD_WORKFLOW_DEFAULT_STEP_TIMEOUT" doc:"How long one attempt of a function step may run when the step's timeout is unset or 0s. An attempt that runs longer fails, and the step tries again while it has attempts left. Empty or 0s means no limit."`
		DefaultRetry       int    `json:"defaultRetry,omitempty" env:"FUNCD_WORKFLOW_DEFAULT_RETRY" validate:"min=0" doc:"How many attempts funcd makes at a function step, the first one included, when its retry.maxAttempts is unset. 0 means 1, a single attempt with no retry."`
		Retention          string `json:"retention,omitempty" env:"FUNCD_WORKFLOW_RETENTION" doc:"How long funcd keeps a workflow run after it ends. A sweep, every hour or sooner when this is shorter, then deletes the run's record and its WorkflowRun. Empty or 0s keeps them with no time limit."`
		PayloadLimit       int64  `json:"payloadLimit,omitempty" env:"FUNCD_WORKFLOW_PAYLOAD_LIMIT" validate:"min=0" doc:"The most bytes that a run's input and each step's output may hold. A larger input is refused with 413 when the WorkflowRun is created, or fails the run with reason PayloadLimitExceeded when a Sensor started it; a larger output fails its step with no retry. 0 means no limit."`
		// MaxStepsInFlight bounds the function-step calls in flight across all runs (ADR-0146); 0 ⇒ no cap.
		MaxStepsInFlight int `json:"maxStepsInFlight,omitempty" env:"FUNCD_WORKFLOW_MAX_STEPS_IN_FLIGHT" validate:"min=0" doc:"The most attempts of function steps that all workflow runs have in flight at once; a step waits for a free slot. 0 means no limit."`
		// ArtifactPollInterval re-checks a Workflow whose step artifact is not pushed; DefaultRetryBackoff is the first
		// retry gap of a step with no retry.backoff, doubled up to 1h; 0 ⇒ none (ADR-0163).
		ArtifactPollInterval string `json:"artifactPollInterval,omitempty" env:"FUNCD_WORKFLOW_ARTIFACT_POLL_INTERVAL" doc:"The longest wait before funcd checks again a Workflow that waits for a step image to reach its registry, or for a Secret that it declares, as a positive duration."`
		DefaultRetryBackoff  string `json:"defaultRetryBackoff,omitempty" env:"FUNCD_WORKFLOW_DEFAULT_RETRY_BACKOFF" doc:"How long a function step waits before its second attempt when its retry.backoff is unset or 0s. Each later wait doubles, up to 1h, and 0s tries again at once. It must be at most 1h."`
		// DataDir is the run-state Badger directory. Empty ⇒ derived as <Storage.DataDir>/workflow in
		// Load(); ignored (in-memory) when Storage.Mode is memory. An explicit value overrides it.
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_WORKFLOW_DATA_DIR" doc:"The directory of the run state, which keeps each workflow run's progress. Empty means <storage.dataDir>/workflow. With storage.mode memory, the run state lives in memory and funcd does not use this directory."`
	} `json:"workflow,omitempty"`

	// Eventing tunes the Sensor action-delivery reliability path (ADR-0118, F85). DeliveryAttempts is the
	// bounded-retry cap before a failed workflow:/function: delivery is dead-lettered. The event store (ADR-0201,
	// ADR-0300), one Badger instance holding the dead letters, the blob seen lists and the change feed's watermarks, is
	// at EventStore.DataDir (default <Storage.DataDir>/eventstore; in-memory when Storage.Mode is memory); Deadletter.Retention (TTL) and
	// Deadletter.MaxEntries (per-namespace count cap) drive the periodic retention sweep of dead letters only. MaxDeliveriesInFlight (the delivery
	// workers), MaxInFlightPerTarget and MaxQueuedPerSensor size the Sensor delivery queue (ADR-0156); each is
	// at least 1 and the per-target cap at most the workers. MaxDeliveriesInFlight is declared before
	// MaxInFlightPerTarget so a bad worker count is the error reported, not the cap's cross-field check.
	Eventing struct {
		DeliveryAttempts      int `json:"deliveryAttempts,omitempty" env:"FUNCD_EVENTING_DELIVERY_ATTEMPTS" validate:"min=0" doc:"How many attempts a Sensor makes to deliver an event to an action's target, the first one included, before it keeps the event as a dead letter. 0 means 3."`
		MaxDeliveriesInFlight int `json:"maxDeliveriesInFlight,omitempty" env:"FUNCD_EVENTING_MAX_DELIVERIES_IN_FLIGHT" validate:"min=1" doc:"How many Sensor delivery attempts funcd makes at once, across all Sensors."`
		MaxInFlightPerTarget  int `json:"maxInFlightPerTarget,omitempty" env:"FUNCD_EVENTING_MAX_IN_FLIGHT_PER_TARGET" validate:"min=1,ltefield=MaxDeliveriesInFlight" doc:"How many Sensor delivery attempts funcd makes at once to one Function or Workflow, across all Sensors. It must be at most eventing.maxDeliveriesInFlight."`
		MaxQueuedPerSensor    int `json:"maxQueuedPerSensor,omitempty" env:"FUNCD_EVENTING_MAX_QUEUED_PER_SENSOR" validate:"min=1" doc:"The most deliveries that one Sensor may hold at once, waiting, in flight or between attempts. A delivery past it becomes a dead letter at once, with no attempt."`
		Deadletter            struct {
			Retention  string `json:"retention,omitempty" env:"FUNCD_EVENTING_DEADLETTER_RETENTION" doc:"How long funcd keeps a dead letter after its delivery last failed. A sweep, every hour or sooner when this is shorter, deletes the older ones. Empty or 0s sets no age limit."`
			MaxEntries int    `json:"maxEntries,omitempty" env:"FUNCD_EVENTING_DEADLETTER_MAX_ENTRIES" validate:"min=0" doc:"The most dead letters that each namespace keeps. The sweep deletes the oldest ones past it, so a namespace may hold more until the next sweep. 0 means no limit."`
		} `json:"deadletter,omitempty"`
		EventStore struct {
			// DataDir is the event store's Badger directory (ADR-0300). Empty ⇒ derived as <Storage.DataDir>/eventstore
			// in Load(); ignored (in-memory) when Storage.Mode is memory. An explicit value overrides it.
			DataDir string `json:"dataDir,omitempty" env:"FUNCD_EVENTING_EVENTSTORE_DATA_DIR" doc:"The directory of the event store, which keeps the events that Sensors could not deliver, the keys that blob EventSources have seen and the SQL change feed's watermarks. Empty means <storage.dataDir>/eventstore. With storage.mode memory, the event store lives in memory and funcd does not use this directory."`
		} `json:"eventstore,omitempty"`
		// BlobPollInterval is the cadence a `blob:` EventSource's prefixes are List-polled for new objects
		// (ADR-0119, F83). A duration ("15s"); one cadence for all blob sources in V1.
		BlobPollInterval string `json:"blobPollInterval,omitempty" env:"FUNCD_EVENTING_BLOB_POLL_INTERVAL" doc:"How often funcd lists the prefixes that each blob EventSource watches, to fire an event for each new or rewritten object. Empty or 0s means 15s."`
		// BucketRecheckInterval re-checks every blob EventSource's Bucket; DeliveryBackoffInitial/Max pace the Sensor
		// delivery retry, an empty max following max(10s, initial) (ADR-0163).
		BucketRecheckInterval  string `json:"bucketRecheckInterval,omitempty" env:"FUNCD_EVENTING_BUCKET_RECHECK_INTERVAL" doc:"How often funcd checks again whether the Bucket of each blob EventSource, or the SQLDatabase of each sql EventSource, exists, as a positive duration. The EventSource's Ready condition follows what it finds."`
		DeliveryBackoffInitial string `json:"deliveryBackoffInitial,omitempty" env:"FUNCD_EVENTING_DELIVERY_BACKOFF_INITIAL" doc:"How long a Sensor waits after a failed attempt before it tries the delivery again, as a positive duration. Each later wait doubles, up to eventing.deliveryBackoffMax."`
		DeliveryBackoffMax     string `json:"deliveryBackoffMax,omitempty" env:"FUNCD_EVENTING_DELIVERY_BACKOFF_MAX" doc:"The longest wait between two attempts of a Sensor delivery. Empty means the larger of 10s and eventing.deliveryBackoffInitial, and a set value must be at least eventing.deliveryBackoffInitial."`
	} `json:"eventing,omitempty"`

	// Invoke tunes function invocation. MaxNestedInFlight caps the nested (fn-to-fn) calls in flight to one
	// Function (ADR-0147); 0 ⇒ the default (10). It cannot be disabled. DefaultTimeout bounds external invokes
	// only (ADR-0151): how long one waits for its response to start when the Function sets no spec.timeout; a
	// link keeps links[].timeout (30 s default). A duration (ADR-0194); empty or 0s ⇒ 60s, at most 1h.
	Invoke struct {
		MaxNestedInFlight int    `json:"maxNestedInFlight,omitempty" env:"FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT" validate:"min=0" doc:"The most calls that Functions make over their links to one Function that may be in flight at once; one more gets 429 at once. 0 means 10, and the cap has no off switch."`
		DefaultTimeout    string `json:"defaultTimeout,omitempty" env:"FUNCD_INVOKE_DEFAULT_TIMEOUT" doc:"How long a call through the data plane, or an App's hook call, waits for its response to start when its Function sets no spec.timeout, a cold start included; a data-plane call past it gets 504. Empty or 0s means 60s, and it is at most 1h. A call over a link keeps the link's own timeout."`
		// ActivationTimeout is the longest cold-start hold of a wake; ReclaimInterval the idle-reclaim cadence (ADR-0163).
		ActivationTimeout string `json:"activationTimeout,omitempty" env:"FUNCD_INVOKE_ACTIVATION_TIMEOUT" doc:"How long a call waits for a ready worker when its Function has none, the start of an idle Function included, before it gets 503. Calls that arrive during one start share its clock. It is a positive duration, less than runtime.bootTimeout."`
		ReclaimInterval   string `json:"reclaimInterval,omitempty" env:"FUNCD_INVOKE_RECLAIM_INTERVAL" doc:"How often funcd stops the workers of each Function with spec.scaling.minReplicas 0 that has served no call for its spec.scaling.idleTimeout, so such a Function can run up to this long past that timeout. It is a positive duration."`
	} `json:"invoke,omitempty"`

	// Functions tunes Functions (ADR-0291). DefaultResources fills each field a solo Function's spec.resources leaves
	// empty: 1 CPU and 512Mi by default; an empty value sets no default for its field.
	Functions struct {
		DefaultResources struct {
			CPU    string `json:"cpu" env:"FUNCD_FUNCTIONS_DEFAULT_CPU" doc:"The CPU cores that each worker of a Function with no spec.pooling.worker gets when its spec.resources.cpu is empty, such as 0.5 or 2; funcd refuses a value below 0.01. Empty sets no default, so such a worker has no CPU limit. The process runtime enforces no limit."`
			Memory string `json:"memory" env:"FUNCD_FUNCTIONS_DEFAULT_MEMORY" doc:"The memory that each worker of a Function with no spec.pooling.worker gets when its spec.resources.memory is empty, a positive size such as 256Mi or 1GiB. Empty sets no default, so such a worker has no memory limit. The process runtime enforces no limit."`
		} `json:"defaultResources,omitempty"`
	} `json:"functions,omitempty"`

	// Site tunes the declarative static web app reconciler (ADR-0139, FEAT-0003/F103). DefaultIndex is
	// the document a Site serves for "/" — and asserts present before it reports Ready — when its
	// spec.index is empty. A relative path (no leading '/').
	Site struct {
		DefaultIndex string `json:"defaultIndex,omitempty" env:"FUNCD_SITE_DEFAULT_INDEX" validate:"omitempty,startsnotwith=/" doc:"The document that a Site serves for / when its spec.index is empty: a path inside its bundle, with no leading slash. A Site turns Ready only once its bundle holds that document."`
	} `json:"site,omitempty"`

	// SQL tunes the SQL databases (ADR-0232, ADR-0233, ADR-0235). MaxDatabasesPerNamespace is the per-namespace SQLDatabase
	// count cap enforced at admission; 0 ⇒ the default (20); a negative value disables the quota. CallTimeout bounds
	// one context.sql call, a positive duration; empty ⇒ 30s.
	SQL struct {
		MaxDatabasesPerNamespace int    `json:"maxDatabasesPerNamespace,omitempty" env:"FUNCD_SQL_MAX_DATABASES_PER_NAMESPACE" doc:"The most SQLDatabases that one namespace may hold: a create past it gets 400. 0 means 20, and a negative value means no cap."`
		CallTimeout              string `json:"callTimeout,omitempty" env:"FUNCD_SQL_CALL_TIMEOUT" doc:"How long one context.sql call of a Function may run before it fails, as a positive duration such as 30s; the database's engine also stops each statement at it. Empty means 30s."`
		// ChangeFeedPort is the port the engines post their changes to (ADR-0235), on the bridge address (ADR-0238);
		// 0 ⇒ 7075 with a bridge address, else a free loopback port chosen at start (ADR-0260).
		ChangeFeedPort int `json:"changeFeedPort,omitempty" env:"FUNCD_SQL_CHANGE_FEED_PORT" validate:"min=0,max=65535" doc:"The port where funcd takes the row changes that the SQL database engines post, which EventSources of kind sql turn into events. funcd listens on its bridge address under the containerd runtime, else on loopback. 0 means 7075 on the bridge address, and a free port chosen at start on loopback."`
	} `json:"sql,omitempty"`

	// Volumes turns the Functions' volumes on (ADR-0262 Decision 3): CapacityBytes is the size of the one volume image
	// on the containerd runtime, preallocated at <storage.dataDir>/volumes.img; 0 ⇒ volumes off. Capacity is the same
	// size with a binary unit, "64Gi" (ADR-0266); at most one of the two is set.
	Volumes struct {
		CapacityBytes int64  `json:"capacityBytes,omitempty" env:"FUNCD_VOLUMES_CAPACITY_BYTES" validate:"min=0" doc:"The size in bytes of the one image that holds every Function volume on the containerd runtime; funcd preallocates it under storage.dataDir, or in a temporary directory with storage.mode memory, and needs root to mount it. 0 turns volumes off, as the process runtime always does: an apply of a Function with a volume is then refused, and one stored before turns Failed with the reason VolumesOff. A larger value grows the image at the next start, and a smaller one stops funcd from starting. Set at most one of this key and volumes.capacity."`
		Capacity      string `json:"capacity,omitempty" env:"FUNCD_VOLUMES_CAPACITY" doc:"The size of the volume image as digits with an optional binary unit, Ki, Mi, Gi or Ti, with or without a B, such as 64Gi: the same as volumes.capacityBytes. Set at most one of the two."`
	} `json:"volumes,omitempty"`

	// App tunes the App reconciler (ADR-0200). UpgradeTimeout bounds an upgrade, a duration more than
	// runtime.bootTimeout; empty ⇒ max(5m, twice runtime.bootTimeout), derived in cmd/funcd. RevisionHistory is the
	// number of AppRevisions an App keeps besides its current one.
	App struct {
		UpgradeTimeout  string `json:"upgradeTimeout,omitempty" env:"FUNCD_APP_UPGRADE_TIMEOUT" doc:"How long an App's new revision has to get every part ready and switch, counted from the latest of its start, the App's resume, a hold's release and the end of its last pre-hook call. A revision that runs out turns Failed, and the current one keeps serving. Empty means 5m, or twice runtime.bootTimeout when that is longer; a set value must be more than runtime.bootTimeout."`
		RevisionHistory int    `json:"revisionHistory,omitempty" env:"FUNCD_APP_REVISION_HISTORY" validate:"min=1,max=100" doc:"How many AppRevisions an App keeps besides its current one; funcd deletes the older ones."`
	} `json:"app,omitempty"`

	// Health paces the storage probes behind the KVStore and Bucket status and the dependency check (ADR-0215):
	// positive durations, the timeout less than the interval.
	Health struct {
		StorageProbeInterval string `json:"storageProbeInterval,omitempty" env:"FUNCD_HEALTH_STORAGE_PROBE_INTERVAL" doc:"How often funcd reads one key of the KV engine and one of the blob store to check that each answers; with an S3 blob.target, each blob read is one request. While a read fails or runs late, every KVStore, or every Bucket, is Degraded with the reason StorageUnreachable. It is a positive duration, more than health.storageProbeTimeout."`
		StorageProbeTimeout  string `json:"storageProbeTimeout,omitempty" env:"FUNCD_HEALTH_STORAGE_PROBE_TIMEOUT" doc:"How long one storage read may take before it counts as failed; one late read is enough to make the stores Degraded. It is a positive duration, less than health.storageProbeInterval."`
	} `json:"health,omitempty"`
}

// Flags are the top precedence tier (CLI flags with no env). MemoryOnly nil ⇒ --memory not set.
type Flags struct{ MemoryOnly *bool }

// DefaultImagePrefix is the built-in runtime.containerd.imagePrefix, the repository the embedded curated runtime
// images are tagged under. With it only the embedded images are used: a runtime image is pulled only from a registry
// the operator chose, an imageOverride entry or a custom imagePrefix.
const DefaultImagePrefix = "funcd/runtime-"

// Defaults is every key's built-in default, before a file or a variable applies.
func Defaults() Config { return defaults() }

// defaults returns the Config with every built-in default set (the bottom precedence tier; identical
// to ADR-0061's defaults). The dataDir-relative containerd paths (Root, CNIConfDir) are derived in
// Load after the merge, when DataDir is final.
func defaults() Config {
	var c Config
	c.Server.ListenAddr = "127.0.0.1:8080"
	c.Server.DataPlaneAddr = "127.0.0.1:0"
	c.Server.BaseDomain = "localhost"
	c.Server.Limits.MaxKeys = 4096
	c.Server.ShutdownTimeout = "15s"
	c.Server.Network.WorkerSyncInterval = "2s"
	c.Storage.Mode = "file"
	c.Storage.DataDir = "/var/lib/funcd"
	c.Functions.DefaultResources.CPU = DefaultFunctionCPU
	c.Functions.DefaultResources.Memory = DefaultFunctionMemory
	c.Auth.Namespaces = []string{"default"}
	c.Runtime.Mode = "process"
	c.Runtime.BootBackoffInitial = "10s"
	c.Runtime.SupervisionPeriod = "10s"
	c.Runtime.BootTimeout = "1m"
	c.Runtime.DrainGrace = "30s"
	c.Runtime.HandOutSettle = "2s"
	c.Runtime.DrainPollInterval = "1s"
	c.Runtime.Process.StopGrace = "3s"
	c.Runtime.Containerd.StopGrace = "10s"
	c.Runtime.Containerd.Snapshotter = "overlayfs"
	c.Controller.GCSweepInterval = "5m"
	c.Controller.RetryBackoffMax = "1s"
	c.Controller.ReferentPollInterval = "2s"
	c.Controller.RouteResyncInterval = "10s"
	c.Lake.EnginePollInterval = "2s"
	c.Lake.EngineProbeTimeout = "2s"
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
	// The KV export's bases (ADR-0209 Decision 5): the same ladder.
	c.Kvstore.Backup.Retention.Hourly = 48
	c.Kvstore.Backup.Retention.Daily = 30
	c.Kvstore.Backup.Retention.Weekly = 12
	// funcd upgrade's pre-upgrade pins kept (ADR-0207, Q10).
	c.Backup.Retention.PreUpgrade = 3
	// Safe mode (ADR-0207): held after 3 unclean starts, stopped after 6; a start is clean after 10 minutes.
	c.Recovery.SafeMode.AfterCrashes = 3
	c.Recovery.SafeMode.StableAfter = "10m"
	// Platform backup runs (ADR-0205): one an hour, a retry 5 minutes after a failure, rpoRisk past 2 hours.
	c.Backup.Interval = v1.Duration(defaultBackupInterval).String()
	c.Backup.RetryInterval = v1.Duration(defaultBackupRetryInterval).String()
	c.Backup.Objectives.RPO = v1.Duration(defaultBackupRPO).String()
	c.WorkloadBackup.MaxListedKeys = DefaultWorkloadBackupMaxListedKeys
	c.WorkloadBackup.MaxListedKeySize = DefaultWorkloadBackupMaxListedKeySize
	c.WorkloadBackup.MaxCopyMembers = DefaultWorkloadBackupMaxCopyMembers
	// The local blob store's mirror (ADR-0208): due hourly, a new epoch every 30 days, each generation kept 30 days.
	c.Blob.Backup.Interval = v1.Duration(defaultBlobBackupInterval).String()
	c.Blob.Backup.Rebaseline = v1.Duration(defaultBlobRebaseline).String()
	c.Blob.Backup.Retention = v1.Duration(defaultBlobRetention).String()
	// S3 gateway (ADR-0080/0085): opt-in; node-private loopback; 1 GiB buffered-object cap.
	c.S3Gateway.Enabled = false
	c.S3Gateway.ListenAddr = "127.0.0.1:9000"
	c.S3Gateway.MaxUploadBytes = defaultS3MaxUpload
	c.S3Gateway.KeyRotationPeriod = "24h"
	// Workflow engine (ADR-0094): one attempt by default (no retry), 300s per-step timeout,
	// 30-day terminal-run retention, 256 KiB run-payload cap.
	c.Workflow.DefaultRetry = 1
	c.Workflow.DefaultStepTimeout = "300s"
	c.Workflow.Retention = "720h"
	c.Workflow.PayloadLimit = defaultWorkflowPayloadLimit
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

// Load builds the effective Config (ADR-0062) from the file at path ("" ⇒ none): os.ReadFile, then Parse. Any
// read/parse/unknown-key/enum error ⇒ fault.Invalid. The returned Config is fully populated + valid.
func Load(path string, flags Flags) (Config, error) {
	var data []byte
	if path != "" {
		var err error
		if data, err = os.ReadFile(path); err != nil { //nolint:gosec // operator-supplied config location
			return Config{}, fault.Invalidf(loadOp, "read config %q: %v", path, err)
		}
	}
	return Parse(path, data, flags)
}

// loadOp names Load in its errors, and Parse's, so the start and funcdctl backup plan print one message for the same
// bytes (ADR-0223 Decision 3).
const loadOp = "config.Load"

// Parse builds the effective Config from data (nil ⇒ none; name appears only in messages): defaults() →
// strict-decode data → overlay env (caarlos0/env; an unset FUNCD_* var leaves the field untouched) → apply the
// --memory flag → make storage.dataDir absolute → derive the dataDir-relative defaults → Validate.
func Parse(name string, data []byte, flags Flags) (Config, error) {
	const op = loadOp
	c := defaults()
	if data != nil {
		if err := yaml.UnmarshalStrict(data, &c); err != nil { // file overrides defaults; unknown key ⇒ error
			return Config{}, fault.Invalidf(op, "parse config %q: %v", name, err)
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
	if c.Eventing.EventStore.DataDir == "" {
		c.Eventing.EventStore.DataDir = filepath.Join(c.Storage.DataDir, "eventstore")
	}
	// The local blob store (ADR-0208): derived only where it serves, so a set blob.dir stays visible to CheckBlob.
	switch {
	case c.Blob.Dir != "":
		abs, err := filepath.Abs(c.Blob.Dir)
		if err != nil {
			return Config{}, fault.Invalidf(op, "config key %q: resolve %q: %v", "blob.dir", c.Blob.Dir, err)
		}
		c.Blob.Dir = abs
	case c.Storage.Mode == "file" && c.Blob.Target == "":
		c.Blob.Dir = filepath.Join(c.Storage.DataDir, "blob")
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
		for _, fs := range [][]Finding{c.CheckBackup(), c.CheckBlob(), c.CheckBridge(), c.CheckLake(), c.CheckQuarantine(), c.CheckKVSweep(), c.CheckFunctions(), c.CheckStorage(), c.CheckWorkloadBackup()} {
			if len(fs) > 0 && fs[0].Error {
				return fault.Invalidf(op, "%s", fs[0].Message)
			}
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
	TokenFile  string   `json:"tokenFile" validate:"required" doc:"The file that holds this credential's token, read at start: one token of visible ASCII characters, at most 4096 bytes. It must be a regular file that funcd's user or root owns, with no access for group or others, such as mode 0600 or 0400. The built-in dev token, or a token that another entry holds, refuses the start."`
	Role       string   `json:"role" validate:"oneof=admin developer viewer" doc:"What the credential may do: an admin acts on every kind everywhere, a developer acts on the namespaced kinds of its namespaces, and a viewer only reads them, never a Secret."`
	Namespaces []string `json:"namespaces,omitempty" doc:"The namespaces in which a developer or viewer acts. Absent means default. An empty list or entry refuses the start, and so does this key on an admin, which spans every namespace."`
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
