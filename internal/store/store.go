// Package store is the metastore / database-layer port (ADR-0006): a generic,
// watchable, versioned record store over ADR-0003 v1alpha1.Object values.
//
// The store SEMANTICS (resourceVersion minting, generation bumping, optimistic
// concurrency, in-process watch, filtering, at-rest encryption) live here ONCE,
// over a minimal key/value Engine seam. Engines (memory, badger) are
// thin and swappable; New wraps any Engine with these semantics.
package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// metaBucket holds store bookkeeping: the monotonic revision counter and the
// store's timeline (ADR-0202). The leading NUL keeps it disjoint from every
// GroupVersionKind.String() bucket and sorts its records first in a snapshot.
const (
	metaBucket     = "\x00store-meta"
	revisionKey    = "revision"
	timelineKey    = "timeline"
	defaultRingCap = 1024
	watchChanBuf   = 64
)

// The meta records' snapshot keys, bucket+NUL+key as every Engine emits them.
const (
	revisionRecordKey = metaBucket + "\x00" + revisionKey
	timelineRecordKey = metaBucket + "\x00" + timelineKey
)

// Store is the metastore port: generic CRUD + List + in-process Watch over
// v1alpha1.Object, keyed by (GroupVersionKind, NamespaceName, ObjectName).
// Every method is ctx-first; every error is an api/fault kind.
type Store interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
	List(ctx context.Context, gvk v1.GroupVersionKind, opts ListOptions) (List, error)
	Create(ctx context.Context, obj v1.Object) (v1.Object, error) // stamps uid, generation=1, resourceVersion, creationTimestamp
	Update(ctx context.Context, obj v1.Object) (v1.Object, error) // RV precondition; bumps generation iff spec changed
	Delete(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, rv string) error
	Watch(ctx context.Context, gvk v1.GroupVersionKind, opts WatchOptions) (Watch, error)
	Close() error
	// Snapshot is Engine.Snapshot without the timeline record (ADR-0202); it returns "<timeline>-<revision>"
	// from the meta records of the same stream, which come first; no revision record reads as 0.
	snapshot.Source
}

// ListOptions narrows a List by namespace, resource group, and tag subset.
type ListOptions struct {
	Namespace     v1.NamespaceName
	ResourceGroup v1.ResourceGroupName
	Tags          v1.Tags
}

// List is the result of a List call: the matching objects plus the collection
// resourceVersion (the store-wide revision at read time).
type List struct {
	Items           []v1.Object
	ResourceVersion string
}

// WatchOptions narrows a Watch and optionally replays from a resourceVersion.
type WatchOptions struct {
	Namespace            v1.NamespaceName
	SinceResourceVersion string
}

// EventType is the kind of change a Watch reports.
type EventType string

const (
	Added    EventType = "Added"
	Modified EventType = "Modified"
	Deleted  EventType = "Deleted"
)

// Event is a single change delivered on a Watch stream.
type Event struct {
	Type   EventType
	Object v1.Object
}

// Watch is an open change stream. Stop releases it (idempotent); ResultChan is
// closed when the watch ends (Stop, ctx cancellation, or a dropped slow watcher).
type Watch interface {
	ResultChan() <-chan Event
	Stop()
}

// Encryptor is the at-rest encryption seam (Decision §7). P-P/F15 supplies the
// implementation (tink/envelope); the store stays generic — it encrypts opaque
// value bytes for the configured kinds. With no encryptor, values are stored as-is.
type Encryptor interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// Option configures a Store at construction (functional options, ADR-0002).
type Option func(*config) error

type config struct {
	enc map[v1.Kind]Encryptor
}

// WithEncryptor applies at-rest encryption to stored values of the named kinds
// (e.g. v1alpha1.KindSecret). Returns an error if the encryptor is nil.
func WithEncryptor(kinds []v1.Kind, enc Encryptor) Option {
	return func(c *config) error {
		if enc == nil {
			return fault.Invalidf("store.WithEncryptor", "encryptor must not be nil")
		}
		for _, k := range kinds {
			c.enc[k] = enc
		}
		return nil
	}
}

