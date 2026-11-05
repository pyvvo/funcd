package funcd

import (
	"cmp"
	"context"
	"crypto/rand"
	"io"
	"net"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/eventing/eventstore"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/kvstore"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/sensor"
	blobsvc "github.com/pyvvo/funcd/internal/services/blob"
	identitysvc "github.com/pyvvo/funcd/internal/services/identity"
	kvsvc "github.com/pyvvo/funcd/internal/services/kv"
	lakesvc "github.com/pyvvo/funcd/internal/services/lake"
	secretsvc "github.com/pyvvo/funcd/internal/services/secret"
	sqlsvc "github.com/pyvvo/funcd/internal/services/sql"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/internal/workflow"
)

const (
	feedWaitPoll          = 50 * time.Millisecond
	sensorDeliveryTimeout = 30 * time.Second
)

// The binding providers (ADR-0305): the high-level providers a Function binds. Each builds its own parts over the
// Platform's shared ones (ADR-0309), secrets first, over the config alone (ADR-0310 Decision 6), and eventing after the
// workload core its Sensors call through. Every stop hook is safe on a provider whose build never ran (#94).

// kvProvider is the KV provider (ADR-0069/0072): the binding-gated facade over the KV driver, which the local API's /kv
// routes call, and the KVStore reconciler. The driver is the config's (WithKVStore, else the in-memory default), so Stop
// closes it even when New failed before build.
type kvProvider struct {
	cfg        *config
	facade     *kvsvc.Facade
	reconciler *kvsvc.Reconciler
	prober     *health.Prober // read by its Dependencies hook (ADR-0308)
}

var (
	_ lifecycle.Starter   = (*kvProvider)(nil)
	_ lifecycle.Reclaimer = (*kvProvider)(nil)
	_ lifecycle.Stopper   = (*kvProvider)(nil)
	_ describer           = (*kvProvider)(nil)
	_ quotaHook           = (*kvProvider)(nil)
	_ admissionHook       = (*kvProvider)(nil)
	_ dependencyHook      = (*kvProvider)(nil)
	_ facetHook           = (*kvProvider)(nil)
	_ backupHook          = (*kvProvider)(nil)
)

// kvDeps are the Platform's parts the KV provider is built over.
type kvDeps struct {
	authz  auth.Authorizer // reads need a permitting Policy, writes are owner-only (ADR-0073, ADR-0074)
	quota  quota.Checker
	prober *health.Prober // the KV probe's result is a KVStore's Ready (ADR-0215 Decision 7)
	ctrl   *controller.Controller
}

func (s *kvProvider) Name() string { return "kv" }

// Quotas is the KVStores' quota, when the KV driver counts its usage.
func (s *kvProvider) Quotas() map[v1.Kind]quota.Source {
	kv, ok := s.cfg.kvStore.(kvstore.Usager)
	if !ok {
		return nil
	}
	return map[v1.Kind]quota.Source{v1.KindKVStore: kvQuotas(kv, s.cfg.store)}
}

// Admissions are the KVStore rules (ADR-0072/0073): the store-count quota (0 ⇒ the default, negative disables it),
// and no delete of a store still bound or holding data. Binding and owner existence are reconcile-time (ADR-0121).
func (s *kvProvider) Admissions(r admission.StoreReader) []admission.Admission {
	c := s.cfg
	return []admission.Admission{
		admission.NewKVStoreQuotaAdmission(r, cmp.Or(c.kvMaxStoresPerNamespace, defaultKVStoresPerNamespace)),
		admission.NewKVStoreDeletionProtectionAdmission(r, kvProber{c.kvStore}),
	}
}

// Dependencies checks a worker's KV bindings: resolve, read authorization and the KV probe.
func (s *kvProvider) Dependencies() []health.Binding {
	return []health.Binding{health.KV(s.facade, s.prober)}
}

// Facet serves the facade on a worker's /kv routes (ADR-0069).
func (s *kvProvider) Facet() local.Facet {
	if s.facade == nil {
		return local.Facet{}
	}
	return local.KVFacet(s.facade)
}

// BackupKinds is the KVStore kind over the KV driver's export and load, each when the driver offers it (ADR-0222).
func (s *kvProvider) BackupKinds() []workload.MemberKind {
	var export func(prefix string) snapshot.Source
	if exp, ok := s.cfg.kvStore.(interface{ SnapshotPrefix(string) snapshot.Source }); ok {
		export = exp.SnapshotPrefix
	}
	load, _ := s.cfg.kvStore.(workload.KVLoader)
	return []workload.MemberKind{workload.KVStores(s.cfg.store, export, load)}
}

func (s *kvProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "kv", Kind: provider.Builtin, Port: "kvstore.KV", Bindings: []string{"spec.bindings.kv"},
		Summary: "namespaced key-value store"}}
}

