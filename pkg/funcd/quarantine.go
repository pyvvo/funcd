package funcd

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/clock"
	platformconfig "github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/store"
)

// quarantineBucketRoot is the blob store's quarantine of Buckets (ADR-0288 Decision 2), outside s3Root, which the S3
// gateway and bucketOrphans read.
const quarantineBucketRoot = "quarantine/bucket/"

// The sweep's pace (ADR-0288 Decision 5): its first run, then each next.
const (
	quarantineSweepFirst = 5 * time.Minute
	quarantineSweepEvery = time.Hour
)

// quarantinePeriod is storage.quarantineRetention, its default when unset.
func (c *config) quarantinePeriodOrDefault() time.Duration {
	if c.quarantinePeriod == 0 {
		return platformconfig.DefaultQuarantineRetention
	}
	return c.quarantinePeriod
}

// quarantineNow reads the quarantine's clock: entries' since and the sweep's cutoff.
func (c *config) quarantineNow() time.Time {
	if c.quarantineClock == nil {
		return clock.System().Now()
	}
	return c.quarantineClock.Now()
}

// bucketQuarantine is the Buckets' quarantine.Store and quarantine.Returner: entry <ns>/<bucket>/<since> holds its
// state at quarantine/bucket/<ns>/<bucket>/<since>/state, the Bucket's objects under …/<since>/data/ and the catalogs
// of the Lakes on its prefixes under …/<since>/catalogs/ (ADR-0293 Decision 6).
type bucketQuarantine struct {
	shared blob.Bucket
	store  store.Store
}

var (
	_ quarantine.Store    = bucketQuarantine{}
	_ quarantine.Returner = bucketQuarantine{}
)

func bucketEntryPrefix(ns v1.NamespaceName, name v1.ObjectName, since time.Time) string {
	return quarantineBucketRoot + string(ns) + "/" + string(name) + "/" + quarantine.Stamp(since) + "/"
}

func (q bucketQuarantine) mover(op string) (blob.PrefixMover, error) {
	m, ok := q.shared.(blob.PrefixMover)
	if !ok {
		return nil, fault.Unavailablef(op, "the blob store cannot move a prefix")
	}
	return m, nil
}

func (q bucketQuarantine) putState(ctx context.Context, prefix string, st quarantine.State) error {
	return q.shared.Put(ctx, prefix+"state", []byte(st), blob.PutOptions{ContentType: "text/plain"})
}

// Move moves ref's objects into its moving entry, or a new one dated now, with MovePrefix: one rename on a file store,
// a server-side copy and a delete per object on S3, resumed by the next start when cut (Decision 3).
func (q bucketQuarantine) Move(ctx context.Context, ref quarantine.Ref, now time.Time) (quarantine.Entry, error) {
	const op = "funcd.bucketQuarantine.Move"
	m, err := q.mover(op)
	if err != nil {
		return quarantine.Entry{}, err
	}
	entries, err := q.List(ctx)
	if err != nil {
		return quarantine.Entry{}, err
	}
	e := quarantine.Entry{Ref: ref, Since: now.UTC().Truncate(time.Millisecond), State: quarantine.Moving}
	for _, x := range entries {
		if x.Ref == ref && x.State == quarantine.Moving {
			e = x
		}
	}
	prefix := bucketEntryPrefix(ref.Namespace, ref.Name, e.Since)
	if err := q.putState(ctx, prefix, quarantine.Moving); err != nil {
		return quarantine.Entry{}, err
	}
	if err := m.MovePrefix(ctx, bucketPrefix(ref.Namespace, ref.Name), prefix+"data/"); err != nil {
		return quarantine.Entry{}, err
	}
	if err := m.MovePrefix(ctx, bucketCatalogPrefix(ref.Namespace, ref.Name), prefix+"catalogs/"); err != nil {
		return quarantine.Entry{}, err
	}
	if err := q.putState(ctx, prefix, quarantine.Kept); err != nil {
		return quarantine.Entry{}, err
	}
	e.State = quarantine.Kept
	return e, nil
}

