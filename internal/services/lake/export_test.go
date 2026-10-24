package lake

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// AnyFunctionBinds exposes anyFunctionBinds to the package's black-box tests.
func AnyFunctionBinds(ctx context.Context, st store.Store, ns v1.NamespaceName, name v1.ObjectName) (bool, error) {
	return anyFunctionBinds(ctx, st, ns, name)
}

// SaveURL exposes saveURL to the package's black-box tests.
func SaveURL(r *Reconciler, address string) (string, error) { return r.saveURL(address) }
