package funcd

import (
	"cmp"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/provider"
	addonsvc "github.com/pyvvo/funcd/internal/services/addon"
)

// addonProvider is the add-ons an admin registers (ADR-0332): one engine per object of an add-on's kind, run by its
// own add-on runtime beside the Lake's and the SQL database's (ADR-0087), with the Addon rules and the object count cap
// at admission.
type addonProvider struct {
	cfg        *config
	reconciler *addonsvc.Reconciler
	reserved   []string // the path segments funcd serves, which no add-on's plural may be
}

var (
	_ describer     = (*addonProvider)(nil)
	_ admissionHook = (*addonProvider)(nil)
)

// addonDeps are the parts the add-on provider is built over: what secrets and the edge built before it.
type addonDeps struct {
	secrets provider.SecretResolver
	routes  *router.Aggregator
	ctrl    *controller.Controller
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

// build builds the engine runtime, which runs containerd's engines, or the runtime WithAddonRuntime gives with its
// modes, and the reconciler, which watches the Addons and the Secrets its objects bind.
func (s *addonProvider) build(d addonDeps) error {
	const op = "funcd.New"
	c := s.cfg
	engineRuntime, modes := c.addonRuntime, c.addonModes
	if engineRuntime == nil {
		var err error
		if engineRuntime, err = provider.NewRuntime(provider.Deps{
			Runtime: c.runtime, OwnerKind: v1.KindAddonObject, Gateway: c.gateway, Logger: c.logger,
			ProbeTimeout: c.pacing.EngineProbeTimeout, StorageRoot: c.providerStorage,
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
	rec, err := addonsvc.NewReconciler(addonsvc.Deps{
		Store: c.store, Provider: engineRuntime, Secrets: d.secrets, Routes: d.routes, Modes: modes, ServerMode: serverMode,
		Logger: c.logger, SupervisionPeriod: c.pacing.SupervisionPeriod, ReferentPollInterval: c.pacing.ReferentPollInterval,
		EnginePollInterval: c.pacing.EnginePollInterval,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build the add-on object reconciler")
	}
	s.reconciler = rec
	s.reserved = controlplane.ReservedPlurals()
	d.ctrl.Register(v1.KindAddonObject.GVK(), rec)
	d.ctrl.Watches(v1.KindAddon.GVK(), rec.MapAddon)
	d.ctrl.Watches(v1.KindSecret.GVK(), rec.MapSecret)
	return nil
}