// build builds the facade and the reconciler, which reclaims a deleted store's prefix and a removed table's sub-prefix
// through the driver's PrefixManager (a driver without one reclaims nothing).
func (s *kvProvider) build(d kvDeps) error {
	const op = "funcd.New"
	c := s.cfg
	resolver, err := kvsvc.NewResolver(metaReader{c.store})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build KV binding resolver")
	}
	if s.facade, err = kvsvc.NewFacade(kvsvc.FacadeDeps{KV: c.kvStore, Resolver: resolver, Authorizer: d.authz, Quota: d.quota,
		Logger: c.logger}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build KV facade")
	}
	s.prober = d.prober
	prefixMgr, _ := c.kvStore.(kvsvc.PrefixManager)
	if s.reconciler, err = kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: c.store, KV: prefixMgr, Logger: c.logger, Health: d.prober,
		Hold: c.gate(), QuarantinePeriod: c.quarantinePeriodOrDefault()}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build KVStore reconciler")
	}
	d.ctrl.Register(v1.KindKVStore.GVK(), s.reconciler)
	d.ctrl.Watches(v1.KindFunction.GVK(), s.reconciler.MapFunction) // status.bindings counts Function.spec.bindings.kv
	return nil
}

// Start marks the KVStores the previous materializer made before any controller, the collector or the control plane
// reads them (ADR-0178 Decision 3).
func (s *kvProvider) Start(ctx context.Context) error {
	if err := workflow.MarkKVStoresOnce(ctx, s.cfg.store, s.cfg.logger); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "mark workflow kv stores")
	}
	return nil
}

// Reclaim reclaims the data of each KVStore deleted before its reconcile ran, before a store of the same name can be
// created (issue #708).
func (s *kvProvider) Reclaim(ctx context.Context) error {
	return s.reconciler.ReclaimDeleted(ctx, s.cfg.quarantineNow())
}

// Stop closes the durable KV driver (ADR-0066), once the workers that call it stopped.
func (s *kvProvider) Stop(context.Context) error {
	if cl, ok := s.cfg.kvStore.(io.Closer); ok {
		_ = cl.Close()
	}
	return nil
}

// blobProvider is the blob provider (ADR-0080/0127): the facade, context.blob's PEP; the Bucket reconciler; and the S3
// gateway, served from its Start to its Stop (ADR-0085), whose key deriver gives the Function and Lake workers their
// key pairs.
type blobProvider struct {
	cfg       *config
	facade    *blobsvc.Facade
	gateway   *s3gateway.Server           // nil unless the S3 gateway is enabled
	keys      s3gateway.Deriver           // zero unless the S3 gateway is enabled (ADR-0270)
	injection function.S3GatewayInjection // how a worker reaches the gateway; zero when it is off
	services  workerServices              // runs the gateway until Stop, after the workers (#1232)
	quota     quota.Checker               // its deletion-protection admission lists the Bucket view over it (ADR-0308)
	prober    *health.Prober              // read by its Dependencies hook (ADR-0308)
}

var (
	_ lifecycle.Starter   = (*blobProvider)(nil)
	_ lifecycle.Reclaimer = (*blobProvider)(nil)
	_ lifecycle.Stopper   = (*blobProvider)(nil)
	_ describer           = (*blobProvider)(nil)
	_ quotaHook           = (*blobProvider)(nil)
	_ admissionHook       = (*blobProvider)(nil)
	_ dependencyHook      = (*blobProvider)(nil)
	_ facetHook           = (*blobProvider)(nil)
	_ backupHook          = (*blobProvider)(nil)
)

// blobDeps are the Platform's parts the blob provider is built over.
type blobDeps struct {
	authz  auth.Authorizer
	quota  quota.Checker
	master []byte // the gateway derives every key pair from it (ADR-0085)
	prober *health.Prober
	ctrl   *controller.Controller
}

func (s *blobProvider) Name() string { return "blob" }

// Quotas is the quota of the Buckets and their prefixes, over the blob substrate.
func (s *blobProvider) Quotas() map[v1.Kind]quota.Source {
	if s.cfg.blob == nil {
		return nil
	}
	return map[v1.Kind]quota.Source{v1.KindBucket: bucketQuotas(s.cfg.blob, s.cfg.store)}
}

// Admissions are the Bucket rules (ADR-0080), the KVStore's parallel: the bucket-count quota, and no delete of a
// Bucket still bound or holding data, which it lists through the substrate view the S3 gateway writes.
func (s *blobProvider) Admissions(r admission.StoreReader) []admission.Admission {
	c := s.cfg
	return []admission.Admission{
		admission.NewBucketQuotaAdmission(r, cmp.Or(c.bucketMaxPerNamespace, defaultBucketsPerNamespace)),
		admission.NewBucketDeletionProtectionAdmission(r, blobProber{lister: blobBucketLister{resolve: s3BucketFor(c.blob, c.store, s.quota)}, shared: c.blob}),
	}
}

// Dependencies checks a worker's blob bindings as kv checks its KV ones; without a blob substrate there are none.
func (s *blobProvider) Dependencies() []health.Binding {
	if s.facade == nil {
		return nil
	}
	return []health.Binding{health.Blob(s.facade, s.prober)}
}

