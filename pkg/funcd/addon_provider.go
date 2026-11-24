package funcd

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/edge/oidc"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/sensor"
	addonsvc "github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// addonProvider is the add-ons an admin registers (ADR-0332): one engine per object of an add-on's kind, run by its
// own add-on runtime beside the Lake's and the SQL database's (ADR-0087), with the Addon rules and the object count cap
// at admission. Its objects are backup members, copied from their engines' own snapshots, and its boot quarantines the
// storage of the objects no longer stored (ADR-0333).
type addonProvider struct {
	cfg        *config
	reconciler *addonsvc.Reconciler
	reserved   []string            // the path segments funcd serves, which no add-on's plural may be
	files      workload.AddonFiles // the engines' files, when the runtime reaches them; nil otherwise
	engines    provider.Runtime    // the runtime of the objects' engines
	// calls are the checks of a Function's call of an engine, and master the root of the proxy's tokens (ADR-0338
	// Decisions 2 and 3).
	calls  *addonsvc.Calls
	master []byte
	// proxyHost is where the add-on proxy listens, the address workers reach funcd's gateways at. listen binds it at its
	// first use, as a Lake's proxy binds at its Lake's pass (ADR-0162): the bridge address may come up only with the
	// first container. proxy is the bound listener, and serve, once Run started, serves one.
	proxyHost string
	proxyMu   sync.Mutex
	proxy     net.Listener
	serve     func(net.Listener)
}

var (
	_ describer           = (*addonProvider)(nil)
	_ admissionHook       = (*addonProvider)(nil)
	_ backupHook          = (*addonProvider)(nil)
	_ facetHook           = (*addonProvider)(nil)
	_ dependencyHook      = (*addonProvider)(nil)
	_ lifecycle.Reclaimer = (*addonProvider)(nil)
	_ lifecycle.Runner    = (*addonProvider)(nil)
	_ lifecycle.Stopper   = (*addonProvider)(nil)
)

// addonDeps are the parts the add-on provider is built over: what secrets and the edge built before it, and the
// executor's indexing of the engines' addresses, which runs before each engine starts.
type addonDeps struct {
	secrets      provider.SecretResolver
	routes       *router.Aggregator
	ctrl         *controller.Controller
	indexEngines func(ctx context.Context) error
	authz        auth.Authorizer // asks addon::call (ADR-0338 Decision 5)
	master       []byte          // the add-on proxy's tokens come from it, with a key of their own
	// engineLine keeps a line that a runtime reading its engines' output itself hands over (ADR-0342 Decision 5).
	engineLine func(spec provider.ProviderSpec, stream, line string)
	// collect runs the owner collector over a namespace once an App's last engineless part goes (ADR-0345 Decision 8).
	collect func(ctx context.Context, ns v1.NamespaceName) error
}

func (s *addonProvider) Name() string { return "addon" }

func (s *addonProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "addon", Kind: provider.Addon, Bindings: []string{"spec.bindings.addon"},
		Summary: "the add-ons an admin registers, built outside funcd: one engine per object (ADR-0332)"}}
}

// Admissions are the Addon rules, whose create takes the cluster-scoped admission lock, and the per-namespace object
// cap (ADR-0332 Decisions 1, 2 and 6).
func (s *addonProvider) Admissions(r admission.StoreReader) []admission.Admission {
	c := s.cfg
	return append(controlplane.NewAddonAdmissions(controlplane.AddonRules{Store: r, Registries: c.addonRegistries,
		AnyRegistry: c.addonAnyRegistry, Reserved: s.reserved, Overrides: c.imageOverrides}),
		admission.NewAddonObjectQuotaAdmission(r, cmp.Or(c.addonMaxObjects, defaultAddonObjectsPerNamespace),
			cmp.Or(c.addonMaxSettings, defaultAddonSettingsPerNamespace)))
}

// Facet serves a Function's calls of its bound objects' engines on its local API (ADR-0338 Decision 2).
func (s *addonProvider) Facet() local.Facet {
	if s.calls == nil {
		return local.Facet{}
	}
	return local.AddonFacet(s.calls, s.limits())
}