// settle writes kept each moving entry with nothing left to move: its Bucket's prefix and catalogs are empty, or a
// Bucket of the name exists again, whose objects not yet moved stay with it (Decision 3).
func (q bucketQuarantine) settle(ctx context.Context) error {
	entries, err := q.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if e.State != quarantine.Moving {
			continue
		}
		left, err := q.holds(ctx, e.Namespace, e.Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if left {
			if _, gerr := q.store.Get(ctx, v1.KindBucket.GVK(), e.Namespace, e.Name); gerr != nil {
				if fault.KindOf(gerr) != fault.NotFound {
					errs = append(errs, gerr)
				}
				continue
			}
		}
		errs = append(errs, q.putState(ctx, bucketEntryPrefix(e.Namespace, e.Name, e.Since), quarantine.Kept))
	}
	return errors.Join(errs...)
}

// holds reports whether the Bucket ns/name has objects or catalogs left in place.
func (q bucketQuarantine) holds(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error) {
	for _, prefix := range []string{bucketPrefix(ns, name), bucketCatalogPrefix(ns, name)} {
		left, _, err := q.shared.ListAfter(ctx, prefix, "", 1)
		if err != nil || len(left) > 0 {
			return len(left) > 0, err
		}
	}
	return false, nil
}

// List returns each entry under quarantine/bucket/, reading one key per entry and seeking past the rest, as
// bucketOrphans does; an entry with objects and no state reads kept.
func (q bucketQuarantine) List(ctx context.Context) ([]quarantine.Entry, error) {
	const op = "funcd.bucketQuarantine.List"
	var out []quarantine.Entry
	after := ""
	for {
		items, _, err := q.shared.ListAfter(ctx, quarantineBucketRoot, after, 1)
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the quarantine")
		}
		if len(items) == 0 {
			return out, nil
		}
		key := items[0].Key
		parts := strings.SplitN(strings.TrimPrefix(key, quarantineBucketRoot), "/", 4)
		after = key
		if len(parts) < 4 {
			continue
		}
		ns, name := v1.NamespaceName(parts[0]), v1.ObjectName(parts[1])
		since, err := quarantine.ParseStamp(parts[2])
		if err != nil || ns.Validate() != nil || name.Validate() != nil {
			continue
		}
		prefix := bucketEntryPrefix(ns, name, since)
		after = max(prefix+string(utf8.MaxRune), key)
		st, err := q.state(ctx, prefix)
		if err != nil {
			return nil, err
		}
		out = append(out, quarantine.Entry{Ref: bucketRef(ns, name), Since: since, State: st})
	}
}

func bucketRef(ns v1.NamespaceName, name v1.ObjectName) quarantine.Ref {
	return quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindBucket, Namespace: ns, Name: name}}
}

func (q bucketQuarantine) state(ctx context.Context, prefix string) (quarantine.State, error) {
	data, err := q.shared.Get(ctx, prefix+"state")
	if fault.KindOf(err) == fault.NotFound {
		return quarantine.Kept, nil
	}
	if err != nil {
		return "", err
	}
	return quarantine.ParseState(string(data))
}

// Expire deletes every entry Expired at cutoff: its objects, then its state.
func (q bucketQuarantine) Expire(ctx context.Context, cutoff time.Time) ([]quarantine.Entry, error) {
	entries, err := q.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []quarantine.Entry
	var errs []error
	for _, e := range entries {
		if !e.Expired(cutoff) {
			continue
		}
		prefix := bucketEntryPrefix(e.Namespace, e.Name, e.Since)
		if err := errors.Join(deletePrefix(ctx, q.shared, prefix+"data/"), deletePrefix(ctx, q.shared, prefix+"catalogs/")); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := q.shared.Delete(ctx, prefix+"state"); err != nil && fault.KindOf(err) != fault.NotFound {
			errs = append(errs, err)
			continue
		}
		out = append(out, e)
	}
	return out, errors.Join(errs...)
}