// Engine is the minimal key/value seam the store is built on. memory + badger
// implement it.
//
// Update is contractually only an ATOMIC WRITE-BATCH (all-or-nothing); the seam
// does NOT promise serializable, in-txn read-your-writes isolation, so the store
// must not rely on it. Cross-writer serialization (the resourceVersion
// compare-and-set) is the store wrapper's job (a write mutex), not the engine's —
// an engine may happen to be serializable (badger's txns are), but the store
// treats the seam conservatively.
//
// View runs read-only operations. The seam does not guarantee a consistent
// point-in-time snapshot across multiple reads on every engine (the memory engine
// holds a read lock, so it is; not all engines must). Consequently List's collection
// resourceVersion is best-effort under concurrent writes; a caller needing a strict
// snapshot re-reads at the returned resourceVersion.
//
// Snapshot emits every record, the timeline included, from one read with Key =
// bucket+NUL+key, and returns "". Load runs before New on an empty engine: it splits
// each Key at its last NUL (keys hold none; metaBucket starts with one) and skips the
// record IsTimelineRecord names, so the next New mints a new timeline (ADR-0202).
type Engine interface {
	View(ctx context.Context, fn func(Txn) error) error
	Update(ctx context.Context, fn func(Txn) error) error
	snapshot.Source
	snapshot.Loader
	Close() error
}

// IsTimelineRecord reports whether (bucket, key) is the store's timeline record, the record every
// Engine.Load skips (ADR-0202 Decision 3).
func IsTimelineRecord(bucket, key string) bool {
	return bucket == metaBucket && key == timelineKey
}

// Txn is the key/value surface within a View/Update. Keys are scoped by bucket.
type Txn interface {
	Get(bucket, key string) ([]byte, bool, error)
	Put(bucket, key string, val []byte) error
	Delete(bucket, key string) error
	Scan(bucket string, fn func(key string, val []byte) error) error
}

// store is the wrapper holding the semantics over an Engine.
type store struct {
	eng      Engine
	enc      map[v1.Kind]Encryptor
	initErr  error  // fail-closed: set if an Option, the initial meta read or the timeline mint failed
	timeline string // this store's timeline (ADR-0202), set by New and never changed

	writeMu sync.Mutex // serializes writers for the resourceVersion compare-and-set

	mu        sync.Mutex // guards rev, subs, ring
	rev       uint64     // latest published revision (the watch cursor high-water mark)
	subs      map[int]*subscriber
	nextSubID int
	ring      []recordedEvent
	ringCap   int
}

// New wraps an Engine with the store semantics. Options configure at-rest
// encryption. The returned Store is safe for concurrent use.
func New(e Engine, opts ...Option) Store {
	c := &config{enc: map[v1.Kind]Encryptor{}}
	s := &store{eng: e, enc: c.enc, subs: map[int]*subscriber{}, ringCap: defaultRingCap}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			s.initErr = err
			return s
		}
	}
	if err := s.loadMeta(context.Background()); err != nil {
		s.initErr = err
	}
	return s
}

// loadMeta preloads the persisted revision, so the watch cursor is correct after a
// restart, and the timeline, minting and storing one when there is none: at first
// start, at the first start after ADR-0202, and after any Engine.Load.
func (s *store) loadMeta(ctx context.Context) error {
	const op = "store.New"
	err := s.eng.View(ctx, func(tx Txn) error {
		rv, err := readRevision(tx)
		if err != nil {
			return err
		}
		s.rev = rv
		raw, found, err := tx.Get(metaBucket, timelineKey)
		if err != nil || !found {
			return err
		}
		if !isLowerHex(string(raw)) {
			return fault.Internalf(op, "corrupt timeline %q", raw)
		}
		s.timeline = string(raw)
		return nil
	})
	if err != nil || s.timeline != "" {
		return err
	}
	tl, err := newTimeline()
	if err != nil {
		return err
	}
	if err := s.eng.Update(ctx, func(tx Txn) error { return tx.Put(metaBucket, timelineKey, []byte(tl)) }); err != nil {
		return err
	}
	s.timeline = tl
	return nil
}