// Dependencies checks a worker's add-on bindings: addon::call and a Ready engine (ADR-0338 Decision 1).
func (s *addonProvider) Dependencies() []health.Binding {
	if s.calls == nil {
		return nil
	}
	return []health.Binding{health.Addon(s.calls)}
}

func (s *addonProvider) limits() local.AddonLimits {
	return local.AddonLimits{Timeout: s.cfg.addonCallTimeout, Payload: s.cfg.addonCallPayload}
}

// proxyFor is where the Function reconciler sends a worker for the add-on proxy, bound at its first use, and the token
// it gives each Function.
func (s *addonProvider) proxyFor() function.AddonProxy {
	if s.calls == nil {
		return function.AddonProxy{}
	}
	master := s.master
	return function.AddonProxy{
		Address: func() (string, error) {
			ln, err := s.listen()
			if err != nil {
				return "", err
			}
			return ln.Addr().String(), nil
		},
		Token: func(ns v1.NamespaceName, kind v1.Kind, name v1.ObjectName) string {
			return addonsvc.Token(master, ns, kind, name)
		},
	}
}

// listen binds the add-on proxy, once: a bind that fails is tried again at the next use. Once Run has started, the new
// listener is served at once.
func (s *addonProvider) listen() (net.Listener, error) {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if s.proxy != nil {
		return s.proxy, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(s.proxyHost, "0"))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, "funcd.addonProvider.listen", "bind the add-on proxy on %s", s.proxyHost)
	}
	s.proxy = ln
	if s.serve != nil {
		go s.serve(ln)
	}
	return ln, nil
}

// addonProxyHeaderTimeout bounds the read of a proxy request's header, as the local API's server does.
const addonProxyHeaderTimeout = 10 * time.Second

// Run serves the add-on proxy (ADR-0338 Decision 3), once bound, until the run context ends.
func (s *addonProvider) Run(ctx context.Context) {
	if s.calls == nil {
		return
	}
	logger := s.cfg.logger.With("component", "services.addon.proxy")
	master := s.master
	srv := &http.Server{
		Handler: local.NewAddonProxy(s.calls, func(token string) (local.Ref, bool) {
			return addonsvc.TokenCaller(master, token)
		}, s.limits(), logger),
		ReadHeaderTimeout: addonProxyHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	serve := func(ln net.Listener) {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
			logger.WarnContext(ctx, "the add-on proxy stopped", "error", err)
		}
	}
	s.proxyMu.Lock()
	s.serve = serve
	ln := s.proxy
	s.proxyMu.Unlock()
	if ln != nil {
		go serve(ln)
	}
	<-ctx.Done()
	_ = srv.Close()
}

// Stop releases the add-on proxy's port, held from its bind whether or not Run served it.
func (s *addonProvider) Stop(context.Context) error {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if s.proxy != nil {
		_ = s.proxy.Close()
	}
	return nil
}

// BackupKinds is the one member kind of every add-on object, through the runtime's file access (ADR-0333 Decision 2).
func (s *addonProvider) BackupKinds() []workload.MemberKind {
	return []workload.MemberKind{workload.Addons(s.cfg.store, s.files)}
}

// Reclaim quarantines the storage of every add-on object no longer stored, unless the platform is held (ADR-0307,
// ADR-0333 Decision 5).
func (s *addonProvider) Reclaim(ctx context.Context) error {
	return s.reconciler.ReclaimOrphans(ctx, s.cfg.quarantineNow())
}

