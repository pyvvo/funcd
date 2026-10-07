package sdk_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario: zero-time-omitted — the body sdk.Apply sends for an object built without a creationTimestamp carries no
// creationTimestamp key and no zero instant.
func TestZeroTimeOmitted(t *testing.T) {
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(sent)
	}))
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL)
	require.NoError(t, err)
	_, err = c.Apply(context.Background(), newFunction("fn", "h"))
	require.NoError(t, err)
	require.NotEmpty(t, sent)
	require.NotContains(t, string(sent), "creationTimestamp")
	require.NotContains(t, string(sent), "0001-01-01")
}

// scenario: non-fixed-input-refused — sdk.DecodeManifest refuses a creationTimestamp that is not the ADR-0196 form and
// accepts the fixed form, quoted or not.
func TestNonFixedInputRefused(t *testing.T) {
	manifest := func(created string) []byte {
		return []byte(`apiVersion: funcd.io/v1alpha1
kind: ConfigMap
metadata:
  name: cfg
  namespace: team-a
  resourceGroup: rg1
  creationTimestamp: ` + created + "\n")
	}
	for _, bad := range []string{`2026-10-07T20:03:35Z`, `"2026-10-07T22:03:35.965+02:00"`, `2026-10-07T20:03:35.965123Z`} {
		_, err := sdk.DecodeManifest(manifest(bad))
		require.Error(t, err, bad)
		require.Equal(t, fault.Invalid, fault.KindOf(err), bad)
		require.ErrorContains(t, err, "exactly 3 fractional digits", bad)
	}
	for _, fixed := range []string{`2026-10-07T20:03:35.965Z`, `"2026-10-07T20:03:35.965Z"`} {
		obj, err := sdk.DecodeManifest(manifest(fixed))
		require.NoError(t, err, fixed)
		require.True(t, time.Time(obj.GetObjectMeta().CreationTime).Equal(time.Date(2026, 10, 7, 20, 3, 35, 965e6, time.UTC)), fixed)
	}
}
