package cedar

import (
	"context"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// regMeta is an in-memory MetaReader for the registry unit tests (an internal package cedar test so it
// can reach the unexported UID helpers + cedar.Authorize). No mock framework — plain maps.
type regMeta struct {
	fns    map[string]*v1.Function
	stores map[string]*v1.KVStore
}

func mkey(ns v1.NamespaceName, name v1.ObjectName) string { return string(ns) + "/" + string(name) }

func (m regMeta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	switch gvk.Kind {
	case v1.KindFunction:
		if f, ok := m.fns[mkey(ns, name)]; ok {
			return f, nil
		}
	case v1.KindKVStore:
		if s, ok := m.stores[mkey(ns, name)]; ok {
			return s, nil
		}
	}
	return nil, fault.NotFoundf("regMeta.Get", "%s %s/%s not found", gvk.Kind, ns, name)
}

// evaluate assembles the registry's built-in PolicySet + per-request entities, then runs cedar.Authorize
// with the supplied principal/resource UIDs — the exact composition the driver performs, minus the
// package-level KnownAction gate (so a brand-new capability's action can be exercised).
func evaluate(t *testing.T, reg *Registry, m MetaReader, principal, resource auth.EntityRef, action auth.Action, rUID cedartypes.EntityUID) bool {
	t.Helper()
	ep, err := reg.EntityProvider(m)
	require.NoError(t, err)
	em, err := ep.EntitiesFor(context.Background(), principal, resource)
	require.NoError(t, err)
	ps, err := cedar.NewPolicySetFromBytes("builtin", []byte(reg.Builtins()))
	require.NoError(t, err)
	pUID, err := principalUID(principal)
	require.NoError(t, err)
	dec, _ := cedar.Authorize(ps, em, cedartypes.Request{
		Principal: pUID,
		Action:    cedartypes.NewEntityUID("Action", cedartypes.String(action)),
		Resource:  rUID,
		Context:   cedartypes.NewRecord(cedartypes.RecordMap{}),
	})
	return bool(dec)
}

// dummyResourceUID mirrors the real UID builders for a test-only "DummyResource" entity type.
func dummyResourceUID(ns v1.NamespaceName, name string) cedartypes.EntityUID {
	return cedartypes.NewEntityUID("DummyResource", cedartypes.String(string(ns)+"/"+name))
}

// dummyCapability is an entirely NEW capability declared only in this test: a fresh action, entity type,
// principal binding, resource materializer, and built-in permit. It is registered as a plain Capability
// value — proving a new capability composes with ZERO edits to the shared assembly code.
func dummyCapability() Capability {
	return Capability{
		Name:            "dummy",
		Actions:         []auth.Action{auth.Action("dummy::act")},
		EmitsEntityType: "DummyResource",
		Resource: func(_ context.Context, _ MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
			if resource.Type != v1.KindKVStore || resource.Path == "" {
				return nil, false, nil
			}
			uid := dummyResourceUID(resource.Namespace, resource.Path)
			return cedartypes.EntityMap{uid: {UID: uid}}, true, nil
		},
		PrincipalBinding: PrincipalBinding{
			Attr: "dummyBindings",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				if _, ok := src.(*v1.Function); !ok {
					return nil
				}
				return []cedartypes.EntityUID{dummyResourceUID(ns, "widget")}
			},
		},
		Builtin: `permit(principal, action == Action::"dummy::act", resource)
  when { principal has dummyBindings && principal.dummyBindings.contains(resource) };`,
	}
}

