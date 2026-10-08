package app_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// reader adapts store.Store to admission.StoreReader, as the control plane's wiring does.
type reader struct{ s store.Store }

func (r reader) List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	res, err := r.s.List(ctx, gvk, store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// recorder admits every write and records it.
type recorder struct{ reqs []admission.Request }

func (*recorder) Name() string                                          { return "record" }
func (*recorder) Phase() admission.Phase                                { return admission.Validating }
func (*recorder) Handles(v1.GroupVersionKind, admission.Operation) bool { return true }
func (r *recorder) Admit(_ context.Context, req admission.Request) (v1.Object, error) {
	r.reqs = append(r.reqs, req)
	return req.Object, nil
}

// partAdmissions is a subset of the admissions a direct write of a part passes, with a bucket-count quota of
// maxBuckets.
func partAdmissions(maxBuckets int) func(admission.StoreReader) []admission.Admission {
	return func(r admission.StoreReader) []admission.Admission {
		return []admission.Admission{
			admission.NewValidateAdmission(),
			admission.NewLinkValidityAdmission(r),
			admission.NewBucketQuotaAdmission(r, maxBuckets),
			admission.NewKVStoreQuotaAdmission(r, 0),
		}
	}
}

func admit(t *testing.T, st store.Store, parts func(admission.StoreReader) []admission.Admission, a *v1.App, old *v1.App) error {
	t.Helper()
	req := admission.Request{Operation: admission.Create, GVK: v1.KindApp.GVK(), Object: a, Identity: auth.Identity{Subject: "dev", Role: auth.RoleDeveloper}}
	if old != nil {
		req.Operation, req.Old = admission.Update, old
	}
	_, err := app.NewAdmission(parts, reader{st}).Admit(context.Background(), req)
	return err
}

func TestAdmissionHandlesAppCreateAndUpdate(t *testing.T) {
	a := app.NewAdmission(partAdmissions(0), reader{store.New(memory.New())})
	require.Equal(t, "app-parts", a.Name())
	require.Equal(t, admission.Validating, a.Phase())
	require.True(t, a.Handles(v1.KindApp.GVK(), admission.Create))
	require.True(t, a.Handles(v1.KindApp.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindApp.GVK(), admission.Delete))
	require.False(t, a.Handles(v1.KindFunction.GVK(), admission.Create))
}

// ADR-0147: app-parts takes the namespace lock when a part admission it runs reads the namespace.
func TestAdmissionReadsNamespaceWhenAPartAdmissionDoes(t *testing.T) {
	r := reader{store.New(memory.New())}
	reads := func(parts func(admission.StoreReader) []admission.Admission) bool {
		nr, ok := app.NewAdmission(parts, r).(admission.NamespaceReading)
		require.True(t, ok)
		return nr.ReadsNamespace()
	}
	require.True(t, reads(partAdmissions(3)), "link-validity and an enabled quota read the namespace")
	require.False(t, reads(func(admission.StoreReader) []admission.Admission {
		return []admission.Admission{admission.NewValidateAdmission(), admission.NewBucketQuotaAdmission(r, 0)}
	}), "a disabled quota does not")
	require.False(t, reads(func(admission.StoreReader) []admission.Admission {
		return []admission.Admission{runReader{}}
	}), "an admission of a kind with no section does not count")
}

// scenario: app-admission-refuses (the quota half) — the quota counts the App's other new parts with the stored ones,
// so two new Buckets one below the bucket-count quota are refused, naming the entry and the quota.
func TestScenarioAppAdmissionRefusesOverQuota(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	_, err := st.Create(ctx, &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: "elsewhere", Namespace: ns, ResourceGroup: "rg1"}})
	require.NoError(t, err)

	err = admit(t, st, partAdmissions(2), todoApp(nil), nil)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "spec.buckets[0]")
	require.ErrorContains(t, err, "bucket-count")

	require.NoError(t, admit(t, st, partAdmissions(2), todoApp(func(a *v1.App) { a.Spec.Buckets = a.Spec.Buckets[1:] }), nil),
		"one new Bucket fits")
	require.NoError(t, admit(t, st, partAdmissions(3), todoApp(nil), nil))

	h := newHarness(t, st)
	h.create(todoApp(func(a *v1.App) { a.Spec.Buckets = a.Spec.Buckets[1:] }))
	h.reconcile()
	stored := h.app()
	require.NoError(t, admit(t, st, partAdmissions(3), todoApp(nil), stored), "an update over a stored Bucket is not counted again")
	err = admit(t, st, partAdmissions(2), todoApp(nil), stored)
	require.ErrorContains(t, err, "spec.buckets[0]", "the stored Bucket and the other part fill the quota")
}

