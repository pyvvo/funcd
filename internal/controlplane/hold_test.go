package controlplane_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// holdPlatform is a HoldPlatform whose Advance fails while failAdvance is set.
type holdPlatform struct {
	advanced    []string
	failAdvance bool
	returned    []hold.QuarantineReturn
}

func (*holdPlatform) Counts(context.Context) (map[v1.Kind]int, error) { return map[v1.Kind]int{}, nil }
func (*holdPlatform) Orphans(context.Context) ([]string, error) {
	return []string{"default/gone/"}, nil
}
func (*holdPlatform) BucketOrphans(context.Context) ([]v1.ObjectRef, error) { return nil, nil }
func (*holdPlatform) SQLOrphans(context.Context) ([]v1.ObjectRef, error) {
	return []v1.ObjectRef{{Kind: v1.KindSQLDatabase, Namespace: "default", Name: "gone-db"}}, nil
}
func (*holdPlatform) VolumeOrphans(context.Context) ([]v1.ObjectRef, error) {
	return []v1.ObjectRef{{Kind: v1.KindFunction, Namespace: "default", Name: "gone/state"}}, nil
}
func (*holdPlatform) Quarantine(context.Context) ([]hold.QuarantineEntry, error) {
	return []hold.QuarantineEntry{{Kind: v1.KindBucket, Namespace: "default", Name: "gone-bucket", State: "kept"}}, nil
}
func (p *holdPlatform) ReturnQuarantined(_ context.Context, r hold.QuarantineReturn) error {
	p.returned = append(p.returned, r)
	return nil
}

func (p *holdPlatform) Advance(_ context.Context, ns v1.NamespaceName, source v1.ObjectName) error {
	if p.failAdvance {
		return fault.Unavailablef("test", "the bucket is down")
	}
	p.advanced = append(p.advanced, string(ns)+"/"+string(source))
	return nil
}

func (*holdPlatform) Pending(context.Context, v1.NamespaceName, v1.ObjectName) (map[string]int, error) {
	return map[string]int{"new": 2}, nil
}

func releaseBody(t *testing.T, advance ...string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string][]string{"advance": advance})
	require.NoError(t, err)
	return b
}

// TestReleaseRefuses: a developer is Forbidden; an --advance naming no blob event of an existing EventSource is
// Invalid; a platform not held is a Conflict; each before any change. A failed Advance keeps the marker, and a rerun
// completes the release.
func TestReleaseRefuses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := store.New(memory.New())
	for _, es := range []*v1.EventSource{
		{ObjectMeta: v1.ObjectMeta{Name: "files"}, Spec: v1.EventSourceSpec{Blob: &v1.BlobSource{Bucket: "inbox", Events: []v1.BlobEvent{{Name: "new"}}}}},
		{ObjectMeta: v1.ObjectMeta{Name: "tick"}, Spec: v1.EventSourceSpec{Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(time.Second)}}}}},
	} {
		es.TypeMeta = v1.TypeMeta{APIVersion: v1.KindEventSource.GVK().APIVersion(), Kind: v1.KindEventSource}
		es.Namespace, es.ResourceGroup = "team-a", "rg"
		_, err := st.Create(ctx, es)
		require.NoError(t, err)
	}
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	p := &holdPlatform{}
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken:   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
			adminToken: {Subject: "ops", Role: auth.RoleAdmin},
		}),
		Hold: controlplane.NewHoldService(controlplane.HoldDeps{Hold: h, Store: st, Platform: p}),
	})
	require.NoError(t, err)
	const release = "/apis/funcd.io/v1alpha1/hold/release"
	marker := filepath.Join(dir, hold.MarkerFile)

	require.Equal(t, http.StatusForbidden, do(t, srv, http.MethodGet, "/apis/funcd.io/v1alpha1/hold", devToken, nil).Code)
	require.Equal(t, http.StatusForbidden, do(t, srv, http.MethodPost, release, devToken, releaseBody(t)).Code)
	for _, bad := range []string{"team-a/tick", "team-a/absent", "nonsense", "team-a/files/x"} {
		rec := do(t, srv, http.MethodPost, release, adminToken, releaseBody(t, "team-a/files", bad))
		require.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", bad, rec.Body)
	}
	require.FileExists(t, marker)
	require.Empty(t, p.advanced, "a refused release advanced a source")

	rec := do(t, srv, http.MethodGet, "/apis/funcd.io/v1alpha1/hold", adminToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var ev hold.Evidence
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ev))
	require.True(t, ev.Held)
	require.Equal(t, map[string]int{"new": 2}, ev.Pending["team-a/files"])
	require.Equal(t, []string{"default/gone/"}, ev.Orphans)
	require.Equal(t, []string{"default/gone-db"}, ev.SQLOrphans, "ADR-0232: a held database directory with no object")
	require.Equal(t, []string{"default/gone/state"}, ev.VolumeOrphans, "ADR-0262: a held local volume with no Function")
	require.Equal(t, []hold.QuarantineEntry{{Kind: v1.KindBucket, Namespace: "default", Name: "gone-bucket", State: "kept"}},
		ev.Quarantine, "ADR-0288: the quarantine, held or not")

	p.failAdvance = true
	require.Equal(t, http.StatusServiceUnavailable, do(t, srv, http.MethodPost, release, adminToken, releaseBody(t, "team-a/files")).Code)
	require.FileExists(t, marker, "a failed Advance keeps the marker")
	require.True(t, h.Held())
	p.failAdvance = false
	rec = do(t, srv, http.MethodPost, release, adminToken, releaseBody(t, "team-a/files"))
	require.Less(t, rec.Code, 300, rec.Body.String())
	require.NoFileExists(t, marker)
	require.False(t, h.Held())
	require.Equal(t, []string{"team-a/files"}, p.advanced)
	require.False(t, h.ReleasedAt().IsZero())

	rec = do(t, srv, http.MethodPost, release, adminToken, releaseBody(t))
	require.Equal(t, http.StatusConflict, rec.Code, "not held")
}

