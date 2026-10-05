package admission

import (
	"context"
	"fmt"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// maxListedMembers bounds the members a refused ResourceGroup delete names.
const maxListedMembers = 5

// Members returns the members of ResourceGroup group in ns (ADR-0170 Decision 7): every object of a namespaced
// kind other than ResourceGroup, in ns, whose metadata.resourceGroup is group and that no controller owns (a
// controlled child goes with its owner). Platform-written records (WorkflowRuns, Invocations) count.
func Members(ctx context.Context, r StoreReader, ns v1.NamespaceName, group v1.ResourceGroupName) ([]v1.Object, error) {
	var out []v1.Object
	for _, kind := range v1.AllKinds() {
		if !kind.Namespaced() || kind == v1.KindResourceGroup {
			continue
		}
		objs, err := r.List(ctx, kind.GVK(), ns)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), "admission.Members", "list %s in %q", kind, ns)
		}
		for _, o := range objs {
			m := o.GetObjectMeta()
			if _, owned := v1.ControllerOf(m.OwnerReferences); m.ResourceGroup == group && !owned {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

// memberRef is a member's "Kind/name".
func memberRef(o v1.Object) string {
	return string(o.GroupVersionKind().Kind) + "/" + string(o.GetObjectMeta().Name)
}

type resourceGroupDeletionProtection struct{ r StoreReader }

// NewResourceGroupDeletionProtectionAdmission returns the Validating admission that refuses deleting a
// ResourceGroup while it has a member (ADR-0170 Decision 7), naming up to five members and the count.
func NewResourceGroupDeletionProtectionAdmission(r StoreReader) Admission {
	return resourceGroupDeletionProtection{r: r}
}

func (resourceGroupDeletionProtection) Name() string { return "resourcegroup-deletion-protection" }
func (resourceGroupDeletionProtection) Phase() Phase { return Validating }

// ReadsNamespace: the member check reads every namespaced kind of the namespace (ADR-0147).
func (resourceGroupDeletionProtection) ReadsNamespace() bool { return true }

func (resourceGroupDeletionProtection) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindResourceGroup.GVK() && op == Delete
}

func (a resourceGroupDeletionProtection) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.resourcegroup-deletion-protection"
	if req.Old == nil {
		return nil, nil
	}
	m := req.Old.GetObjectMeta()
	members, err := Members(ctx, a.r, m.Namespace, v1.ResourceGroupName(m.Name))
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return req.Old, nil
	}
	names := make([]string, 0, maxListedMembers)
	for _, o := range members[:min(len(members), maxListedMembers)] {
		names = append(names, memberRef(o))
	}
	return nil, fault.Conflictf(op, "ResourceGroup %q still has %d member(s): %s; delete them first, or delete with force",
		m.Name, len(members), strings.Join(names, ", ")+more(len(members)))
}

func more(n int) string {
	if n <= maxListedMembers {
		return ""
	}
	return fmt.Sprintf(", and %d more", n-maxListedMembers)
}