// scenario: new-capability-no-entitiesfor-edit — a new capability registered as a Capability value
// authorizes a request (its binding Set + resource entity materialized, its built-in permit evaluated)
// with NO edit to the shared EntitiesFor/schema/built-in-assembly code — the registry composes it.
func TestScenarioNewCapabilityNoEntitiesForEdit(t *testing.T) {
	t.Parallel()
	reg, err := NewRegistry([]Capability{dummyCapability()}, []PrincipalSource{FunctionPrincipalSource()})
	require.NoError(t, err)

	// The registry assembled the new capability's vocabulary from the value alone.
	require.True(t, reg.KnownAction("dummy::act"), "the new action joins the assembled vocabulary")
	require.True(t, reg.KnownEntityType("DummyResource"), "the new entity type joins the assembled vocabulary")

	m := regMeta{fns: map[string]*v1.Function{
		"default/app": {ObjectMeta: v1.ObjectMeta{Name: "app", Namespace: "default", ResourceGroup: "rg1"}},
	}}
	principal := auth.EntityRef{Type: v1.KindFunction, Namespace: "default", Name: "app"}

	// bound: app.dummyBindings contains DummyResource "default/widget" ⇒ the new built-in permit grants it.
	boundRes := auth.EntityRef{Type: v1.KindKVStore, Namespace: "default", Name: "any", Path: "widget"}
	require.True(t, evaluate(t, reg, m, principal, boundRes, "dummy::act", dummyResourceUID("default", "widget")),
		"the new capability's binding grants its action via the assembled built-in — no shared-code edit")

	// unbound: a different DummyResource is not in the binding Set ⇒ default-deny holds for the new capability too.
	unboundRes := auth.EntityRef{Type: v1.KindKVStore, Namespace: "default", Name: "any", Path: "gadget"}
	require.False(t, evaluate(t, reg, m, principal, unboundRes, "dummy::act", dummyResourceUID("default", "gadget")),
		"an unbound resource stays default-deny under the new capability")
}

// scenario: binding-grant-still-self-enforces — a Function with spec.kv reads a bound table via the
// registered KV capability's built-in permit (no Policy), and an unbound table stays default-deny —
// proven directly over the registry-assembled provider + built-ins.
func TestScenarioBindingGrantStillSelfEnforces(t *testing.T) {
	t.Parallel()
	reg, err := NewRegistry(
		[]Capability{KVCapability(), InvokeCapability(), S3Capability()},
		[]PrincipalSource{FunctionPrincipalSource(), CatalogServicePrincipalSource()},
	)
	require.NoError(t, err)

	m := regMeta{
		fns: map[string]*v1.Function{
			"default/analytics": {
				ObjectMeta: v1.ObjectMeta{Name: "analytics", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.FunctionSpec{KV: []v1.FunctionKV{{Alias: "cust", Store: "orders", Table: "customers"}}},
			},
		},
		stores: map[string]*v1.KVStore{
			"default/orders": {
				ObjectMeta: v1.ObjectMeta{Name: "orders", Namespace: "default", ResourceGroup: "rg1"},
				Spec:       v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "customers"}, {Name: "public"}}},
			},
		},
	}
	principal := auth.EntityRef{Type: v1.KindFunction, Namespace: "default", Name: "analytics"}

	bound := auth.EntityRef{Type: v1.KindKVStore, Namespace: "default", Name: "orders", Path: "customers"}
	bUID, err := resourceUID(bound)
	require.NoError(t, err)
	require.True(t, evaluate(t, reg, m, principal, bound, auth.ActionKVRead, bUID),
		"a declared spec.kv binding grants kv::read via the registry-assembled built-in")

	unbound := auth.EntityRef{Type: v1.KindKVStore, Namespace: "default", Name: "orders", Path: "public"}
	uUID, err := resourceUID(unbound)
	require.NoError(t, err)
	require.False(t, evaluate(t, reg, m, principal, unbound, auth.ActionKVRead, uUID),
		"an unbound table stays default-deny (the grant is per-table)")
}

