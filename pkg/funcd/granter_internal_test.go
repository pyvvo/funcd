package funcd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// storeGranter grants a nil pin when the target Function exists, and a pin only when it names the target and its
// Revision is still controlled by the pinned Function UID with the pinned digest (ADR-0190 Decision 4).
func TestStoreGranterPins(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "shared", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "oci://example/shared:v1"
	created, err := st.Create(ctx, fn)
	require.NoError(t, err)
	fn = created.(*v1.Function)
	const digest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	robj, _ := v1.NewObject(v1.KindRevision)
	rev := robj.(*v1.Revision)
	owner := v1.ObjectRef{Kind: v1.KindFunction, Namespace: "default", Name: "shared"}
	rev.Name, rev.Namespace, rev.ResourceGroup = "shared-1", "default", "rg1"
	rev.OwnerReferences = []v1.OwnerReference{{ObjectRef: owner, UID: fn.UID, Controller: true}}
	rev.Spec = v1.RevisionSpec{Function: owner, Number: 1, Image: fn.Spec.Image, ImageDigest: digest}
	_, err = st.Create(ctx, rev)
	require.NoError(t, err)

	g := storeGranter{store: st}
	pin := v1.RevisionPin{Function: "shared", FunctionUID: fn.UID, Revision: "shared-1", ImageDigest: digest}
	require.True(t, g.Allow("default", "shared", nil), "a nil pin is granted by name")
	require.False(t, g.Allow("default", "absent", nil))
	require.True(t, g.Allow("default", "shared", &pin))
	other := pin
	other.Function = "other"
	require.False(t, g.Allow("default", "shared", &other), "a pin of another Function")
	stale := pin
	stale.FunctionUID = "earlier"
	require.False(t, g.Allow("default", "shared", &stale), "a Revision of another incarnation")
	drift := pin
	drift.ImageDigest = "sha256:" + "f123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	require.False(t, g.Allow("default", "shared", &drift), "a Revision whose digest differs from the pin")
	gone := pin
	gone.Revision = "shared-2"
	require.False(t, g.Allow("default", "shared", &gone), "a Revision that is gone")
}