// Facet serves the facade on a worker's /blob routes (ADR-0127).
func (s *blobProvider) Facet() local.Facet {
	if s.facade == nil {
		return local.Facet{}
	}
	return local.BlobFacet(s.facade)
}

// BackupKinds is the Bucket kind over the blob substrate, with the catalogs of the prefixes no Lake of it is located on
// (ADR-0293 Decision 6, ADR-0312).
func (s *blobProvider) BackupKinds() []workload.MemberKind {
	return []workload.MemberKind{workload.Buckets(s.cfg.store, s.cfg.blob, bucketPrefix, bucketCatalogPrefix)}
}

func (s *blobProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{
		{Name: "blob", Kind: provider.Builtin, Port: "blob.Bucket", Bindings: []string{"spec.bindings.blob"}, Summary: "opaque-byte object substrate"},
		{Name: "s3", Kind: provider.Builtin, Port: "blob.Bucket", Bindings: []string{"spec.bindings.blob"}, Summary: "S3-protocol frontend over blob (ADR-0080)"},
	}
}

// build builds the facade over the same PDP, per-namespace Bucket view (s3BucketFor) and keyspace as the S3 gateway, so
// an object context.blob writes is one the gateway serves; the Bucket reconciler, whose status follows the blob probe
// (ADR-0215 Decision 7); and the gateway when it is enabled.
func (s *blobProvider) build(d blobDeps) error {
	const op = "funcd.New"
	c := s.cfg
	s.quota, s.prober = d.quota, d.prober
	resolver, err := blobsvc.NewResolver(metaReader{c.store})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build blob binding resolver")
	}
	if s.facade, err = blobsvc.NewFacade(blobsvc.FacadeDeps{Resolver: resolver, BucketFor: s3BucketFor(c.blob, c.store, d.quota),
		Authorizer: d.authz, Logger: c.logger}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build blob facade")
	}
	reconciler, err := blobsvc.NewReconciler(blobsvc.ReconcilerDeps{Store: c.store, Health: d.prober, Logger: c.logger})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build Bucket reconciler")
	}
	d.ctrl.Register(v1.KindBucket.GVK(), reconciler)
	if !c.s3gwEnabled {
		return nil
	}
	return s.buildGateway(d)
}

// buildGateway builds the S3 gateway over the blob substrate, with the PDP as its PEP (ADR-0080/0085), and the key
// pairs a worker is given to call it.
func (s *blobProvider) buildGateway(d blobDeps) error {
	const op = "funcd.New"
	c := s.cfg
	// ADR-0270: a fresh salt per start, kept in memory, so a restart ends every earlier pair.
	var salt []byte
	if c.s3gwKeyPeriod > 0 {
		salt = make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "draw the S3 key salt")
		}
	}
	gwDeps := s3gateway.Deps{
		BucketFor:      s3BucketFor(c.blob, c.store, d.quota),
		Buckets:        s3Buckets(c.store),
		PDP:            d.authz,
		Master:         d.master,
		KeyPeriod:      c.s3gwKeyPeriod,
		KeySalt:        salt,
		Listen:         c.s3gwListenAddr,
		MaxUploadBytes: c.s3gwMaxUploadBytes,
		Catalogs: func(ns v1.NamespaceName) blob.Bucket {
			// no quota caps a catalog; the view asks the free-space floor (ADR-0296 Decision 4)
			return blob.Quoted(blob.Prefixed(c.blob, lakeCatalogPrefix(ns)), d.quota, quota.Subject{}, func(string) bool { return false })
		},
		CatalogMaxBytes: c.lakeCatalogMaxBytes,
		LakeAt:          lakeAt(c.store),
		// ADR-0135: an external Identity's keys resolve to their stored secret, so it authenticates over SigV4.
		External: identitysvc.NewExternalKeys(c.store),
		Logger:   c.logger,
		Held:     c.callGate(),
	}
	srv, err := s3gateway.New(gwDeps)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build s3gateway")
	}
	keys := gwDeps.Deriver()
	s.gateway, s.keys = srv, keys
	s.injection = function.S3GatewayInjection{
		Enabled:    true,
		ListenAddr: c.s3gwListenAddr,
		Endpoint:   c.s3gwEndpoint,
		Derive: func(kind v1.Kind, ns, name string) (string, string) {
			kp := keys.Keypair(kind, ns, name)
			return kp.AccessKey, kp.SecretKey
		},
		Period:     keys.Period,
		RenewalAge: keys.RenewalAge,
	}
	return nil
}

// Start serves the S3 gateway, and a taken port fails the start: versitygw binds the address only once Run serves it
// (#497, ADR-0028).
func (s *blobProvider) Start(ctx context.Context) error {
	if gw := s.gateway; gw != nil {
		s.services.start(ctx, loop(s.cfg.logger, "s3 gateway stopped", gw.Run))
		if err := gw.Wait(ctx); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "funcd.Run", "start the s3 gateway")
		}
	}
	return nil
}

