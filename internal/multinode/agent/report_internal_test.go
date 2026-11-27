package agent

import (
	"context"
	"errors"
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
// oracle: docs/design/multinode/writeback.fizz#TheControlPlaneConverges
func TestADeleteTheControlPlaneDidNotTakeIsSentAgain(t *testing.T) {
	ctx := context.Background()
	cp := &deletes{away: true}
	a := &Agent{cfg: Config{Store: store.New(memory.New())},
		log: slog.New(slog.DiscardHandler), mine: map[v1.NamespaceName]bool{"e1": true}, pushed: map[string]string{}, unsent: map[string]nodeapi.ObjectRef{}}
	a.id.Store(&identity{name: "vm1", client: cp})
	run := &v1.JobRun{}
	run.Name, run.Namespace, run.UID = "report-1", "e1", "u1"
	require.NoError(t, a.push(ctx, store.Deleted, run), "a delete goes into the queue, not into an error")
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

// refusingPuts is a node API client whose control plane answers each whole write with an error of kind.
type refusingPuts struct {
	nodeapi.Client
	kind fault.Kind
}

func (r refusingPuts) PutObject(context.Context, nodeapi.ObjectWrite) error {
	return fault.Wrapf(errors.New("refused"), r.kind, "test", "the control plane answers %s", r.kind)
}

func TestAWriteBackTheControlPlaneRefusesLetsTheSnapshotGoOn(t *testing.T) {
	ctx := context.Background()
	st := store.New(memory.New())
	run := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun}}
	run.Name, run.Namespace, run.ResourceGroup, run.Spec.Job = "big", "e1", "rg1", "report"
	_, err := st.Create(ctx, run)
	require.NoError(t, err)
	agentOf := func(kind fault.Kind) *Agent {
		a := &Agent{cfg: Config{Store: st}, log: slog.New(slog.DiscardHandler), mine: map[v1.NamespaceName]bool{"e1": true},
			pushed: map[string]string{}, unsent: map[string]nodeapi.ObjectRef{}}
		a.id.Store(&identity{name: "vm1", client: refusingPuts{kind: kind}})
		return a
	}
	for _, kind := range []fault.Kind{fault.Invalid, fault.Forbidden, fault.Conflict, fault.PayloadTooLarge} {
		made, err := agentOf(kind).pushMade(ctx)
		require.NoError(t, err, "a run the control plane refuses (%s) lets the snapshot go on", kind)
		require.True(t, made[key(v1.KindJobRun, "e1", "big")])
	}
	_, err = agentOf(fault.Unavailable).pushMade(ctx)
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "one that did not reach it holds the snapshot back")
}
