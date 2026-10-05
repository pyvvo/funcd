package eventing

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

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

// watchEntry is one registered named blob event: which Bucket + key prefix it watches.
type watchEntry struct {
	bucket v1.ObjectName
	prefix string
}

// BlobWatcher polls each registered blob EventSource, diffs each prefix against the persisted Watermark, and
// publishes one named CloudEvent per new object through the existing ADR-0108 Publisher (ADR-0119).
// Registration is driven by the KindEventSource reconciler (ADR-0015 one-per-gvk); Run is a side loop
// started by the pkg/funcd lifecycle, mirroring eventing.Source.Run for timers. Advance-on-emission gives
// at-least-once: a crash between publish and save re-emits next poll (the Sensor/ADR-0118 absorb it).
type BlobWatcher struct {
	lister    BucketLister
	publisher Publisher
	marks     Watermark
	interval  time.Duration
	logger    *slog.Logger

	mu      sync.Mutex
	watches map[eventKey]watchEntry
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
		lister:    lister,
		publisher: pub,
		marks:     marks,
		interval:  interval,
		logger:    log.With("component", "eventing.blobwatch"),
		watches:   map[eventKey]watchEntry{},
	}, nil
}

// Register (re)registers every named event of a blob source and prunes events dropped from the spec — the
// reconciler calls it on a Ready blob EventSource (ADR-0119).
func (w *BlobWatcher) Register(ns v1.NamespaceName, source v1.ObjectName, bs *v1.BlobSource) {
	w.mu.Lock()
	defer w.mu.Unlock()
	want := make(map[eventKey]bool, len(bs.Events))
	for i := range bs.Events {
		ev := &bs.Events[i]
		k := eventKey{ns: ns, source: source, event: ev.Name}
		want[k] = true
		w.watches[k] = watchEntry{bucket: bs.Bucket, prefix: ev.Prefix}
	}
	for k := range w.watches { // prune events removed from the spec
		if k.ns == ns && k.source == source && !want[k] {
			delete(w.watches, k)
		}
	}
}

// Deregister removes every named event of one EventSource (delete / kind-change).
func (w *BlobWatcher) Deregister(ns v1.NamespaceName, source v1.ObjectName) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k := range w.watches {
		if k.ns == ns && k.source == source {
			delete(w.watches, k)
		}
	}
}

// ActiveWatches reports the number of registered named blob events (observability + the reconcile scenario).
func (w *BlobWatcher) ActiveWatches() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watches)
}

// Run polls the registered set every interval until ctx is cancelled. Started by the pkg/funcd lifecycle
// beside the timer Source.Run; it is not part of the reconciler.
func (w *BlobWatcher) Run(ctx context.Context) error {
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

// poll snapshots the registry, then lists-diffs-publishes each watched prefix once.
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
}

// pollOne lists one watched prefix, publishes a named CloudEvent for every object new against the persisted
// cursor, then saves the advanced cursor. It advances on emission (at-least-once).
func (w *BlobWatcher) pollOne(ctx context.Context, k eventKey, e watchEntry) error {
	objs, err := w.lister.List(ctx, k.ns, e.bucket, e.prefix)
	if err != nil {
		return err
	}
	prior, err := w.marks.Load(ctx, k.ns, k.source, k.event)
	if err != nil {
		return err
	}
	next := advance(prior, objs)
	for i := range objs {
		o := objs[i]
		if !isNew(prior, o) {
			continue
		}
		ev, berr := NewBlobEvent(k.ns, k.source, k.event, BlobEventData{
			Bucket:  string(e.bucket),
			Key:     o.Key,
			Size:    o.Size,
			Version: versionOf(o),
			Time:    o.ModTime.UTC(),
		})
		if berr != nil {
			return berr
		}
		if perr := w.publisher.Publish(ctx, ev); perr != nil {
			return perr
		}
	}
	return w.marks.Save(ctx, k.ns, k.source, k.event, next)
}

// isNew applies the new-object rule against the PRIOR cursor (ADR-0119): newer ModTime, or the same newest
// ModTime but an unseen key (the tie-break set).
func isNew(prior Cursor, o blob.Attributes) bool {
	if o.ModTime.After(prior.MaxModTime) {
		return true
	}
	if o.ModTime.Equal(prior.MaxModTime) {
		for _, k := range prior.KeysAtMax {
			if k == o.Key {
				return false
			}
		}
		return true
	}
	return false
}

// advance computes the next cursor from the prior + the listed objects (ADR-0119): a non-regressing
// MaxModTime and the bounded key set AT that timestamp (prior tie keys carried when the max is unchanged).
func advance(prior Cursor, objs []blob.Attributes) Cursor {
	next := Cursor{MaxModTime: prior.MaxModTime}
	for i := range objs {
		if objs[i].ModTime.After(next.MaxModTime) {
			next.MaxModTime = objs[i].ModTime
		}
	}
	if next.MaxModTime.Equal(prior.MaxModTime) {
		next.KeysAtMax = append(next.KeysAtMax, prior.KeysAtMax...)
	}
	seen := make(map[string]bool, len(next.KeysAtMax))
	for _, k := range next.KeysAtMax {
		seen[k] = true
	}
	for i := range objs {
		if objs[i].ModTime.Equal(next.MaxModTime) && !seen[objs[i].Key] {
			next.KeysAtMax = append(next.KeysAtMax, objs[i].Key)
			seen[objs[i].Key] = true
		}
	}
	return next
}

// versionOf is the V1 object-version fingerprint (ADR-0119): a (ModTime,Size) pair, NOT a content ETag.
// blob.Attributes carries the content MD5 when the driver has one (ADR-0159); a per-object ETag from it is
// ADR-0119's follow-on additive `data.etag` field.
func versionOf(o blob.Attributes) string {
	return strconv.FormatInt(o.ModTime.UTC().UnixNano(), 10) + "-" + strconv.FormatInt(o.Size, 10)
}