// Reclaim reclaims the objects of each Bucket deleted before its second purge ran (ADR-0199 Decision 7).
func (s *blobProvider) Reclaim(ctx context.Context) error {
	c := s.cfg
	return reclaimDeletedBuckets(ctx, c.blob, c.store, c.logger, c.quarantineNow(), c.quarantinePeriodOrDefault())
}

// Stop stops the S3 gateway after the runtime stopped the workers that use it (#1232). Its Serve ends first, so Close
// cannot fail a Serve that still runs, and it closes before blob.Close, since it serves from the blob substrate.
func (s *blobProvider) Stop(ctx context.Context) error {
	s.services.stop(ctx)
	if s.gateway != nil {
		return s.gateway.Close()
	}
	return nil
}

// sqlProvider is the SQL provider (ADR-0232): one rqlite engine per SQLDatabase, run by its own add-on runtime with
// durable storage (ADR-0231); the facade, context.sql's PEP (ADR-0233); and the change feed every engine posts its
// changes to (ADR-0235).
type sqlProvider struct {
	cfg        *config
	facade     *sqlsvc.Facade
	reconciler *sqlsvc.Reconciler
	feed       *sqlsvc.Feed
	fanout     *eventing.Fanout // the feed's boot gate waits for the Sensors to subscribe to it
	feedHost   string
	feedPort   int
	// feedListener is bound by build on loopback with the port unset (ADR-0260); nil ⇒ Run binds the port.
	feedListener net.Listener
}

var (
	_ lifecycle.Reclaimer = (*sqlProvider)(nil)
	_ lifecycle.Runner    = (*sqlProvider)(nil)
	_ lifecycle.Stopper   = (*sqlProvider)(nil)
	_ describer           = (*sqlProvider)(nil)
	_ quotaHook           = (*sqlProvider)(nil)
	_ admissionHook       = (*sqlProvider)(nil)
	_ dependencyHook      = (*sqlProvider)(nil)
	_ facetHook           = (*sqlProvider)(nil)
	_ backupHook          = (*sqlProvider)(nil)
)

// sqlDeps are the Platform's parts the SQL provider is built over.
type sqlDeps struct {
	authz  auth.Authorizer
	quota  quota.Checker
	master []byte // the engines' passwords come from it, as the S3 keys and the lake tokens do (ADR-0232)
	ctrl   *controller.Controller
	fanout *eventing.Fanout
	events *eventstore.Store // holds the feed's watermarks
}

func (s *sqlProvider) Name() string { return "sql" }

// Quotas is the SQLDatabases' quota: its pages in use, read through its engine.
func (s *sqlProvider) Quotas() map[v1.Kind]quota.Source {
	return map[v1.Kind]quota.Source{v1.KindSQLDatabase: sqlQuotas(s.facade.Usage, s.cfg.store)}
}

// Admissions are the SQLDatabase rules (ADR-0232): the count quota, and no API delete while the engine holds a table.
func (s *sqlProvider) Admissions(r admission.StoreReader) []admission.Admission {
	c := s.cfg
	return []admission.Admission{
		admission.NewSQLDatabaseQuotaAdmission(r, cmp.Or(c.sqlMaxDatabasesPerNamespace, defaultSQLDatabasesPerNamespace)),
		admission.NewSQLDatabaseDeletionProtectionAdmission(r, s.reconciler.Prober()),
	}
}

// Dependencies checks a worker's SQL bindings: resolve, read authorization and a Ready engine (ADR-0233 Decision 8).
func (s *sqlProvider) Dependencies() []health.Binding {
	return []health.Binding{health.SQL(s.facade)}
}

// Facet serves the facade on a worker's /sql routes (ADR-0233).
func (s *sqlProvider) Facet() local.Facet {
	if s.facade == nil {
		return local.Facet{}
	}
	return local.SQLFacet(s.facade)
}

// BackupKinds is the SQLDatabase kind, through each database's engine (ADR-0234).
func (s *sqlProvider) BackupKinds() []workload.MemberKind {
	return []workload.MemberKind{workload.Databases(s.cfg.store, s.reconciler.Backups())}
}

func (s *sqlProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "sql", Kind: provider.Addon, Bindings: []string{"spec.bindings.sql"},
		Summary: "one rqlite SQL engine per SQLDatabase (ADR-0232)"}}
}

