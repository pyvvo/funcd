// Package workload is an app owner's backup (ADR-0222): the members a BackupSchedule's scope names, one full copy of
// them under <target>/<ns>/<backup>-<uid>/ (Decision 4) and the reads a Restore makes of it. Each member's file is
// ADR-0203's framed records in one Seal stream, put in 8 MiB parts IfNotExist; a lake's objects go before its
// catalog.db, checked against it (Decision 5); the plain manifest.yaml goes last. Each kind of member is one MemberKind,
// which its provider hands over (ADR-0311).
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

// Format is the workload manifest's format; a changed meaning, framing or layout bumps it. A funcd reads one format:
// from 2 on a Bucket's file holds none of its Lakes' data (ADR-0312 Decision 8).
const Format = 2

const (
	manifestName = "manifest.yaml"
	// checkpointName is a DuckLake catalog's checkpoint under its prefix (ADR-0086), and its record's key in a lake
	// member.
	checkpointName = "_ducklake/catalog.db"
	// lakeAttempts is the copies of one lake a run makes before LakeUnstable (Decision 5).
	lakeAttempts = 3
	listPage     = 1000
)

// The failures a run's record names by reason (Decisions 4, 5; ADR-0234 Decision 2).
var (
	ErrLakeUnstable        = errors.New("the lake changed under every copy")
	ErrTargetUnconditional = errors.New("the target refuses a create-if-absent put")
	ErrEngineNotReady      = errors.New("the database's engine is not ready")
)

// sqlRecordBytes is the size of one record of a database's file (ADR-0234 Decision 2).
const sqlRecordBytes = 8 << 20

// MaxManifestBytes is the largest manifest a Restore reads. A member's entry takes under 1 KiB, so this holds over 16,000
// members.
const MaxManifestBytes = 16 << 20

// Member is one store of a scope. Location is a Lake member's spec.location, which Write copies.
type Member struct {
	Kind     v1.Kind
	Name     v1.ObjectName
	Location v1.LakeLocation
	Volume   v1.ObjectName // with Kind Function: the Function's local volume (ADR-0262 Decision 11)
	// LakePrefixes, with Kind Bucket, are its Lake prefixes as Resolve read them, so a Lake applied during a run leaves each
	// byte in one member (ADR-0312).
	LakePrefixes []string
}

// Options are where Write puts the copy and to whom it seals it: Path is <ns>/<backup>-<uid>/ on Target, Recipients
// the fingerprints the manifest records.
type Options struct {
	Target     blob.Bucket
	Path       string
	Seal       backup.Seal
	Recipients []string
}

