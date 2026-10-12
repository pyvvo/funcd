// Package workload is an app owner's backup (ADR-0222): the members a BackupSchedule's scope names, one full copy of
// them under <target>/<ns>/<backup>-<uid>/ (Decision 4) and the reads a Restore makes of it. Each member's file is
// ADR-0203's framed records in one Seal stream, put in 8 MiB parts IfNotExist; a catalog's objects go before its
// catalog.db, checked against it (Decision 5); the plain manifest.yaml goes last.
package workload

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// Format is the workload manifest's format; a changed meaning, framing or layout bumps it.
const Format = 1

const (
	manifestName = "manifest.yaml"
	// checkpointName is a DuckLake catalog's checkpoint under its prefix (ADR-0086).
	checkpointName = "_ducklake/catalog.db"
	// catalogAttempts is the copies of one catalog a run makes before CatalogUnstable (Decision 5).
	catalogAttempts = 3
	listPage        = 1000
)

// The failures a run's record names by reason (Decisions 4, 5).
var (
	ErrCatalogUnstable     = errors.New("the catalog changed under every copy")
	ErrTargetUnconditional = errors.New("the target refuses a create-if-absent put")
)

// Member is one store of a scope. Catalog is a CatalogService member's spec.catalog, which Write copies.
type Member struct {
	Kind    v1.Kind
	Name    v1.ObjectName
	Catalog v1.CatalogRef
}

// Sources are what Write reads: the KV driver's per-store export (ADR-0209 SnapshotPrefix; nil when the driver offers
// none), the shared blob store and a Bucket's prefix in it.
type Sources struct {
	KV           func(prefix string) snapshot.Source
	Blob         blob.Bucket
	BucketPrefix func(ns v1.NamespaceName, b v1.ObjectName) string
}

// Options are where Write puts the copy and to whom it seals it: Path is <ns>/<backup>-<uid>/ on Target, Recipients
// the fingerprints the manifest records.
type Options struct {
	Target     blob.Bucket
	Path       string
	Seal       backup.Seal
	Recipients []string
}

// MemberFile is one member's files in the manifest: a store's one, a catalog's files then catalog.db, of Attempt.
type MemberFile struct {
	Kind    v1.Kind            `json:"kind"`
	Name    v1.ObjectName      `json:"name"`
	Attempt int                `json:"attempt,omitempty"`
	Files   []backup.StoreFile `json:"files"`
}

// Manifest is a Backup's manifest.yaml, plain, written last: the copy exists once it does. Backup is the copy's
// directory, <backup>-<uid>.
type Manifest struct {
	Format     int          `json:"format"`
	Namespace  string       `json:"namespace"`
	Backup     string       `json:"backup"`
	Funcd      string       `json:"funcd"`
	At         v1.Timestamp `json:"at"`
	Members    []MemberFile `json:"members"`
	Recipients []string     `json:"recipients,omitempty"`
}

// Resolve lists the members s names in ns (Decision 2), KVStores, Buckets then CatalogServices: by name, the
// stores of the App's kv, buckets and catalogs entries, or the stores of the ResourceGroup. A named member or App that
// does not exist is fault.NotFound naming it.
func Resolve(ctx context.Context, st store.Store, ns v1.NamespaceName, s v1.BackupScope) ([]Member, error) {
	const op = "workload.Resolve"
	names := map[v1.Kind][]v1.ObjectName{
		v1.KindKVStore: s.KVStores, v1.KindBucket: s.Buckets, v1.KindCatalogService: s.Catalogs,
	}
	switch {
	case s.App != "":
		obj, err := st.Get(ctx, v1.KindApp.GVK(), ns, s.App)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "App %s/%s", ns, s.App)
		}
		a := obj.(*v1.App)
		names = map[v1.Kind][]v1.ObjectName{}
		for _, e := range a.Spec.KV {
			names[v1.KindKVStore] = append(names[v1.KindKVStore], cmp.Or(e.Name, e.Ref))
		}
		for _, e := range a.Spec.Buckets {
			names[v1.KindBucket] = append(names[v1.KindBucket], cmp.Or(e.Name, e.Ref))
		}
		for _, e := range a.Spec.Catalogs {
			names[v1.KindCatalogService] = append(names[v1.KindCatalogService], cmp.Or(e.Name, e.Ref))
		}
	case s.ResourceGroup != "":
		names = map[v1.Kind][]v1.ObjectName{}
		for _, k := range memberKinds() {
			l, err := st.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
			if err != nil {
				return nil, fault.Wrapf(err, fault.KindOf(err), op, "list %s in %q", k, ns)
			}
			for _, o := range l.Items {
				if o.GetResourceGroup() == s.ResourceGroup {
					names[k] = append(names[k], o.GetName())
				}
			}
			slices.Sort(names[k])
		}
	}
	var out []Member
	for _, k := range memberKinds() {
		for _, n := range names[k] {
			obj, err := st.Get(ctx, k.GVK(), ns, n)
			if err != nil {
				return nil, fault.Wrapf(err, fault.KindOf(err), op, "member %s/%s", k, n)
			}
			m := Member{Kind: k, Name: n}
			if cs, ok := obj.(*v1.CatalogService); ok {
				m.Catalog = cs.Spec.Catalog
			}
			out = append(out, m)
		}
	}
	return out, nil
}