// build builds the facade, the engine runtime, the change feed and the reconciler. The feed listens on the lake proxies'
// host, which workers reach.
func (s *sqlProvider) build(d sqlDeps) error {
	const op = "funcd.New"
	c := s.cfg
	engines := sqlsvc.NewEngines()
	resolver, err := sqlsvc.NewResolver(metaReader{c.store})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build SQL binding resolver")
	}
	if s.facade, err = sqlsvc.NewFacade(sqlsvc.FacadeDeps{
		Resolver: resolver, Authorizer: d.authz, Engines: engines, Master: d.master, CallTimeout: c.sqlCallTimeout,
		Quota: d.quota, Logger: c.logger,
	}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build SQL facade")
	}
	engineRuntime := c.sqlProvider
	if engineRuntime == nil {
		if engineRuntime, err = provider.NewRuntime(provider.Deps{
			Runtime: c.runtime, OwnerKind: v1.KindSQLDatabase, Gateway: c.gateway, Logger: c.logger,
			ProbeTimeout: c.pacing.EngineProbeTimeout, StorageRoot: c.providerStorage,
		}); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "build the SQL provider runtime")
		}
	}
	watermarks := d.events.ChangeWatermarks()
	if s.feed, err = sqlsvc.NewFeed(sqlsvc.FeedDeps{Store: c.store, Publisher: d.fanout, Watermarks: watermarks, Master: d.master,
		Hold: c.gate(), NamespaceHold: c.nsGate(), Logger: c.logger}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build the SQL change feed")
	}
	s.fanout = d.fanout
	s.feedHost = cmp.Or(c.bridgeAddress, "127.0.0.1")
	if c.imageFor != nil && c.bridgeAddress == "" {
		c.logger.Warn("sql change feed listens on loopback, which engine containers cannot reach: give funcd its bridge " +
			"address (WithBridgeAddress), else no EventSource of kind sql fires")
	}
	c.logger.Info("bridge address", "address", s.feedHost)
	s.feedPort = c.sqlChangeFeedPort
	if c.bridgeAddress == "" && s.feedPort == 0 {
		// ADR-0260 Decision 1: on loopback an unset port is the one a bind of port 0 gets, held from here to Shutdown.
		if s.feedListener, err = s.feed.Listen(context.Background(), s.feedHost, 0); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "bind the SQL change feed")
		}
		s.feedPort = s.feedListener.Addr().(*net.TCPAddr).Port
	}
	s.feedPort = cmp.Or(s.feedPort, 7075)
	if s.reconciler, err = sqlsvc.NewReconciler(sqlsvc.ReconcilerDeps{
		Store: c.store, Provider: engineRuntime, Engines: engines, Master: d.master, ImageFor: c.imageFor,
		Hold: c.gate(), Logger: c.logger, SupervisionPeriod: c.pacing.SupervisionPeriod,
		DurableStorage: c.providerStorage != "" && !c.providerStorageEphemeral,
		ChangeFeedURL:  s.feed.URL(s.feedHost, s.feedPort), Watermarks: watermarks,
		QuarantinePeriod: c.quarantinePeriodOrDefault(),
	}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build SQLDatabase reconciler")
	}
	d.ctrl.Register(v1.KindSQLDatabase.GVK(), s.reconciler)
	d.ctrl.Watches(v1.KindFunction.GVK(), s.reconciler.MapFunction) // status.bindings counts Function.spec.bindings.sql
	d.ctrl.Watches(v1.KindRestore.GVK(), s.reconciler.MapRestore)   // a Succeeded Restore clears DataMissing (ADR-0234)
	return nil
}

// Reclaim reclaims the directory of each SQLDatabase deleted before its reconcile ran (ADR-0232 Decision 7).
func (s *sqlProvider) Reclaim(ctx context.Context) error {
	return s.reconciler.ReclaimOrphans(ctx, s.cfg.quarantineNow())
}

// Run opens and serves the engines' change feed (ADR-0235) until the run context ends.
func (s *sqlProvider) Run(ctx context.Context) {
	if s.feed == nil {
		return
	}
	serve := func(ctx context.Context) error {
		if s.feedListener != nil {
			return s.feed.ServeListener(ctx, s.feedListener)
		}
		return s.feed.Serve(ctx, s.feedHost, s.feedPort)
	}
	runAll(ctx, s.openFeed, loop(s.cfg.logger, "sql change feed stopped", serve))
}

// sqlFeedBootWait bounds how long the change feed's boot gate waits for the Sensors present at the start to subscribe
// (ADR-0235 Decision 4); one that never does, an invalid Sensor, keeps no other change waiting past it.
const sqlFeedBootWait = 30 * time.Second

// openFeed opens the change feed's boot gate once every Sensor present at the start has subscribed to each event it
// names, so a change redelivered at boot reaches it; until then rqlite's deliveries answer 503 and are retried.
func (s *sqlProvider) openFeed(ctx context.Context) {
	defer s.feed.Loaded()
	logger := s.cfg.logger
	l, err := s.cfg.store.List(ctx, v1.KindSensor.GVK(), store.ListOptions{})
	if err != nil {
		logger.WarnContext(ctx, "sql change feed: list the Sensors to wait for", "error", err)
		return
	}
	type dep struct {
		ns            v1.NamespaceName
		source, event v1.ObjectName
	}
	var deps []dep
	for _, o := range l.Items {
		if se, ok := o.(*v1.Sensor); ok {
			for _, d := range se.Spec.On {
				deps = append(deps, dep{ns: se.Namespace, source: d.Source, event: d.Event})
			}
		}
	}
	deadline := time.Now().Add(sqlFeedBootWait)
	for {
		deps = slices.DeleteFunc(deps, func(d dep) bool { return s.fanout.Subscribed(d.ns, d.source, d.event) })
		if len(deps) == 0 {
			return
		}
		if time.Now().After(deadline) {
			logger.WarnContext(ctx, "sql change feed: opening with Sensors not subscribed", "waiting", len(deps),
				"first", string(deps[0].ns)+"/"+string(deps[0].source)+"/"+string(deps[0].event))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(feedWaitPoll):
		}
	}
}

