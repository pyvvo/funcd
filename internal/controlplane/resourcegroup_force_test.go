package controlplane_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const groupNS v1.NamespaceName = "team-ns"

const viewerToken = "viewer-token"

// newGroupServer is the API with the link and ResourceGroup admissions; withCollector wires the real collector.
func newGroupServer(t *testing.T, withCollector bool) (http.Handler, store.Store) {
	t.Helper()
	st := store.New(memory.New())
	nss := []v1.NamespaceName{groupNS}
	for i := range racePairs {
		nss = append(nss, raceNS(i))
	}
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:    {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: nss},
		viewerToken: {Subject: "viewer", Role: auth.RoleViewer, Namespaces: nss},
	})
	r := raceReader{st}
	d := controlplane.Deps{
		Store: st, Authorizer: rbac.New(), Credentials: creds,
		Admissions: []admission.Admission{
			admission.NewLinkValidityAdmission(r),
			admission.NewLinkDeletionProtectionAdmission(r),
			admission.NewResourceGroupDeletionProtectionAdmission(r),
		},
	}
	if withCollector {
		col, err := gc.New(gc.Deps{Store: st})
		require.NoError(t, err)
		d.Collector = col
	}
	h, err := controlplane.NewServer(d)
	require.NoError(t, err)
	return h, st
}

func groupBody(t *testing.T, ns v1.NamespaceName, name string) []byte {
	t.Helper()
	b, err := json.Marshal(v1.ResourceGroup{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindResourceGroup.GVK().APIVersion(), Kind: v1.KindResourceGroup},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: ns, ResourceGroup: v1.ResourceGroupName(name)},
	})
	require.NoError(t, err)
	return b
}

func memberBody(t *testing.T, kind v1.Kind, ns v1.NamespaceName, name, group string) []byte {
	t.Helper()
	obj, _ := v1.NewObject(kind)
	m := obj.GetObjectMeta()
	m.Name, m.Namespace, m.ResourceGroup = v1.ObjectName(name), ns, v1.ResourceGroupName(group)
	if fn, ok := obj.(*v1.Function); ok {
		fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "app.handler", "oci://example/app:v1"
	}
	b, err := json.Marshal(obj)
	require.NoError(t, err)
	return b
}

func groupPath(ns v1.NamespaceName, name string, force bool) string {
	p := kindPath(ns, "resourcegroups") + "/" + name
	if force {
		p += "?force=true"
	}
	return p
}

func present(t *testing.T, st store.Store, kind v1.Kind, ns v1.NamespaceName, name string) bool {
	t.Helper()
	_, err := st.Get(context.Background(), kind.GVK(), ns, v1.ObjectName(name))
	if fault.KindOf(err) == fault.NotFound {
		return false
	}
	require.NoError(t, err)
	return true
}

// scenario: resourcegroup-nonempty-delete-refused — deleting team while it holds a ⇒ 409 naming Function/a.
func TestResourceGroupNonEmptyDeleteRefused(t *testing.T) {
	srv, st := newGroupServer(t, true)
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "functions"), memberBody(t, v1.KindFunction, groupNS, "a", "team")})

	rec := do(t, srv, http.MethodDelete, groupPath(groupNS, "team", false), devToken, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	_, detail := problemOf(t, rec)
	require.Contains(t, detail, "Function/a")
	require.Contains(t, detail, "1 member")
	require.True(t, present(t, st, v1.KindResourceGroup, groupNS, "team"))
	require.True(t, present(t, st, v1.KindFunction, groupNS, "a"))
}

func TestResourceGroupDeleteNamesFiveMembersAndTheCount(t *testing.T) {
	srv, _ := newGroupServer(t, true)
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	for i := range 7 {
		mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "configmaps"), memberBody(t, v1.KindConfigMap, groupNS, fmt.Sprintf("c%d", i), "team")})
	}
	rec := do(t, srv, http.MethodDelete, groupPath(groupNS, "team", false), devToken, nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	_, detail := problemOf(t, rec)
	require.Contains(t, detail, "7 member")
	require.Contains(t, detail, "and 2 more")
}

// A controlled child goes with its owner (ADR-0170 Decision 7): a Secret the Identity controls carries the group but
// is not a member, so the refusal names the Identity alone.
func TestResourceGroupControlledChildIsNotAMember(t *testing.T) {
	srv, st := newGroupServer(t, true)
	ctx := context.Background()
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	idObj, _ := v1.NewObject(v1.KindIdentity)
	id := idObj.(*v1.Identity)
	id.Name, id.Namespace, id.ResourceGroup, id.Spec.Type = "ext", groupNS, "team", v1.IdentityTypeExternal
	owner, err := st.Create(ctx, id)
	require.NoError(t, err)
	secObj, _ := v1.NewObject(v1.KindSecret)
	sec := secObj.(*v1.Secret)
	sec.Name, sec.Namespace, sec.ResourceGroup = "ext-cred", groupNS, "team"
	sec.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"k": []byte("v")}}
	sec.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindIdentity, Namespace: groupNS, Name: "ext"}, UID: owner.GetObjectMeta().UID, Controller: true}}
	_, err = st.Create(ctx, sec)
	require.NoError(t, err)

	rec := do(t, srv, http.MethodDelete, groupPath(groupNS, "team", false), devToken, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	_, detail := problemOf(t, rec)
	require.Contains(t, detail, "1 member")
	require.Contains(t, detail, "Identity/ext")
	require.NotContains(t, detail, "Secret/ext-cred")
}

