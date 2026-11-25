package controlplane_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

// ADR-0332 Decision 6: an Addon's image is pinned by digest, with no tag, and comes from a listed registry, its host
// after docker.io normalization.
func TestAddonImageAllowed(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("a", 64)
	registries := []string{"ghcr.io", "docker.io", "localhost:5000"}
	for image, want := range map[string]bool{
		"ghcr.io/pyvvo/funcd-rauthy" + digest:              true,
		"GHCR.io/pyvvo/funcd-rauthy" + digest:              true,  // a host in another case is the same registry
		"ghcr.io/PyVVO/funcd-rauthy" + digest:              false, // a repository name is lower case
		"traefik/whoami" + digest:                          true,  // docker.io
		"busybox" + digest:                                 true,  // docker.io/library
		"localhost:5000/rauthy" + digest:                   true,
		"quay.io/pyvvo/funcd-rauthy" + digest:              false,
		"ghcr.io/pyvvo/funcd-rauthy:0.1.1":                 false,
		"ghcr.io/pyvvo/funcd-rauthy:0.1.1" + digest:        false,
		"ghcr.io/pyvvo/funcd-rauthy@sha256:abc":            false,
		"ghcr.io.evil.example/pyvvo/funcd-rauthy" + digest: false,
	} {
		require.Equal(t, want, controlplane.AddonImageAllowed(image, registries), image)
	}
	require.False(t, controlplane.AddonImageAllowed("ghcr.io/pyvvo/funcd-rauthy"+digest, nil), "no registry refuses every image")
}

// ADR-0332 Decision 1: the reserved plurals are every path segment funcd serves, its kinds' plurals and its other
// routes', and never the add-on routes' own template.
func TestReservedPlurals(t *testing.T) {
	t.Parallel()
	got := controlplane.ReservedPlurals()
	for _, want := range []string{"functions", "lakes", "sqldatabases", "addons", "namespaces", "runtimeclasses", "deadletters", "volumes", "hold"} {
		require.Contains(t, got, want)
	}
	for _, seg := range got {
		require.NotContains(t, seg, "{", seg)
	}
}

// ADR-0334 Decision 5: under funcdctl dev an Addon's image may come from any registry, still pinned by digest; without
// AnyRegistry an unlisted registry is refused.
func TestAddonRulesAnyRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	write := func(image string) admission.Request {
		a := &v1.Addon{Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider", Image: image, Workloads: []v1.Workload{v1.WorkloadEngine},
			Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080}}}}}
		return admission.Request{Operation: admission.Create, GVK: v1.KindAddon.GVK(), Object: a}
	}
	digested := "quay.io/pyvvo/funcd-rauthy@sha256:" + strings.Repeat("a", 64)
	rules := func(r controlplane.AddonRules) admission.Admission { return controlplane.NewAddonAdmissions(r)[1] }
	_, err := rules(controlplane.AddonRules{AnyRegistry: true}).Admit(ctx, write(digested))
	require.NoError(t, err, "any registry")
	_, err = rules(controlplane.AddonRules{AnyRegistry: true}).Admit(ctx, write("quay.io/pyvvo/funcd-rauthy:0.1.1"))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a tag is still refused: %v", err)
	_, err = rules(controlplane.AddonRules{Registries: []string{"ghcr.io"}}).Admit(ctx, write(digested))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an unlisted registry: %v", err)
}

// ADR-0340 Decisions 2 and 4: the settings constraints and the Addon's uniqueness read the store, so both take the
// admission lock (ADR-0147): the settings admission on an object's create and replace, the uniqueness on an Addon's
// create and update.
func TestAddonAdmissionsTakeTheLock(t *testing.T) {
	t.Parallel()
	byName := map[string]admission.Admission{}
	for _, a := range controlplane.NewAddonAdmissions(controlplane.AddonRules{}) {
		byName[a.Name()] = a
	}
	for name, ops := range map[string][]admission.Operation{
		"addon-settings": {admission.Create, admission.Update},
		"addon-unique":   {admission.Create, admission.Update},
	} {
		a, ok := byName[name]
		require.True(t, ok, name)
		reader, ok := a.(admission.NamespaceReading)
		require.True(t, ok && reader.ReadsNamespace(), "%s reads under the lock", name)
		gvk := v1.KindAddonObject.GVK()
		if name == "addon-unique" {
			gvk = v1.KindAddon.GVK()
		}
		for _, op := range ops {
			require.True(t, a.Handles(gvk, op), "%s handles %s", name, op)
		}
	}
}