// Stop releases the change feed's loopback port, which New holds whether or not Run served it (ADR-0260).
func (s *sqlProvider) Stop(context.Context) error {
	if s.feedListener != nil {
		_ = s.feedListener.Close()
	}
	return nil
}

// lakeProvider is the Lake provider (ADR-0086/0087): one DuckDB/Quack engine per Lake, run by its add-on runtime, and
// the node-private PEP proxies in front of the engines (ADR-0137).
type lakeProvider struct {
	cfg        *config
	proxy      *lakegw.Manager
	reconciler *lakesvc.Reconciler
}

var (
	_ lifecycle.BeforeStopper = (*lakeProvider)(nil)
	_ describer               = (*lakeProvider)(nil)
	_ backupHook              = (*lakeProvider)(nil)
)

// lakeDeps are the parts the Lake provider is built over: the Platform's, and what blob, before it in the list, built.
type lakeDeps struct {
	authz   auth.Authorizer
	master  []byte // the proxies derive and check the per-function lake tokens with it (ADR-0137)
	floor   *quota.Floor
	secrets lakesvc.SecretResolver
	routes  *router.Aggregator // the opt-in external edge entry to a proxy (ADR-0138)
	// s3 is blob's: an engine's key pair is derived over its Lake identity by the deriver a Function's comes from
	// (ADR-0085, ADR-0175).
	s3   function.S3GatewayInjection
	ctrl *controller.Controller
}

func (s *lakeProvider) Name() string { return "lake" }

// BackupKinds is the Lake kind over the blob substrate, its catalog in the catalog store (ADR-0271, ADR-0293), which stops
// an engine with the reconciler that runs it (ADR-0312 Decision 9).
func (s *lakeProvider) BackupKinds() []workload.MemberKind {
	return []workload.MemberKind{workload.Lakes(s.cfg.store, s.cfg.blob, bucketPrefix, lakeCatalogPrefix, s.reconciler.StopEngine)}
}

func (s *lakeProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "lake", Kind: provider.Addon, Bindings: []string{"spec.bindings.lake"},
		Summary: "the lake service: DuckLake over DuckDB and Quack (F48, ADR-0086)"}}
}

// build builds the proxies, which bind and publish the bridge address or the shared loopback (ADR-0238), the engine
// runtime (funcdctl dev's process engine when it overrides it, ADR-0125) and the Lake reconciler.
func (s *lakeProvider) build(d lakeDeps) error {
	const op = "funcd.New"
	c := s.cfg
	s.proxy = lakegw.NewManager(cmp.Or(c.bridgeAddress, "127.0.0.1"), lakegw.NewLakeKeys(d.master, c.store), d.authz, c.logger)
	engineRuntime := c.lakeProvider
	if engineRuntime == nil {
		var floor provider.Floor
		if d.floor != nil {
			floor = d.floor
		}
		var err error
		if engineRuntime, err = provider.NewRuntime(provider.Deps{
			Runtime: c.runtime, OwnerKind: v1.KindLake, Gateway: c.gateway, Logger: c.logger,
			ProbeTimeout: c.pacing.EngineProbeTimeout, StorageRoot: c.providerStorage,
			ScratchRoot: c.scratchRoot, ScratchMountRoot: c.scratchMountRoot, Floor: floor,
		}); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "build provider runtime")
		}
	}
	rd := lakesvc.ReconcilerDeps{
		Store:      c.store,
		Provider:   engineRuntime,
		Secrets:    d.secrets,
		S3Endpoint: c.s3gwEndpoint,
		ImageFor:   c.imageFor,
		Logger:     c.logger,
		Proxy:      s.proxy,
		Routes:     d.routes,

		SupervisionPeriod:    c.pacing.SupervisionPeriod,
		ReferentPollInterval: c.pacing.ReferentPollInterval,
		EnginePollInterval:   c.pacing.EnginePollInterval,
		SaveInterval:         c.lakeSaveInterval,
		// Only funcdctl dev overrides the Lake runtime: its process engine keeps a local catalog (ADR-0125, ADR-0279).
		LocalCatalog: c.lakeProvider != nil,
	}
	if c.lakeProvider == nil && c.scratchRoot != "" {
		rd.ScratchBytes, rd.CatalogMaxBytes = c.lakeScratchBytes, c.lakeCatalogMaxBytes // ADR-0294
	}
	if d.s3.Enabled {
		rd.Derive, rd.RenewalAge = d.s3.Derive, d.s3.RenewalAge
		if rd.S3Endpoint == "" {
			rd.S3Endpoint = "http://" + d.s3.ListenAddr
		}
	}
	reconciler, err := lakesvc.NewReconciler(rd)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build Lake reconciler")
	}
	s.reconciler = reconciler
	d.ctrl.Register(v1.KindLake.GVK(), reconciler)
	// ADR-0162: a released listener closes once no Function binds it.
	d.ctrl.Watches(v1.KindFunction.GVK(), reconciler.MapFunction)
	return nil
}

