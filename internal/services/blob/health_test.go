package blob_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	iblob "github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/health"
	svcblob "github.com/pyvvo/funcd/internal/services/blob"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// noStorage is a blob bucket that fails the test on any storage call.
type noStorage struct {
	iblob.Bucket
	t *testing.T
}

func (b noStorage) Get(context.Context, string) ([]byte, error) {
	b.t.Error("CheckRead must not call the storage")
	return nil, nil
}

func (b noStorage) Exists(context.Context, string) (bool, error) {
	b.t.Error("CheckRead must not call the storage")
	return false, nil
}

// CheckRead resolves the alias, asks s3::read on its prefix and resolves its bucket, as Get does, with no storage call
// (ADR-0215 Decision 3).
func TestFacadeCheckRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	require.NoError(t, newFacade(t, noStorage{t: t}, s3PDP{readOK: true}).CheckRead(ctx, "default", "fn", "files"))
	require.Equal(t, fault.Forbidden, fault.KindOf(newFacade(t, noStorage{t: t}, s3PDP{}).CheckRead(ctx, "default", "fn", "files")),
		"no s3::read")
	require.Equal(t, fault.Forbidden, fault.KindOf(newFacade(t, noStorage{t: t}, s3PDP{readOK: true}).CheckRead(ctx, "default", "fn", "other")),
		"an unbound alias")

	gone, err := svcblob.NewFacade(svcblob.FacadeDeps{
		Resolver:   fakeResolver{},
		BucketFor:  func(v1.NamespaceName, string) (iblob.Bucket, bool) { return nil, false },
		Authorizer: s3PDP{readOK: true},
	})
	require.NoError(t, err)
	require.Error(t, gone.CheckRead(ctx, "default", "fn", "files"), "the bound Bucket does not exist")
}

type probe struct{ err atomic.Pointer[error] }

func (p *probe) fail(err error) { p.err.Store(&err) }
func (p *probe) pass()          { p.err.Store(nil) }
func (p *probe) run(context.Context) error {
	if e := p.err.Load(); e != nil {
		return *e
	}
	return nil
}

func probeOnce(p *health.Prober) {
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	cancel()
}

func mkBucket(name v1.ObjectName) *v1.Bucket {
	b := &v1.Bucket{}
	b.TypeMeta = v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}
	b.Name, b.Namespace, b.ResourceGroup = name, "default", "rg1"
	b.Spec.Prefixes = []v1.BucketPrefix{{Name: "attachments", Owner: "todo-api"}}
	return b
}

// ADR-0215 Decision 7 (health-storage-down, the Bucket half): a new Bucket is Ready=True at its generation
// after one pass; while the blob probe fails every Bucket is Degraded, Ready=False StorageUnreachable; it is Ready again
// once the probe passes; a pass whose result did not change writes nothing, and no pass requeues.
func TestBucketStatusFollowsTheBlobProbe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkBucket("todo-files"))
	require.NoError(t, err)
	blobProbe := &probe{}
	prober, err := health.NewProber(nil, time.Hour, time.Second, map[health.Target]health.Probe{health.TargetBlob: blobProbe.run}, nil)
	require.NoError(t, err)
	probeOnce(prober)
	r, err := svcblob.NewReconciler(svcblob.ReconcilerDeps{Store: st, Health: prober})
	require.NoError(t, err)
	pass := func(name v1.ObjectName) *v1.Bucket {
		t.Helper()
		res, rerr := r.Reconcile(ctx, controller.Request{GVK: v1.KindBucket.GVK(), Namespace: "default", Name: name})
		require.NoError(t, rerr)
		require.Zero(t, res.RequeueAfter)
		obj, gerr := st.Get(ctx, v1.KindBucket.GVK(), "default", name)
		require.NoError(t, gerr)
		return obj.(*v1.Bucket)
	}
	requireReady := func(b *v1.Bucket, phase v1.Phase, status v1.ConditionStatus, reason, msg string) {
		t.Helper()
		require.Equal(t, phase, b.Status.Phase)
		require.Equal(t, b.Generation, b.Status.ObservedGeneration)
		c, ok := b.Status.Conditions.Get("Ready")
		require.True(t, ok)
		require.Equal(t, v1.Condition{Type: "Ready", Status: status, Reason: reason, Message: msg, ObservedGeneration: b.Generation,
			LastTransitionTime: c.LastTransitionTime}, c)
	}

	b := pass("todo-files")
	requireReady(b, v1.PhaseReady, v1.ConditionTrue, "", "")
	require.Equal(t, b.ResourceVersion, pass("todo-files").ResourceVersion, "an unchanged result writes no status")

	b.Spec.Prefixes = append(b.Spec.Prefixes, v1.BucketPrefix{Name: "exports", Owner: "todo-api"})
	_, err = st.Update(ctx, b)
	require.NoError(t, err)
	b = pass("todo-files")
	require.EqualValues(t, 2, b.Generation)
	requireReady(b, v1.PhaseReady, v1.ConditionTrue, "", "")

	blobProbe.fail(errors.New("s3: connection refused"))
	probeOnce(prober)
	_, err = st.Create(ctx, mkBucket("todo-tmp"))
	require.NoError(t, err)
	for _, name := range []v1.ObjectName{"todo-files", "todo-tmp"} {
		b = pass(name)
		requireReady(b, v1.PhaseDegraded, v1.ConditionFalse, "StorageUnreachable", "s3: connection refused")
		require.Equal(t, b.ResourceVersion, pass(name).ResourceVersion, "still failing: no write")
	}

	blobProbe.pass()
	probeOnce(prober)
	requireReady(pass("todo-files"), v1.PhaseReady, v1.ConditionTrue, "", "")

	res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindBucket.GVK(), Namespace: "default", Name: "gone"})
	require.NoError(t, err, "a deleted Bucket has nothing to report")
	require.Zero(t, res.RequeueAfter)
}

// Without a prober a Bucket is Ready, as a KVStore is; the reconciler needs a store.
func TestBucketReconcilerWithoutProber(t *testing.T) {
	t.Parallel()
	_, err := svcblob.NewReconciler(svcblob.ReconcilerDeps{})
	require.Equal(t, fault.Invalid, fault.KindOf(err))

	ctx := context.Background()
	st := store.New(storemem.New())
	_, err = st.Create(ctx, mkBucket("todo-files"))
	require.NoError(t, err)
	r, err := svcblob.NewReconciler(svcblob.ReconcilerDeps{Store: st})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindBucket.GVK(), Namespace: "default", Name: "todo-files"})
	require.NoError(t, err)
	obj, err := st.Get(ctx, v1.KindBucket.GVK(), "default", "todo-files")
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, obj.(*v1.Bucket).Status.Phase)
}
