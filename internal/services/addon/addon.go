// Package addon runs the objects of the add-ons an admin registers (ADR-0332): one engine per object, from its Addon's
// image, on the add-on runtime funcd's own engines use (ADR-0087), fenced so that it reaches nothing and only funcd
// reaches it (ADR-0269, Decision 3), with the environment its Addon renders over the object's config and the listed
// keys of its bound Secrets (Decision 4), and its edge entry under the Addon's basePath (Decision 5). An object waits
// Pending, with no engine, while its Addon is gone or no longer fits it (ADR-0121 Decision 2). An in-place Restore's
// copy loads through two engine starts, and a deleted object's storage is removed or quarantined (ADR-0333).
package addon

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/store"
)

// The conditions an object reports: Ready, and IngressReady while it declares spec.ingress.
const (
	condReady        = v1.ConditionType("Ready")
	condIngressReady = v1.ConditionType("IngressReady")
)

// The reasons an object waits Pending (ADR-0332 Decisions 3 and 4).
const (
	reasonAddonNotFound        = "AddonNotFound"
	reasonConfigInvalid        = "ConfigInvalid"
	reasonRuntimeUnavailable   = "RuntimeUnavailable"
	reasonEnvInvalid           = "EnvInvalid"
	reasonSecretNotFound       = "SecretNotFound"
	reasonBindingResolveFailed = "BindingResolveFailed"
	reasonEngineNotReady       = "EngineNotReady"
	reasonEngineConvergeFailed = "EngineConvergeFailed" // as a Lake and a SQL database name a failed converge
	reasonProgrammed           = "Programmed"
	// reasonProviderNotFound is IngressReady's while the object's oidc stance names no OIDCProvider (ADR-0335
	// Decision 10).
	reasonProviderNotFound = "ProviderNotFound"
)

// expectStatus is the answer of an engine's readiness path that makes it Ready (ADR-0332 Decision 1).
const expectStatus = 200

// defaultPollInterval paces a wait when Deps leaves an interval unset.
const defaultPollInterval = 2 * time.Second

// Deps configures the Reconciler. Store and Provider are required.
type Deps struct {
	Store    store.Store
	Provider provider.Runtime
	// Secrets resolves an object's bound Secrets, each read asked of Cedar as the object (ADR-0321 Decision 5). nil holds
	// an object that binds a Secret, BindingResolveFailed.
	Secrets provider.SecretResolver
	// Routes takes each object's edge entry (ADR-0138); nil programs none.
	Routes router.EntrySetter
	// RunsImages is whether Provider runs images, as it does on containerd and under funcdctl dev: on a process server
	// it runs none, and each object waits Pending, RuntimeUnavailable, naming the process driver (ADR-0344 Decision 9).
	RunsImages bool
	Logger     *slog.Logger
	// SupervisionPeriod requeues a Ready object, so a dead engine is re-created (ADR-0142); 0 ⇒
	// controller.SupervisionPeriod. ReferentPollInterval re-checks a wait no watch ends, a lifted forbid (ADR-0163);
	// EnginePollInterval re-checks an engine that does not answer its probe yet. 0 ⇒ 2 s.
	SupervisionPeriod    time.Duration
	ReferentPollInterval time.Duration
	EnginePollInterval   time.Duration
	// Files places and removes a Restore's copy in an engine's storage (ADR-0333 Decision 6); nil reaches none.
	Files workload.AddonFiles
	// Volumes removes or quarantines a deleted object's storage (ADR-0231, ADR-0288); nil keeps none.
	Volumes provider.Volumes
	// Hold is the platform's: while it is held, no storage is removed or moved (ADR-0231 Decision 4). nil is never held.
	Hold hold.Gate
	// QuarantinePeriod is storage.quarantineRetention, the until a quarantine move logs; Now dates each move (ADR-0288).
	// Now nil ⇒ time.Now.
	QuarantinePeriod time.Duration
	Now              func() time.Time
	// FollowCurrent makes every object follow its Addon's current revision, as funcdctl dev runs one developer's
	// loop with no other namespace to protect (ADR-0339 Decision 7).
	FollowCurrent bool
	// Collect runs the owner garbage collector over a namespace (gc.CollectNamespace): once an App's last engineless
	// part goes, the App's other parts go then, not at the next sweep (ADR-0345 Decision 8). nil collects nothing.
	Collect func(ctx context.Context, ns v1.NamespaceName) error
	// Egress is server.network.egress: on, an engine has no output fence and its egress passes the egress gateway, as a
	// Function worker's does (ADR-0352 Decision 2); off, it is fenced with no endpoint.
	Egress bool
	// Images reads the images of the kinds whose Function comes from one (ADR-0358 Decision 4); nil reads none.
	Images ImageReader
}