// Return moves ref's entry back into the Bucket's prefix, then deletes its state (Decision 6). A kept entry needs an
// empty Bucket; a returning one resumes.
func (q bucketQuarantine) Return(ctx context.Context, ref quarantine.Ref, since time.Time) error {
	const op = "funcd.bucketQuarantine.Return"
	m, err := q.mover(op)
	if err != nil {
		return err
	}
	entries, err := q.List(ctx)
	if err != nil {
		return err
	}
	e, err := quarantine.Pick(entries, ref, since)
	if err != nil {
		return err
	}
	prefix, dst := bucketEntryPrefix(e.Namespace, e.Name, e.Since), bucketPrefix(ref.Namespace, ref.Name)
	if e.State == quarantine.Kept {
		left, err := q.holds(ctx, ref.Namespace, ref.Name)
		if err != nil {
			return err
		}
		if left {
			return fault.Conflictf(op, "%s holds objects: the quarantine returns only into an empty Bucket", ref)
		}
		if err := q.putState(ctx, prefix, quarantine.Returning); err != nil {
			return err
		}
	}
	if err := m.MovePrefix(ctx, prefix+"data/", dst); err != nil {
		return err
	}
	if err := m.MovePrefix(ctx, prefix+"catalogs/", bucketCatalogPrefix(ref.Namespace, ref.Name)); err != nil {
		return err
	}
	if err := q.shared.Delete(ctx, prefix+"state"); err != nil && fault.KindOf(err) != fault.NotFound {
		return err
	}
	return nil
}

// deletePrefix deletes every object under prefix, a page at a time.
func deletePrefix(ctx context.Context, b blob.Bucket, prefix string) error {
	for {
		items, more, err := b.ListAfter(ctx, prefix, "", 1000)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := b.Delete(ctx, it.Key); err != nil && fault.KindOf(err) != fault.NotFound {
				return err
			}
		}
		if !more {
			return nil
		}
	}
}

// quarantineStores are the platform's quarantines (ADR-0288 Decision 2), the sweep's and hold status's.
func (p *Platform) quarantineStores() []quarantine.Store {
	stores := []quarantine.Store{bucketQuarantine{shared: p.cfg.blob, store: p.cfg.store}}
	if p.kvReconciler != nil {
		stores = append(stores, p.kvReconciler.Quarantine())
	}
	if p.sqlReconciler != nil {
		stores = append(stores, p.sqlReconciler.Quarantine())
	}
	if p.fnReconciler != nil {
		if s := p.fnReconciler.Quarantine(); s != nil { // nil while volumes are off
			stores = append(stores, s)
		}
	}
	return stores
}

// runQuarantineSweep runs sweepQuarantine quarantineSweepFirst after start, then every quarantineSweepEvery, until ctx
// ends: never inside the start (Decision 5).
func (p *Platform) runQuarantineSweep(ctx context.Context) {
	timer := time.NewTimer(quarantineSweepFirst)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.sweepQuarantine(ctx)
			timer.Reset(quarantineSweepEvery)
		}
	}
}

// sweepQuarantine deletes the entries one period old, unless the platform is held (ADR-0206 Decision 6).
func (p *Platform) sweepQuarantine(ctx context.Context) {
	if p.cfg.gate().Held() {
		return
	}
	cutoff := p.cfg.quarantineNow().Add(-p.cfg.quarantinePeriodOrDefault())
	for _, s := range p.quarantineStores() {
		gone, err := s.Expire(ctx, cutoff)
		for _, e := range gone {
			p.logger.LogAttrs(ctx, slog.LevelInfo, "deleted quarantined data", quarantineAttrs(e)...)
		}
		if err != nil && ctx.Err() == nil {
			p.logger.WarnContext(ctx, "quarantine sweep incomplete", "error", err)
		}
	}
}

func quarantineAttrs(e quarantine.Entry) []slog.Attr {
	attrs := []slog.Attr{slog.String("kind", string(e.Kind)), slog.String("namespace", string(e.Namespace)),
		slog.String("name", string(e.Name))}
	if e.Volume != "" {
		attrs = append(attrs, slog.String("volume", string(e.Volume)))
	}
	return append(attrs, slog.String("since", e.Since.Format(time.RFC3339Nano)))
}

