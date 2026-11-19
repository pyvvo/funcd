package funcd

import (
	"cmp"
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/edge/oidc"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/provider"
	addonsvc "github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/store"
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
}

var (
	_ describer           = (*addonProvider)(nil)
	_ admissionHook       = (*addonProvider)(nil)
	_ backupHook          = (*addonProvider)(nil)
	_ lifecycle.Reclaimer = (*addonProvider)(nil)
)

// addonDeps are the parts the add-on provider is built over: what secrets and the edge built before it, and the
// executor's indexing of the engines' addresses, which runs before each engine starts.
type addonDeps struct {
	secrets      provider.SecretResolver
	routes       *router.Aggregator
	ctrl         *controller.Controller
	indexEngines func(ctx context.Context) error
}

func (s *addonProvider) Name() string { return "addon" }

func (s *addonProvider) Descriptors() []provider.Descriptor {
	return []provider.Descriptor{{Name: "addon", Kind: provider.Addon,
		Summary: "the add-ons an admin registers, built outside funcd: one engine per object (ADR-0332)"}}
}

// Admissions are the Addon rules, whose create takes the cluster-scoped admission lock, and the per-namespace object
// cap (ADR-0332 Decisions 1, 2 and 6).
func (s *addonProvider) Admissions(r admission.StoreReader) []admission.Admission {
	c := s.cfg
	return append(controlplane.NewAddonAdmissions(controlplane.AddonRules{Store: r, Registries: c.addonRegistries, Reserved: s.reserved}),
		admission.NewAddonObjectQuotaAdmission(r, cmp.Or(c.addonMaxObjects, defaultAddonObjectsPerNamespace)))
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

// build builds the engine runtime, which runs containerd's engines, or the runtime WithAddonRuntime gives with its
// modes, and the reconciler, which watches the Addons, the Secrets its objects bind and the Restores that load them. A
// runtime that is also workload.AddonFiles and provider.Volumes reaches the engines' files and storage (ADR-0333).
func (s *addonProvider) build(d addonDeps) error {
	const op = "funcd.New"
	c := s.cfg
	engineRuntime, modes := c.addonRuntime, c.addonModes
	if engineRuntime == nil {
		var err error
		if engineRuntime, err = provider.NewRuntime(provider.Deps{
			Runtime: c.runtime, OwnerKind: v1.KindAddonObject, Gateway: c.gateway, Logger: c.logger,
			ProbeTimeout: c.pacing.EngineProbeTimeout, StorageRoot: c.providerStorage, BeforeStart: d.indexEngines,
		}); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "build the add-on provider runtime")
		}
		modes = nil
		if c.imageFor != nil {
			modes = []string{v1.AddonRuntimeContainerd}
		}
	}
	serverMode := "process"
	if c.imageFor != nil {
		serverMode = v1.AddonRuntimeContainerd
	}
	s.files, _ = engineRuntime.(workload.AddonFiles)
	volumes, _ := engineRuntime.(provider.Volumes)
	rec, err := addonsvc.NewReconciler(addonsvc.Deps{
		Store: c.store, Provider: engineRuntime, Secrets: d.secrets, Routes: d.routes, Modes: modes, ServerMode: serverMode,
		Logger: c.logger, SupervisionPeriod: c.pacing.SupervisionPeriod, ReferentPollInterval: c.pacing.ReferentPollInterval,
		EnginePollInterval: c.pacing.EnginePollInterval, Files: s.files, Volumes: volumes, Hold: c.gate(),
		QuarantinePeriod: c.quarantinePeriodOrDefault(), Now: c.quarantineNow,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build the add-on object reconciler")
	}
	s.reconciler = rec
	s.reserved = controlplane.ReservedPlurals()
	d.ctrl.Register(v1.KindAddonObject.GVK(), rec)
	d.ctrl.Watches(v1.KindAddon.GVK(), rec.MapAddon)
	d.ctrl.Watches(v1.KindSecret.GVK(), rec.MapSecret)
	d.ctrl.Watches(v1.KindOIDCProvider.GVK(), rec.MapOIDCProvider)
	d.ctrl.Watches(v1.KindRestore.GVK(), rec.MapRestore)
	return nil
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