func TestResourceGroupEmptyDeleteSucceeds(t *testing.T) {
	srv, st := newGroupServer(t, false)
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "configmaps"), memberBody(t, v1.KindConfigMap, groupNS, "other", "elsewhere")})
	mustDo(t, srv, call{http.MethodDelete, groupPath(groupNS, "team", false), nil})
	require.False(t, present(t, st, v1.KindResourceGroup, groupNS, "team"))
}

// scenario: resourcegroup-force-delete-cascades (control-plane half) — members, then their controlled children, then
// the group.
func TestResourceGroupForceDeleteCascades(t *testing.T) {
	srv, st := newGroupServer(t, true)
	ctx := context.Background()
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "functions"), memberBody(t, v1.KindFunction, groupNS, "a", "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "configmaps"), memberBody(t, v1.KindConfigMap, groupNS, "c", "team")})
	idObj, _ := v1.NewObject(v1.KindIdentity)
	id := idObj.(*v1.Identity)
	id.Name, id.Namespace, id.ResourceGroup, id.Spec.Type = "ext", groupNS, "team", v1.IdentityTypeExternal
	owner, err := st.Create(ctx, id)
	require.NoError(t, err)
	secObj, _ := v1.NewObject(v1.KindSecret)
	sec := secObj.(*v1.Secret)
	sec.Name, sec.Namespace, sec.ResourceGroup = "ext", groupNS, "team"
	sec.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: map[string][]byte{"k": []byte("v")}}
	sec.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindIdentity, Namespace: groupNS, Name: "ext"}, UID: owner.GetObjectMeta().UID, Controller: true}}
	_, err = st.Create(ctx, sec)
	require.NoError(t, err)

	mustDo(t, srv, call{http.MethodDelete, groupPath(groupNS, "team", true), nil})
	for _, o := range []struct {
		kind v1.Kind
		name string
	}{{v1.KindResourceGroup, "team"}, {v1.KindFunction, "a"}, {v1.KindConfigMap, "c"}, {v1.KindIdentity, "ext"}, {v1.KindSecret, "ext"}} {
		require.False(t, present(t, st, o.kind, groupNS, o.name), "%s/%s is gone", o.kind, o.name)
	}
}

// scenario: resourcegroup-force-stops-at-protected-member (control-plane half) — a protected member stops force with
// its 409; the member and the group remain.
func TestResourceGroupForceStopsAtProtectedMember(t *testing.T) {
	srv, st := newGroupServer(t, true)
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "functions"), memberBody(t, v1.KindFunction, groupNS, "b", "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "configmaps"), memberBody(t, v1.KindConfigMap, groupNS, "c", "team")})
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "functions"), linkedFunctionBody(t, groupNS, "a", "b")})

	rec := do(t, srv, http.MethodDelete, groupPath(groupNS, "team", true), devToken, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	_, detail := problemOf(t, rec)
	require.Contains(t, detail, `"b"`)
	require.True(t, present(t, st, v1.KindFunction, groupNS, "b"))
	require.True(t, present(t, st, v1.KindResourceGroup, groupNS, "team"))
	require.False(t, present(t, st, v1.KindConfigMap, groupNS, "c"), "an unprotected member is deleted; no rollback")
}

func TestResourceGroupForceChecksInOrder(t *testing.T) {
	srv, _ := newGroupServer(t, true)
	mustDo(t, srv, call{http.MethodPost, kindPath(groupNS, "resourcegroups"), groupBody(t, groupNS, "team")})
	require.Equal(t, http.StatusForbidden, do(t, srv, http.MethodDelete, groupPath(groupNS, "team", true), viewerToken, nil).Code)
	require.Equal(t, http.StatusNotFound, do(t, srv, http.MethodDelete, groupPath(groupNS, "missing", true), devToken, nil).Code)

	noCol, _ := newGroupServer(t, false)
	require.Equal(t, http.StatusServiceUnavailable, do(t, noCol, http.MethodDelete, groupPath(groupNS, "missing", true), devToken, nil).Code,
		"no collector wired is answered before the group lookup")
	require.Equal(t, http.StatusForbidden, do(t, noCol, http.MethodDelete, groupPath(groupNS, "team", true), viewerToken, nil).Code)
}

// A forced delete of a group holding Function bN races a PUT of aN linking to bN: whichever wins, no stored link
// names a missing Function, because each forced member delete runs under the namespace admission lock.
func TestResourceGroupForceRacesLinkWrites(t *testing.T) {
	srv, st := newGroupServer(t, true)
	pairs := make([][2]call, racePairs)
	for i := range racePairs {
		ns := raceNS(i)
		mustDo(t, srv, call{http.MethodPost, kindPath(ns, "resourcegroups"), groupBody(t, ns, "g")})
		mustDo(t, srv, call{http.MethodPost, kindPath(ns, "functions"), memberBody(t, v1.KindFunction, ns, "b", "g")})
		mustDo(t, srv, call{http.MethodPost, kindPath(ns, "functions"), linkedFunctionBody(t, ns, "a", "")})
		pairs[i] = [2]call{
			{http.MethodDelete, groupPath(ns, "g", true), nil},
			{http.MethodPut, kindPath(ns, "functions") + "/a", linkedFunctionBody(t, ns, "a", "b")},
		}
	}
	race(t, srv, pairs)
	for i := range racePairs {
		ns := raceNS(i)
		for from, targets := range linksOf(t, st, ns) {
			for _, to := range targets {
				require.True(t, present(t, st, v1.KindFunction, ns, string(to)), "%s: %s links to a missing %s", ns, from, to)
			}
		}
	}
}