// build builds the engine runtime, which runs the engines' images on a containerd server and none on a process server,
// or takes the runtime WithAddonRuntime gives, which runs them (ADR-0344 Decision 9), the reconciler, which watches the
// Addons, the Secrets its objects bind, the Restores that load them and the AppRevisions that move an App's parts, and
// the Addon's reconciler, which stamps its revisions (ADR-0339). A runtime that is also workload.AddonFiles and
// provider.Volumes reaches the engines' files and storage (ADR-0333).
func (s *addonProvider) build(d addonDeps) error {
	const op = "funcd.New"
	c := s.cfg
	engineRuntime, runsImages := c.addonRuntime, true
	if engineRuntime == nil {
		var err error
		if engineRuntime, err = provider.NewRuntime(provider.Deps{
			Runtime: c.runtime, OwnerKind: v1.KindAddonObject, Gateway: c.gateway, Logger: c.logger,
			ProbeTimeout: c.pacing.EngineProbeTimeout, StorageRoot: c.providerStorage, BeforeStart: d.indexEngines,
		}); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "build the add-on provider runtime")
		}
		runsImages = c.imageFor != nil
	}
	s.engines = engineRuntime
	followDevLines(engineRuntime, "", d.engineLine)
	s.files, _ = engineRuntime.(workload.AddonFiles)
	volumes, _ := engineRuntime.(provider.Volumes)
	rec, err := addonsvc.NewReconciler(addonsvc.Deps{
		Store: c.store, Provider: engineRuntime, Secrets: d.secrets, Routes: d.routes, RunsImages: runsImages,
		Logger: c.logger, SupervisionPeriod: c.pacing.SupervisionPeriod, ReferentPollInterval: c.pacing.ReferentPollInterval,
		EnginePollInterval: c.pacing.EnginePollInterval, Files: s.files, Volumes: volumes, Hold: c.gate(),
		QuarantinePeriod: c.quarantinePeriodOrDefault(), Now: c.quarantineNow, FollowCurrent: c.addonFollowCurrent,
		Collect: d.collect, Egress: c.netManager != nil, Images: c.addonImageReader(),
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build the add-on object reconciler")
	}
	s.reconciler = rec
	s.reserved = controlplane.ReservedPlurals()
	if d.master != nil {
		s.calls, s.master = addonsvc.NewCalls(c.store, d.authz, c.nsGate().HeldIn), d.master
		s.proxyHost = cmp.Or(c.bridgeAddress, "127.0.0.1")
		if _, err := s.listen(); err != nil {
			c.logger.Info("the add-on proxy binds at its first use", "address", s.proxyHost, "error", err)
		}
	}
	d.ctrl.Register(v1.KindAddonObject.GVK(), rec)
	d.ctrl.Register(v1.KindAddon.GVK(), rec.Revisions())
	d.ctrl.Watches(v1.KindAddon.GVK(), rec.MapAddon)
	d.ctrl.Watches(v1.KindAddonRevision.GVK(), rec.MapAddonRevision)
	d.ctrl.Watches(v1.KindAppRevision.GVK(), rec.MapAppRevision)
	d.ctrl.Watches(v1.KindSecret.GVK(), rec.MapSecret)
	d.ctrl.Watches(v1.KindOIDCProvider.GVK(), rec.MapOIDCProvider)
	d.ctrl.Watches(v1.KindRestore.GVK(), rec.MapRestore)
	d.ctrl.Watches(v1.KindAddonObject.GVK(), rec.MapSettings)
	return nil
}

// addonImages is the production addonsvc.ImageReader (ADR-0358 Decision 4): what an artifact's manifest says of it. An
// Addon's images are pinned by digest in a registry, under funcdctl dev too, whose own resolvers know only the builds of
// its session.
type addonImages struct{}

func (addonImages) Read(ctx context.Context, image string) (v1.RuntimeName, v1.WorkflowContract, error) {
	rt, err := runtimeResolver{}.Runtime(ctx, image)
	if err != nil {
		return "", v1.WorkflowContract{}, err
	}
	c, _, err := contractResolver{}.Contract(ctx, image)
	return rt, c, err
}

// addonImageReader reads the images of the add-ons' kinds: a test's, else the registries'.
func (c *config) addonImageReader() addonsvc.ImageReader {
	if c.addonImages != nil {
		return c.addonImages
	}
	return addonImages{}
}

// storedAddons is the sign-in's view of the stored add-on objects (ADR-0335 Decisions 1 and 7): an OIDCProvider's
// addon names one, whose engine the back channel reaches at its own address.
type storedAddons struct{ store store.Store }

