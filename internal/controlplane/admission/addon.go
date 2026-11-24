package admission

import (
	"context"
	"fmt"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// --- add-on object count quota (ADR-0332 Decision 2, ADR-0340 Decision 6): Create on AddonObject ---------------------

type addonObjectQuota struct {
	r           StoreReader
	maxEngines  int
	maxSettings int
}

// NewAddonObjectQuotaAdmission returns the Validating admission that rejects creating an add-on object when its
// namespace already holds the most of its class: maxEngines objects of engine and engineless kinds, all add-ons
// together, as the SQLDatabase quota does, or maxSettings objects of settings kinds, every kind together (ADR-0340
// Decision 6). An engineless object drives its Function's calls and the runs they start, so it counts with the engine
// objects (ADR-0345 names no cap of its own). A maximum ≤ 0 disables its class's cap.
func NewAddonObjectQuotaAdmission(r StoreReader, maxEngines, maxSettings int) Admission {
	return addonObjectQuota{r: r, maxEngines: maxEngines, maxSettings: maxSettings}
}

func (addonObjectQuota) Name() string { return "addonobject-quota" }
func (addonObjectQuota) Phase() Phase { return Validating }

// ReadsNamespace: the count reads the namespace's add-on objects, only while a cap is enabled (ADR-0147).
func (a addonObjectQuota) ReadsNamespace() bool { return a.maxEngines > 0 || a.maxSettings > 0 }

func (addonObjectQuota) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindAddonObject.GVK() && op == Create
}

func (a addonObjectQuota) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.addonobject-quota"
	o, ok := req.Object.(*v1.AddonObject)
	if !ok {
		return req.Object, nil
	}
	settings, err := settingsPlurals(ctx, a.r)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list Addons")
	}
	isSettings := settings[v1.AddonPlural(o.AddonKind())]
	limit, what, key := a.maxEngines, "add-on objects of engine and engineless kinds", "addons.maxObjectsPerNamespace"
	if isSettings {
		limit, what, key = a.maxSettings, "settings objects", "addons.maxSettingsPerNamespace"
	}
	if limit <= 0 {
		return req.Object, nil
	}
	existing, err := a.r.List(ctx, v1.KindAddonObject.GVK(), o.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list add-on objects in %q", o.Namespace)
	}
	n := 0
	for _, obj := range existing {
		if x, ok := obj.(*v1.AddonObject); ok && settings[v1.AddonPlural(x.AddonKind())] == isSettings {
			n++
		}
	}
	if n >= limit {
		return nil, fault.Invalidf(op, "namespace %q already holds the maximum %d %s that %s allows", o.Namespace, limit, what, key)
	}
	return req.Object, nil
}

// settingsPlurals are the plurals of every settings kind the Addons declare.
func settingsPlurals(ctx context.Context, r StoreReader) (map[string]bool, error) {
	addons, err := r.List(ctx, v1.KindAddon.GVK(), "")
	if err != nil {
		return nil, fmt.Errorf("list Addons: %w", err)
	}
	out := map[string]bool{}
	for _, obj := range addons {
		if a, ok := obj.(*v1.Addon); ok {
			for _, st := range a.Spec.KindAttributes().Settings {
				out[v1.AddonPlural(st.Kind)] = true
			}
		}
	}
	return out, nil
}
