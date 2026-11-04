package workload

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// Lakes is the Lake kind (Decision 5, ADR-0271): lake/<name>/<attempt>/files/ holds a lake's objects under its
// location, then lake/<name>/<attempt>/catalog.db/ the catalog as it was before they were listed. The catalog lives in the
// catalog store under catalogPrefix (ADR-0293). A Restore loads into the prefix asLocation, where no Lake may be located
// (ADR-0312 Decision 10), or the lake's location emptied first: its files, then its catalog. An in-place Restore does so
// with the lake's engine stopped until release (stop; nil does nothing), so no engine outlives the load (#1384).
func Lakes(st store.Store, shared blob.Bucket, prefix func(v1.NamespaceName, v1.ObjectName) string, catalogPrefix func(v1.NamespaceName) string,
	stop func(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (release func(), err error)) MemberKind {
	return lakes{st: st, shared: shared, prefix: prefix, catalogPrefix: catalogPrefix, stop: stop}
}

type lakes struct {
	st            store.Store
	shared        blob.Bucket
	prefix        func(v1.NamespaceName, v1.ObjectName) string
	catalogPrefix func(v1.NamespaceName) string
	stop          func(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (release func(), err error)
}

func (lakes) Kind() v1.Kind { return v1.KindLake }

// catalogKey is the blob key of the catalog of the Lake at loc, in the catalog store under its location. Its member record
// keeps the key checkpointName.
func (k lakes) catalogKey(ns v1.NamespaceName, loc v1.LakeLocation) string {
	return k.catalogPrefix(ns) + string(loc.Bucket) + "/" + loc.Prefix + "/" + checkpointName
}

// Write copies one lake in at most lakeAttempts attempts: a listing of the prefix, the checkpoint C1, every other object
// of the prefix, then C1. C1 is copied as it is, never opened: a file it names was written before it, so it is in the
// listing, and an attempt whose copy misses a listed key, deleted meanwhile, runs again. A lake without a checkpoint still
// copies its objects, with no catalog.db/, since its Bucket's copy skips them (ADR-0312 Decision 5); with no object either
// it is an empty member.
func (k lakes) Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, mb Member) (MemberFile, error) {
	const op = "workload.Write"
	root := k.prefix(ns, mb.Location.Bucket) + mb.Location.Prefix + "/"
	catalog := k.catalogKey(ns, mb.Location)
	var last string
	for attempt := 1; attempt <= lakeAttempts; attempt++ {
		listed, err := keys(ctx, k.shared, root)
		if err != nil {
			return MemberFile{}, err
		}
		c1, err := k.shared.Get(ctx, catalog)
		noCatalog := fault.KindOf(err) == fault.NotFound
		if noCatalog && !slices.ContainsFunc(listed, func(k string) bool { return k != checkpointName }) {
			return MemberFile{Kind: mb.Kind, Name: mb.Name}, nil
		}
		if err != nil && !noCatalog {
			return MemberFile{}, fault.Wrapf(err, fault.KindOf(err), op, "lake %s: read its checkpoint", mb.Name)
		}
		var attrs blob.Attributes
		if !noCatalog {
			if attrs, err = k.shared.Attributes(ctx, catalog); err != nil {
				return MemberFile{}, fault.Wrapf(err, fault.KindOf(err), op, "lake %s: read its checkpoint", mb.Name)
			}
		}
		dir := fmt.Sprintf("lake/%s/%d/", mb.Name, attempt)
		copied := map[string]bool{}
		files, err := w.File(ctx, dir+"files", func(emit func(snapshot.Record) error) error {
			return objects(ctx, k.shared, root, func(key string) bool { return key == checkpointName }, func(r snapshot.Record) error {
				copied[string(r.Key)] = true
				return emit(r)
			})
		})
		if err != nil {
			return MemberFile{}, err
		}
		if i := slices.IndexFunc(listed, func(k string) bool { return k != checkpointName && !copied[k] }); i >= 0 {
			last = listed[i]
			continue
		}
		if noCatalog {
			return MemberFile{Kind: mb.Kind, Name: mb.Name, Attempt: attempt, Files: []backup.StoreFile{files}}, nil
		}
		db, err := w.File(ctx, dir+"catalog.db", func(emit func(snapshot.Record) error) error {
			return emit(snapshot.Record{Key: []byte(checkpointName), Value: EncodeObject(attrs.ContentType, attrs.Metadata, c1)})
		})
		if err != nil {
			return MemberFile{}, err
		}
		return MemberFile{Kind: mb.Kind, Name: mb.Name, Attempt: attempt, Files: []backup.StoreFile{files, db}}, nil
	}
	return MemberFile{}, fault.Wrapf(ErrLakeUnstable, fault.Conflict, op, "lake %s: %d copies missed %q, deleted while it was copied",
		mb.Name, lakeAttempts, last)
}

func (lakes) Parse(seg []string) (v1.BackupMember, int, bool) {
	if len(seg) < 4 || seg[0] != "lake" {
		return v1.BackupMember{}, 0, false
	}
	attempt, err := strconv.Atoi(seg[2])
	if err != nil {
		return v1.BackupMember{}, 0, false
	}
	return v1.BackupMember{Kind: v1.KindLake, Name: v1.ObjectName(seg[1])}, attempt, true
}

func (k lakes) Destination(ctx context.Context, ns v1.NamespaceName, rm v1.RestoreMember) (Destination, error) {
	var loc v1.LakeLocation
	if rm.AsLocation != nil {
		if _, err := k.st.Get(ctx, v1.KindBucket.GVK(), ns, rm.AsLocation.Bucket); err != nil {
			return nil, err
		}
		loc = *rm.AsLocation
	} else {
		obj, err := k.st.Get(ctx, v1.KindLake.GVK(), ns, rm.Name)
		if err != nil {
			return nil, err
		}
		loc = obj.(*v1.Lake).Spec.Location
	}
	return lakeDestination{st: k.st, shared: k.shared, view: blob.Prefixed(k.shared, k.prefix(ns, loc.Bucket)+loc.Prefix+"/"),
		catalog: k.catalogKey(ns, loc), inPlace: rm.InPlace, ns: ns, name: rm.Name, loc: loc, stop: k.stop}, nil
}

type lakeDestination struct {
	st           store.Store
	shared, view blob.Bucket // the blob store, and the lake's prefix in it
	catalog      string      // the blob key its catalog loads to
	inPlace      bool
	ns           v1.NamespaceName
	name         v1.ObjectName
	loc          v1.LakeLocation
	stop         func(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (release func(), err error)
}

// Prepare stops the lake's engine until release when it loads in place (ADR-0312 Decision 9): the hold is on by then.
func (d lakeDestination) Prepare(ctx context.Context) (func(), error) {
	if !d.inPlace || d.stop == nil {
		return none, nil
	}
	return d.stop(ctx, d.ns, d.name)
}

// IsEmpty reports a prefix with no object and no catalog, whose inlined rows are data (ADR-0293 Decision 5), and where no
// Lake is located: this path takes no hold, so no engine may stop there, and a running one would overwrite the load
// (ADR-0312 Decision 10).
func (d lakeDestination) IsEmpty(ctx context.Context) (bool, error) {
	l, err := d.st.List(ctx, v1.KindLake.GVK(), store.ListOptions{Namespace: d.ns})
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "workload.Lakes", "list the Lakes of %q", d.ns)
	}
	if slices.ContainsFunc(l.Items, func(o v1.Object) bool {
		lk, ok := o.(*v1.Lake)
		return ok && lk.Spec.Location == d.loc
	}) {
		return false, nil
	}
	has, err := d.shared.Exists(ctx, d.catalog)
	if err != nil || has {
		return false, err
	}
	items, _, err := d.view.ListAfter(ctx, "", "", 1)
	return len(items) == 0, err
}

func (d lakeDestination) Load(ctx context.Context, f MemberFile, open RecordOpener) error {
	if err := wantFiles(f, 0, 2); err != nil {
		return err
	}
	if d.inPlace {
		if err := d.Clear(ctx); err != nil {
			return err
		}
	}
	for _, file := range f.Files {
		err := loadObjects(ctx, file, open, func(key string, body []byte, o blob.PutOptions) error {
			if key == CatalogRecord {
				return d.shared.Put(ctx, d.catalog, body, o)
			}
			return d.view.Put(ctx, key, body, o)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Clear deletes the catalog, then each object under the prefix.
func (d lakeDestination) Clear(ctx context.Context) error {
	if err := d.shared.Delete(ctx, d.catalog); err != nil && fault.KindOf(err) != fault.NotFound {
		return err
	}
	for {
		items, more, err := d.view.ListAfter(ctx, "", "", listPage)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := d.view.Delete(ctx, it.Key); err != nil && fault.KindOf(err) != fault.NotFound {
				return err
			}
		}
		if !more || len(items) == 0 {
			return nil
		}
	}
}
