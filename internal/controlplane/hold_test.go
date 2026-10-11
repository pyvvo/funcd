package controlplane_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// holdPlatform is a HoldPlatform whose Advance fails while failAdvance is set.
type holdPlatform struct {
	advanced    []string
	failAdvance bool
}

func (*holdPlatform) Counts(context.Context) (map[v1.Kind]int, error) { return map[v1.Kind]int{}, nil }
func (*holdPlatform) Orphans(context.Context) ([]string, error) {
	return []string{"default/gone/"}, nil
}
func (*holdPlatform) BucketOrphans(context.Context) ([]v1.ObjectRef, error) { return nil, nil }

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

// TestReleaseNamespaceWaitsForRestore (ADR-0222 Decision 8): hold status lists a namespace's markers, the restore
// marker with its Restore's phase; a release of the namespace while that in-place Restore runs is a Conflict naming
// it, with both markers kept; once the Restore has failed the release lifts both.
func TestReleaseNamespaceWaitsForRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	rs := &v1.Restore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRestore.GVK().APIVersion(), Kind: v1.KindRestore},
		ObjectMeta: v1.ObjectMeta{Name: "rewind", Namespace: "orders", ResourceGroup: "rg"},
		Spec: v1.RestoreSpec{Backup: "b1", Source: v1.RestoreSource{CredentialsSecret: "reader", IdentitySecret: "key"},
			Members: []v1.RestoreMember{{Kind: v1.KindKVStore, Name: "state", InPlace: true}}},
		Status: v1.RestoreStatus{Phase: v1.RunRunning},
	}
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
