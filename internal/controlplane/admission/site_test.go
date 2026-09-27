package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

func mkSite(prefix, digest string) *v1.Site {
	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = "bi", "default", "rg1"
	s.Spec = v1.SiteSpec{Image: "oci-layout:///layout:bi", Bucket: v1.SiteBucket{Name: "reports"}, Prefix: prefix}
	s.Status.Digest = digest
	return s
}

// scenario: prefix-is-immutable (ADR-0139 §7) — an Update changing spec.prefix is Invalid whether or
// not the Site has materialized (the admission never consults Old.Status, which an apply wipes); a
// non-prefix Update (a new image, a changed host) is admitted; Create is not handled.
func TestScenarioSitePrefixIsImmutable(t *testing.T) {
	adm := admission.NewSitePrefixImmutableAdmission()
	gvk := v1.KindSite.GVK()
	require.Equal(t, "site-prefix-immutable", adm.Name())
	require.Equal(t, admission.Validating, adm.Phase())
	require.True(t, adm.Handles(gvk, admission.Update))
	require.False(t, adm.Handles(gvk, admission.Create))
	require.False(t, adm.Handles(v1.KindRoute.GVK(), admission.Update))

	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, oldDigest := range []string{digest, ""} {
		_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: gvk, Old: mkSite("bi", oldDigest), Object: mkSite("reports", "")})
		require.Equal(t, fault.Invalid, fault.KindOf(err), "a changed prefix is Invalid (old digest %q)", oldDigest)
		require.Contains(t, err.Error(), "spec.prefix is immutable")
	}

	changedImage := mkSite("bi", "")
	changedImage.Spec.Image = "oci-layout:///layout:bi-v2"
	changedImage.Spec.Ingress.Host = "bi.example.com"
	out, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: gvk, Old: mkSite("bi", digest), Object: changedImage})
	require.NoError(t, err, "a non-prefix Update is admitted")
	require.Same(t, changedImage, out, "the object passes through unchanged")
}
