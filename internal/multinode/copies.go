package multinode

import (
	"slices"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// NodeKinds are the cluster-scoped kinds a node follows, besides its Namespaces: the runtimes its workers run on, the API
// tokens that span one of its namespaces (decision 7), and the add-ons with the revisions their objects pin (ADR-0339).
func NodeKinds() []v1.Kind {
	return []v1.Kind{v1.KindRuntimeClass, v1.KindAPIToken, v1.KindAddon, v1.KindAddonRevision}
}

func nodeKind(k v1.Kind) bool { return slices.Contains(NodeKinds(), k) }

// KeepDecisions gives to, an object whose status a node reports, what the control plane records on that status itself,
// from from, its own copy: who started a JobRun and the UID of its Job (ADR-0347 Decision 3), and a person's decision on
// its preview (ADR-0348 Decisions 3 and 5). An approval is of one preview, so it goes with a node's preview of another
// digest. A node that merges a run it follows, and a control plane that takes a node's report, keep the same.
func KeepDecisions(to, from v1.Object) {
	keepEnd(to, from)
	run, ok := to.(*v1.JobRun)
	if !ok {
		return
	}
	rec, ok := from.(*v1.JobRun)
	if !ok {
		return
	}
	s, r := &run.Status, rec.Status
	if s.StartedBy == "" {
		s.StartedBy = r.StartedBy
	}
	if s.JobUID == "" {
		s.JobUID = r.JobUID
	}
	if s.RejectedBy == "" {
		s.RejectedBy = r.RejectedBy
	}
	if s.ApprovedAt.IsZero() && rec.Approved() && s.Preview != nil && s.Preview.Digest == r.Preview.Digest {
		s.ApprovedAt, s.ApprovedBy = r.ApprovedAt, r.ApprovedBy
	}
}

// keepEnd gives to the status of from, the control plane's record of it, once a run, a backup or a restore ended there in
// another phase than to's. Only a node restored to a point before that end reports one.
func keepEnd(to, from v1.Object) {
	switch t := to.(type) {
	case *v1.JobRun:
		if f, ok := from.(*v1.JobRun); ok && f.Status.Phase.Ended() && t.Status.Phase != f.Status.Phase {
			t.Status = f.Status
		}
	case *v1.WorkflowRun:
		if f, ok := from.(*v1.WorkflowRun); ok && ended(f.Status.Phase) && t.Status.Phase != f.Status.Phase {
			t.Status = f.Status
		}
	case *v1.Backup:
		if f, ok := from.(*v1.Backup); ok && ended(f.Status.Phase) && t.Status.Phase != f.Status.Phase {
			t.Status = f.Status
		}
	case *v1.Restore:
		if f, ok := from.(*v1.Restore); ok && ended(f.Status.Phase) && t.Status.Phase != f.Status.Phase {
			t.Status = f.Status
		}
	}
}

func ended(p v1.RunPhase) bool {
	return p == v1.RunSucceeded || p == v1.RunFailed || p == v1.RunCancelled
}

// KeepPause gives to, one side's copy of a WorkflowRun, the pause of from, the other side's, when from's spec is the later
// one by its generation: the restore of a node paused the run (ADR-0206 Decision 7), and the control plane takes it.
func KeepPause(to, from v1.Object) {
	t, ok := to.(*v1.WorkflowRun)
	if !ok {
		return
	}
	if f, ok := from.(*v1.WorkflowRun); ok && f.Spec.Paused && f.Generation > t.Generation && !ended(t.Status.Phase) {
		t.Spec.Paused, t.Generation = true, f.Generation
	}
}
