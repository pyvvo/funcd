package secrets_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/services/roles"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// noPolicies is a namespace with no Policy: the built-in rules alone decide.
type noPolicies struct{}

func (noPolicies) Policies(context.Context) ([]v1.Policy, string, error) { return nil, "1", nil }

// benchSecrets are the Secrets Function reader binds.
func benchSecrets() []string { return []string{"a", "b", "c"} }

// benchResolver resolves over the Secret capability as pkg/funcd builds it (ADR-0327 Decision 1), in a namespace where
// reader binds three Secrets and two RolesAssignments name writers: what each secret::read decision reads.
func benchResolver(b *testing.B) *secrets.Resolver {
	b.Helper()
	ctx := context.Background()
	st := store.New(memory.New())
	create := func(o v1.Object) {
		_, err := st.Create(ctx, o)
		require.NoError(b, err)
	}
	fobj, _ := v1.NewObject(v1.KindFunction)
	fn := fobj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "reader", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///unused.mjs"
	for _, n := range benchSecrets() {
		obj, _ := v1.NewObject(v1.KindSecret)
		sec := obj.(*v1.Secret)
		sec.Name, sec.Namespace, sec.ResourceGroup = v1.ObjectName(n), "default", "rg1"
		sec.Spec.Data = map[string][]byte{"K_" + n: []byte("v")}
		create(sec)
		fn.Spec.Bindings.Secret = append(fn.Spec.Bindings.Secret, v1.ObjectName(n))
	}
	create(fn)
	for _, w := range []v1.ObjectName{"w1", "w2"} {
		obj, _ := v1.NewObject(v1.KindRolesAssignment)
		ra := obj.(*v1.RolesAssignment)
		ra.Name, ra.Namespace, ra.ResourceGroup = "grant-"+w, "default", "rg1"
		ra.Spec.Principal = &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: w}
		ra.Spec.Assignments = []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "Secret Data Writer"},
			Scope: &v1.ScopeRef{Kind: v1.ScopeKindSecret, Name: "a"}}}
		create(ra)
	}
	reg, err := cedar.NewRegistry([]cedar.Capability{cedar.SecretCapabilityWithWriters(roles.NewLister(st))},
		[]cedar.PrincipalSource{cedar.FunctionPrincipalSource()})
	require.NoError(b, err)
	ep, err := reg.EntityProvider(st)
	require.NoError(b, err)
	pdp, err := cedar.New(cedar.Deps{Entities: ep, Policies: noPolicies{}, Builtins: reg.Builtins()})
	require.NoError(b, err)
	res, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: pdp})
	require.NoError(b, err)
	return res
}

// BenchmarkResolveEnv is one ctx.secret read of three bound Secrets as the Function: a decision and a get for each.
func BenchmarkResolveEnv(b *testing.B) {
	res := benchResolver(b)
	ctx, id, names := context.Background(), secrets.WorkloadIdentity(v1.KindFunction, "default", "reader"), benchSecrets()
	for b.Loop() {
		if _, err := res.ResolveEnv(ctx, id, "default", names); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCheckRead is the readiness check's question for one bound Secret.
func BenchmarkCheckRead(b *testing.B) {
	res := benchResolver(b)
	ctx, id := context.Background(), secrets.WorkloadIdentity(v1.KindFunction, "default", "reader")
	for b.Loop() {
		if err := res.CheckRead(ctx, id, "default", "a"); err != nil {
			b.Fatal(err)
		}
	}
}