// TestQuarantineReturnRoute (ADR-0288 Decision 6): POST …/quarantine/return reaches the platform for an admin, with
// the request as sent, and is Forbidden for a developer.
func TestQuarantineReturnRoute(t *testing.T) {
	st := store.New(memory.New())
	p := &holdPlatform{}
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken:   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
			adminToken: {Subject: "ops", Role: auth.RoleAdmin},
		}),
		Hold: controlplane.NewHoldService(controlplane.HoldDeps{Store: st, Platform: p}),
	})
	require.NoError(t, err)
	const path = "/apis/funcd.io/v1alpha1/quarantine/return"
	body, err := json.Marshal(hold.QuarantineReturn{Kind: v1.KindBucket, Namespace: "team-a", Name: "uploads"})
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, do(t, srv, http.MethodPost, path, devToken, body).Code)
	require.Empty(t, p.returned)
	rec := do(t, srv, http.MethodPost, path, adminToken, body)
	require.Less(t, rec.Code, 300, rec.Body.String())
	require.Equal(t, []hold.QuarantineReturn{{Kind: v1.KindBucket, Namespace: "team-a", Name: "uploads"}}, p.returned)
}

// TestReleaseNamespaceWaitsForRestore (ADR-0222 Decision 8): hold status lists a namespace's markers, the restore
// marker with its Restore's phase; a release of the namespace while that in-place Restore runs is a Conflict naming
// it, with both markers kept; once the Restore has failed the release lifts both.
func TestReleaseNamespaceWaitsForRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	rs := restoreOf("rewind", v1.RestoreMember{Kind: v1.KindKVStore, Name: "state", InPlace: true})
	rs.Status.Phase = v1.RunRunning
	created, err := st.Create(ctx, rs)
	require.NoError(t, err)
	h, err := hold.Open(t.TempDir())
	require.NoError(t, err)
	since := v1.NewTimestamp(time.Now())
	require.NoError(t, h.HoldNamespace("orders", hold.Marker{Reason: hold.ReasonData, Since: since}))
	require.NoError(t, h.HoldNamespace("orders", hold.Marker{Reason: hold.ReasonRestore, Since: since, Restore: "rewind"}))
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{adminToken: {Subject: "ops", Role: auth.RoleAdmin}}),
		Hold:        controlplane.NewHoldService(controlplane.HoldDeps{Hold: h, Store: st, Platform: &holdPlatform{}}),
	})
	require.NoError(t, err)
	body := []byte(`{"namespaces":["orders"]}`)
	const release = "/apis/funcd.io/v1alpha1/hold/release"

	rec := do(t, srv, http.MethodGet, "/apis/funcd.io/v1alpha1/hold", adminToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var ev hold.Evidence
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ev))
	require.Len(t, ev.Namespaces, 2)
	require.Equal(t, hold.ReasonRestore, ev.Namespaces[1].Reason)
	require.Equal(t, v1.ObjectName("rewind"), ev.Namespaces[1].Restore)
	require.Equal(t, v1.RunRunning, ev.Namespaces[1].Phase)

	rec = do(t, srv, http.MethodPost, release, adminToken, body)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Restore rewind")
	require.Len(t, h.NamespaceMarkers()["orders"], 2, "a refused release keeps both markers")
	require.True(t, h.HeldIn("orders"))

	rs = created.(*v1.Restore)
	rs.Status.Phase = v1.RunFailed
	_, err = st.Update(ctx, rs)
	require.NoError(t, err)
	rec = do(t, srv, http.MethodPost, release, adminToken, body)
	require.Less(t, rec.Code, 300, rec.Body.String())
	require.False(t, h.HeldIn("orders"))
	require.Equal(t, http.StatusConflict, do(t, srv, http.MethodPost, release, adminToken, body).Code, "not held")
}

