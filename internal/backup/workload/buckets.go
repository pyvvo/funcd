package workload

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// Buckets is the Bucket kind (ADR-0312): blob/<bucket>/ holds the Bucket's objects under prefix in shared, but those under
// its Lake prefixes, which the Lakes' members hold, and blob/<bucket>/catalogs/, only when there is one to copy, the
// catalogs of its other prefixes under catalogs. A Restore loads them into the Bucket as, or into the Bucket itself,
// emptied first, and touches neither objects nor catalogs under that Bucket's Lake prefixes, or a prefix whose owner Lake is
// gone.
func Buckets(st store.Store, shared blob.Bucket, prefix, catalogs func(v1.NamespaceName, v1.ObjectName) string) MemberKind {
	return buckets{st: st, shared: shared, prefix: prefix, catalogs: catalogs}
}

type buckets struct {
	st               store.Store
	shared           blob.Bucket
	prefix, catalogs func(v1.NamespaceName, v1.ObjectName) string
}

func (buckets) Kind() v1.Kind { return v1.KindBucket }

func (k buckets) Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, m Member) (MemberFile, error) {
	skip := func(key string) bool { return underLake(key, m.LakePrefixes) }
	name := "blob/" + string(m.Name)
	f, err := w.File(ctx, name, func(emit func(snapshot.Record) error) error {
		return objects(ctx, k.shared, k.prefix(ns, m.Name), skip, emit)
	})
	if err != nil {
		return MemberFile{}, err
	}
	files := []backup.StoreFile{f}
	catalogs := k.catalogs(ns, m.Name)
	held, err := keys(ctx, k.shared, catalogs)
	if err != nil {
		return MemberFile{}, err
	}
	if slices.ContainsFunc(held, func(key string) bool { return !skip(key) }) {
		c, err := w.File(ctx, name+"/catalogs", func(emit func(snapshot.Record) error) error {
			return objects(ctx, k.shared, catalogs, skip, emit)
		})
		if err != nil {
			return MemberFile{}, err
		}
		files = append(files, c)
	}
	return MemberFile{Kind: m.Kind, Name: m.Name, Files: files}, nil
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
	return bucketDestination{st: k.st, ns: ns, name: dest, view: blob.Prefixed(k.shared, k.prefix(ns, dest)),
		catalogs: blob.Prefixed(k.shared, k.catalogs(ns, dest)), inPlace: rm.InPlace}, nil
}

type bucketDestination struct {
	st             store.Store
	ns             v1.NamespaceName
	name           v1.ObjectName
	view, catalogs blob.Bucket // its objects, and the catalogs of its prefixes
	inPlace        bool
}

func (bucketDestination) Prepare(context.Context) (func(), error) { return none, nil }

// lakes reads the prefixes a Restore leaves, now: a Lake applied since the plan keeps its prefix, and a gone one keeps its data.
func (d bucketDestination) lakes(ctx context.Context) ([]string, error) {
	obj, err := d.st.Get(ctx, v1.KindBucket.GVK(), d.ns, d.name)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "workload.Buckets", "get Bucket %s/%s", d.ns, d.name)
	}
	ps, err := lakePrefixes(obj.(*v1.Bucket), true, reader(ctx, d.st, d.ns))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.name)
	}
	return out, nil
}

// IsEmpty reports a Bucket with no object and no catalog outside its Lake prefixes: a catalog no Lake owns is the Bucket's
// data (ADR-0293 Decision 6).
func (d bucketDestination) IsEmpty(ctx context.Context) (bool, error) {
	lakes, err := d.lakes(ctx)
	if err != nil {
		return false, err
	}
	empty := true
	for _, v := range []blob.Bucket{d.view, d.catalogs} {
		err := outsideLakes(ctx, v, lakes, func(string) (bool, error) {
			empty = false
			return false, nil
		})
		if err != nil || !empty {
			return false, err
		}
	}
	return true, nil
}

// Load puts its files' records outside its Lake prefixes: Files[0] among its objects, Files[1] among its catalogs. A
// record under a Lake prefix, from before that Lake took it, leaves no trace.
func (d bucketDestination) Load(ctx context.Context, f MemberFile, open RecordOpener) error {
	if err := wantFiles(f, 1, 2); err != nil {
		return err
	}
	if d.inPlace {
		if err := d.Clear(ctx); err != nil {
			return err
		}
	}
	lakes, err := d.lakes(ctx)
	if err != nil {
		return err
	}
	for i, file := range f.Files {
		view := d.view
		if i > 0 {
			view = d.catalogs
		}
		err := loadObjects(ctx, file, open, func(key string, body []byte, o blob.PutOptions) error {
			if underLake(key, lakes) {
				return nil
			}
			return view.Put(ctx, key, body, o)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Clear deletes each object and each catalog outside its Lake prefixes.
func (d bucketDestination) Clear(ctx context.Context) error {
	lakes, err := d.lakes(ctx)
	if err != nil {
		return err
	}
	for _, v := range []blob.Bucket{d.view, d.catalogs} {
		err := outsideLakes(ctx, v, lakes, func(key string) (bool, error) {
			if err := v.Delete(ctx, key); err != nil && fault.KindOf(err) != fault.NotFound {
				return false, err
			}
			return true, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// underLake reports a key under one of the prefixes lakes, by <prefix>/, so a sibling <prefix>2/ is not.
func underLake(key string, lakes []string) bool {
	return slices.ContainsFunc(lakes, func(p string) bool { return strings.HasPrefix(key, p+"/") })
}

// outsideLakes calls each for every key of view outside lakes, in key order, seeking past a Lake prefix rather than listing
// it, until each returns false.
func outsideLakes(ctx context.Context, view blob.Bucket, lakes []string, each func(key string) (bool, error)) error {
	after := ""
	for {
		items, more, err := view.ListAfter(ctx, "", after, listPage)
		if err != nil {
			return err
		}
		for _, it := range items {
			if i := slices.IndexFunc(lakes, func(p string) bool { return strings.HasPrefix(it.Key, p+"/") }); i >= 0 {
				after, more = max(lakes[i]+"/"+string(utf8.MaxRune), it.Key), true
				break
			}
			next, err := each(it.Key)
			if err != nil || !next {
				return err
			}
			after = it.Key
		}
		if !more || len(items) == 0 {
			return nil
		}
	}
}