// reads is a StoreReader over fixed objects.
type reads []v1.Object

func (s reads) List(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	var out []v1.Object
	for _, o := range s {
		if o.GroupVersionKind() == gvk && (ns == "" || o.GetNamespace() == ns) {
			out = append(out, o)
		}
	}
	return out, nil
}

// rauthyRevision is the revision number of rauthy, whose image says what schema the config of a RauthyPasswordPolicy has.
func rauthyRevision(number int64, spec v1.AddonSpec, schema string) *v1.AddonRevision {
	rev := &v1.AddonRevision{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonRevision}}
	rev.Name, rev.Spec = v1.AddonRevisionName("rauthy", number), v1.AddonRevisionSpec{Addon: "rauthy", Number: number, Spec: spec}
	rev.Status.Contracts = []v1.AddonKindContract{{Kind: "RauthyPasswordPolicy", Runtime: "nodejs22", Contract: v1.WorkflowContract{
		Input: json.RawMessage(`{"properties":{"objects":{"type":"array","items":{"type":"object","properties":{"config":` + schema + `}}}}}`)}}}
	return rev
}

// ADR-0358 Decision 5: a settings object's target is of its Addon's own kind, so an object of another kind that has the
// name pins nothing for it, and the current revision judges its write; before any revision has the kind, no contract does,
// and the Addon's own constraints stand.
func TestAddonSettingsWriteWithNoPin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}}
	a.Name = "rauthy"
	a.Spec = v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider", Image: "ghcr.io/pyvvo/funcd-rauthy@sha256:" + strings.Repeat("a", 64),
		Workloads: []v1.Workload{v1.WorkloadEngine}, Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080, Settings: []v1.AddonSettings{{
			Kind: "RauthyPasswordPolicy", Target: "spec.config.provider", Constraints: v1.AddonConstraints{Count: v1.AddonCount{PerTarget: 1}},
			Function: v1.AddonFunction{Image: "ghcr.io/pyvvo/rauthy-settings@sha256:" + strings.Repeat("b", 64)}}}}}}}
	object := func(kind string, name v1.ObjectName, config string) *v1.AddonObject {
		o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject}}
		o.Name, o.Namespace, o.Spec.Kind, o.Spec.Config = name, "default", kind, json.RawMessage(config)
		return o
	}
	admit := func(objs reads, o *v1.AddonObject) error {
		for _, ad := range controlplane.NewAddonAdmissions(controlplane.AddonRules{Store: objs}) {
			if !ad.Handles(v1.KindAddonObject.GVK(), admission.Create) {
				continue
			}
			if _, err := ad.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindAddonObject.GVK(), Object: o}); err != nil {
				return err
			}
		}
		return nil
	}
	strict := object("RauthyPasswordPolicy", "strict", `{"provider":"login"}`)
	other := object("RauthyPasswordPolicy", "other", `{"provider":"login","maxAge":30}`)
	require.NoError(t, admit(reads{a}, other), "no contract is read yet")
	require.ErrorContains(t, admit(reads{a, strict}, other), "count.perTarget", "the Addon's own constraints stand")

	former := rauthyRevision(1, a.Spec, `{"type":"object","additionalProperties":false,"properties":{"provider":{"type":"string"},"maxAge":{"type":"integer"}}}`)
	current := rauthyRevision(2, a.Spec, `{"type":"object","additionalProperties":false,"properties":{"provider":{"type":"string"}}}`)
	a.Status.CurrentRevision = current.Name
	login := object("IdentityProvider", "login", `{}`)
	login.Status.AddonRevision = former.Name
	require.NoError(t, admit(reads{a, former, current, login}, other), "login pins the former one")
	login.Spec.Kind = "InfraStack"
	require.ErrorContains(t, admit(reads{a, former, current, login}, other), "maxAge", "an InfraStack of that name is no target")
}