// reclaimDeletedBuckets moves into the quarantine the objects of every Bucket prefix no Bucket names (ADR-0288,
// before ADR-0199 Decision 7's purge): a crash between a Bucket's delete and its second purge, or a restore that
// brought back objects its metastore names no Bucket for. It then settles the moving entries.
func reclaimDeletedBuckets(ctx context.Context, shared blob.Bucket, st store.Store, logger *slog.Logger, now time.Time,
	period time.Duration) error {
	q := bucketQuarantine{shared: shared, store: st}
	orphans, err := bucketOrphans(ctx, shared, st)
	errs := []error{err}
	for _, b := range orphans {
		e, merr := q.Move(ctx, bucketRef(b.Namespace, b.Name), now)
		if merr != nil {
			errs = append(errs, merr)
			continue
		}
		logger.WarnContext(ctx, "moved the data of a Bucket with no object into the quarantine", "namespace", b.Namespace,
			"name", b.Name, "until", e.Until(period).Format(time.RFC3339))
	}
	errs = append(errs, q.settle(ctx))
	return errors.Join(errs...)
}

func (h holdPlatform) Quarantine(ctx context.Context) ([]hold.QuarantineEntry, error) {
	period := h.p.cfg.quarantinePeriodOrDefault()
	var out []hold.QuarantineEntry
	for _, s := range h.p.quarantineStores() {
		entries, err := s.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			out = append(out, hold.QuarantineEntry{Kind: e.Kind, Namespace: e.Namespace, Name: e.Name, Volume: e.Volume,
				Since: v1.NewTimestamp(e.Since), Until: v1.NewTimestamp(e.Until(period)), State: string(e.State)})
		}
	}
	slices.SortFunc(out, func(a, b hold.QuarantineEntry) int {
		return strings.Compare(string(a.Kind)+"/"+string(a.Namespace)+"/"+string(a.Name)+"/"+string(a.Volume),
			string(b.Kind)+"/"+string(b.Namespace)+"/"+string(b.Name)+"/"+string(b.Volume))
	})
	return out, nil
}

// ReturnQuarantined checks that the object exists again, then returns its entry (ADR-0288 Decision 6).
func (h holdPlatform) ReturnQuarantined(ctx context.Context, r hold.QuarantineReturn) error {
	const op = "funcd.ReturnQuarantined"
	if err := r.Namespace.Validate(); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "namespace")
	}
	if err := r.Name.Validate(); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "name")
	}
	var ret quarantine.Returner
	switch r.Kind {
	case v1.KindKVStore:
		if h.p.kvReconciler != nil {
			ret = h.p.kvReconciler.Quarantine()
		}
	case v1.KindBucket:
		ret = bucketQuarantine{shared: h.p.cfg.blob, store: h.p.cfg.store}
	case v1.KindFunction:
		if err := r.Volume.Validate(); err != nil {
			return fault.Wrapf(err, fault.Invalid, op, "a Function's quarantine returns a volume: volume")
		}
		if h.p.fnReconciler != nil {
			ret, _ = h.p.fnReconciler.Quarantine().(quarantine.Returner)
		}
	default:
		return fault.Invalidf(op, "kind %q: the quarantine returns KVStore, Bucket and Function volume data", r.Kind)
	}
	if r.Kind != v1.KindFunction && r.Volume != "" {
		return fault.Invalidf(op, "volume %q applies to kind Function", r.Volume)
	}
	if ret == nil {
		return fault.Unavailablef(op, "no %s quarantine on this daemon", r.Kind)
	}
	if _, err := h.p.cfg.store.Get(ctx, r.Kind.GVK(), r.Namespace, r.Name); err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return fault.Conflictf(op, "%s %s/%s does not exist: apply it again, then return its data", r.Kind, r.Namespace, r.Name)
		}
		return err
	}
	var since time.Time
	if r.Since != nil {
		since = time.Time(*r.Since)
	}
	ref := quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}, Volume: r.Volume}
	return ret.Return(ctx, ref, since)
}
