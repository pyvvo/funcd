package funcd

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/store"
)

// measurePage is the most objects one listing of a Bucket measure returns.
const measurePage = 1000

// storeLimit reads a subject's cap from its object in st through quotaOf (ADR-0292 Decision 2); a missing object, or
// one of another type, has none.
func storeLimit(st store.Store, kind v1.Kind, quotaOf func(o v1.Object, prefix string) v1.SizeQuota) func(context.Context, quota.Subject) (int64, bool, error) {
	return func(ctx context.Context, s quota.Subject) (int64, bool, error) {
		obj, err := st.Get(ctx, kind.GVK(), s.Namespace, s.Name)
		if fault.KindOf(err) == fault.NotFound {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		n, ok := quotaOf(obj, s.Prefix).Limit()
		return n, ok, nil
	}
}

// bucketQuotas is the quota Source of Buckets and their prefixes: the cap is the Bucket's spec.quota or its prefix's,
// the usage the sum of the sizes listed under the substrate prefix.
func bucketQuotas(shared blob.Bucket, st store.Store) quota.Source {
	return quota.Source{
		Limit: storeLimit(st, v1.KindBucket, func(o v1.Object, prefix string) v1.SizeQuota {
			b, _ := o.(*v1.Bucket)
			if b == nil {
				return v1.SizeQuota{}
			}
			return bucketQuota(b, prefix)
		}),
		Measure: func(ctx context.Context, s quota.Subject) (int64, error) {
			prefix := bucketPrefix(s.Namespace, s.Name)
			if s.Prefix != "" {
				prefix += s.Prefix + "/"
			}
			var total int64
			after := ""
			for {
				items, more, err := shared.ListAfter(ctx, prefix, after, measurePage)
				if err != nil {
					return 0, err
				}
				for _, it := range items {
					total += it.Size
				}
				if !more || len(items) == 0 {
					return total, nil
				}
				after = items[len(items)-1].Key
			}
		},
	}
}

// bucketQuota is the size quota of b, or of its prefix when prefix is set; an undeclared prefix has none.
func bucketQuota(b *v1.Bucket, prefix string) v1.SizeQuota {
	if prefix == "" {
		return b.Spec.Quota.SizeQuota
	}
	for _, p := range b.Spec.Prefixes {
		if p.Name == prefix {
			return p.Quota
		}
	}
	return v1.SizeQuota{}
}

// kvQuotas is the quota Source of KVStores: the cap is the store's spec.quota, the usage what its engine counts under
// <ns>/<store>/.
func kvQuotas(kv kvstore.Usager, st store.Store) quota.Source {
	return quota.Source{
		Limit: storeLimit(st, v1.KindKVStore, func(o v1.Object, _ string) v1.SizeQuota {
			if ks, _ := o.(*v1.KVStore); ks != nil {
				return ks.Spec.Quota.SizeQuota
			}
			return v1.SizeQuota{}
		}),
		Measure: func(ctx context.Context, s quota.Subject) (int64, error) {
			return kv.Usage(ctx, workload.KVPrefix(s.Namespace, s.Name))
		},
	}
}

// sqlQuotas is the quota Source of SQLDatabases: the cap is the database's spec.quota, the usage its pages in use,
// read through its engine.
func sqlQuotas(usage func(context.Context, v1.NamespaceName, v1.ObjectName) (int64, error), st store.Store) quota.Source {
	return quota.Source{
		Limit: storeLimit(st, v1.KindSQLDatabase, func(o v1.Object, _ string) v1.SizeQuota {
			if d, _ := o.(*v1.SQLDatabase); d != nil {
				return d.Spec.Quota
			}
			return v1.SizeQuota{}
		}),
		Measure: func(ctx context.Context, s quota.Subject) (int64, error) {
			return usage(ctx, s.Namespace, s.Name)
		},
	}
}
