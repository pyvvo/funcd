package admission

import (
	"context"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// StoreReader is the read-only store view a cross-resource admission needs. It is a subset of
// store.Store declared here so the admission package stays a near-leaf (the wiring adapts the real
// store.Store to this interface).
type StoreReader interface {
	List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error)
}

// --- link-validity (ADR-0064): Create/Update on Function -------------------------------------

type linkValidity struct{ r StoreReader }

// NewLinkValidityAdmission returns the Validating admission that enforces ADR-0064's cross-resource
// link rules on Create/Update: every link target exists in the namespace, and the namespace link
// graph stays acyclic with the applied function (a self-link is a degenerate cycle).
func NewLinkValidityAdmission(r StoreReader) Admission { return linkValidity{r: r} }

func (linkValidity) Name() string { return "link-validity" }
func (linkValidity) Phase() Phase { return Validating }

func (linkValidity) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindFunction.GVK() && (op == Create || op == Update)
}

func (a linkValidity) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.link-validity"
	fn, ok := req.Object.(*v1.Function)
	if !ok || len(fn.Spec.Links) == 0 {
		return req.Object, nil
	}
	ns := fn.Namespace
	others, err := a.r.List(ctx, v1.KindFunction.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list functions in %q", ns)
	}

	// Build the namespace link digraph from the stored functions, taking the applied function's
	// edges from the INCOMING object (not its stored copy), and note which functions exist.
	adj := make(map[string][]string)
	exists := map[string]bool{string(fn.Name): true} // the applied function exists (create or update)
	for _, o := range others {
		f, ok := o.(*v1.Function)
		if !ok {
			continue
		}
		exists[string(f.Name)] = true
		if f.Name == fn.Name {
			continue // applied function's edges come from the incoming object below
		}
		for _, l := range f.Spec.Links {
			adj[string(f.Name)] = append(adj[string(f.Name)], string(l.Target))
		}
	}
	for _, l := range fn.Spec.Links {
		if !exists[string(l.Target)] {
			return nil, fault.Invalidf(op, "spec.links[%s].target %q does not exist in namespace %q", l.Alias, l.Target, ns)
		}
		adj[string(fn.Name)] = append(adj[string(fn.Name)], string(l.Target))
	}

	if cyc := findCycle(string(fn.Name), adj); cyc != "" {
		return nil, fault.Invalidf(op, "spec.links would create a dependency cycle (%s)", cyc)
	}
	return req.Object, nil
}

// findCycle runs a coloured DFS from start and returns the cycle path (or "" if none reachable).
// A self-link (start → start) is detected as a degenerate cycle.
func findCycle(start string, adj map[string][]string) string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var path []string
	var dfs func(n string) string
	dfs = func(n string) string {
		color[n] = gray
		path = append(path, n)
		for _, m := range adj[n] {
			switch color[m] {
			case gray:
				return strings.Join(append(path, m), " → ")
			case white:
				if c := dfs(m); c != "" {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		color[n] = black
		return ""
	}
	return dfs(start)
}

// --- link-deletion-protection (ADR-0064): Delete on Function ----------------------------------

type linkDeletionProtection struct{ r StoreReader }

// NewLinkDeletionProtectionAdmission returns the Validating admission that rejects deleting a
// Function that is another Function's link target (ADR-0064). It reads Request.Old.
func NewLinkDeletionProtectionAdmission(r StoreReader) Admission { return linkDeletionProtection{r: r} }

func (linkDeletionProtection) Name() string { return "link-deletion-protection" }
func (linkDeletionProtection) Phase() Phase { return Validating }

func (linkDeletionProtection) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindFunction.GVK() && op == Delete
}

func (a linkDeletionProtection) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.link-deletion-protection"
	if req.Old == nil {
		return nil, nil
	}
	target := req.Old.GetObjectMeta().Name
	ns := req.Old.GetObjectMeta().Namespace
	others, err := a.r.List(ctx, v1.KindFunction.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list functions in %q", ns)
	}
	for _, o := range others {
		f, ok := o.(*v1.Function)
		if !ok || f.Name == target {
			continue
		}
		for _, l := range f.Spec.Links {
			if l.Target == target {
				return nil, fault.Conflictf(op, "function %q is linked by %q (alias %q); remove the link first", target, f.Name, l.Alias)
			}
		}
	}
	return req.Old, nil
}
