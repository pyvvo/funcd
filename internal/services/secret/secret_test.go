package secret_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	secretsvc "github.com/pyvvo/funcd/internal/services/secret"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// recordingResolver answers every Secret with one key, allows every write, and records the principal each call carries.
type recordingResolver struct {
	mu         sync.Mutex
	principals []*auth.EntityRef
}

func (r *recordingResolver) record(p *auth.EntityRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.principals = append(r.principals, p)
}

func (r *recordingResolver) ResolveEnv(_ context.Context, id auth.Identity, _ v1.NamespaceName, names []string) (map[string]string, error) {
	r.record(id.Principal)
	out := map[string]string{}
	for _, n := range names {
		out[n] = "v"
	}
	return out, nil
}

func (r *recordingResolver) CheckRead(_ context.Context, id auth.Identity, _ v1.NamespaceName, _ string) error {
	r.record(id.Principal)
	return nil
}

func (r *recordingResolver) CheckWrite(_ context.Context, id auth.Identity, _ v1.NamespaceName, _ string) error {
	r.record(id.Principal)
	return nil
}

// ownerResolver decides a write as the PDP does, from the stored Secret's owner at decision time, and runs after once,
// when it first allows: the moment between a write's decision and its update.
type ownerResolver struct {
	recordingResolver
	st    store.Store
	after func()
	once  sync.Once
}

func (r *ownerResolver) CheckWrite(ctx context.Context, id auth.Identity, ns v1.NamespaceName, name string) error {
	obj, err := r.st.Get(ctx, v1.KindSecret.GVK(), ns, v1.ObjectName(name))
	if err != nil {
		return err
	}
	if obj.(*v1.Secret).Spec.Owner != id.Principal.Name {
		return fault.Forbiddenf("ownerResolver", "write secret %q denied: not its owner", name)
	}
	if r.after != nil {
		r.once.Do(r.after)
	}
	return nil
}

// conflictStore answers Conflict to every Secret update and counts them.
type conflictStore struct {
	store.Store
	updates atomic.Int32
}

func (s *conflictStore) Update(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if obj.GroupVersionKind().Kind == v1.KindSecret {
		s.updates.Add(1)
		return nil, fault.Conflictf("conflictStore", "resourceVersion mismatch")
	}
	return s.Store.Update(ctx, obj)
}

// writeStore holds Secret oauth (TOKEN t1), owned by refresher, and Functions refresher and api, which bind it, and
// stranger, which binds nothing, all in team-a.
func writeStore(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	st := store.New(memory.New())
	obj, _ := v1.NewObject(v1.KindSecret)
	sec := obj.(*v1.Secret)
	sec.Name, sec.Namespace, sec.ResourceGroup = "oauth", "team-a", "rg1"
	sec.Spec.Owner = "refresher"
	sec.Spec.Data = map[string][]byte{"TOKEN": []byte("t1")}
	_, err := st.Create(ctx, sec)
	require.NoError(t, err)
	for _, n := range []v1.ObjectName{"refresher", "api", "stranger"} {
		obj, _ := v1.NewObject(v1.KindFunction)
		fn := obj.(*v1.Function)
		fn.Name, fn.Namespace, fn.ResourceGroup = n, "team-a", "rg1"
		fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///unused.mjs"
		if n != "stranger" {
			fn.Spec.Bindings.Secret = []v1.ObjectName{"oauth"}
		}
		_, err := st.Create(ctx, fn)
		require.NoError(t, err)
	}
	return st
}

func facade(t *testing.T, st store.Store, res secretsvc.Resolver) *secretsvc.Facade {
	t.Helper()
	f, err := secretsvc.NewFacade(secretsvc.Deps{Store: st, Resolver: res})
	require.NoError(t, err)
	return f
}

func data(t *testing.T, st store.Store) map[string][]byte {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindSecret.GVK(), "team-a", "oauth")
	require.NoError(t, err)
	return obj.(*v1.Secret).Spec.Data
}

// The facade reads, and answers the readiness check, as the calling Function (ADR-0321 Decisions 3 and 4).
func TestFacadeAsksAsTheCallingFunction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "reports", "team-a", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///unused.mjs"
	fn.Spec.Bindings.Secret = []v1.ObjectName{"api-keys"}
	_, err := st.Create(ctx, fn)
	require.NoError(t, err)
	res := &recordingResolver{}
	f := facade(t, st, res)

	keys, err := f.List(ctx, "team-a", "reports")
	require.NoError(t, err)
	require.Equal(t, []string{"api-keys"}, keys)
	require.NoError(t, f.CheckRead(ctx, "team-a", "reports", "api-keys"))
	want := &auth.EntityRef{Type: v1.KindFunction, Namespace: "team-a", Name: "reports"}
	require.Equal(t, []*auth.EntityRef{want, want}, res.principals)
}