func memberKinds() []v1.Kind { return []v1.Kind{v1.KindKVStore, v1.KindBucket, v1.KindCatalogService} }

// KVPrefix is a KVStore's key prefix in the KV substrate, <ns>/<store>/ (ADR-0072).
func KVPrefix(ns v1.NamespaceName, name v1.ObjectName) string {
	return string(ns) + "/" + string(name) + "/"
}

// Write copies ms under o.Path, then puts the manifest (Decisions 4, 5): a KVStore to kv/<store>/, a Bucket's
// objects to blob/<bucket>/, a catalog's objects but catalog.db to catalog/<name>/<attempt>/files/ and then its
// catalog.db to catalog/<name>/<attempt>/catalog.db/. A refused IfNotExist is ErrTargetUnconditional; a catalog whose
// checkpoint names an object no attempt copied is ErrCatalogUnstable.
func Write(ctx context.Context, ns v1.NamespaceName, ms []Member, src Sources, o Options) (Manifest, error) {
	const op = "workload.Write"
	m := Manifest{
		Format: Format, Namespace: string(ns), Backup: path.Base(strings.TrimSuffix(o.Path, "/")), Funcd: version.Version,
		At: v1.NewTimestamp(time.Now()), Recipients: o.Recipients,
	}
	w := &writer{o: o, buf: make([]byte, 0, backup.PartBytes)}
	for _, mb := range ms {
		mf := MemberFile{Kind: mb.Kind, Name: mb.Name}
		var f backup.StoreFile
		var err error
		switch mb.Kind {
		case v1.KindKVStore:
			if src.KV == nil {
				return Manifest{}, fault.Unavailablef(op, "member %s/%s: the KV driver exports no store", mb.Kind, mb.Name)
			}
			f, err = w.file(ctx, "kv/"+string(mb.Name), func(emit func(snapshot.Record) error) error {
				_, err := src.KV(KVPrefix(ns, mb.Name)).Snapshot(ctx, emit)
				return err
			})
			mf.Files = []backup.StoreFile{f}
		case v1.KindBucket:
			f, err = w.file(ctx, "blob/"+string(mb.Name), func(emit func(snapshot.Record) error) error {
				return objects(ctx, src.Blob, src.BucketPrefix(ns, mb.Name), nil, emit)
			})
			mf.Files = []backup.StoreFile{f}
		case v1.KindCatalogService:
			mf, err = w.catalog(ctx, ns, mb, src)
		default:
			err = fault.Invalidf(op, "member %s/%s: want a KVStore, Bucket or CatalogService", mb.Kind, mb.Name)
		}
		if err != nil {
			return Manifest{}, err
		}
		m.Members = append(m.Members, mf)
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return Manifest{}, fault.Wrapf(err, fault.Internal, op, "encode the manifest")
	}
	if err := w.put(ctx, o.Path+manifestName, data); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// catalog copies one catalog in at most catalogAttempts attempts (Decision 5): the checkpoint C1, every other object
// of the prefix, the check that each file C1 names was copied, then C1.
// A catalog without a checkpoint is an empty member.
func (w *writer) catalog(ctx context.Context, ns v1.NamespaceName, mb Member, src Sources) (MemberFile, error) {
	const op = "workload.Write"
	root := src.BucketPrefix(ns, mb.Catalog.Bucket) + mb.Catalog.Prefix + "/"
	var last string
	for attempt := 1; attempt <= catalogAttempts; attempt++ {
		c1, err := src.Blob.Get(ctx, root+checkpointName)
		if fault.KindOf(err) == fault.NotFound {
			// no checkpoint yet: DuckLake opens a missing catalog.db as a new, empty catalog, and no object under the
			// prefix is part of one, so the member is empty rather than a failure of the whole Backup
			return MemberFile{Kind: mb.Kind, Name: mb.Name}, nil
		}
		if err != nil {
			return MemberFile{}, fault.Wrapf(err, fault.KindOf(err), op, "catalog %s: read its checkpoint", mb.Name)
		}
		attrs, err := src.Blob.Attributes(ctx, root+checkpointName)
		if err != nil {
			return MemberFile{}, fault.Wrapf(err, fault.KindOf(err), op, "catalog %s: read its checkpoint", mb.Name)
		}
		dir := fmt.Sprintf("catalog/%s/%d/", mb.Name, attempt)
		copied := map[string]bool{}
		files, err := w.file(ctx, dir+"files", func(emit func(snapshot.Record) error) error {
			return objects(ctx, src.Blob, root, func(key string) bool { return key == checkpointName }, func(r snapshot.Record) error {
				copied[string(r.Key)] = true
				return emit(r)
			})
		})
		if err != nil {
			return MemberFile{}, err
		}
		listed, err := CatalogFiles(c1)
		if err != nil {
			return MemberFile{}, fault.Wrapf(err, fault.KindOf(err), op, "catalog %s", mb.Name)
		}
		if i := slices.IndexFunc(listed, func(k string) bool { return !copied[k] }); i >= 0 {
			last = listed[i]
			continue
		}
		db, err := w.file(ctx, dir+"catalog.db", func(emit func(snapshot.Record) error) error {
			return emit(snapshot.Record{Key: []byte(checkpointName), Value: EncodeObject(attrs.ContentType, attrs.Metadata, c1)})
		})
		if err != nil {
			return MemberFile{}, err
		}
		return MemberFile{Kind: mb.Kind, Name: mb.Name, Attempt: attempt, Files: []backup.StoreFile{files, db}}, nil
	}
	return MemberFile{}, fault.Wrapf(ErrCatalogUnstable, fault.Conflict, op, "catalog %s: %d copies missed %q, which its checkpoint names",
		mb.Name, catalogAttempts, last)
}

// objects emits every object under prefix in key order, its key relative to prefix and its value EncodeObject's,
// skipping the keys skip names; an object deleted after the listing is not emitted.
func objects(ctx context.Context, b blob.Bucket, prefix string, skip func(string) bool, emit func(snapshot.Record) error) error {
	const op = "workload.Write"
	after := ""
	for {
		items, more, err := b.ListAfter(ctx, prefix, after, listPage)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "list %q", prefix)
		}
		for _, it := range items {
			rel := strings.TrimPrefix(it.Key, prefix)
			if skip != nil && skip(rel) {
				continue
			}
			data, err := b.Get(ctx, it.Key)
			if fault.KindOf(err) == fault.NotFound {
				continue
			}
			if err != nil {
				return fault.Wrapf(err, fault.KindOf(err), op, "read %q", it.Key)
			}
			attrs, err := b.Attributes(ctx, it.Key)
			if fault.KindOf(err) == fault.NotFound {
				continue
			}
			if err != nil {
				return fault.Wrapf(err, fault.KindOf(err), op, "read %q", it.Key)
			}
			if err := emit(snapshot.Record{Key: []byte(rel), Value: EncodeObject(attrs.ContentType, attrs.Metadata, data)}); err != nil {
				return err
			}
		}
		if !more || len(items) == 0 {
			return nil
		}
		after = items[len(items)-1].Key
	}
}

