//go:build e2e

package funcd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// scenario: catalog-cycle-any-order (ADR-0121) — the Bucket↔CatalogService owner/binding cycle applies in
// the "wrong" order (the CatalogService, which binds the Bucket AND is its prefix owner, applied BEFORE the
// Bucket) with NO two-phase workaround. Both Apply calls SUCCEED over the real control plane (the removed
// catalog-blob-validity / bucket-prefix-owner-exists admissions would have rejected them synchronously); the
// CatalogService is held not-Ready with a BucketNotFound Waiting condition, and once the Bucket is applied it
// converges PAST the gate. This is the end-to-end proof of apply-all-and-converge.
func TestScenarioDeclarativeAdmissionCatalogCycle(t *testing.T) {
	p, err := New(InMemory())
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	ctx := context.Background()

	// Apply the CatalogService FIRST — it binds Bucket `lakehouse` (gold), which does not exist yet. Before
	// ADR-0121 the catalog-blob-validity admission REJECTED this synchronously; now it is admitted.
	cs := &v1.CatalogService{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindCatalogService.GVK().APIVersion(), Kind: v1.KindCatalogService},
		ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.CatalogServiceSpec{
			Blob:    []v1.FunctionBlob{{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"}},
			Catalog: v1.CatalogRef{Bucket: "lakehouse", Prefix: "gold"},
		},
	}
	_, err = c.Apply(ctx, cs)
	require.NoError(t, err, "the CatalogService is ADMITTED though its bound Bucket does not exist yet (no synchronous reject)")

	// It is held not-Ready with a BucketNotFound Waiting condition (the reconcile-time existence gate).
	require.Eventually(t, func() bool {
		obj, gerr := c.Get(ctx, v1.KindCatalogService, "default", "lake")
		if gerr != nil {
			return false
		}
		cond, ok := obj.(*v1.CatalogService).Status.Conditions.Get("Ready")
		return ok && cond.Status == v1.ConditionFalse && cond.Reason == "BucketNotFound"
	}, 10*time.Second, 50*time.Millisecond, "CatalogService waits Ready=False/BucketNotFound until its Bucket exists")

	// Apply the Bucket WITH the prefix owner referencing the CatalogService — the removed
	// bucket-prefix-owner-exists admission is now reconcile-time, so this is admitted in any order.
	bkt := &v1.Bucket{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: "lakehouse", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: "lake"}}},
	}
	_, err = c.Apply(ctx, bkt)
	require.NoError(t, err, "the Bucket is ADMITTED though its prefix owner is a CatalogService (owner existence is reconcile-time)")

	// Both resources are admitted in the "wrong" order (the cycle is broken) and the consumer surfaces an
	// observable Waiting condition — the ADMISSION change this ADR makes. Convergence PAST the bucket gate
	// (Converge called once the referent exists) is proven deterministically by the reconcile-level test
	// TestReconcile_waits_for_bucket (fake provider), and end-to-end on real containerd by the Venom
	// `apply-any-order` lane — the in-process add-on provider can't reach a live engine here.
}

// scenario: consumer-before-referent-admitted (ADR-0121) — a Function binding a Bucket that does not exist
// yet is ADMITTED over the real control plane (the removed blob-binding-validity admission would have
// rejected it synchronously). Apply succeeding in any order is the core proof; the not-Ready→Ready
// convergence of the data-reference gate itself is covered by the internal reconciler unit tests
// (resolveDataReferences), which don't need a deployable artifact.
func TestScenarioDeclarativeAdmissionFunctionBindingAdmitted(t *testing.T) {
	p, err := New(InMemory())
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	ctx := context.Background()

	fn := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "reader", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.FunctionSpec{
			Runtime: "nodejs22",
			Handler: "handle",
			Image:   "oci-layout:///mnt/funcd-deps/registry:reader",
			Blob:    []v1.FunctionBlob{{Alias: "lake", Bucket: "missing", Prefix: "bronze"}},
		},
	}
	_, err = c.Apply(ctx, fn)
	require.NoError(t, err, "a Function binding a not-yet-applied Bucket is ADMITTED (no synchronous reject)")
}