// version formats revision n as a resourceVersion of this store's timeline.
func (s *store) version(n uint64) string { return Version{Timeline: s.timeline, N: n}.String() }

// Snapshot emits the engine's records but the timeline record, from one read, and
// returns "<timeline>-<revision>" of that read (ADR-0202 Decision 1).
func (s *store) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) {
	if s.initErr != nil {
		return "", s.initErr
	}
	var v Version
	if _, err := s.eng.Snapshot(ctx, func(r snapshot.Record) error {
		switch string(r.Key) {
		case timelineRecordKey:
			v.Timeline = string(r.Value)
			return nil
		case revisionRecordKey:
			n, err := parseRevision(r.Value)
			if err != nil {
				return err
			}
			v.N = n
		}
		return emit(r)
	}); err != nil {
		return "", err
	}
	return v.String(), nil
}

func (s *store) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	bucket := gvk.String()
	key := keyFor(ns, name)
	var out v1.Object
	err := s.eng.View(ctx, func(tx Txn) error {
		raw, found, err := tx.Get(bucket, key)
		if err != nil {
			return err
		}
		if !found {
			return fault.NotFoundf("store.Get", "%s %q not found", gvk.Kind, name)
		}
		obj, err := s.decode(ctx, gvk.Kind, raw)
		if err != nil {
			return err
		}
		out = obj
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *store) List(ctx context.Context, gvk v1.GroupVersionKind, opts ListOptions) (List, error) {
	if s.initErr != nil {
		return List{}, s.initErr
	}
	bucket := gvk.String()
	var items []v1.Object
	var rev uint64
	err := s.eng.View(ctx, func(tx Txn) error {
		if err := tx.Scan(bucket, func(_ string, val []byte) error {
			obj, err := s.decode(ctx, gvk.Kind, val)
			if err != nil {
				return err
			}
			if matches(obj, opts) {
				items = append(items, obj)
			}
			return nil
		}); err != nil {
			return err
		}
		r, err := readRevision(tx)
		if err != nil {
			return err
		}
		rev = r
		return nil
	})
	if err != nil {
		return List{}, err
	}
	sort.Slice(items, func(i, j int) bool {
		mi, mj := items[i].GetObjectMeta(), items[j].GetObjectMeta()
		if mi.Namespace != mj.Namespace {
			return mi.Namespace < mj.Namespace
		}
		return mi.Name < mj.Name
	})
	return List{Items: items, ResourceVersion: s.version(rev)}, nil
}

// Create persists a new object. When the object carries an empty Name but a GenerateName prefix
// (ObjectMeta.GenerateName), the store assigns Name = GenerateName + a random suffix and retries on the
// near-impossible collision — a client can create without inventing a unique name. Otherwise Name is
// required (an empty Name with no GenerateName fails validation, unchanged).
func (s *store) Create(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	if obj.GetObjectMeta().GenerateName == "" {
		return s.createOnce(ctx, obj) // normal path: Name required, single attempt
	}
	// generateName path: work on a clone (the caller's input stays read-only) and regenerate the name on
	// a collision, bounded.
	work, cerr := cloneObject(obj)
	if cerr != nil {
		return nil, cerr
	}
	wm := work.GetObjectMeta()
	for attempt := 0; attempt < 8; attempt++ {
		if wm.Name == "" {
			wm.Name = v1.GenerateObjectName(wm.GenerateName)
		}
		created, err := s.createOnce(ctx, work)
		if err == nil {
			return created, nil
		}
		if fault.KindOf(err) != fault.Conflict {
			return nil, err
		}
		wm.Name = "" // collided — regenerate on the next attempt
	}
	return nil, fault.Conflictf("store.Create", "could not assign a unique %s name after 8 tries", work.GroupVersionKind().Kind)
}

func (s *store) createOnce(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	if err := obj.Validate(); err != nil {
		return nil, err
	}
	// Stamp server fields on a clone — GetObjectMeta aliases the caller's struct, so
	// mutating it would silently inject uid/generation/resourceVersion into the caller's
	// input. The input is read-only; the returned + published object is the store's.
	obj, err := cloneObject(obj)
	if err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	gvk := obj.GroupVersionKind()
	meta := obj.GetObjectMeta()
	bucket := gvk.String()
	key := keyFor(meta.Namespace, meta.Name)

	uid, err := newUID()
	if err != nil {
		return nil, err
	}
	var rev uint64
	err = s.eng.Update(ctx, func(tx Txn) error {
		if _, found, gerr := tx.Get(bucket, key); gerr != nil {
			return gerr
		} else if found {
			return fault.Conflictf("store.Create", "%s %q already exists", gvk.Kind, meta.Name)
		}
		r, nerr := nextRevision(tx)
		if nerr != nil {
			return nerr
		}
		rev = r
		meta.UID = uid
		meta.Generation = 1
		meta.ResourceVersion = s.version(rev)
		meta.CreationTime = v1.NewTimestamp(time.Now())
		val, eerr := s.encode(ctx, gvk.Kind, obj)
		if eerr != nil {
			return eerr
		}
		return tx.Put(bucket, key, val)
	})
	if err != nil {
		return nil, err
	}
	// Return the stamped work object to the caller and publish an INDEPENDENT clone to
	// watchers, so a caller mutating the returned value cannot corrupt the watch stream
	// (Event.Object is an interface — a shared pointee would alias the ring + subscribers).
	published, err := cloneObject(obj)
	if err != nil {
		return nil, err
	}
	s.publish(rev, Event{Type: Added, Object: published})
	return obj, nil
}

func (s *store) Update(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	if err := obj.Validate(); err != nil {
		return nil, err
	}
	meta := obj.GetObjectMeta()
	if meta.ResourceVersion == "" {
		return nil, fault.Invalidf("store.Update", "resourceVersion is required for update")
	}
	// Stamp on a clone so the caller's input object is never mutated (see Create).
	obj, err := cloneObject(obj)
	if err != nil {
		return nil, err
	}
	meta = obj.GetObjectMeta()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	gvk := obj.GroupVersionKind()
	bucket := gvk.String()
	key := keyFor(meta.Namespace, meta.Name)

	// Field policy: Update preserves the store-owned uid + creationTime from the current
	// stored object and re-stamps generation (on spec change) + resourceVersion; every
	// other ObjectMeta/spec/status field comes from the caller (last-writer-wins under
	// the RV precondition).
	var rev uint64
	var noop bool
	err = s.eng.Update(ctx, func(tx Txn) error {
		raw, found, gerr := tx.Get(bucket, key)
		if gerr != nil {
			return gerr
		}
		if !found {
			return fault.NotFoundf("store.Update", "%s %q not found", gvk.Kind, meta.Name)
		}
		cur, derr := s.decode(ctx, gvk.Kind, raw)
		if derr != nil {
			return derr
		}
		curMeta := cur.GetObjectMeta()
		if curMeta.ResourceVersion != meta.ResourceVersion {
			return fault.Conflictf("store.Update", "%s %q resourceVersion mismatch", gvk.Kind, meta.Name)
		}
		gen := curMeta.Generation
		changed, cerr := specChanged(cur, obj)
		if cerr != nil {
			return cerr
		}
		if changed {
			gen++
		}
		meta.UID = curMeta.UID
		meta.Generation = gen
		meta.CreationTime = curMeta.CreationTime
		// No-op coalescing (ADR-0047): align the incoming RV to the stored one, then compare.
		// A byte-identical Update changes nothing — skip the revision bump, the Put, and the
		// watch event, so a reconcile that observed no change does not self-trigger another
		// (control-loop quiescence). The compare runs AFTER the RV precondition above, so
		// optimistic concurrency (the loser of two racing writes hits Conflict) is unaffected.
		meta.ResourceVersion = curMeta.ResourceVersion
		same, eqErr := equalContent(cur, obj)
		if eqErr != nil {
			return eqErr
		}
		if same {
			noop = true
			return nil
		}
		r, nerr := nextRevision(tx)
		if nerr != nil {
			return nerr
		}
		rev = r
		meta.ResourceVersion = s.version(rev)
		val, eerr := s.encode(ctx, gvk.Kind, obj)
		if eerr != nil {
			return eerr
		}
		return tx.Put(bucket, key, val)
	})
	if err != nil {
		return nil, err
	}
	if noop {
		// Nothing changed: no revision advance, no Modified event. `obj` already carries the
		// stored object's uid/generation/creationTime/resourceVersion (set above) + identical content.
		return obj, nil
	}
	// Independent clone for the watch stream; return the stamped work object (see Create).
	published, err := cloneObject(obj)
	if err != nil {
		return nil, err
	}
	s.publish(rev, Event{Type: Modified, Object: published})
	return obj, nil
}

// equalContent reports whether a and b serialize to identical JSON. json.Marshal sorts map
// keys, so the compare is order-stable (Tags, Config.Data, labels). Callers align the
// store-owned fields (uid/generation/creationTime/resourceVersion) before comparing, so only
// a real spec/status/meta change yields inequality — the basis for no-op-write coalescing (ADR-0047).
func equalContent(a, b v1.Object) (bool, error) {
	const op = "store.equalContent"
	ab, err := json.Marshal(a)
	if err != nil {
		return false, fault.Wrapf(err, fault.Internal, op, "marshal stored object")
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false, fault.Wrapf(err, fault.Internal, op, "marshal incoming object")
	}
	return bytes.Equal(ab, bb), nil
}

func (s *store) Delete(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, rv string) error {
	if s.initErr != nil {
		return s.initErr
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	bucket := gvk.String()
	key := keyFor(ns, name)

	var (
		rev     uint64
		deleted v1.Object
	)
	err := s.eng.Update(ctx, func(tx Txn) error {
		raw, found, gerr := tx.Get(bucket, key)
		if gerr != nil {
			return gerr
		}
		if !found {
			return fault.NotFoundf("store.Delete", "%s %q not found", gvk.Kind, name)
		}
		cur, derr := s.decode(ctx, gvk.Kind, raw)
		if derr != nil {
			return derr
		}
		if rv != "" && cur.GetObjectMeta().ResourceVersion != rv {
			return fault.Conflictf("store.Delete", "%s %q resourceVersion mismatch", gvk.Kind, name)
		}
		r, nerr := nextRevision(tx)
		if nerr != nil {
			return nerr
		}
		rev = r
		deleted = cur
		return tx.Delete(bucket, key)
	})
	if err != nil {
		return err
	}
	// The event is the change at rev, and an event's resourceVersion is the watch cursor (ADR-0006 §2).
	deleted.GetObjectMeta().ResourceVersion = s.version(rev)
	s.publish(rev, Event{Type: Deleted, Object: deleted})
	return nil
}

func (s *store) Close() error {
	s.mu.Lock()
	for id, sub := range s.subs {
		sub.stop()
		delete(s.subs, id)
	}
	s.mu.Unlock()
	return s.eng.Close()
}

// --- helpers ---

// keyFor builds the per-object key. Cluster-scoped kinds carry an empty
// namespace, yielding "/name"; namespaced kinds yield "namespace/name".
func keyFor(ns v1.NamespaceName, name v1.ObjectName) string {
	return string(ns) + "/" + string(name)
}

func newUID() (v1.UID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fault.Internalf("store.newUID", "generate uid: %v", err)
	}
	return v1.UID(hex.EncodeToString(b[:])), nil
}

func readRevision(tx Txn) (uint64, error) {
	raw, found, err := tx.Get(metaBucket, revisionKey)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	return parseRevision(raw)
}

func parseRevision(raw []byte) (uint64, error) {
	n, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return 0, fault.Internalf("store.readRevision", "corrupt revision %q: %v", raw, err)
	}
	return n, nil
}

