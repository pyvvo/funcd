package eventing

import (
	"context"
	"log/slog"
	"maps"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// defaultBlobPollInterval is the platform-wide blob poll cadence when none is configured (ADR-0119).
const defaultBlobPollInterval = 15 * time.Second

// BucketLister resolves a Bucket resource to its scoped blob listing surface (ADR-0007/0080) and lists a
// prefix over it. The V1 driver reuses pkg/funcd's s3BucketFor resolver — the SAME blob.Bucket view external
// S3-frontend writes land in — so detection is writer-agnostic (it depends only on List). A missing Bucket
// resolves as fault.NotFound.
type BucketLister interface {
	List(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) ([]blob.Attributes, error)
}

// watchEntry is one registered named blob event: which Bucket + key prefix it watches, the EventSource UID
// and the source's purge epoch at registration (ADR-0157).
type watchEntry struct {
	bucket     v1.ObjectName
	prefix     string
	uid        v1.UID
	purgeEpoch uint64
}

// sourceKey identifies one EventSource.
type sourceKey struct {
	ns     v1.NamespaceName
	source v1.ObjectName
}

// WatchHooks connect the BlobWatcher to the store; NewSource sets them before Run (ADR-0157 Decisions 8, 9).
type WatchHooks struct {
	Exists      func(ctx context.Context, src SourceRef) (bool, error)
	SaveFailing func(ctx context.Context, src SourceRef, failing map[v1.ObjectName]error) error // empty map clears
}

// saveState is one source's set of events whose last Save failed, and the set last reported on its status.
type saveState struct {
	failing  map[v1.ObjectName]error
	reported map[v1.ObjectName]bool
}

// BlobWatcher polls each registered blob EventSource, diffs each prefix against the persisted SeenList, and
// publishes one named CloudEvent per new or rewritten object through the existing ADR-0108 Publisher
// (ADR-0119, ADR-0157). Registration is driven by the KindEventSource reconciler (ADR-0015 one-per-gvk); Run
// is a side loop started by the pkg/funcd lifecycle. A key is recorded after its publish, so a crash between
// publish and save re-emits next poll (at-least-once; the Sensor/ADR-0118 absorb it).
type BlobWatcher struct {
	lister    BucketLister
	publisher Publisher
	marks     Watermark
	interval  time.Duration
	logger    *slog.Logger

	// recordMu is taken before mu. It orders a poll's Load and Save against Purge, so a poll in flight across a
	// Purge never re-creates the purged record; no Publish runs under it.
	recordMu sync.Mutex

	mu          sync.Mutex
	watches     map[eventKey]watchEntry
	purgeEpochs map[sourceKey]uint64
	warnedKeys  map[string]bool
	saves       map[sourceKey]*saveState
	hooks       WatchHooks
}

// NewBlobWatcher builds the watcher. lister/publisher/marks are required; interval ≤ 0 ⇒ the 15s default.
func NewBlobWatcher(lister BucketLister, pub Publisher, marks Watermark, interval time.Duration, log *slog.Logger) (*BlobWatcher, error) {
	if lister == nil {
		return nil, fault.Invalidf("eventing.NewBlobWatcher", "bucket lister is required")
	}
	if pub == nil {
		return nil, fault.Invalidf("eventing.NewBlobWatcher", "publisher is required")
	}
	if marks == nil {
		return nil, fault.Invalidf("eventing.NewBlobWatcher", "watermark is required")
	}
	if interval <= 0 {
		interval = defaultBlobPollInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &BlobWatcher{
		lister:      lister,
		publisher:   pub,
		marks:       marks,
		interval:    interval,
		logger:      log.With("component", "eventing.blobwatch"),
		watches:     map[eventKey]watchEntry{},
		purgeEpochs: map[sourceKey]uint64{},
		warnedKeys:  map[string]bool{},
		saves:       map[sourceKey]*saveState{},
	}, nil
}

// SetHooks installs the store hooks the start sweep and the save-failure condition use.
func (w *BlobWatcher) SetHooks(h WatchHooks) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.hooks = h
}

// Register (re)registers every named event of a blob source and prunes events dropped from the spec — the
// reconciler calls it on a Ready blob EventSource. It stamps the source's current purge epoch into each entry
// and never increments it. A pruned event keeps its record until the source is purged (ADR-0157 Decision 5).
func (w *BlobWatcher) Register(ns v1.NamespaceName, source v1.ObjectName, uid v1.UID, bs *v1.BlobSource) {
	w.mu.Lock()
	defer w.mu.Unlock()
	epoch := w.purgeEpochs[sourceKey{ns: ns, source: source}]
	want := make(map[eventKey]bool, len(bs.Events))
	for i := range bs.Events {
		ev := &bs.Events[i]
		k := eventKey{ns: ns, source: source, event: ev.Name}
		want[k] = true
		w.watches[k] = watchEntry{bucket: bs.Bucket, prefix: ev.Prefix, uid: uid, purgeEpoch: epoch}
	}
	st := w.saves[sourceKey{ns: ns, source: source}]
	for k := range w.watches {
		if k.ns == ns && k.source == source && !want[k] {
			delete(w.watches, k)
			if st != nil {
				delete(st.failing, k.event)
			}
		}
	}
}

// Deregister removes every named event of one EventSource and keeps its records (BucketNotFound).
func (w *BlobWatcher) Deregister(ns v1.NamespaceName, source v1.ObjectName) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dropSourceLocked(ns, source)
}

