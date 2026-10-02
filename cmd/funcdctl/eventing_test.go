package main

import (
	"context"
	"encoding/json"
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
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	dlmemory "github.com/pyvvo/funcd/internal/eventing/deadletter/memory"
	"github.com/pyvvo/funcd/internal/sensor"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

type failingInvoker struct{}

func (failingInvoker) Invoke(context.Context, v1.NamespaceName, v1.ObjectName, eventing.CloudEvent) error {
	return fault.Unavailablef("test.invoke", "target returned 500")
}

func seedReplayable(t *testing.T, st store.Store, dlq deadletter.Store, id string) {
	t.Helper()
	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindSensor)
	se := obj.(*v1.Sensor)
	se.Name, se.Namespace, se.ResourceGroup = "s", "team-a", "rg1"
	se.Spec.On = []v1.Dependency{{Name: "d", Source: "git", Event: "push"}}
	se.Spec.Do = []v1.Action{{Name: "notify", On: "d", Function: "mailer"}}
	_, err := st.Create(ctx, se)
	require.NoError(t, err)
	ce, err := eventing.NewNamedEvent("team-a", "git", "push")
	require.NoError(t, err)
	payload, err := json.Marshal(ce)
	require.NoError(t, err)
	require.NoError(t, dlq.Put(ctx, deadletter.DeadLetter{
		ID: id, Namespace: "team-a", Sensor: "s", Source: "git", Event: "push", Action: "notify",
		Payload: payload, Attempts: 3, Reason: "boom", FailedAt: time.Now().UTC(),
	}))
}

// The replay command reports a re-park only when the delivery attempt failed and the entry was re-parked;
// a missing entry or a deleted Sensor is NotFound with no re-park claim, and the entry is left as it was.
func TestIssue175_ReplayReportsReparkOnlyOnDeliveryFailure(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	dlq := dlmemory.New()
	r, err := sensor.NewReconciler(sensor.Deps{Store: st, Subscriber: eventing.NewFanout(), Invoker: failingInvoker{}, DeadLetters: dlq})
	require.NoError(t, err)
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(), Credentials: creds, DeadLetters: dlq, Replayer: r,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)
	seedReplayable(t, st, dlq, "01DL")

	err = execCLI(io.Discard, c, "eventing", "dlq", "replay", "01NOSUCHID", "-n", "team-a")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.NotContains(t, err.Error(), "re-parked", "an unknown id has no entry to re-park")

	err = execCLI(io.Discard, c, "eventing", "dlq", "replay", "01DL", "-n", "team-a")
	require.Error(t, err)
	require.Contains(t, err.Error(), "re-parked", "a failed delivery re-parks the entry")
	dl, err := dlq.Get(ctx, "team-a", "01DL")
	require.NoError(t, err)
	require.Equal(t, 0, dl.Attempts)

	cur, err := st.Get(ctx, v1.KindSensor.GVK(), "team-a", "s")
	require.NoError(t, err)
	require.NoError(t, st.Delete(ctx, v1.KindSensor.GVK(), "team-a", "s", cur.GetObjectMeta().ResourceVersion))
	err = execCLI(io.Discard, c, "eventing", "dlq", "replay", "01DL", "-n", "team-a")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.NotContains(t, err.Error(), "re-parked", "a deleted Sensor is not re-parked")
	after, err := dlq.Get(ctx, "team-a", "01DL")
	require.NoError(t, err)
	require.True(t, dl.FailedAt.Equal(after.FailedAt), "the entry is untouched")
}
