// Package revhold reads which step Function revisions open workflow runs hold (ADR-0190 Decision 7): the
// status.pins of every WorkflowRun in a namespace that is not terminal and not cancelled. It is read from the
// store at each retire or delete decision, never kept in a shared map, so the Function reconciler, the workflow
// materializer and the owner garbage collector each read the same truth.
package revhold

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// Holds maps a Function name to its held Revision names, each with the UID of the Function the pin names.
type Holds map[v1.ObjectName]map[v1.ObjectName]v1.UID

// Held lists the WorkflowRuns in ns once and returns the revisions their pins hold.
func Held(ctx context.Context, st store.Store, ns v1.NamespaceName) (Holds, error) {
	res, err := st.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "revhold.Held", "list workflow runs in %q", ns)
	}
	held := Holds{}
	for _, obj := range res.Items {
		run, ok := obj.(*v1.WorkflowRun)
		if !ok || !holding(run) {
			continue
		}
		for _, p := range run.Status.Pins {
			if held[p.Function] == nil {
				held[p.Function] = map[v1.ObjectName]v1.UID{}
			}
			held[p.Function][p.Revision] = p.FunctionUID
		}
	}
	return held, nil
}

// Revision reports whether revision rev of the Function fn with uid is held.
func (h Holds) Revision(fn v1.ObjectName, uid v1.UID, rev v1.ObjectName) bool {
	held, ok := h[fn][rev]
	return ok && held == uid
}

// Function reports whether any revision of the Function fn with uid is held.
func (h Holds) Function(fn v1.ObjectName, uid v1.UID) bool {
	for _, held := range h[fn] {
		if held == uid {
			return true
		}
	}
	return false
}

// holding reports whether run's pins hold: it is neither terminal nor cancelled (Decision 10).
func holding(run *v1.WorkflowRun) bool {
	if run.Spec.Cancel {
		return false
	}
	switch run.Status.Phase {
	case v1.RunSucceeded, v1.RunFailed, v1.RunCancelled:
		return false
	}
	return true
}