func nextRevision(tx Txn) (uint64, error) {
	cur, err := readRevision(tx)
	if err != nil {
		return 0, err
	}
	next := cur + 1
	if err := tx.Put(metaBucket, revisionKey, []byte(strconv.FormatUint(next, 10))); err != nil {
		return 0, err
	}
	return next, nil
}

// encode marshals obj to JSON, encrypting the value for kinds with an Encryptor.
func (s *store) encode(ctx context.Context, k v1.Kind, obj v1.Object) ([]byte, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fault.Internalf("store.encode", "marshal %s: %v", k, err)
	}
	if enc, ok := s.enc[k]; ok {
		ct, eerr := enc.Encrypt(ctx, raw)
		if eerr != nil {
			return nil, fault.Internalf("store.encode", "encrypt %s: %v", k, eerr)
		}
		return ct, nil
	}
	return raw, nil
}

// decode reverses encode: decrypts (if configured) then unmarshals into a typed Object.
func (s *store) decode(ctx context.Context, k v1.Kind, data []byte) (v1.Object, error) {
	if enc, ok := s.enc[k]; ok {
		pt, err := enc.Decrypt(ctx, data)
		if err != nil {
			return nil, fault.Internalf("store.decode", "decrypt %s: %v", k, err)
		}
		data = pt
	}
	obj, ok := v1.NewObject(k)
	if !ok {
		return nil, fault.Internalf("store.decode", "unknown kind %q", k)
	}
	if err := json.Unmarshal(data, obj); err != nil {
		return nil, fault.Internalf("store.decode", "unmarshal %s: %v", k, err)
	}
	return obj, nil
}

