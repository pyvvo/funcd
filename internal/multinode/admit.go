package multinode

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/store"
)

// nodeStateAdmissions are the checks before a write that read the state of the node a namespace runs on (FEAT-0002
// F250): the deletion protection of a KVStore, a Bucket and a SQLDatabase, the booking of a Function's or a Job's volumes,
// and the in-place restore guard.
func nodeStateAdmissions() []string {
	return []string{"kvstore-deletion-protection", "bucket-deletion-protection", "sqldatabase-deletion-protection",
		"function-volumes", "restore-in-place"}
}

// needsNode reports whether the check named name reads the node's state for req: the volume booking only for a workload
// that declares volumes, and the restore guard only for an in-place Restore, so a write that needs nothing of the node
// goes through while the node is away.
func needsNode(name string, req admission.Request) bool {
	switch name {
	case "function-volumes":
		switch w := req.Object.(type) {
		case *v1.Function:
			return len(w.Spec.Volumes) > 0
		case *v1.Job:
			return len(w.Spec.Volumes) > 0
		}
		return false
	case "restore-in-place":
		obj := req.Object
		if req.Operation == admission.Delete {
			obj = req.Old
		}
		r, ok := obj.(*v1.Restore)
		return ok && r.InPlace()
	}
	return true
}

// NodeState is the node-state checks among as, which a node runs when the control plane asks it.
func NodeState(as []admission.Admission) []admission.Admission {
	var out []admission.Admission
	for _, a := range as {
		if slices.Contains(nodeStateAdmissions(), a.Name()) {
			out = append(out, a)
		}
	}
	return out
}

// AdmitRequest is an admission request as a node call carries it.
type AdmitRequest struct {
	Operation admission.Operation `json:"operation"`
	Kind      v1.Kind             `json:"kind"`
	Object    json.RawMessage     `json:"object,omitempty"`
	Old       json.RawMessage     `json:"old,omitempty"`
	Identity  auth.Identity       `json:"identity"`
	DryRun    bool                `json:"dryRun,omitempty"`
}

// Request rebuilds the admission request.
func (r AdmitRequest) Request() (admission.Request, error) {
	const op = "multinode.AdmitRequest"
	out := admission.Request{Operation: r.Operation, GVK: r.Kind.GVK(), Identity: r.Identity, DryRun: r.DryRun}
	decode := func(raw json.RawMessage) (v1.Object, error) {
		if len(raw) == 0 {
			return nil, nil
		}
		obj, ok := v1.NewObject(r.Kind)
		if !ok {
			return nil, fault.Invalidf(op, "unknown kind %q", r.Kind)
		}
		if err := json.Unmarshal(raw, obj); err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "decode the %s", r.Kind)
		}
		return obj, nil
	}
	var err error
	if out.Object, err = decode(r.Object); err != nil {
		return admission.Request{}, err
	}
	out.Old, err = decode(r.Old)
	return out, err
}

// scheduleCheck is the check of a BackupSchedule's target against the blob store (ADR-0222 Decision 6).
const scheduleCheck = "backup-schedule"

// Forward wraps the node-state checks among as: for a namespace another node runs, that node runs its own (F250). An
// offline node refuses the write, so no protection is skipped. A BackupSchedule's check needs no call: it reads the
// address of the node's blob store in the WorkerNode's status.
func (c *Cluster) Forward(as []admission.Admission) []admission.Admission {
	out := make([]admission.Admission, len(as))
	for i, a := range as {
		out[i] = a
		switch {
		case slices.Contains(nodeStateAdmissions(), a.Name()):
			out[i] = forwarded{router: c.router(), inner: a}
		case a.Name() == scheduleCheck:
			out[i] = outsideNodeStore{Admission: a, st: c.cfg.Store, placement: c.placement}
		}
	}
	return out
}

// outsideNodeStore is the schedule check for a namespace another node runs, against that node's blob store: none is known
// of a node that has not joined.
type outsideNodeStore struct {
	admission.Admission
	st        store.Store
	placement *Placement
}

func (o outsideNodeStore) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	ns := req.Object.GetObjectMeta().Namespace
	if !o.placement.Skip(ns) {
		return o.Admission.Admit(ctx, req)
	}
	node, err := o.placement.NodeOf(ctx, ns)
	if err != nil {
		return nil, err
	}
	var url string
	if node != "" {
		obj, err := o.st.Get(ctx, v1.KindWorkerNode.GVK(), "", node)
		if err != nil {
			return nil, err
		}
		if wn, ok := obj.(*v1.WorkerNode); ok {
			url = wn.Status.BlobStore
		}
	}
	return admission.NewBackupScheduleAdmission(url).Admit(ctx, req)
}

type forwarded struct {
	router
	inner admission.Admission
}

func (f forwarded) Name() string           { return f.inner.Name() }
func (f forwarded) Phase() admission.Phase { return f.inner.Phase() }
func (f forwarded) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return f.inner.Handles(gvk, op)
}

func (f forwarded) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	const op = "multinode.forward"
	obj := req.Object
	if obj == nil {
		obj = req.Old
	}
	if obj == nil || !f.placement.Skip(obj.GetObjectMeta().Namespace) || !needsNode(f.inner.Name(), req) {
		return f.inner.Admit(ctx, req)
	}
	ns := obj.GetObjectMeta().Namespace
	node, err := f.placement.NodeOf(ctx, ns)
	if err != nil {
		return nil, err
	}
	if node == "" { // a namespace no node runs keeps no state to protect
		return f.inner.Admit(ctx, req)
	}
	in := AdmitRequest{Operation: req.Operation, Kind: req.GVK.Kind, Identity: req.Identity, DryRun: req.DryRun}
	if req.Object != nil {
		if in.Object, err = json.Marshal(req.Object); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
		}
	}
	if req.Old != nil {
		if in.Old, err = json.Marshal(req.Old); err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
		}
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	a, err := f.calls.Do(ctx, node, Call{Kind: CallAdmit, Namespace: ns, Name: f.inner.Name(), Body: body})
	if err != nil {
		return nil, err
	}
	if len(a.Body) == 0 || req.Object == nil {
		return req.Object, nil
	}
	out, ok := v1.NewObject(req.GVK.Kind)
	if !ok {
		return nil, fault.Internalf(op, "unknown kind %q", req.GVK.Kind)
	}
	if err := json.Unmarshal(a.Body, out); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "decode worker node %s's answer", node)
	}
	return out, nil
}
