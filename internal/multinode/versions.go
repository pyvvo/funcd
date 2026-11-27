package multinode

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/store"
)

// newerRelease reports whether a node's release is newer than the control plane's (ADR-0374): a node runs
// the control plane's release or an older one, never a newer one. Releases that do not order, such as a build of a
// branch, pass.
func newerRelease(node, controlPlane string) bool {
	c, ok := version.Compare(node, controlPlane)
	return ok && c > 0
}

// FieldVersions are the spec fields that need a node API newer than its first version (ADR-0374), by kind and by
// JSON path such as "spec.retry", which goes through the items of a list, and through a map's values as "*": a write that
// sets one into a namespace whose node speaks an older version is refused.
type FieldVersions map[v1.Kind]map[string]int

// nodeFields is this release's FieldVersions: none while the node API has its first version only.
func nodeFields() FieldVersions { return FieldVersions{} }

// nodeTooOld refuses a spec field that the node of the object's namespace is too old to run (ADR-0374). A node that
// has not joined yet has no known version, and a namespace on the control plane's own node has none to check.
type nodeTooOld struct {
	st        store.Store
	placement *Placement
	fields    FieldVersions
}

func (nodeTooOld) Name() string           { return "node-too-old" }
func (nodeTooOld) Phase() admission.Phase { return admission.Validating }
func (a nodeTooOld) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return (op == admission.Create || op == admission.Update) && len(a.fields[gvk.Kind]) > 0
}

func (a nodeTooOld) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	const op = "admission.node-too-old"
	node, err := a.placement.NodeOf(ctx, req.Object.GetObjectMeta().Namespace)
	if err != nil || a.placement.Local(node) {
		return req.Object, err
	}
	obj, err := a.st.Get(ctx, v1.KindWorkerNode.GVK(), "", node)
	if err != nil {
		return nil, err
	}
	wn, ok := obj.(*v1.WorkerNode)
	if !ok || wn.Status.NodeAPIVersion == 0 {
		return req.Object, nil
	}
	raw, err := json.Marshal(req.Object)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	fields := a.fields[req.GVK.Kind]
	for _, path := range slices.Sorted(maps.Keys(fields)) {
		if need := fields[path]; need > wn.Status.NodeAPIVersion && fieldSet(raw, path) {
			return nil, fault.Invalidf(op, "worker node %s speaks node API version %d, and %s needs version %d: upgrade the node first",
				node, wn.Status.NodeAPIVersion, path, need)
		}
	}
	return req.Object, nil
}

// fieldSet reports whether the JSON raw holds a value at the dotted path, in any item of the lists on the way.
func fieldSet(raw json.RawMessage, path string) bool {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil && items != nil {
		return slices.ContainsFunc(items, func(i json.RawMessage) bool { return fieldSet(i, path) })
	}
	if path == "" {
		return string(raw) != "null"
	}
	key, rest, _ := strings.Cut(path, ".")
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	if key == "*" {
		return slices.ContainsFunc(slices.Collect(maps.Values(m)), func(v json.RawMessage) bool { return fieldSet(v, rest) })
	}
	next, ok := m[key]
	return ok && fieldSet(next, rest)
}
