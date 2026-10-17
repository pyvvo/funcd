package owned_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/route/owned"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func site(t *testing.T, st store.Store, name v1.ObjectName) *v1.Site {
	t.Helper()
	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
	s.Name, s.Namespace, s.ResourceGroup = name, "staging", "rg1"
	s.Spec = v1.SiteSpec{Image: "oci-layout:///x:v1", Bucket: v1.SiteBucket{Name: "web"}, Prefix: "web"}
	obj, err := st.Create(context.Background(), s)
	require.NoError(t, err)
	return obj.(*v1.Site)
}

func spec(path string) v1.RouteSpec {
	return v1.RouteSpec{Rules: []v1.RouteRule{{Path: path, Backend: v1.RouteBackend{Function: "fn"}}}}
}

func TestFunctionRouteName(t *testing.T) {
	t.Parallel()
	require.Equal(t, v1.ObjectName("fn-fleet-api"), owned.FunctionRouteName("fleet-api"))
	long := v1.ObjectName(strings.Repeat("a", 63))
	got := owned.FunctionRouteName(long)
	require.Len(t, string(got), 63)
	require.True(t, strings.HasPrefix(string(got), "fn-aaa"))
	require.NotEqual(t, got, owned.FunctionRouteName(long[:62]+"b"), "the hash tells long names apart")
}

func TestEnsure_CreatesUpdatesAndStampsTheController(t *testing.T) {
	t.Parallel()
	ctx, st := context.Background(), store.New(memory.New())
	s := site(t, st, "fleet")
	rt, err := owned.Ensure(ctx, st, s, s.Name, spec("/a"))
	require.NoError(t, err)
	require.True(t, v1.ControlledBy(rt.OwnerReferences, v1.KindSite, s.UID))
	require.True(t, owned.IsOwned(rt))
	require.Equal(t, v1.ResourceGroupName("rg1"), rt.ResourceGroup)

	again, err := owned.Ensure(ctx, st, s, s.Name, spec("/a"))
	require.NoError(t, err)
	require.Equal(t, rt.ResourceVersion, again.ResourceVersion, "an unchanged spec writes nothing")
	moved, err := owned.Ensure(ctx, st, s, s.Name, spec("/b"))
	require.NoError(t, err)
	require.Equal(t, "/b", moved.Spec.Rules[0].Path)
}

// A Route of that name the owner does not control with its current UID is left untouched (first writer wins).
func TestEnsure_LeavesAForeignRoute(t *testing.T) {
	t.Parallel()
	ctx, st := context.Background(), store.New(memory.New())
	s := site(t, st, "fleet")
	foreign := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
	foreign.Name, foreign.Namespace, foreign.ResourceGroup = "fleet", "staging", "rg1"
	foreign.Spec = spec("/yours")
	_, err := st.Create(ctx, foreign)
	require.NoError(t, err)

	_, err = owned.Ensure(ctx, st, s, s.Name, spec("/a"))
	require.ErrorIs(t, err, owned.ErrNotOwned)
	require.NoError(t, owned.Remove(ctx, st, s, s.Name))
	obj, err := st.Get(ctx, v1.KindRoute.GVK(), "staging", "fleet")
	require.NoError(t, err)
	require.Equal(t, "/yours", obj.(*v1.Route).Spec.Rules[0].Path)
	require.False(t, owned.IsOwned(obj.(*v1.Route)))

	older := *s
	older.UID = "an-older-incarnation"
	_, err = owned.Ensure(ctx, st, &older, "other", spec("/a"))
	require.NoError(t, err)
	_, err = owned.Ensure(ctx, st, s, "other", spec("/a"))
	require.ErrorIs(t, err, owned.ErrNotOwned, "a Route an older incarnation controls is not this owner's")
}

func TestRemove_DeletesTheOwnedRoute(t *testing.T) {
	t.Parallel()
	ctx, st := context.Background(), store.New(memory.New())
	s := site(t, st, "fleet")
	_, err := owned.Ensure(ctx, st, s, s.Name, spec("/a"))
	require.NoError(t, err)
	require.NoError(t, owned.Remove(ctx, st, s, s.Name))
	_, err = st.Get(ctx, v1.KindRoute.GVK(), "staging", "fleet")
	require.Error(t, err)
	require.NoError(t, owned.Remove(ctx, st, s, s.Name), "removing an absent Route is a no-op")
}

func TestMapOwner(t *testing.T) {
	t.Parallel()
	ctx, st := context.Background(), store.New(memory.New())
	long := v1.ObjectName(strings.Repeat("f", 63))
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = long, "staging", "rg1"
	_, err := st.Create(ctx, fn)
	require.NoError(t, err)

	route := func(name v1.ObjectName, ctl *v1.OwnerReference) *v1.Route {
		rt := &v1.Route{}
		rt.Name, rt.Namespace = name, "staging"
		if ctl != nil {
			rt.OwnerReferences = []v1.OwnerReference{*ctl}
		}
		return rt
	}
	names := func(reqs []controller.Request) []v1.ObjectName {
		out := []v1.ObjectName{}
		for _, r := range reqs {
			out = append(out, r.Name)
		}
		return out
	}
	sites, fns := owned.MapOwner(st, v1.KindSite), owned.MapOwner(st, v1.KindFunction)
	ctl := &v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindSite, Name: "real"}, Controller: true}

	require.Equal(t, []v1.ObjectName{"real", "fleet"}, names(sites(ctx, route("fleet", ctl))), "by controller and by name")
	require.Equal(t, []v1.ObjectName{"fleet"}, names(sites(ctx, route("fleet", nil))), "a foreign Route re-runs a stuck owner")
	require.Equal(t, []v1.ObjectName{"fleet-api"}, names(fns(ctx, route("fn-fleet-api", nil))))
	require.Equal(t, []v1.ObjectName{long}, names(fns(ctx, route(owned.FunctionRouteName(long), nil))), "a shortened name maps through a List")
	require.Empty(t, fns(ctx, route("fleet", nil)))
}
