package admission

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// --- add-on object count quota (ADR-0332 Decision 2): Create on AddonObject ------------------

type addonObjectQuota struct {
	r               StoreReader
	maxPerNamespace int
}

// NewAddonObjectQuotaAdmission returns the Validating admission that rejects creating an object of an add-on's kind
// when its namespace already holds maxPerNamespace of them, all add-ons together, as the SQLDatabase quota does.
// maxPerNamespace ≤ 0 disables the quota.
func NewAddonObjectQuotaAdmission(r StoreReader, maxPerNamespace int) Admission {
	return addonObjectQuota{r: r, maxPerNamespace: maxPerNamespace}
}

func (addonObjectQuota) Name() string { return "addonobject-quota" }
func (addonObjectQuota) Phase() Phase { return Validating }

// ReadsNamespace: the count check reads the namespace's add-on objects, only while the quota is enabled (ADR-0147).
func (a addonObjectQuota) ReadsNamespace() bool { return a.maxPerNamespace > 0 }

func (addonObjectQuota) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindAddonObject.GVK() && op == Create
}

func (a addonObjectQuota) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.addonobject-quota"
	if _, ok := req.Object.(*v1.AddonObject); !ok {
		return req.Object, nil
	}
	return countQuota(ctx, a.r, op, v1.KindAddonObject, req.Object, a.maxPerNamespace, "add-on objects")
}