// writer puts a run's files: each record framed, through the seal, hashed and counted, cut in parts.
type writer struct {
	o   Options
	buf []byte
}

func (w *writer) put(ctx context.Context, key string, data []byte) error {
	const op = "workload.Write"
	err := w.o.Target.Put(ctx, key, data, blob.PutOptions{IfNotExist: true})
	switch {
	case err == nil:
		return nil
	case backup.ConditionRefused(err):
		return fault.Wrapf(errors.Join(ErrTargetUnconditional, err), fault.Invalid, op, "put %q", key)
	}
	return fault.Wrapf(err, fault.KindOf(err), op, "put %q", key)
}

// file writes the records emit produces as the file name/, part-00000 first; an empty file is one empty part.
func (w *writer) file(ctx context.Context, name string, produce func(emit func(snapshot.Record) error) error) (backup.StoreFile, error) {
	p := &parts{w: w, dir: w.o.Path + name + "/", h: sha256.New(),
		put: func(key string, data []byte) error { return w.put(ctx, key, data) }}
	var sealed io.WriteCloser = nopCloser{p}
	if w.o.Seal != nil {
		var err error
		if sealed, err = w.o.Seal(p); err != nil {
			return backup.StoreFile{}, fault.Wrapf(err, fault.KindOf(err), "workload.Write", "seal %s", name)
		}
	}
	bw := bufio.NewWriterSize(sealed, 64<<10)
	err := produce(func(r snapshot.Record) error { return backup.WriteRecord(bw, r) })
	if err == nil {
		err = errors.Join(bw.Flush(), sealed.Close(), p.flush(true))
	}
	if err != nil {
		return backup.StoreFile{}, err
	}
	return backup.StoreFile{Name: name, Parts: p.n, Bytes: p.bytes, SHA256: hex.EncodeToString(p.h.Sum(nil))}, nil
}

