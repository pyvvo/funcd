package function

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func nameHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:8]
}

// ADR-0172 Decision 4: <fn>-<gen> while it fits 63, else the cut name, 8 hex digits of SHA-256 of the name and the
// generation, exactly 63.
func TestRevisionName(t *testing.T) {
	t.Parallel()
	n61, n62, n63 := strings.Repeat("a", 61), strings.Repeat("b", 62), strings.Repeat("c", 63)
	for _, c := range []struct {
		name string
		gen  int64
		want string
	}{
		{"greeter", 1, "greeter-1"},
		{n61, 1, n61 + "-1"},
		{n61, 9, n61 + "-9"},
		{n61, 10, n61[:51] + "-" + nameHash(n61) + "-10"},
		{n62, 1, n62[:52] + "-" + nameHash(n62) + "-1"},
		{n63, 1, n63[:52] + "-" + nameHash(n63) + "-1"},
		{n63, math.MaxInt64, n63[:34] + "-" + nameHash(n63) + "-9223372036854775807"},
	} {
		fn := &v1.Function{}
		fn.Name, fn.Generation = v1.ObjectName(c.name), c.gen
		got := revisionName(fn)
		require.Equal(t, c.want, got, "%d characters at generation %d", len(c.name), c.gen)
		require.LessOrEqual(t, len(got), maxRevisionName)
		require.NoError(t, v1.ObjectName(got).Validate())
		require.Equal(t, got, revisionName(fn), "stable")
	}
}

// ADR-0172 Decision 5: whose a stored Revision is.
func TestRevisionOf(t *testing.T) {
	t.Parallel()
	fn := &v1.Function{}
	fn.Name, fn.UID = "greeter", "uid-new"
	self := v1.ObjectRef{Kind: v1.KindFunction, Namespace: "default", Name: "greeter"}
	other := v1.ObjectRef{Kind: v1.KindFunction, Namespace: "default", Name: "other"}
	owner := func(ref v1.ObjectRef, uid v1.UID, controller bool) []v1.OwnerReference {
		return []v1.OwnerReference{{ObjectRef: ref, UID: uid, Controller: controller}}
	}
	for _, c := range []struct {
		what            string
		refs            []v1.OwnerReference
		spec            v1.ObjectRef
		current, served string
		want            revisionOwner
	}{
		{"controller ref of this UID", owner(self, "uid-new", true), self, "", "", revSelf},
		{"controller ref of another UID of this name", owner(self, "uid-old", true), self, "greeter-1", "", revNamesake},
		{"controller ref of another Function", owner(other, "uid-o", true), self, "greeter-1", "", revOther},
		{"controller ref of another kind", owner(v1.ObjectRef{Kind: v1.KindResourceGroup, Name: "greeter"}, "uid-new", true), self, "", "", revOther},
		{"ref-less, spec.function another Function", nil, other, "greeter-1", "", revOther},
		{"ref-less, named by currentRevision", nil, self, "greeter-1", "", revSelf},
		{"ref-less, named by servingRevision", nil, self, "", "greeter-1", revSelf},
		{"ref-less, unnamed by the status", nil, self, "greeter-2", "greeter-2", revNamesake},
		{"a non-controller ref only, named by the status", owner(self, "uid-old", false), self, "greeter-1", "", revSelf},
	} {
		rev := &v1.Revision{}
		rev.Name, rev.OwnerReferences, rev.Spec.Function = "greeter-1", c.refs, c.spec
		f := *fn
		f.Status.CurrentRevision, f.Status.ServingRevision = c.current, c.served
		require.Equal(t, c.want, revisionOf(rev, &f), c.what)
	}
}

// refLessRevision stores greeter-1 as a stamp before 2dc9d26 left it: no ownerRef.
func refLessRevision(t *testing.T, st store.Store) *v1.Function {
	t.Helper()
	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "greeter", "default", "rg1"
	created, err := st.Create(ctx, fn)
	require.NoError(t, err)
	obj, _ = v1.NewObject(v1.KindRevision)
	rev := obj.(*v1.Revision)
	rev.Name, rev.Namespace, rev.ResourceGroup = "greeter-1", "default", "rg1"
	rev.Spec = v1.RevisionSpec{Function: v1.ObjectRef{Kind: v1.KindFunction, Namespace: "default", Name: "greeter"}, Number: 1, ImageDigest: "sha256:A"}
	_, err = st.Create(ctx, rev)
	require.NoError(t, err)
	fn = created.(*v1.Function)
	fn.Status.ServingRevision = "greeter-1"
	return fn
}

// Adopting a ref-less Revision of this Function writes it the controller ownerRef (ADR-0172 Decision 5).
func TestAdoptionWritesOwnerRef(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	fn := refLessRevision(t, st)
	digest, err := (&Reconciler{store: st}).ensureRevision(context.Background(), fn)
	require.NoError(t, err)
	require.Equal(t, "sha256:A", digest)
	require.Equal(t, "greeter-1", fn.Status.CurrentRevision)
	obj, err := st.Get(context.Background(), v1.KindRevision.GVK(), "default", "greeter-1")
	require.NoError(t, err)
	require.True(t, v1.ControlledBy(obj.(*v1.Revision).OwnerReferences, v1.KindFunction, fn.UID))
}

// An adoption whose Update fails returns the error before currentRevision is set (ADR-0172 Decision 5).
func TestAdoptionUpdateErrorReturned(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	fn := refLessRevision(t, st)
	refusing := RefusingStore{Store: st, UpdateErr: fault.Conflictf("test", "the Revision changed")}
	_, err := (&Reconciler{store: refusing}).ensureRevision(context.Background(), fn)
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.Empty(t, fn.Status.CurrentRevision)
	obj, err := st.Get(context.Background(), v1.KindRevision.GVK(), "default", "greeter-1")
	require.NoError(t, err)
	require.Empty(t, obj.(*v1.Revision).OwnerReferences)
}
