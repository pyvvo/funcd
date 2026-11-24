package admission_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

// addonReader lists Addons, or fails.
type addonReader struct {
	addons []*v1.Addon
	err    error
}

func (r addonReader) List(_ context.Context, gvk v1.GroupVersionKind, _ v1.NamespaceName) ([]v1.Object, error) {
	if r.err != nil {
		return nil, r.err
	}
	var out []v1.Object
	if gvk.Kind == v1.KindAddon {
		for _, a := range r.addons {
			out = append(out, a)
		}
	}
	return out, nil
}

// ADR-0352 Decision 1: an item of an add-on kind is admitted only when a stored Addon registers it, spelled the same, as
// a kind whose objects run an engine, the kind of its entry that serves Engine; a Function or a Job item needs no Addon.
func TestEgressAppliesToAdmission(t *testing.T) {
	t.Parallel()
	tunnel := &v1.Addon{ObjectMeta: v1.ObjectMeta{Name: "frpc"}, Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "frpc",
		Workloads: []v1.Workload{v1.WorkloadEngine}, Attributes: &v1.AddonAttributes{Kind: "Tunnel",
			Settings: []v1.AddonSettings{{Kind: "TunnelRoute"}}}}}}}
	stack := &v1.Addon{ObjectMeta: v1.ObjectMeta{Name: "infrastack"}, Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "infrastack",
		Workloads: []v1.Workload{v1.WorkloadEngineless}, Attributes: &v1.AddonAttributes{Kind: "InfraStack"}}}}}
	deno := &v1.Addon{ObjectMeta: v1.ObjectMeta{Name: "deno"}, Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "deno2",
		Workloads: []v1.Workload{v1.WorkloadFunction, v1.WorkloadJob}}}}}
	policy := func(refs ...v1.AppliesToRef) *v1.EgressPolicy {
		ep := &v1.EgressPolicy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindEgressPolicy.GVK().APIVersion(), Kind: v1.KindEgressPolicy}}
		ep.Name, ep.Namespace, ep.ResourceGroup = "egress", "team", "rg1"
		ep.Spec = v1.EgressPolicySpec{AppliesTo: refs, Rules: []v1.EgressRule{{To: v1.EgressTo{Domains: []string{"example.com"}}}}}
		return ep
	}
	admit := func(r addonReader, op admission.Operation, ep *v1.EgressPolicy) error {
		a := admission.NewEgressAppliesToAdmission(r)
		require.True(t, a.Handles(v1.KindEgressPolicy.GVK(), op))
		_, err := a.Admit(context.Background(), admission.Request{Operation: op, GVK: v1.KindEgressPolicy.GVK(), Object: ep})
		return err
	}
	registered := addonReader{addons: []*v1.Addon{tunnel, stack, deno}}
	for _, ok := range []*v1.EgressPolicy{
		policy(v1.AppliesToRef{Kind: "Tunnel", Name: "public"}, v1.AppliesToRef{Kind: "Function", Name: "api"}),
		policy(v1.AppliesToRef{Kind: "Job", Name: "tofu"}),
		policy(),
	} {
		require.NoError(t, admit(registered, admission.Create, ok))
		require.NoError(t, admit(registered, admission.Update, ok))
	}
	require.NoError(t, admit(addonReader{err: errors.New("unreadable")}, admission.Create, policy(v1.AppliesToRef{Kind: "Job", Name: "tofu"})),
		"an item of funcd's kinds reads no Addon")
	for name, tc := range map[string]struct {
		r   addonReader
		ref v1.AppliesToRef
	}{
		"a kind no Addon registers":    {registered, v1.AppliesToRef{Kind: "Gateway", Name: "public"}},
		"a settings kind":              {registered, v1.AppliesToRef{Kind: "TunnelRoute", Name: "public"}},
		"an engineless kind":           {registered, v1.AppliesToRef{Kind: "InfraStack", Name: "public"}},
		"another case of the kind":     {registered, v1.AppliesToRef{Kind: "TUNNEL", Name: "public"}},
		"a kind whose Addon is absent": {addonReader{}, v1.AppliesToRef{Kind: "Tunnel", Name: "public"}},
	} {
		err := admit(tc.r, admission.Create, policy(v1.AppliesToRef{Kind: "Function", Name: "api"}, tc.ref))
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", name, err)
		require.True(t, strings.Contains(err.Error(), "spec.appliesTo[1].kind"), "%s: %v", name, err)
	}
	err := admit(addonReader{err: errors.New("unreadable")}, admission.Create, policy(v1.AppliesToRef{Kind: "Tunnel", Name: "public"}))
	require.Equal(t, fault.Internal, fault.KindOf(err), "a failed read refuses the write: %v", err)
	require.False(t, admission.NewEgressAppliesToAdmission(registered).Handles(v1.KindEgressPolicy.GVK(), admission.Delete))
}
