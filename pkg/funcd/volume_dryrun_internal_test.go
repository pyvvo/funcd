package funcd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/volume"
)

// TestIssue1205_DryRunBooksNoVolume: a dry run (ADR-0220) answers what the real write would and books nothing, so a
// real apply right after it, whatever size it sets, finds the capacity the dry run found (#1205).
func TestIssue1205_DryRunBooksNoVolume(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory(), WithLogger(slog.New(slog.DiscardHandler)),
		WithFunctionVolumes(volume.Options{Root: t.TempDir(), CapacityBytes: 4 << 30}, false))
	require.NoError(t, err)
	srv := httptest.NewServer(p.httpServer.Handler)
	t.Cleanup(srv.Close)
	spec := func(size string) v1.FunctionSpec {
		return v1.FunctionSpec{Runtime: "nodejs22", Handler: "app.handler", Image: "oci://example/app:v1",
			Volumes: []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, Size: v1.ByteSize(size)}}}
	}
	post := func(kind string, obj v1.Object, dryRun bool) (int, string) {
		t.Helper()
		body, err := json.Marshal(obj)
		require.NoError(t, err)
		url := srv.URL + "/apis/funcd.io/v1alpha1/namespaces/default/" + kind
		if dryRun {
			url += "?dryRun=true"
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+DevToken)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		out, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(out)
	}
	apply := func(name, size string, dryRun bool) (int, string) {
		t.Helper()
		return post("functions", &v1.Function{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
			ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
			Spec:       spec(size),
		}, dryRun)
	}

	// an App's parts pass the same admission: its dry run books none of its Functions' volumes
	code, out := post("apps", &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "shop", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.AppSpec{Functions: []v1.AppFunction{{Name: "app-cart", FunctionSpec: spec("1GiB")}}},
	}, true)
	require.Less(t, code, 300, "dry run of an App with a 1GiB volume: %s", out)
	code, out = apply("app-cart", "2GiB", false)
	require.Less(t, code, 300, "app-cart applied with 2GiB right after the App's dry run with 1GiB: %s", out)

	code, out = apply("cart", "1GiB", true)
	require.Less(t, code, 300, "dry run of cart with 1GiB: %s", out)
	code, out = apply("cart", "1GiB", false)
	require.Less(t, code, 300, "cart applied with 1GiB: %s", out)
	code, out = apply("big", "1GiB", true)
	require.Less(t, code, 300, "dry run of big with 1GiB beside 3GiB booked: %s", out)
	code, out = apply("other", "1GiB", false)
	require.Less(t, code, 300, "other applied with 1GiB right after big's dry run: %s", out)
	code, out = apply("late", "1GiB", true)
	require.Equal(t, http.StatusConflict, code, "a dry run answers the refusal of the real write: %s", out)
	require.Contains(t, out, "VolumeCapacity")
}
