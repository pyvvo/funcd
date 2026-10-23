package funcd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/volume"
)

// volumeAPI posts Functions and Apps, each with one local volume state, to a platform's control plane; the platform
// runs no pass, so a booking stays unbacked.
type volumeAPI struct {
	t   *testing.T
	p   *Platform
	srv *httptest.Server
}

func newVolumeAPI(t *testing.T, o volume.Options) volumeAPI {
	t.Helper()
	o.Root = t.TempDir()
	p, err := New(InMemory(), WithLogger(slog.New(slog.DiscardHandler)), WithFunctionVolumes(o, false))
	require.NoError(t, err)
	srv := httptest.NewServer(p.httpServer.Handler)
	t.Cleanup(srv.Close)
	return volumeAPI{t: t, p: p, srv: srv}
}

func stateVolumeSpec(size string) v1.FunctionSpec {
	return v1.FunctionSpec{Runtime: "nodejs22", Handler: "app.handler", Image: "oci://example/app:v1",
		Volumes: []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, Size: v1.ByteSize(size)}}}
}

func (a volumeAPI) post(kind string, obj v1.Object, dryRun bool) (int, string) {
	a.t.Helper()
	body, err := json.Marshal(obj)
	require.NoError(a.t, err)
	url := a.srv.URL + "/apis/funcd.io/v1alpha1/namespaces/default/" + kind
	if dryRun {
		url += "?dryRun=true"
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	require.NoError(a.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+DevToken)
	resp, err := a.srv.Client().Do(req)
	require.NoError(a.t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(a.t, err)
	return resp.StatusCode, string(out)
}

func (a volumeAPI) function(name, size string, dryRun bool) (int, string) {
	a.t.Helper()
	return a.post("functions", &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       stateVolumeSpec(size),
	}, dryRun)
}

// TestIssue1205_DryRunBooksNoVolume: a dry run (ADR-0220) answers what the real write would and books nothing, so a
// real apply right after it, whatever size it sets, finds the capacity the dry run found (#1205).
func TestIssue1205_DryRunBooksNoVolume(t *testing.T) {
	t.Parallel()
	api := newVolumeAPI(t, volume.Options{CapacityBytes: 4 << 30})

	// an App's parts pass the same admission: its dry run books none of its Functions' volumes
	code, out := api.post("apps", &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "shop", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.AppSpec{Functions: []v1.AppFunction{{Name: "app-cart", FunctionSpec: stateVolumeSpec("1GiB")}}},
	}, true)
	require.Less(t, code, 300, "dry run of an App with a 1GiB volume: %s", out)
	code, out = api.function("app-cart", "2GiB", false)
	require.Less(t, code, 300, "app-cart applied with 2GiB right after the App's dry run with 1GiB: %s", out)

	code, out = api.function("cart", "1GiB", true)
	require.Less(t, code, 300, "dry run of cart with 1GiB: %s", out)
	code, out = api.function("cart", "1GiB", false)
	require.Less(t, code, 300, "cart applied with 1GiB: %s", out)
	code, out = api.function("big", "1GiB", true)
	require.Less(t, code, 300, "dry run of big with 1GiB beside 3GiB booked: %s", out)
	code, out = api.function("other", "1GiB", false)
	require.Less(t, code, 300, "other applied with 1GiB right after big's dry run: %s", out)
	code, out = api.function("late", "1GiB", true)
	require.Equal(t, http.StatusConflict, code, "a dry run answers the refusal of the real write: %s", out)
	require.Contains(t, out, "VolumeCapacity")
}

// TestIssue1208_AppliedVolumeKeepsItsBooking: an applied Function's local volume keeps the space its apply booked until
// a pass reserves it, however long its passes wait; a booking no stored Function declares, an apply refused after its
// admission, still lapses after BookingTTL (#1208).
func TestIssue1208_AppliedVolumeKeepsItsBooking(t *testing.T) {
	t.Parallel()
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	api := newVolumeAPI(t, volume.Options{CapacityBytes: 4 << 20, Clock: func() time.Time { return time.Unix(0, now.Load()) }})
	code, out := api.function("cart", "3Mi", false)
	require.Less(t, code, 300, "cart applied with a 3Mi volume: %s", out)
	require.NoError(t, api.p.volumes.Book(context.Background(), volume.ID{Namespace: "default", Function: "refused", Name: "state"}, 1<<20))
	now.Add(int64(2 * time.Minute)) // past BookingTTL, and no pass reserved cart's volume

	code, out = api.function("thumbs", "3Mi", false)
	require.Equal(t, http.StatusConflict, code, "thumbs took the space cart's apply booked: %s", out)
	require.Contains(t, out, "VolumeCapacity")
	code, out = api.function("small", "1Mi", false)
	require.Less(t, code, 300, "the space of the refused apply's booking is free again: %s", out)
}