// scenario: assembled-schema-covers-all — the KnownAction/KnownEntityType vocabulary is the UNION of
// the registered capabilities' contributions (no capability silently dropped).
func TestScenarioAssembledSchemaCoversAll(t *testing.T) {
	t.Parallel()
	reg, err := NewRegistry(
		[]Capability{KVCapability(), InvokeCapability(), S3Capability()},
		[]PrincipalSource{FunctionPrincipalSource(), CatalogServicePrincipalSource()},
	)
	require.NoError(t, err)

	for _, a := range []auth.Action{auth.ActionKVRead, auth.ActionKVWrite, auth.ActionLinkInvoke, auth.ActionS3Read, auth.ActionS3Write} {
		require.True(t, reg.KnownAction(a), "action %q must be covered by the assembled vocabulary", a)
	}
	require.False(t, reg.KnownAction("egress::send"), "an unregistered action is not known")

	for _, et := range []string{"Function", "KVStore", "KVTable", "Bucket", "BlobPrefix", "S3Identity"} {
		require.True(t, reg.KnownEntityType(et), "entity type %q must be covered by the assembled vocabulary", et)
	}
	require.False(t, reg.KnownEntityType("Secret"), "an unregistered entity type is not known")

	// The assembled built-in PolicySet is the union of the capabilities' Builtin — every capability's
	// permit is present (none dropped).
	builtins := reg.Builtins()
	require.Contains(t, builtins, `Action::"kv::read"`)
	require.Contains(t, builtins, `Action::"kv::write"`)
	require.Contains(t, builtins, `Action::"link::invoke"`)
	require.Contains(t, builtins, `Action::"s3::read"`)
	require.Contains(t, builtins, `Action::"s3::write"`)
	_, cerr := cedar.NewPolicySetFromBytes("builtin", []byte(builtins))
	require.NoError(t, cerr, "the assembled built-in PolicySet compiles")
}

// scenario: unmodeled-principal-or-resource-faults — a request whose principal or resource type matches
// no registered capability is an Internal fault (a wiring bug), unchanged from today.
func TestScenarioUnmodeledPrincipalOrResourceFaults(t *testing.T) {
	t.Parallel()
	reg, err := NewRegistry(
		[]Capability{KVCapability(), InvokeCapability(), S3Capability()},
		[]PrincipalSource{FunctionPrincipalSource(), CatalogServicePrincipalSource()},
	)
	require.NoError(t, err)
	ep, err := reg.EntityProvider(regMeta{})
	require.NoError(t, err)

	// Unmodeled principal type (not Function/S3Identity) ⇒ Internal fault.
	_, perr := ep.EntitiesFor(context.Background(),
		auth.EntityRef{Type: v1.KindSecret, Namespace: "default", Name: "p"},
		auth.EntityRef{Type: v1.KindKVStore, Namespace: "default", Name: "orders", Path: "customers"})
	require.Error(t, perr)
	require.Equal(t, fault.Internal, fault.KindOf(perr), "an unmodeled principal is an Internal wiring fault")

	// Unmodeled resource type (matches no capability's Resource) ⇒ Internal fault.
	_, rerr := ep.EntitiesFor(context.Background(),
		auth.EntityRef{Type: v1.KindFunction, Namespace: "default", Name: "app"},
		auth.EntityRef{Type: v1.KindSecret, Namespace: "default", Name: "x"})
	require.Error(t, rerr)
	require.Equal(t, fault.Internal, fault.KindOf(rerr), "an unmodeled resource is an Internal wiring fault")
}

// TestNewRegistryValidation — NewRegistry rejects a duplicate Name or Action across capabilities and
// requires at least one PrincipalSource (fault.Invalid).
func TestNewRegistryValidation(t *testing.T) {
	t.Parallel()

	src := []PrincipalSource{FunctionPrincipalSource()}

	_, err := NewRegistry([]Capability{KVCapability(), InvokeCapability(), S3Capability()}, src)
	require.NoError(t, err, "the default capability set is valid")

	// duplicate Name.
	dupName := []Capability{{Name: "kv", Actions: []auth.Action{"a::1"}}, {Name: "kv", Actions: []auth.Action{"a::2"}}}
	_, err = NewRegistry(dupName, src)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a duplicate Name is fault.Invalid")

	// duplicate Action across capabilities.
	dupAction := []Capability{{Name: "one", Actions: []auth.Action{"a::1"}}, {Name: "two", Actions: []auth.Action{"a::1"}}}
	_, err = NewRegistry(dupAction, src)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a duplicate Action is fault.Invalid")

	// no principal source.
	_, err = NewRegistry([]Capability{KVCapability()}, nil)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a missing principal source is fault.Invalid")

	// empty Name.
	_, err = NewRegistry([]Capability{{Name: "", Actions: []auth.Action{"a::1"}}}, src)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an empty Name is fault.Invalid")
}