// BeforeStop closes the node-private PEP proxies before the runtime stops the engines behind them (ADR-0137).
func (s *lakeProvider) BeforeStop(context.Context) error {
	if s.proxy != nil {
		s.proxy.Shutdown()
	}
	return nil
}

// eventingProvider is the eventing provider (ADR-0108): the EventSource reconciler and its blob poll watcher
// (ADR-0119), the Sensor reconciler, which delivers the named events to their actions (ADR-0109), and the event store's
// dead-letter tenant, where a delivery its retries could not make ends (ADR-0118).
type eventingProvider struct {
	cfg              *config
	source           *eventing.Source
	blobWatcher      *eventing.BlobWatcher // the hold advances and lists its pending objects (ADR-0206)
	sensorReconciler *sensor.Reconciler    // owns the retry workers, drained within drainTimeout, and the replay seam
	deadLetters      deadletter.Store      // the control plane reads, replays and discards them
	drainTimeout     time.Duration
}

var (
	_ lifecycle.Runner = (*eventingProvider)(nil)
	_ describer        = (*eventingProvider)(nil)
)

// eventingDeps are the parts eventing is built over: the Platform's, and the workload core's its Sensors call through.
type eventingDeps struct {
	fanout       *eventing.Fanout  // a firing EventSource publishes on it, and the Sensors subscribe to it (ADR-0108)
	events       *eventstore.Store // holds the blob watcher's seen lists and the dead letters (ADR-0201)
	quota        quota.Checker     // the blob watcher lists the Bucket view over it
	endpoints    activator.Endpoints
	waker        sensor.Waker // the activator: a cold target wakes before its event is delivered (ADR-0033)
	calls        *activator.CallTracker
	chain        *invoke.Chain // every call to a worker runs it (ADR-0302)
	drainTimeout time.Duration
	ctrl         *controller.Controller
}

func (s *eventingProvider) Name() string { return "eventing" }

func (s *eventingProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "eventing", Kind: provider.Builtin, Summary: "CloudEvents trigger delivery (EventSource)"}}
}

// build builds the blob watcher, which lists the Bucket view the S3 gateway writes, keeps its seen lists in the event
// store and publishes a named CloudEvent per new object (ADR-0119); the EventSource reconciler over it; and the Sensor
// reconciler, which binds the named events to their actions, a WorkflowRun or a Function call, and dead-letters a
// delivery its bounded retries could not make (ADR-0109, ADR-0118). It runs once the workload core exists, as the
// Executor's captureLogs does.
func (s *eventingProvider) build(d eventingDeps) error {
	const op = "funcd.New"
	c := s.cfg
	blobWatcher, err := eventing.NewBlobWatcher(blobBucketLister{resolve: s3BucketFor(c.blob, c.store, d.quota)}, d.fanout,
		d.events.SeenLists(), c.blobPollInterval, c.logger)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build blob watcher")
	}
	blobWatcher.SetHold(c.gate())
	blobWatcher.SetNamespaceHold(c.nsGate())
	s.blobWatcher, s.deadLetters, s.drainTimeout = blobWatcher, d.events.DeadLetters(), d.drainTimeout
	if s.source, err = eventing.NewSource(eventing.Deps{
		Store:     c.store,
		Publisher: d.fanout,
		Blob:      blobWatcher,
		Logger:    c.logger,
		Hold:      c.gate(),

		NamespaceHold:         c.nsGate(),
		BucketRecheckInterval: c.pacing.BucketRecheckInterval,
	}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build eventing source")
	}
	if s.sensorReconciler, err = sensor.NewReconciler(sensor.Deps{
		Store:      c.store,
		Subscriber: d.fanout,
		Invoker: &sensor.HTTPInvoker{Endpoints: d.endpoints, Waker: d.waker, Protocol: protocolOf(c.store),
			Client: workerClient(d.calls, sensorDeliveryTimeout), Chain: d.chain},
		DeadLetters:           s.deadLetters,
		DeliveryAttempts:      c.deliveryAttempts,
		MaxDeliveriesInFlight: c.sensorMaxDeliveriesInFlight,
		MaxInFlightPerTarget:  c.sensorMaxInFlightPerTarget,
		MaxQueuedPerSensor:    c.sensorMaxQueuedPerSensor,
		Logger:                c.logger,

		DeliveryBackoffInitial: c.pacing.DeliveryBackoffInitial,
		DeliveryBackoffMax:     c.pacing.deliveryBackoffMax(),
		Hold:                   c.gate(),
		NamespaceHold:          c.nsGate(),
	}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build sensor reconciler")
	}
	d.ctrl.Register(v1.KindEventSource.GVK(), s.source)
	d.ctrl.Register(v1.KindSensor.GVK(), s.sensorReconciler)
	d.ctrl.Watches(v1.KindFunction.GVK(), s.sensorReconciler.MapFunction) // a Function that serves HTTP fails its Sensors (ADR-0243)
	return nil
}

