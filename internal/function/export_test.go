package function

import (
	"context"
	"encoding/json"
	"strings"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
)

// BootTimeout is how long the reconciler lets a replica run before readiness judges it never ready (ADR-0030 §4b).
const BootTimeout = defaultBootTimeout

// ErrRevisionStampFailed is the error a pass returns when its Revision cannot be stamped (ADR-0172).
var ErrRevisionStampFailed = errRevisionStampFailed

// RefusingStore is a store.Store whose Revision writes fail: Create with CreateErr and Update with UpdateErr, when set.
type RefusingStore struct {
	store.Store
	CreateErr, UpdateErr error
}

func (s RefusingStore) Create(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if s.CreateErr != nil && obj.GroupVersionKind().Kind == v1.KindRevision {
		return nil, s.CreateErr
	}
	return s.Store.Create(ctx, obj)
}

func (s RefusingStore) Update(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if s.UpdateErr != nil && obj.GroupVersionKind().Kind == v1.KindRevision {
		return nil, s.UpdateErr
	}
	return s.Store.Update(ctx, obj)
}

// PoolLiveLen is how many pool workers r holds a liveness time for.
func PoolLiveLen(r *Reconciler) int {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	n := 0
	for id := range r.live {
		if strings.Contains(string(id), "/"+poolInstancePrefix) {
			n++
		}
	}
	return n
}

// BootCount is the boot-crash count r holds for worker id (ADR-0160, ADR-0225).
func BootCount(r *Reconciler, id runtime.InstanceID) int {
	c, _ := r.boot.crash(id)
	return c.count
}

// RunAnswer is how a run's executor ended (ADR-0347 Decision 5).
type RunAnswer = runAnswer

// NewRunAnswer is an executor's end with phase, reason, message and the handler's result.
func NewRunAnswer(phase v1.JobRunPhase, reason, message string, result json.RawMessage) RunAnswer {
	return runAnswer{phase: phase, reason: reason, message: message, result: result}
}

// RunsWith is r's run reconciler with launch as the executor of its runs.
func RunsWith(r *Reconciler, cfg RunsConfig, launch func(ctx context.Context, run *v1.JobRun, rev *v1.Revision) RunAnswer) *JobRunReconciler {
	j := r.Runs(cfg)
	j.launch = func(ctx context.Context, run *v1.JobRun, _ *v1.Job, rev *v1.Revision) runAnswer {
		return launch(ctx, run, rev)
	}
	return j
}

// ReconcilerWithStore is a reconciler on deps with st as its store.
func ReconcilerWithStore(t interface{ Fatal(...interface{}) }, deps Deps, st store.Store) *Reconciler {
	deps.Store = st
	r, err := NewReconciler(deps)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