// restoreOf is Restore orders/<name> of Backup b1 loading m.
func restoreOf(name v1.ObjectName, m v1.RestoreMember) *v1.Restore {
	return &v1.Restore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRestore.GVK().APIVersion(), Kind: v1.KindRestore},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "orders", ResourceGroup: "rg"},
		Spec: v1.RestoreSpec{Backup: "b1", Source: v1.RestoreSource{CredentialsSecret: "reader", IdentitySecret: "key"},
			Members: []v1.RestoreMember{m}},
	}
}

// TestRestoreInPlaceOnePerNamespace (ADR-0222 Decision 7): while an in-place Restore of a namespace has not ended, a
// second in-place one is a Conflict naming it, and a Restore into a new name is admitted; once the first has failed,
// the second is admitted.
func TestRestoreInPlaceOnePerNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	authz := rbac.New()
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: authz,
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{adminToken: {Subject: "ops", Role: auth.RoleAdmin}}),
		Admissions:  []admission.Admission{admission.NewRestoreInPlaceAdmission(authz, raceReader{st}, nil)},
	})
	require.NoError(t, err)
	const restores = "/apis/funcd.io/v1alpha1/namespaces/orders/restores"
	create := func(name v1.ObjectName, m v1.RestoreMember) *httptest.ResponseRecorder {
		body, err := json.Marshal(restoreOf(name, m))
		require.NoError(t, err)
		return do(t, srv, http.MethodPost, restores, adminToken, body)
	}
	inPlace := v1.RestoreMember{Kind: v1.KindKVStore, Name: "state", InPlace: true}

	rec := create("first", inPlace)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = create("second", inPlace)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Restore first")
	rec = create("copy", v1.RestoreMember{Kind: v1.KindKVStore, Name: "state", As: "state-r"})
	require.Equal(t, http.StatusOK, rec.Code, "a Restore into a new name is unaffected: %s", rec.Body)

	obj, err := st.Get(ctx, v1.KindRestore.GVK(), "orders", "first")
	require.NoError(t, err)
	first := obj.(*v1.Restore)
	first.Status.Phase = v1.RunFailed
	_, err = st.Update(ctx, first)
	require.NoError(t, err)
	rec = create("second", inPlace)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestRestoreInPlaceDeleteWaitsForEnd (ADR-0222 Decision 7): an in-place Restore that is Running cannot be deleted, a
// Conflict naming it; a Restore into a new name deletes; once the in-place one has failed it deletes.
func TestRestoreInPlaceDeleteWaitsForEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	authz := rbac.New()
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: authz,
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{adminToken: {Subject: "ops", Role: auth.RoleAdmin}}),
		Admissions:  []admission.Admission{admission.NewRestoreInPlaceAdmission(authz, raceReader{st}, nil)},
	})
	require.NoError(t, err)
	const restores = "/apis/funcd.io/v1alpha1/namespaces/orders/restores"
	setPhase := func(name v1.ObjectName, phase v1.RunPhase) {
		obj, err := st.Get(ctx, v1.KindRestore.GVK(), "orders", name)
		require.NoError(t, err)
		rs := obj.(*v1.Restore)
		rs.Status.Phase = phase
		_, err = st.Update(ctx, rs)
		require.NoError(t, err)
	}
	for name, m := range map[v1.ObjectName]v1.RestoreMember{
		"rewind": {Kind: v1.KindKVStore, Name: "state", InPlace: true},
		"copy":   {Kind: v1.KindKVStore, Name: "state", As: "state-r"},
	} {
		body, err := json.Marshal(restoreOf(name, m))
		require.NoError(t, err)
		rec := do(t, srv, http.MethodPost, restores, adminToken, body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		setPhase(name, v1.RunRunning)
	}

	rec := do(t, srv, http.MethodDelete, restores+"/rewind", adminToken, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Restore rewind")
	rec = do(t, srv, http.MethodDelete, restores+"/copy", adminToken, nil)
	require.Less(t, rec.Code, 300, "a Restore into a new name is unaffected: %s", rec.Body)

	setPhase("rewind", v1.RunFailed)
	rec = do(t, srv, http.MethodDelete, restores+"/rewind", adminToken, nil)
	require.Less(t, rec.Code, 300, rec.Body.String())
	_, err = st.Get(ctx, v1.KindRestore.GVK(), "orders", "rewind")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}
