package function

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// BootTimeout is how long the reconciler lets a replica run before readiness judges it never ready (ADR-0030 §4b).
const BootTimeout = bootTimeout

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
