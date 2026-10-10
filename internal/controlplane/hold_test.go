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