// MemberFile is one member's files in the manifest: a store's one, a lake's files then catalog.db, of Attempt.
type MemberFile struct {
	Kind    v1.Kind            `json:"kind"`
	Name    v1.ObjectName      `json:"name"`
	Attempt int                `json:"attempt,omitempty"`
	Volume  v1.ObjectName      `json:"volume,omitempty"`
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

// Resolve lists the members s names in ns (Decision 2), KVStores, Buckets, Lakes, SQLDatabases then Function volumes:
// by name, the stores of the App's kv, buckets, lakes and sql entries and its Functions' local volumes, or the stores of
// the ResourceGroup. In each form a Bucket brings in the Lakes on its Lake prefixes as their own members, once, after the
// Lakes s names, whichever App or ResourceGroup holds them (ADR-0312). A named member or App that does not exist is
// fault.NotFound naming it; a scope over limits.CopyMembers is fault.Invalid naming the limit.
func Resolve(ctx context.Context, st store.Store, ns v1.NamespaceName, s v1.BackupScope, limits Limits) ([]Member, error) {
	const op = "workload.Resolve"
	names := map[v1.Kind][]v1.ObjectName{
		v1.KindKVStore: s.KVStores, v1.KindBucket: s.Buckets, v1.KindLake: s.Lakes, v1.KindSQLDatabase: s.Databases,
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
		for _, e := range a.Spec.Lakes {
			names[v1.KindLake] = append(names[v1.KindLake], cmp.Or(e.Name, e.Ref))
		}
		for _, e := range a.Spec.SQL {
			names[v1.KindSQLDatabase] = append(names[v1.KindSQLDatabase], cmp.Or(e.Name, e.Ref))
		}
		for _, f := range a.Spec.Functions {
			for _, v := range f.Volumes {
				if f.Name != "" && v.Type == v1.VolumeLocal {
					s.Volumes = append(s.Volumes, string(f.Name)+"/"+string(v.VolumeName()))
				}
			}
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
	var out, brought []Member
	read := reader(ctx, st, ns)
	for _, k := range memberKinds() {
		for _, n := range names[k] {
			obj, err := read(k, n)
			if err != nil {
				return nil, fault.Wrapf(err, fault.KindOf(err), op, "member %s/%s", k, n)
			}
			m := Member{Kind: k, Name: n}
			switch o := obj.(type) {
			case *v1.Lake:
				m.Location = o.Spec.Location
			case *v1.Bucket:
				prefixes, err := lakePrefixes(o, false, read)
				if err != nil {
					return nil, err
				}
				for _, p := range prefixes {
					m.LakePrefixes = append(m.LakePrefixes, p.name)
					if !slices.Contains(names[v1.KindLake], p.owner) {
						brought = append(brought, Member{Kind: v1.KindLake, Name: p.owner, Location: v1.LakeLocation{Bucket: o.Name, Prefix: p.name}})
					}
				}
			}
			out = append(out, m)
		}
		if k == v1.KindLake {
			out = append(out, brought...)
		}
	}
	vols, err := volumeMembers(ctx, st, ns, s.Volumes)
	if err != nil {
		return nil, err
	}
	out = append(out, vols...)
	if len(out) > limits.CopyMembers {
		return nil, fault.Invalidf(op, "the scope resolves to %d members, more than the %d one copy may hold", len(out), limits.CopyMembers)
	}
	return out, nil
}

// lakePrefix is one Lake prefix of a Bucket, and the Lake that owns it.
type lakePrefix struct {
	name  string
	owner v1.ObjectName
}

// reader reads each object of ns once: one Resolve sees one Lake at one location, so what a Bucket's copy skips and what
// the Lake's member copies agree.
func reader(ctx context.Context, st store.Store, ns v1.NamespaceName) func(v1.Kind, v1.ObjectName) (v1.Object, error) {
	read := map[string]v1.Object{}
	return func(k v1.Kind, n v1.ObjectName) (v1.Object, error) {
		key := string(k) + "/" + string(n)
		if obj, ok := read[key]; ok {
			return obj, nil
		}
		obj, err := st.Get(ctx, k.GVK(), ns, n)
		if err == nil {
			read[key] = obj
		}
		return obj, err
	}
}

// lakePrefixes reads b's Lake prefixes (ADR-0312 Decision 1): those whose owner has kind Lake and is located there, in spec
// order. Every other prefix is the Bucket's. With gone it adds those whose owner Lake does not exist, which a Restore leaves
// alone, as a rewind would delete that Lake's data. The location test stays beside ADR-0312 Decision 7: a Lake owns every
// ownerless prefix of a bucket it defines, and sits on one of them.
func lakePrefixes(b *v1.Bucket, gone bool, read func(v1.Kind, v1.ObjectName) (v1.Object, error)) ([]lakePrefix, error) {
	var out []lakePrefix
	for _, p := range b.Spec.Prefixes {
		if p.Owner.Kind != v1.KindLake {
			continue
		}
		obj, err := read(v1.KindLake, p.Owner.Name)
		switch {
		case fault.KindOf(err) == fault.NotFound:
			if !gone {
				continue
			}
		case err != nil:
			return nil, fault.Wrapf(err, fault.KindOf(err), "workload.lakePrefixes", "get Lake %s/%s", b.Namespace, p.Owner.Name)
		default:
			if lk, ok := obj.(*v1.Lake); !ok || lk.Spec.Location != (v1.LakeLocation{Bucket: b.Name, Prefix: p.Name}) {
				continue
			}
		}
		out = append(out, lakePrefix{name: p.Name, owner: p.Owner.Name})
	}
	return out, nil
}

// volumeMembers resolves "<function>/<name>" scope entries: the Function exists and declares that local volume.
func volumeMembers(ctx context.Context, st store.Store, ns v1.NamespaceName, refs []string) ([]Member, error) {
	const op = "workload.Resolve"
	var out []Member
	for _, ref := range refs {
		fn, name, _ := strings.Cut(ref, "/")
		obj, err := st.Get(ctx, v1.KindFunction.GVK(), ns, v1.ObjectName(fn))
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "member volume %s", ref)
		}
		f, _ := obj.(*v1.Function)
		if f == nil || !slices.ContainsFunc(f.Spec.Volumes, func(v v1.VolumeEntry) bool { return v.Type == v1.VolumeLocal && string(v.VolumeName()) == name }) {
			return nil, fault.NotFoundf(op, "member volume %s: function %s declares no local volume %s", ref, fn, name)
		}
		out = append(out, Member{Kind: v1.KindFunction, Name: v1.ObjectName(fn), Volume: v1.ObjectName(name)})
	}
	return out, nil
}

func memberKinds() []v1.Kind {
	return []v1.Kind{v1.KindKVStore, v1.KindBucket, v1.KindLake, v1.KindSQLDatabase}
}

// KVPrefix is a KVStore's key prefix in the KV substrate, <ns>/<store>/ (ADR-0072).
func KVPrefix(ns v1.NamespaceName, name v1.ObjectName) string {
	return string(ns) + "/" + string(name) + "/"
}

// Write copies ms under o.Path, then puts the manifest (Decisions 4, 5): each member's kind among kinds writes its files
// under its own directory of the copy (ADR-0311), a KVStore to kv/<store>/, a Bucket to blob/<bucket>/, a lake to
// lake/<name>/<attempt>/, a SQLDatabase to sql/<name>/ and a Function's volume to volume/<function>/<volume>/. A member
// that no kind serves is fault.Invalid naming its kind, before the first put; a refused IfNotExist is
// ErrTargetUnconditional.
func Write(ctx context.Context, ns v1.NamespaceName, ms []Member, kinds []MemberKind, o Options) (Manifest, error) {
	const op = "workload.Write"
	served := make([]MemberKind, len(ms))
	for i, mb := range ms {
		j := slices.IndexFunc(kinds, func(k MemberKind) bool { return k.Kind() == mb.Kind })
		if j < 0 {
			return Manifest{}, fault.Invalidf(op, "member %s/%s: no member kind serves %s", mb.Kind, mb.Name, mb.Kind)
		}
		served[i] = kinds[j]
	}
	m := Manifest{
		Format: Format, Namespace: string(ns), Backup: path.Base(strings.TrimSuffix(o.Path, "/")), Funcd: version.Version,
		At: v1.NewTimestamp(time.Now()), Recipients: o.Recipients,
	}
	w := &writer{o: o, buf: make([]byte, 0, backup.PartBytes)}
	for i, mb := range ms {
		mf, err := served[i].Write(ctx, w, ns, mb)
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

// CatalogRecord is the key of a lake member's catalog record, wherever the catalog lives (ADR-0293 Decision 5).
const CatalogRecord = checkpointName

// keys lists the keys under prefix, relative to it.
func keys(ctx context.Context, b blob.Bucket, prefix string) ([]string, error) {
	const op = "workload.Write"
	var out []string
	after := ""
	for {
		items, more, err := b.ListAfter(ctx, prefix, after, listPage)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "list %q", prefix)
		}
		for _, it := range items {
			out = append(out, strings.TrimPrefix(it.Key, prefix))
		}
		if !more || len(items) == 0 {
			return out, nil
		}
		after = items[len(items)-1].Key
	}
}

// streamFile emits a stream (a database's SQLite file from its engine, a volume's tar) as records of sqlRecordBytes,
// keyed by their 1-based index in six digits; it never holds more than one record in memory.
func streamFile(op, what string, rc io.ReadCloser, emit func(snapshot.Record) error) error {
	defer func() { _ = rc.Close() }()
	buf := make([]byte, sqlRecordBytes)
	for i := 1; ; i++ {
		n, err := io.ReadFull(rc, buf)
		if n > 0 {
			if eerr := emit(snapshot.Record{Key: fmt.Appendf(nil, "%06d", i), Value: bytes.Clone(buf[:n])}); eerr != nil {
				return eerr
			}
		}
		switch {
		case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
			return nil
		case err != nil:
			return fault.Wrapf(err, fault.KindOf(err), op, "%s: read its file", what)
		}
	}
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

// File writes the records emit produces as the file name/, part-00000 first; an empty file is one empty part.
func (w *writer) File(ctx context.Context, name string, produce func(emit func(snapshot.Record) error) error) (backup.StoreFile, error) {
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

// ReadManifest reads the manifest under path; one over MaxManifestBytes or of any format but Format is fault.Invalid.
func ReadManifest(ctx context.Context, src blob.Bucket, path string) (Manifest, error) {
	const op = "workload.ReadManifest"
	data, err := readAtMost(ctx, src, path+manifestName, MaxManifestBytes)
	if err != nil {
		return Manifest{}, fault.Wrapf(err, fault.KindOf(err), op, "read %s%s", path, manifestName)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fault.Wrapf(err, fault.Invalid, op, "decode %s%s", path, manifestName)
	}
	if m.Format != Format {
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

// readAtMost reads the object at key, asking its target for no more bytes than it reports. An object it reports over limit
// is fault.Invalid naming it, with nothing read.
func readAtMost(ctx context.Context, src blob.Bucket, key string, limit int64) ([]byte, error) {
	attrs, err := src.Attributes(ctx, key)
	if err != nil {
		return nil, err
	}
	switch {
	case attrs.Size > limit:
		return nil, fault.Invalidf("workload.readAtMost", "%s holds %d bytes, and funcd writes at most %d", key, attrs.Size, limit)
	case attrs.Size <= 0:
		return []byte{}, nil
	}
	if rr, ok := src.(blob.RangeReader); ok {
		return rr.GetRange(ctx, key, 0, attrs.Size)
	}
	return src.Get(ctx, key)
}

// partReader reads a file's parts in order, one read each. The writer cuts a file's sealed bytes, so a part is at most
// backup.PartBytes, sealed or not.
type partReader struct {
	get   func(key string) ([]byte, error)
	dir   string
	parts int
	i     int
	cur   *bytes.Reader
}

func newPartReader(ctx context.Context, src blob.Bucket, dir string, parts int) *partReader {
	return &partReader{get: func(key string) ([]byte, error) { return readAtMost(ctx, src, key, backup.PartBytes) }, dir: dir, parts: parts}
}

func (r *partReader) Read(p []byte) (int, error) {
	for r.cur == nil || r.cur.Len() == 0 {
		if r.i >= r.parts {
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