// Run delivers events, polls the blob EventSources (ADR-0119), retries the Sensor's actions and sweeps the dead letters
// (ADR-0118). The retries drain within the platform's drain bound once the run context ends.
func (s *eventingProvider) Run(ctx context.Context) {
	c := s.cfg
	loops := []func(context.Context){loop(c.logger, "eventing stopped", s.source.Run)}
	if s.blobWatcher != nil {
		loops = append(loops, loop(c.logger, "blob watcher stopped", s.blobWatcher.Run))
	}
	if s.sensorReconciler != nil {
		loops = append(loops, func(ctx context.Context) { s.sensorReconciler.RunRetryWorkers(ctx, s.drainTimeout) })
	}
	if s.deadLetters != nil && (c.deadletterRetention > 0 || c.deadletterMaxEntries > 0) {
		loops = append(loops, s.runDeadLetterRetention)
	}
	runAll(ctx, loops...)
}

// secretsProvider is the secrets provider (ADR-0310 Decision 6): the PDP-checked secret resolver (ADR-0022), which the
// Function reconciler (ADR-0057), lake and the workload backups read, and the facade a worker reads each secret
// through, on every read (ADR-0249).
type secretsProvider struct {
	cfg      *config
	resolver *secrets.Resolver
	facade   *secretsvc.Facade
}

var (
	_ describer = (*secretsProvider)(nil)
	_ facetHook = (*secretsProvider)(nil)
)

func (s *secretsProvider) Name() string { return "secrets" }

func (s *secretsProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "secrets", Kind: provider.Builtin, Port: "secrets.Resolver",
		Bindings: []string{"spec.bindings.secret"}, Summary: "secret resolution + at-rest encryption"}}
}

// build builds, over the config alone, the resolver, which reads through the store and authorizer the control plane
// uses, then the facade over it. New builds it first, after the authorizer's default.
func (s *secretsProvider) build() error {
	const op = "funcd.New"
	c := s.cfg
	var err error
	if s.resolver, err = secrets.NewResolver(secrets.Deps{Store: c.store, Authorizer: c.authorizer, Logger: c.logger}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build secrets resolver")
	}
	if s.facade, err = secretsvc.NewFacade(secretsvc.Deps{Store: c.store, Resolver: s.resolver}); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build secret facade")
	}
	return nil
}

// Facet serves the facade on a worker's /secret routes (ADR-0249).
func (s *secretsProvider) Facet() local.Facet {
	if s.facade == nil {
		return local.Facet{}
	}
	return local.SecretFacet(s.facade)
}

// invokeProvider is the fn-to-fn invoke provider (ADR-0064), served from the worker-node local API. It builds nothing:
// its links check reads the PDP its build keeps.
type invokeProvider struct {
	cfg *config
	pdp auth.Authorizer
}

var (
	_ describer      = (*invokeProvider)(nil)
	_ dependencyHook = (*invokeProvider)(nil)
)

// invokeDeps are the Platform's parts the invoke provider is built over.
type invokeDeps struct {
	authz auth.Authorizer // a link to a Function is also asked link::invoke (ADR-0075)
}

// build keeps the PDP, which New builds after the options; it runs before New builds the dependency check (ADR-0309
// Decision 3), so the links check asks link::invoke.
func (s *invokeProvider) build(d invokeDeps) error {
	s.pdp = d.authz
	return nil
}

func (s *invokeProvider) Name() string { return "invoke" }

func (s *invokeProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "invoke", Kind: provider.Builtin, Bindings: []string{"spec.bindings.links"},
		Summary: "synchronous fn-to-fn RPC (context.invoke)"}}
}

// Dependencies checks a worker's links: link-as-grant, link::invoke, and a target that exists and is not Failed.
func (s *invokeProvider) Dependencies() []health.Binding {
	st := s.cfg.store
	return []health.Binding{health.Links(local.NewResolver(st), s.pdp, st)}
}

// catalogEntry is a provider with no hook: its Function-facing service reaches a worker in its env, and it only names
// its catalog entry.
type catalogEntry struct{ d provider.Descriptor }

var _ describer = catalogEntry{}

func (c catalogEntry) Name() string                       { return c.d.Name }
func (c catalogEntry) Descriptors() []provider.Descriptor { return []provider.Descriptor{c.d} }

// hookless are the providers that only name their catalog entry.
func hookless() []lifecycle.Provider {
	return []lifecycle.Provider{
		catalogEntry{provider.Descriptor{Name: "config", Kind: provider.Builtin, Bindings: []string{"spec.bindings.config"},
			Summary: "ConfigMap values a Function reads from its env (ADR-0245)"}},
	}
}
