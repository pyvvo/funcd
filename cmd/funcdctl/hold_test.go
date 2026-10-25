package main

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
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
	"github.com/pyvvo/funcd/pkg/sdk"
)

// quietPlatform is a HoldPlatform with nothing to report.
type quietPlatform struct{}

func (quietPlatform) Counts(context.Context) (map[v1.Kind]int, error)            { return nil, nil }
func (quietPlatform) Orphans(context.Context) ([]string, error)                  { return nil, nil }
func (quietPlatform) BucketOrphans(context.Context) ([]v1.ObjectRef, error)      { return nil, nil }
func (quietPlatform) SQLOrphans(context.Context) ([]v1.ObjectRef, error)         { return nil, nil }
func (quietPlatform) VolumeOrphans(context.Context) ([]v1.ObjectRef, error)      { return nil, nil }
func (quietPlatform) Quarantine(context.Context) ([]hold.QuarantineEntry, error) { return nil, nil }
func (quietPlatform) ReturnQuarantined(context.Context, hold.QuarantineReturn) error {
	return nil
}
func (quietPlatform) Advance(context.Context, v1.NamespaceName, v1.ObjectName) error {
	return nil
}

func (quietPlatform) Pending(context.Context, v1.NamespaceName, v1.ObjectName) (map[string]int, error) {
	return nil, nil
}

// `funcdctl hold status` prints the evidence as YAML and `hold release` lifts the hold; a second release is a
// Conflict.
func TestHoldStatusAndRelease(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	st := store.New(memory.New())
	const admin = "admin-token"
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{admin: {Subject: "ops", Role: auth.RoleAdmin}}),
		Hold:        controlplane.NewHoldService(controlplane.HoldDeps{Hold: h, Store: st, Platform: quietPlatform{}}),
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	c, err := sdk.New(ts.URL, sdk.WithToken(admin))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "hold", "status"))
	require.Contains(t, out.String(), "held: true")
	require.Contains(t, out.String(), "reason: restore")
	out.Reset()
	require.NoError(t, execCLI(&out, c, "hold", "release"))
	require.Equal(t, "released\n", out.String())
	require.False(t, h.Held())
	require.Equal(t, fault.Conflict, fault.KindOf(execCLI(io.Discard, c, "hold", "release")))
}

// `funcdctl hold release --namespace` (ADR-0222 Decision 8) removes the data marker restore run left, by an admin: a
// developer of the namespace is Forbidden, a namespace without the marker a Conflict; the platform stays unheld.
func TestHoldReleaseNamespace(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, hold.MarkNamespaces(dir, []v1.NamespaceName{"orders"}, hold.Marker{Reason: hold.ReasonData}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	st := store.New(memory.New())
	const admin, dev = "admin-token", "dev-token"
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			admin: {Subject: "ops", Role: auth.RoleAdmin},
			dev:   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"orders"}},
		}),
		Hold: controlplane.NewHoldService(controlplane.HoldDeps{Hold: h, Store: st, Platform: quietPlatform{}}),
	})
	require.NoError(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	developer, err := sdk.New(ts.URL, sdk.WithToken(dev))
	require.NoError(t, err)
	require.Equal(t, fault.Forbidden, fault.KindOf(execCLI(io.Discard, developer, "hold", "release", "--namespace", "orders")))
	require.True(t, h.HeldIn("orders"))
	c, err := sdk.New(ts.URL, sdk.WithToken(admin))
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "hold", "release", "--namespace", "orders"))
	require.Equal(t, "released\n", out.String())
	require.False(t, h.HeldIn("orders"))
	require.False(t, h.ReleasedAtIn("orders").IsZero())
	require.Equal(t, fault.Conflict, fault.KindOf(execCLI(io.Discard, c, "hold", "release", "--namespace", "orders")))
	require.Error(t, execCLI(io.Discard, c, "hold", "release", "--namespace", "orders", "--advance", "orders/files"))
}
