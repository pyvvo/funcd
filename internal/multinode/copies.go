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