// cloneObject deep-copies via a JSON round-trip so returned/published objects do
// not alias the caller's value.
func cloneObject(obj v1.Object) (v1.Object, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fault.Internalf("store.clone", "marshal: %v", err)
	}
	fresh, ok := v1.NewObject(obj.GroupVersionKind().Kind)
	if !ok {
		return nil, fault.Internalf("store.clone", "unknown kind %q", obj.GroupVersionKind().Kind)
	}
	if err := json.Unmarshal(raw, fresh); err != nil {
		return nil, fault.Internalf("store.clone", "unmarshal: %v", err)
	}
	return fresh, nil
}

// specChanged reports whether the JSON "spec" sub-tree differs between a and b.
// The generic Object exposes no typed accessor, so generation tracks the spec
// via its serialized form (ADR-0006 §2).
func specChanged(a, b v1.Object) (bool, error) {
	sa, err := specOf(a)
	if err != nil {
		return false, err
	}
	sb, err := specOf(b)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(sa, sb), nil
}

func specOf(obj v1.Object) ([]byte, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fault.Internalf("store.specOf", "marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fault.Internalf("store.specOf", "unmarshal: %v", err)
	}
	return m["spec"], nil
}

// matches applies the ListOptions filters to an object.
func matches(obj v1.Object, opts ListOptions) bool {
	meta := obj.GetObjectMeta()
	if opts.Namespace != "" && meta.Namespace != opts.Namespace {
		return false
	}
	if opts.ResourceGroup != "" && meta.ResourceGroup != opts.ResourceGroup {
		return false
	}
	for k, v := range opts.Tags {
		if meta.Tags[k] != v {
			return false
		}
	}
	return true
}