func (s storedAddons) Get(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (oidc.AddonObject, error) {
	obj, err := s.store.Get(ctx, v1.KindAddonObject.GVK(), ns, name)
	if err != nil {
		return oidc.AddonObject{}, err
	}
	o, ok := obj.(*v1.AddonObject)
	if !ok {
		return oidc.AddonObject{}, fault.Internalf("funcd.storedAddons", "object %s/%s is not an AddonObject", ns, name)
	}
	return addonView(o), nil
}

func (s storedAddons) List(ctx context.Context, ns v1.NamespaceName) ([]oidc.AddonObject, error) {
	list, err := s.store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, err
	}
	out := make([]oidc.AddonObject, 0, len(list.Items))
	for _, item := range list.Items {
		if o, ok := item.(*v1.AddonObject); ok {
			out = append(out, addonView(o))
		}
	}
	return out, nil
}

func addonView(o *v1.AddonObject) oidc.AddonObject {
	return oidc.AddonObject{Name: o.Name, Address: o.Status.Address, Ingress: o.Spec.Ingress}
}

// engineNamespaces are the namespaces of the engines the add-on runtime runs, a stopping one included: those of a
// runtime that names them, as provider.NewRuntime's does.
func (s *addonProvider) engineNamespaces() []v1.NamespaceName {
	if n, ok := s.engines.(interface{ Namespaces() []v1.NamespaceName }); ok {
		return n.Namespaces()
	}
	return nil
}

// settingsReplyLimit bounds a settings Function's answer as the local API bounds a call's answer, maxInvokeBytes
// (ADR-0341 Decision 2).
const settingsReplyLimit = 1 << 20

// addonObjectsCaller calls an engineless kind's Function with one group's list as a CloudEvent of type
// funcd.addon.objects from funcd://<namespace>/addon/<function>, through an invoker of the App hooks' kind, and reads
// its answer, at most 1 MiB (ADR-0345 Decision 2).
type addonObjectsCaller struct{ inv *sensor.HTTPInvoker }

func (a addonObjectsCaller) Call(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, call addonsvc.ObjectsCall) (addonsvc.SettingsReply, error) {
	const op = "funcd.addonObjectsCaller"
	data, err := json.Marshal(call)
	if err != nil {
		return addonsvc.SettingsReply{}, fault.Internalf(op, "marshal the call: %v", err)
	}
	now := time.Now()
	ev := eventing.CloudEvent{SpecVersion: "1.0", ID: fmt.Sprintf("objects-%d", now.UnixNano()),
		Source: fmt.Sprintf("funcd://%s/addon/%s", ns, fn), Type: "funcd.addon.objects",
		Time: v1.NewTimestamp(now), DataContentType: "application/json", Data: data}
	body, err := a.inv.InvokeReply(ctx, ns, fn, ev, settingsReplyLimit)
	if err != nil {
		return addonsvc.SettingsReply{}, err
	}
	var reply addonsvc.SettingsReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return addonsvc.SettingsReply{}, fault.Unavailablef(op, "the answer of %s/%s is no objects reply: %v", ns, fn, err)
	}
	return reply, nil
}

// addonSettingsCaller calls a settings kind's Function with one target's list as a CloudEvent, through an invoker of the
// App hooks' kind, and reads its answer (ADR-0341 Decision 2).
type addonSettingsCaller struct{ inv *sensor.HTTPInvoker }

func (a addonSettingsCaller) Call(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, call addonsvc.SettingsCall) (addonsvc.SettingsReply, error) {
	const op = "funcd.addonSettingsCaller"
	data, err := json.Marshal(call)
	if err != nil {
		return addonsvc.SettingsReply{}, fault.Internalf(op, "marshal the call: %v", err)
	}
	now := time.Now()
	ev := eventing.CloudEvent{SpecVersion: "1.0", ID: fmt.Sprintf("settings-%d", now.UnixNano()),
		Source: fmt.Sprintf("funcd://%s/addon/%s", ns, call.Target.Name), Type: "funcd.addon.settings",
		Time: v1.NewTimestamp(now), DataContentType: "application/json", Data: data}
	body, err := a.inv.InvokeReply(ctx, ns, fn, ev, settingsReplyLimit)
	if err != nil {
		return addonsvc.SettingsReply{}, err
	}
	var reply addonsvc.SettingsReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return addonsvc.SettingsReply{}, fault.Unavailablef(op, "the answer of %s/%s is no settings reply: %v", ns, fn, err)
	}
	return reply, nil
}
