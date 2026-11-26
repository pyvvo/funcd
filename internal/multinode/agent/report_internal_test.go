package agent

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// deletes is a node API client that records the deletes it is asked, and takes them while the control plane is there.
type deletes struct {
	nodeapi.Client
	away  bool
	asked []nodeapi.ObjectRef
}

func (d *deletes) DeleteObject(_ context.Context, ref nodeapi.ObjectRef) error {
	d.asked = append(d.asked, ref)
	if d.away {
		return fault.Unavailablef("test", "the control plane is away")
	}
	return nil
}

// A delete that the control plane did not take is sent again, until it does.
func TestADeleteTheControlPlaneDidNotTakeIsSentAgain(t *testing.T) {
	ctx := context.Background()
	cp := &deletes{away: true}
	a := &Agent{cfg: Config{Store: store.New(memory.New())},
		log: slog.New(slog.DiscardHandler), mine: map[v1.NamespaceName]bool{"e1": true}, pushed: map[string]string{}, unsent: map[string]nodeapi.ObjectRef{}}
	a.id.Store(&identity{name: "vm1", client: cp})
	run := &v1.JobRun{}
	run.Name, run.Namespace, run.UID = "report-1", "e1", "u1"
	a.push(ctx, store.Deleted, run)
	require.Len(t, cp.asked, 1)
	a.resendDeletes(ctx)
	require.Len(t, a.unsent, 1, "still away")
	cp.away = false
	a.resendDeletes(ctx)
	a.resendDeletes(ctx)
	require.Len(t, cp.asked, 3, "once it was taken, it is not sent again")
	want := nodeapi.ObjectRef{Kind: v1.KindJobRun, Namespace: "e1", Name: "report-1", UID: "u1"}
	require.Equal(t, []nodeapi.ObjectRef{want, want, want}, cp.asked)
}