// parts cuts what is written into parts of backup.PartBytes, putting each once full.
type parts struct {
	put   func(key string, data []byte) error
	w     *writer
	dir   string
	h     hash.Hash
	n     int
	bytes int64
}

func (p *parts) Write(b []byte) (int, error) {
	p.h.Write(b)
	p.bytes += int64(len(b))
	for rest := b; len(rest) > 0; {
		k := min(len(rest), backup.PartBytes-len(p.w.buf))
		p.w.buf = append(p.w.buf, rest[:k]...)
		rest = rest[k:]
		if len(p.w.buf) == backup.PartBytes {
			if err := p.flush(false); err != nil {
				return 0, err
			}
		}
	}
	return len(b), nil
}

// flush puts the buffered part; at the end it puts the last one, an empty one when the file has no part yet.
func (p *parts) flush(end bool) error {
	if len(p.w.buf) == 0 && (!end || p.n > 0) {
		return nil
	}
	if err := p.put(fmt.Sprintf("%spart-%05d", p.dir, p.n), p.w.buf); err != nil {
		return err
	}
	p.n++
	p.w.buf = p.w.buf[:0]
	return nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// ReadManifest reads the manifest under path; a format above Format is fault.Invalid.
func ReadManifest(ctx context.Context, src blob.Bucket, path string) (Manifest, error) {
	const op = "workload.ReadManifest"
	data, err := src.Get(ctx, path+manifestName)
	if err != nil {
		return Manifest{}, fault.Wrapf(err, fault.KindOf(err), op, "read %s%s", path, manifestName)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fault.Wrapf(err, fault.Invalid, op, "decode %s%s", path, manifestName)
	}
	if m.Format > Format {
		return Manifest{}, fault.Invalidf(op, "%s%s has format %d; this funcd reads format %d", path, manifestName, m.Format, Format)
	}
	return m, nil
}

// Integrity reads every part of every file m names and checks its bytes and sha256; a missing part or a mismatch is
// fault.Invalid.
func Integrity(ctx context.Context, src blob.Bucket, path string, m Manifest) error {
	const op = "workload.Integrity"
	for _, mf := range m.Members {
		for _, f := range mf.Files {
			h := sha256.New()
			n, err := io.Copy(h, newPartReader(ctx, src, path+f.Name+"/", f.Parts))
			if err != nil {
				return fault.Wrapf(err, fault.Invalid, op, "read %s%s", path, f.Name)
			}
			if sum := hex.EncodeToString(h.Sum(nil)); n != f.Bytes || sum != f.SHA256 {
				return fault.Invalidf(op, "%s%s holds %d bytes of sha256 %s; the manifest names %d bytes of %s",
					path, f.Name, n, sum, f.Bytes, f.SHA256)
			}
		}
	}
	return nil
}

// Records reads f's records through open: io.EOF at the end, a torn record fault.Invalid.
func Records(ctx context.Context, src blob.Bucket, path string, f backup.StoreFile, open backup.Unseal) func() (snapshot.Record, error) {
	var next func() (snapshot.Record, error)
	return func() (snapshot.Record, error) {
		if next == nil {
			var r io.Reader = newPartReader(ctx, src, path+f.Name+"/", f.Parts)
			if open != nil {
				var err error
				if r, err = open(r); err != nil {
					return snapshot.Record{}, err
				}
			}
			next = backup.Records(r)
		}
		return next()
	}
}

// partReader reads a file's parts in order, one Get each.
type partReader struct {
	get   func(key string) ([]byte, error)
	dir   string
	parts int
	i     int
	cur   *bytes.Reader
}

func newPartReader(ctx context.Context, src blob.Bucket, dir string, parts int) *partReader {
	return &partReader{get: func(key string) ([]byte, error) { return src.Get(ctx, key) }, dir: dir, parts: parts}
}

func (r *partReader) Read(p []byte) (int, error) {
	for r.cur == nil || r.cur.Len() == 0 {
		if r.i == r.parts {
			return 0, io.EOF
		}
		data, err := r.get(fmt.Sprintf("%spart-%05d", r.dir, r.i))
		if err != nil {
			return 0, err
		}
		r.cur, r.i = bytes.NewReader(data), r.i+1
	}
	return r.cur.Read(p)
}

// EncodeObject frames an object as uvarint-length fields (ADR-0203): its content type, its metadata's count and each
// key and value in key order, then its body.
func EncodeObject(contentType string, md map[string]string, body []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(len(contentType)))
	out = append(out, contentType...)
	out = binary.AppendUvarint(out, uint64(len(md)))
	for _, k := range slices.Sorted(maps.Keys(md)) {
		out = binary.AppendUvarint(out, uint64(len(k)))
		out = append(out, k...)
		out = binary.AppendUvarint(out, uint64(len(md[k])))
		out = append(out, md[k]...)
	}
	out = binary.AppendUvarint(out, uint64(len(body)))
	return append(out, body...)
}