// Purge removes every named event of one EventSource and deletes its records (delete, kind change). The
// incremented purge epoch makes a poll in flight stop without saving, also across a same-UID Register.
func (w *BlobWatcher) Purge(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error {
	w.recordMu.Lock()
	w.mu.Lock()
	w.dropSourceLocked(ns, source)
	w.purgeEpochs[sourceKey{ns: ns, source: source}]++
	w.mu.Unlock()
	w.recordMu.Unlock()
	return w.marks.Delete(ctx, ns, source)
}

// dropSourceLocked removes a source's entries and forgets its failing set; w.mu must be held.
func (w *BlobWatcher) dropSourceLocked(ns v1.NamespaceName, source v1.ObjectName) {
	for k := range w.watches {
		if k.ns == ns && k.source == source {
			delete(w.watches, k)
		}
	}
	delete(w.saves, sourceKey{ns: ns, source: source})
}

// ActiveWatches reports the number of registered named blob events (observability + the reconcile scenario).
func (w *BlobWatcher) ActiveWatches() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watches)
}

// Run sweeps orphan records once, then polls the registered set every interval until ctx is cancelled.
// Started by the pkg/funcd lifecycle beside the timer Source.Run; it is not part of the reconciler.
func (w *BlobWatcher) Run(ctx context.Context) error {
	w.sweep(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// sweep deletes the records of sources the store no longer holds (ADR-0157 Decision 8). Records are listed
// before the store is read, so a source created meanwhile is never swept.
func (w *BlobWatcher) sweep(ctx context.Context) {
	w.mu.Lock()
	exists := w.hooks.Exists
	w.mu.Unlock()
	if exists == nil {
		return
	}
	srcs, err := w.marks.ListSources(ctx)
	if err != nil {
		w.logger.WarnContext(ctx, "blob record sweep: list failed", "error", err)
		return
	}
	for _, src := range srcs {
		ok, err := exists(ctx, src)
		if err != nil {
			w.logger.WarnContext(ctx, "blob record sweep: source lookup failed", "namespace", src.Namespace, "eventsource", src.Name, "error", err)
			continue
		}
		if ok {
			continue
		}
		if err := w.marks.Delete(ctx, src.Namespace, src.Name); err != nil {
			w.logger.WarnContext(ctx, "blob record sweep: delete failed", "namespace", src.Namespace, "eventsource", src.Name, "error", err)
		}
	}
}

// poll snapshots the registry, then lists-diffs-publishes each watched prefix once and reports changed
// save-failure sets.
func (w *BlobWatcher) poll(ctx context.Context) {
	w.mu.Lock()
	snapshot := make(map[eventKey]watchEntry, len(w.watches))
	for k, e := range w.watches {
		snapshot[k] = e
	}
	w.mu.Unlock()
	for k, e := range snapshot {
		if err := w.pollOne(ctx, k, e); err != nil {
			w.logger.WarnContext(ctx, "blob poll failed", "eventsource", k.source, "event", k.event, "bucket", e.bucket, "error", err)
		}
	}
	w.reportSaveFailures(ctx)
}

// pollOne lists e's prefix, fires every listed key that is unseen or whose version changed, prunes keys the
// listing no longer holds, and saves the record if it changed (ADR-0157 Decision 2). It skips, or stops
// without saving, once the live entry no longer matches e.
func (w *BlobWatcher) pollOne(ctx context.Context, k eventKey, e watchEntry) error {
	objs, err := w.lister.List(ctx, k.ns, e.bucket, e.prefix)
	if err != nil {
		return err
	}
	w.recordMu.Lock()
	if !w.matches(k, e) {
		w.recordMu.Unlock()
		return nil
	}
	rec, err := w.marks.Load(ctx, k.ns, k.source, k.event)
	w.recordMu.Unlock()
	if err != nil {
		return err
	}
	changed := false
	if rec.Bucket != e.bucket || rec.Prefix != e.prefix || rec.UID != e.uid {
		rec = SeenList{Bucket: e.bucket, Prefix: e.prefix, UID: e.uid, Seen: map[string]string{}}
		changed = true
	}
	listed := make(map[string]bool, len(objs))
	var fireErr error
	for i := range objs {
		o := objs[i]
		listed[o.Key] = true
		if fireErr != nil {
			continue
		}
		if !utf8.ValidString(o.Key) {
			w.warnInvalidKey(ctx, k, o.Key)
			continue
		}
		version := versionOf(o)
		if v, ok := rec.Seen[o.Key]; ok && v == version {
			continue
		}
		ev, berr := NewBlobEvent(k.ns, k.source, k.event, BlobEventData{
			Bucket:  string(e.bucket),
			Key:     o.Key,
			Size:    o.Size,
			Version: version,
			Time:    v1.NewTimestamp(o.ModTime),
		})
		if berr != nil {
			fireErr = berr
			continue
		}
		if !w.matches(k, e) {
			return nil
		}
		if perr := w.publisher.Publish(ctx, ev); perr != nil {
			fireErr = perr
			continue
		}
		rec.Seen[o.Key] = version
		changed = true
	}
	for key := range rec.Seen {
		if !listed[key] {
			delete(rec.Seen, key)
			changed = true
		}
	}
	saveErr := w.saveIfLive(ctx, k, e, rec, changed)
	if fireErr != nil {
		return fireErr
	}
	return saveErr
}

// saveIfLive saves rec if it changed, under the record lock and only while the live entry still matches e,
// and records the outcome for the SeenListSaved condition.
func (w *BlobWatcher) saveIfLive(ctx context.Context, k eventKey, e watchEntry, rec SeenList, changed bool) error {
	w.recordMu.Lock()
	defer w.recordMu.Unlock()
	if !w.matches(k, e) {
		return nil
	}
	var err error
	if changed {
		err = w.marks.Save(ctx, k.ns, k.source, k.event, rec)
	}
	w.noteSave(k, e, err)
	return err
}

// matches reports whether the live entry of k still equals the snapshotted e (bucket, prefix, UID, epoch).
func (w *BlobWatcher) matches(k eventKey, e watchEntry) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	live, ok := w.watches[k]
	return ok && live == e
}

// warnInvalidKey logs a non-UTF-8 key once per process: JSON would store it as U+FFFD and it would never match
// after a Load, so it is skipped (ADR-0157 Decision 1).
func (w *BlobWatcher) warnInvalidKey(ctx context.Context, k eventKey, key string) {
	w.mu.Lock()
	first := !w.warnedKeys[key]
	w.warnedKeys[key] = true
	w.mu.Unlock()
	if first {
		w.logger.WarnContext(ctx, "blob key is not valid UTF-8; skipped", "eventsource", k.source, "event", k.event, "key", strconv.Quote(key))
	}
}

// noteSave records whether k's record is persisted: a failed Save marks the event failing; a successful or
// unneeded one (the stored record already equals the listing) clears it. It records nothing once e is no
// longer k's live entry, so a Register prune or Deregister that ran during the Save is not undone.
func (w *BlobWatcher) noteSave(k eventKey, e watchEntry, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if live, ok := w.watches[k]; !ok || live != e {
		return
	}
	sk := sourceKey{ns: k.ns, source: k.source}
	st := w.saves[sk]
	if err == nil {
		if st != nil {
			delete(st.failing, k.event)
		}
		return
	}
	if st == nil {
		st = &saveState{failing: map[v1.ObjectName]error{}, reported: map[v1.ObjectName]bool{}}
		w.saves[sk] = st
	}
	st.failing[k.event] = err
}

// reportSaveFailures calls SaveFailing for each source whose failing set differs from the one last reported;
// a set counts as reported only after a successful status write, so a conflict retries at the next poll.
func (w *BlobWatcher) reportSaveFailures(ctx context.Context) {
	type pending struct {
		key     sourceKey
		st      *saveState
		failing map[v1.ObjectName]error
	}
	w.mu.Lock()
	report := w.hooks.SaveFailing
	var todo []pending
	for sk, st := range w.saves {
		if sameEvents(st.failing, st.reported) {
			if len(st.failing) == 0 {
				delete(w.saves, sk)
			}
			continue
		}
		todo = append(todo, pending{key: sk, st: st, failing: maps.Clone(st.failing)})
	}
	w.mu.Unlock()
	if report == nil {
		return
	}
	for _, p := range todo {
		if err := report(ctx, SourceRef{Namespace: p.key.ns, Name: p.key.source}, p.failing); err != nil {
			w.logger.WarnContext(ctx, "blob seen-list status write failed", "eventsource", p.key.source, "error", err)
			continue
		}
		w.mu.Lock()
		if w.saves[p.key] == p.st {
			p.st.reported = make(map[v1.ObjectName]bool, len(p.failing))
			for ev := range p.failing {
				p.st.reported[ev] = true
			}
		}
		w.mu.Unlock()
	}
}

// sameEvents reports whether the failing events equal the reported ones.
func sameEvents(failing map[v1.ObjectName]error, reported map[v1.ObjectName]bool) bool {
	if len(failing) != len(reported) {
		return false
	}
	for ev := range failing {
		if !reported[ev] {
			return false
		}
	}
	return true
}

// versionOf is the V1 object-version fingerprint (ADR-0119): a (ModTime,Size) pair, NOT a content ETag.
// blob.Attributes carries the content MD5 when the driver has one (ADR-0159); a per-object ETag from it is
// ADR-0119's follow-on additive `data.etag` field.
func versionOf(o blob.Attributes) string {
	return strconv.FormatInt(o.ModTime.UTC().UnixNano(), 10) + "-" + strconv.FormatInt(o.Size, 10)
}