// Decision 3: each part is admitted over a view that holds the App's other parts, as if they were stored.
func TestAdmissionViewHoldsTheOtherParts(t *testing.T) {
	st := store.New(memory.New())
	link := func(target v1.ObjectName) func(*v1.App) {
		return func(a *v1.App) {
			a.Spec.Functions[0].Links = []v1.FunctionLink{{Alias: "mail", Target: target}}
			a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Name: "todo-mail", FunctionSpec: apiSpec()})
		}
	}
	require.NoError(t, admit(t, st, partAdmissions(0), todoApp(link("todo-mail")), nil), "a link to a part of the same App")
	err := admit(t, st, partAdmissions(0), todoApp(link("nowhere")), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "spec.functions[0] (Function/todo-api)")
	require.ErrorContains(t, err, "link-validity")
}

// scenario: app-store-deletion-flip (the admission half) — a ref to an object this App controls is refused; a ref
// to a retained store, or to an object that does not exist yet, is admitted.
func TestScenarioAppStoreDeletionFlipRefusesAControlledRef(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(nil))
	stored := h.app()
	toRef := func(i int) func(*v1.App) {
		return func(a *v1.App) {
			a.Spec.KV[i] = v1.AppKVStore{Ref: a.Spec.KV[i].Name}
		}
	}
	err := admit(t, h.st, partAdmissions(0), todoApp(toRef(1)), stored)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "spec.kv[1].ref")

	require.NoError(t, admit(t, h.st, partAdmissions(0), todoApp(toRef(0)), stored), "a retained store carries only the marker")
	require.NoError(t, admit(t, h.st, partAdmissions(0), todoApp(func(a *v1.App) {
		a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Ref: "mailer"})
	}), stored), "a ref target need not exist (ADR-0121)")
}

// Decision 3: a part is admitted as a create when absent, else as an update over the stored object, with the App
// writer's identity; a part admission's refusal keeps its kind.
func TestAdmissionAdmitsEachPartAsItsDirectWrite(t *testing.T) {
	h := newHarness(t, nil)
	h.create(todoApp(func(a *v1.App) { a.Spec.Routes = nil }))
	h.reconcile()
	rec := &recorder{}
	parts := func(admission.StoreReader) []admission.Admission { return []admission.Admission{rec} }
	require.NoError(t, admit(t, h.st, parts, todoApp(nil), h.app()))

	require.Len(t, rec.reqs, 7)
	for _, req := range rec.reqs {
		m := req.Object.GetObjectMeta()
		require.Equal(t, "dev", req.Identity.Subject)
		require.Equal(t, req.Object.GroupVersionKind(), req.GVK)
		if req.GVK.Kind == v1.KindRoute {
			require.Equal(t, admission.Create, req.Operation, "an absent part is created")
			require.Nil(t, req.Old)
			continue
		}
		require.Equal(t, admission.Update, req.Operation, "%s/%s", req.GVK.Kind, m.Name)
		require.Equal(t, m.Name, req.Old.GetObjectMeta().Name)
		require.NotEmpty(t, req.Old.GetObjectMeta().UID, "Old is the stored object")
	}

	conflict := func(admission.StoreReader) []admission.Admission {
		return []admission.Admission{refuser{}}
	}
	err := admit(t, h.st, conflict, todoApp(nil), h.app())
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.ErrorContains(t, err, "spec.kv[0] (KVStore/todo-store)")
}

// refuser refuses every write with a Conflict.
type refuser struct{}

func (refuser) Name() string                                          { return "refuse" }
func (refuser) Phase() admission.Phase                                { return admission.Validating }
func (refuser) Handles(v1.GroupVersionKind, admission.Operation) bool { return true }
func (refuser) Admit(context.Context, admission.Request) (v1.Object, error) {
	return nil, fault.Conflictf("admission.refuse", "refused")
}

// runReader reads the namespace on a WorkflowRun write, a kind with no App section.
type runReader struct{ refuser }

func (runReader) ReadsNamespace() bool { return true }
func (runReader) Handles(gvk v1.GroupVersionKind, _ admission.Operation) bool {
	return gvk == v1.KindWorkflowRun.GVK()
}