// A write sets or removes one key of a Secret the caller binds, asked as the calling Function (ADR-0327 Decision 4); a
// delete of a missing key is no error, and another Secret is Forbidden before any read of it.
func TestFacadeWritesOneKeyAsTheCallingFunction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := writeStore(t)
	res := &recordingResolver{}
	f := facade(t, st, res)

	require.NoError(t, f.Put(ctx, "team-a", "refresher", "oauth", "REFRESH", "r1"))
	require.Equal(t, map[string][]byte{"TOKEN": []byte("t1"), "REFRESH": []byte("r1")}, data(t, st))
	require.NoError(t, f.Delete(ctx, "team-a", "refresher", "oauth", "TOKEN"))
	require.NoError(t, f.Delete(ctx, "team-a", "refresher", "oauth", "TOKEN"), "a missing key's delete")
	require.Equal(t, map[string][]byte{"REFRESH": []byte("r1")}, data(t, st))
	want := &auth.EntityRef{Type: v1.KindFunction, Namespace: "team-a", Name: "refresher"}
	require.Equal(t, []*auth.EntityRef{want, want, want}, res.principals)

	err := f.Put(ctx, "team-a", "stranger", "oauth", "TOKEN", "x")
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "a Secret the caller does not bind: %v", err)
	require.Len(t, res.principals, 3, "an unbound Secret is refused before any decision")
}

// scenario: secret-concurrent-writes — two writers of different keys of one Secret both keep their key: a write that
// loses the race to the other's update starts over from a fresh read (ADR-0327 Decision 4). Under eight racing writers a
// write may end in 409 after its 5 attempts; one acknowledged stays, and one refused wrote nothing.
//
// oracle: docs/design/secret/key-write.fizz#AckedWritesStay
func TestScenarioSecretConcurrentWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := writeStore(t)
	other := facade(t, st, &recordingResolver{})
	res := &ownerResolver{st: st}
	res.after = func() { require.NoError(t, other.Put(ctx, "team-a", "api", "oauth", "OTHER", "o1")) }
	require.NoError(t, facade(t, st, res).Put(ctx, "team-a", "refresher", "oauth", "TOKEN", "t2"))
	require.Equal(t, map[string][]byte{"TOKEN": []byte("t2"), "OTHER": []byte("o1")}, data(t, st), "both writes stay")

	f := facade(t, st, &recordingResolver{})
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { errs[i] = f.Put(ctx, "team-a", "api", "oauth", fmt.Sprintf("K%d", i), "v") })
	}
	wg.Wait()
	got := data(t, st)
	for i, err := range errs {
		key := fmt.Sprintf("K%d", i)
		if err != nil {
			require.Equal(t, fault.Conflict, fault.KindOf(err), "%s: %v", key, err)
			require.NotContains(t, got, key, "a refused write wrote nothing")
			continue
		}
		require.Equal(t, "v", string(got[key]), "an acknowledged %s stays", key)
	}
}

// A write decides after its read, and lands only on the version it read: an owner removed between the decision and the
// update makes the update conflict, and the next attempt's decision refuses it (ADR-0327 Decision 4).
//
// oracle: docs/design/secret/key-write.fizz#OnlyWritersWrite
func TestSecretWriteDecidesAfterItsRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := writeStore(t)
	res := &ownerResolver{st: st}
	res.after = func() {
		obj, err := st.Get(ctx, v1.KindSecret.GVK(), "team-a", "oauth")
		require.NoError(t, err)
		sec := obj.(*v1.Secret)
		sec.Spec.Owner = ""
		sec.Spec.Data = map[string][]byte{"TOKEN": []byte("applied")}
		_, err = st.Update(ctx, sec)
		require.NoError(t, err)
	}
	err := facade(t, st, res).Put(ctx, "team-a", "refresher", "oauth", "TOKEN", "t2")
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "%v", err)
	require.Equal(t, map[string][]byte{"TOKEN": []byte("applied")}, data(t, st), "the former owner wrote nothing")
}

// A write gives up after its 5 attempts with a Conflict, and changes nothing (ADR-0327 Decision 4).
//
// oracle: docs/design/secret/key-write.fizz#AttemptsAreBounded
// oracle: docs/design/secret/key-write.fizz#WritesEnd
func TestSecretWriteGivesUpAfterItsAttempts(t *testing.T) {
	t.Parallel()
	inner := writeStore(t)
	cs := &conflictStore{Store: inner}
	err := facade(t, cs, &recordingResolver{}).Put(context.Background(), "team-a", "refresher", "oauth", "TOKEN", "t2")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.Equal(t, int32(5), cs.updates.Load(), "the ADR's 5 attempts in all")
	require.Equal(t, map[string][]byte{"TOKEN": []byte("t1")}, data(t, inner))
}