// DecodeObject reverses EncodeObject; a torn or trailing frame is fault.Invalid.
func DecodeObject(v []byte) (contentType string, md map[string]string, body []byte, err error) {
	const op = "workload.DecodeObject"
	field := func() ([]byte, bool) {
		n, k := binary.Uvarint(v)
		if k <= 0 || n > uint64(len(v)-k) {
			return nil, false
		}
		end := k + int(n)
		out := v[k:end]
		v = v[end:]
		return out, true
	}
	ct, ok := field()
	if !ok {
		return "", nil, nil, fault.Invalidf(op, "torn content type")
	}
	count, k := binary.Uvarint(v)
	if k <= 0 || count > uint64(len(v)) {
		return "", nil, nil, fault.Invalidf(op, "torn metadata count")
	}
	v = v[k:]
	if count > 0 {
		md = make(map[string]string, count)
	}
	for range count {
		key, ok := field()
		val, ok2 := field()
		if !ok || !ok2 {
			return "", nil, nil, fault.Invalidf(op, "torn metadata")
		}
		md[string(key)] = string(val)
	}
	body, ok = field()
	if !ok || len(v) > 0 {
		return "", nil, nil, fault.Invalidf(op, "torn or trailing body")
	}
	return string(ct), md, bytes.Clone(body), nil
}