// Reconciler is the controller.Reconciler for AddonObject.
type Reconciler struct {
	store         store.Store
	prov          provider.Runtime
	secrets       provider.SecretResolver
	routes        router.EntrySetter
	runsImages    bool
	logger        *slog.Logger
	period        time.Duration
	referentPoll  time.Duration
	enginePoll    time.Duration
	files         workload.AddonFiles
	volumes       provider.Volumes
	gate          hold.Gate
	quarantine    time.Duration
	now           func() time.Time
	followCurrent bool
	collect       func(ctx context.Context, ns v1.NamespaceName) error
	egress        bool
	images        ImageReader

	mu       sync.Mutex
	seen     map[provider.ProviderRef]seenObject // each object's UID and deletion policy at its last pass
	parts    map[provider.ProviderRef]v1.UID     // the App of each engineless part at its last pass (ADR-0345 Decision 8)
	answered map[v1.UID]bool                     // the held objects a deleting call's answer wrote (ADR-0345 Decision 6)

	// The settings step (ADR-0341): the caller of the settings Functions, the controller's Enqueue, each call's bound,
	// and what this process knows of each target's calls.
	settingsMu      sync.Mutex
	caller          SettingsCaller
	enqueue         func(controller.Request)
	settingsTimeout time.Duration
	settings        map[settingsKey]*settingsState
	targets         map[provider.ProviderRef]targetSeen
	// The engineless step (ADR-0345): the caller of the engineless kinds' Functions and what this process knows of each
	// group's calls.
	objectsCaller ObjectsCaller
	groups        map[groupKey]*settingsState
}

// seenObject is what a pass saw of an object that its deletion needs once the object is gone.
type seenObject struct {
	uid      v1.UID
	deletion v1.DeletionPolicy
}

// NewReconciler builds the add-on object reconciler.
func NewReconciler(d Deps) (*Reconciler, error) {
	const op = "services.addon.NewReconciler"
	switch {
	case d.Store == nil:
		return nil, fault.Invalidf(op, "store is required")
	case d.Provider == nil:
		return nil, fault.Invalidf(op, "provider runtime is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	period := orDefault(d.SupervisionPeriod, controller.SupervisionPeriod)
	gate, now := d.Hold, d.Now
	if gate == nil {
		gate = hold.Never
	}
	if now == nil {
		now = time.Now
	}
	return &Reconciler{
		store: d.Store, prov: d.Provider, secrets: d.Secrets, routes: d.Routes,
		runsImages: d.RunsImages, logger: logger.With("component", "services.addon.reconciler"),
		period:       period,
		referentPoll: orDefault(d.ReferentPollInterval, defaultPollInterval),
		enginePoll:   min(orDefault(d.EnginePollInterval, defaultPollInterval), period),
		files:        d.Files, volumes: d.Volumes, gate: gate, quarantine: d.QuarantinePeriod, now: now,
		followCurrent:   d.FollowCurrent,
		collect:         d.Collect,
		egress:          d.Egress,
		images:          d.Images,
		seen:            map[provider.ProviderRef]seenObject{},
		parts:           map[provider.ProviderRef]v1.UID{},
		answered:        map[v1.UID]bool{},
		groups:          map[groupKey]*settingsState{},
		settingsTimeout: DefaultSettingsTimeout,
		settings:        map[settingsKey]*settingsState{},
		targets:         map[provider.ProviderRef]targetSeen{},
	}, nil
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// routeSource is an object's edge-aggregator source key (ADR-0138), disjoint from the Routes' and the Lakes'.
func routeSource(ns v1.NamespaceName, name v1.ObjectName) string {
	return "addon/" + string(ns) + "/" + string(name)
}

// MapAddon maps an Addon's change to every object of its kinds, in every namespace, whatever the Addon's name: an
// object belongs to the Addon that registers its kind (ADR-0332 Decision 2), and an Addon's kind never changes. A
// settings object follows its Addon too (ADR-0341 Decision 4).
func (r *Reconciler) MapAddon(ctx context.Context, obj v1.Object) []controller.Request {
	a, ok := obj.(*v1.Addon)
	if !ok {
		return nil
	}
	return r.objects(ctx, "", func(o *v1.AddonObject) bool {
		return slices.ContainsFunc(a.Spec.Kinds(), func(k string) bool { return v1.SameAddonKind(o.Spec.Kind, k) })
	})
}

// MapSecret maps a Secret's change to the objects of its namespace that bind it: a new Secret ends a SecretNotFound
// wait, and a changed one replaces their engines (ADR-0332 Decision 4).
func (r *Reconciler) MapSecret(ctx context.Context, obj v1.Object) []controller.Request {
	m := obj.GetObjectMeta()
	return r.objects(ctx, m.Namespace, func(o *v1.AddonObject) bool {
		for _, s := range o.Spec.Bindings.Secret {
			if s == m.Name {
				return true
			}
		}
		return false
	})
}

// MapOIDCProvider maps an OIDCProvider's change to the objects of its namespace whose own ingress.auth names it, so a
// new provider ends a ProviderNotFound (ADR-0335 Decision 10).
func (r *Reconciler) MapOIDCProvider(ctx context.Context, obj v1.Object) []controller.Request {
	m := obj.GetObjectMeta()
	return r.objects(ctx, m.Namespace, func(o *v1.AddonObject) bool {
		p, _, ok := o.Spec.Ingress.Stance().OIDC()
		return ok && p == m.Name
	})
}

// objects are the requests of the stored objects of ns ("" for all) that match.
func (r *Reconciler) objects(ctx context.Context, ns v1.NamespaceName, match func(*v1.AddonObject) bool) []controller.Request {
	list, err := r.store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		r.logger.WarnContext(ctx, "could not list the add-on objects a change affects", "error", err)
		return nil
	}
	var reqs []controller.Request
	for _, item := range list.Items {
		if o, ok := item.(*v1.AddonObject); ok && match(o) {
			reqs = append(reqs, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: o.Namespace, Name: o.Name})
		}
	}
	return reqs
}

// configOf is an object's config, an empty object when unset.
func configOf(o *v1.AddonObject) json.RawMessage {
	if len(o.Spec.Config) == 0 {
		return json.RawMessage(`{}`)
	}
	return o.Spec.Config
}
