package cedar

import (
	"context"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// s3Meta is a MetaReader with Functions + Buckets (the s3Resource materializer reads the Bucket for a
// prefix's owner). Shared by the internal-package cedar tests (e.g. managed_identity_test.go). `mkey`
// lives in capability_test.go.
type s3Meta struct {
	fns     map[string]*v1.Function
	buckets map[string]*v1.Bucket
}

func (m s3Meta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	switch gvk.Kind {
	case v1.KindFunction:
		if f, ok := m.fns[mkey(ns, name)]; ok {
			return f, nil
		}
	case v1.KindBucket:
		if b, ok := m.buckets[mkey(ns, name)]; ok {
			return b, nil
		}
	}
	return nil, fault.NotFoundf("s3Meta.Get", "%s %s/%s not found", gvk.Kind, ns, name)
}
