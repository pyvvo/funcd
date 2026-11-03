package workload

import (
	"cmp"
	"context"
	"strings"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// Buckets is the Bucket kind: blob/<bucket>/ holds the Bucket's objects under prefix in shared, then the catalogs of
// the Lakes on its prefixes under catalogs in the catalog store (ADR-0293; nil keeps them among its objects), each
// keyed <prefix>/_ducklake/catalog.db as the Bucket held it before and read before the walk (#1383). A Restore loads
// them into the Bucket as, or into the Bucket itself, emptied first by purge, which takes its catalogs too.
func Buckets(st store.Store, shared blob.Bucket, prefix, catalogs func(v1.NamespaceName, v1.ObjectName) string,
	purge gc.BucketPurger) MemberKind {
	return buckets{st: st, shared: shared, prefix: prefix, catalogs: catalogs, purge: purge}
}

type buckets struct {
	st               store.Store
	shared           blob.Bucket
	prefix, catalogs func(v1.NamespaceName, v1.ObjectName) string
	purge            gc.BucketPurger
}

func (buckets) Kind() v1.Kind { return v1.KindBucket }

func (k buckets) Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, m Member) (MemberFile, error) {
	f, err := w.File(ctx, "blob/"+string(m.Name), func(emit func(snapshot.Record) error) error {
		var catalogs []snapshot.Record
		if k.catalogs != nil {
			err := objects(ctx, k.shared, k.catalogs(ns, m.Name), func(key string) bool { return !bucketCatalog(key) },
				func(r snapshot.Record) error {
					catalogs = append(catalogs, r)
					return nil
				})
			if err != nil {
				return err
			}
		}
		if err := objects(ctx, k.shared, k.prefix(ns, m.Name), nil, emit); err != nil {
			return err
		}
		for _, r := range catalogs {
			if err := emit(r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return MemberFile{}, err
	}
	return MemberFile{Kind: m.Kind, Name: m.Name, Files: []backup.StoreFile{f}}, nil
}

func (buckets) Parse(seg []string) (v1.BackupMember, int, bool) {
	if len(seg) < 3 || seg[0] != "blob" {
		return v1.BackupMember{}, 0, false
	}
	return v1.BackupMember{Kind: v1.KindBucket, Name: v1.ObjectName(seg[1])}, 0, true
}

func (k buckets) Destination(ctx context.Context, ns v1.NamespaceName, rm v1.RestoreMember) (Destination, error) {
	dest := cmp.Or(rm.As, rm.Name)
	if _, err := k.st.Get(ctx, v1.KindBucket.GVK(), ns, dest); err != nil {
		return nil, err
	}
	d := bucketDestination{ns: ns, name: dest, view: blob.Prefixed(k.shared, k.prefix(ns, dest)), purge: k.purge,
		inPlace: rm.InPlace}
	if k.catalogs != nil {
		d.catalogs = blob.Prefixed(k.shared, k.catalogs(ns, dest))
	}
	return d, nil
}

type bucketDestination struct {
	ns             v1.NamespaceName
	name           v1.ObjectName
	view, catalogs blob.Bucket // its objects, and the catalogs of the Lakes on its prefixes (nil: among its objects)
	purge          gc.BucketPurger
	inPlace        bool
}

func (bucketDestination) Prepare(context.Context) (func(), error) { return none, nil }

// IsEmpty reports a Bucket with no object and no catalog of a Lake on its prefixes, whose rows are its data (ADR-0293
// Decision 6).
func (d bucketDestination) IsEmpty(ctx context.Context) (bool, error) {
	for _, v := range []blob.Bucket{d.catalogs, d.view} {
		if v == nil {
			continue
		}
		items, _, err := v.ListAfter(ctx, "", "", 1)
		if err != nil || len(items) > 0 {
			return false, err
		}
	}
	return true, nil
}

func (d bucketDestination) Load(ctx context.Context, f MemberFile, open RecordOpener) error {
	if d.inPlace {
		if err := d.Clear(ctx); err != nil {
			return err
		}
	}
	for _, file := range f.Files {
		err := loadObjects(ctx, file, open, func(key string, body []byte, o blob.PutOptions) error {
			if d.catalogs != nil && bucketCatalog(key) {
				return d.catalogs.Put(ctx, key, body, o)
			}
			return d.view.Put(ctx, key, body, o)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (d bucketDestination) Clear(ctx context.Context) error { return d.purge.Purge(ctx, d.ns, d.name) }

// bucketCatalog reports a Bucket member's record of the catalog of the Lake on one of its prefixes,
// <prefix>/_ducklake/catalog.db, which a Restore puts in the catalog store.
func bucketCatalog(key string) bool {
	p, ok := strings.CutSuffix(key, "/"+checkpointName)
	return ok && p != "" && !strings.Contains(p, "/")
}
