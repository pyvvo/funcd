package admission

import (
	"context"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// --- site-prefix-immutable (ADR-0139): Update on Site --------------------------------------

type sitePrefixImmutable struct{}

// NewSitePrefixImmutableAdmission returns the Validating admission that rejects any change to
// spec.prefix on Update. The reconciler only ever adds prefixes to the Bucket, so a changed prefix
// would orphan the old one with its objects (ADR-0139 Decision §7). It needs no dependencies — Old
// and Object are both on the Request — and it never consults Old.Status, which an apply wipes.
func NewSitePrefixImmutableAdmission() Admission { return sitePrefixImmutable{} }

func (sitePrefixImmutable) Name() string { return "site-prefix-immutable" }
func (sitePrefixImmutable) Phase() Phase { return Validating }

func (sitePrefixImmutable) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindSite.GVK() && op == Update
}

func (sitePrefixImmutable) Admit(_ context.Context, req Request) (v1.Object, error) {
	const op = "admission.site-prefix-immutable"
	oldS, ok := req.Old.(*v1.Site)
	if !ok {
		return req.Object, nil
	}
	newS, ok := req.Object.(*v1.Site)
	if !ok {
		return req.Object, nil
	}
	if newS.Spec.Prefix != oldS.Spec.Prefix {
		return nil, fault.Invalidf(op, "spec.prefix is immutable (%q → %q): the prefix holds the site's bundles and is never removed from the bucket; a new prefix is a new Site", oldS.Spec.Prefix, newS.Spec.Prefix)
	}
	return req.Object, nil
}
