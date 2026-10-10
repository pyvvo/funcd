// Package blobmirror mirrors the local blob store to the platform backup target (ADR-0208 Decision 4): each run
// freezes the store into a hard-link image, uploads the objects whose id the current epoch lacks, then an index and a
// manifest, written last; nothing on the target is ever read, overwritten or deleted. Restore puts a generation back.
package blobmirror

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/version"
)

// Format is the mirror manifest's format. Object.Gen is the n of objects/<id>/<n>/; Object.SHA256 is the hex SHA-256
// of its stored bytes, all parts in order.
const Format = 1

// The layout under the target root.
const (
	rootPrefix   = "blob/"
	manifestName = "manifest.yaml"
	shaPrefix    = "sha256-"
)

// Config builds a Mirror. Target is backup.target; Ready is its Ready then Conditional (ADR-0203), nil ⇒ always ready
// and conditional; Seal and Recipients are ADR-0204's. RetryInterval follows a failed run as ADR-0205 Decision 2's
// does. Hold is ADR-0206's gate, nil ⇒ never held; Record is ADR-0205's Recorder("blob"), nil ⇒ log only.
type Config struct {
	Dir, FrozenDir                                 string
	Target                                         blob.Bucket
	Ready                                          func(context.Context) (conditional bool, err error)
	Seal                                           backup.Seal
	Recipients                                     []string
	Interval, Rebaseline, Retention, RetryInterval time.Duration
	Logger                                         *slog.Logger
	Hold                                           interface{ Held() bool }
	Record                                         func(start time.Time, err error)
}

// Manifest is a generation's manifest.yaml, created last. Recipients are the sorted fingerprints the objects and the
// index are sealed to, in every object id; none ⇒ unsealed.
type Manifest struct {
	Format     int                `json:"format"`
	Generation uint64             `json:"generation"`
	Epoch      uint64             `json:"epoch"`
	At         v1alpha1.Timestamp `json:"at"`
	Funcd      string             `json:"funcd"`
	Objects    int                `json:"objects"`
	Index      backup.StoreFile   `json:"index"`
	Recipients []string           `json:"recipients,omitempty"`
}

// Object is one index line: a store object and its upload.
type Object struct {
	Key         string            `json:"key"`
	ID          string            `json:"id"`
	SHA256      string            `json:"sha256"`
	ContentType string            `json:"contentType,omitempty"`
	Gen         uint64            `json:"gen"`
	Parts       int               `json:"parts"`
	Size        int64             `json:"size"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Entry is one generation a listing shows; At is its manifest's ModTime, zero while incomplete.
type Entry struct {
	Generation, Epoch uint64
	Complete          bool
	At                time.Time
}

// Mirror runs the local store's mirror.
type Mirror interface {
	// Run writes one generation.
	Run(ctx context.Context) (Manifest, error)
	// Loop runs when due until ctx ends: due an Interval after the newest complete generation, after a failure at
	// most RetryInterval later. Each outcome goes to Record, a Ready refusal too, a cancel never. While held it
	// neither runs nor records and checks again after Interval.
	Loop(ctx context.Context)
}

type mirror struct {
	cfg   Config
	log   *slog.Logger
	run   sync.Mutex
	clock clock.Clock
	after func(time.Duration) <-chan time.Time
	link  func(oldname, newname string) error
	hooks hooks
}

// hooks are the test seams of a freeze: after each walk (its prefix, "" for the store) and link, and before the copy.
type hooks struct {
	afterWalk  func(prefix string)
	afterLink  func(key string)
	beforeCopy func()
}

// New checks cfg: an empty Dir, no Target, or a time not positive, or a rebaseline or retention below the interval,
// is fault.Invalid. An empty FrozenDir is <Dir>-frozen.
func New(cfg Config) (Mirror, error) {
	const op = "blobmirror.New"
	switch {
	case cfg.Dir == "":
		return nil, fault.Invalidf(op, "the blob store directory is empty")
	case cfg.Target == nil:
		return nil, fault.Invalidf(op, "no backup target")
	case cfg.Interval <= 0 || cfg.Rebaseline <= 0 || cfg.Retention <= 0 || cfg.RetryInterval <= 0:
		return nil, fault.Invalidf(op, "interval %s, rebaseline %s, retention %s and retry interval %s must be positive",
			cfg.Interval, cfg.Rebaseline, cfg.Retention, cfg.RetryInterval)
	case cfg.Rebaseline < cfg.Interval || cfg.Retention < cfg.Interval:
		return nil, fault.Invalidf(op, "rebaseline %s and retention %s must be at least the interval %s",
			cfg.Rebaseline, cfg.Retention, cfg.Interval)
	}
	if cfg.FrozenDir == "" {
		cfg.FrozenDir = strings.TrimRight(cfg.Dir, string(os.PathSeparator)) + "-frozen"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Ready == nil {
		cfg.Ready = func(context.Context) (bool, error) { return true, nil }
	}
	return &mirror{cfg: cfg, log: cfg.Logger, clock: clock.System(), after: time.After, link: os.Link}, nil
}

// LifecycleRule is the operator's expiry of the target's blob/ prefix: ⌈(rebaseline + retention) / 24h⌉ days, so no
// object expires before the last generation of its epoch is retention old.
func LifecycleRule(rebaseline, retention time.Duration) (prefix string, days int) {
	const day = 24 * time.Hour
	return rootPrefix, int((rebaseline + retention + day - 1) / day)
}

func (m *mirror) Loop(ctx context.Context) {
	due := m.firstDue(ctx)
	for m.waitUntil(ctx, due) {
		start := m.clock.Now()
		if m.cfg.Hold != nil && m.cfg.Hold.Held() {
			due = start.Add(m.cfg.Interval)
			continue
		}
		_, err := m.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		end := m.clock.Now()
		due = start.Add(m.cfg.Interval)
		if err != nil {
			m.log.ErrorContext(ctx, "blob mirror run failed", "error", err)
			if retry := end.Add(m.cfg.RetryInterval); retry.Before(due) {
				due = retry
			}
		}
		if m.cfg.Record != nil {
			m.cfg.Record(start, err)
		}
	}
}

// firstDue is an interval after the newest complete generation, now when none or the listing fails.
func (m *mirror) firstDue(ctx context.Context) time.Time {
	now := m.clock.Now()
	es, err := List(ctx, m.cfg.Target)
	if err != nil {
		return now
	}
	var last time.Time
	for _, e := range es {
		if e.Complete && e.At.After(last) {
			last = e.At
		}
	}
	if last.IsZero() {
		return now
	}
	return last.Add(m.cfg.Interval)
}

func (m *mirror) waitUntil(ctx context.Context, due time.Time) bool {
	if d := due.Sub(m.clock.Now()); d > 0 {
		select {
		case <-ctx.Done():
			return false
		case <-m.after(d):
		}
	}
	return ctx.Err() == nil
}

// Run waits for Ready, lists blob/ once, freezes the store, uploads what the epoch lacks, then the index and the
// manifest. The image goes on every path.
func (m *mirror) Run(ctx context.Context) (Manifest, error) {
	m.run.Lock()
	defer m.run.Unlock()
	start := m.clock.Now()
	conditional, err := m.cfg.Ready(ctx)
	if err != nil {
		return Manifest{}, err
	}
	l, err := list(ctx, m.cfg.Target)
	if err != nil {
		return Manifest{}, err
	}
	n, epoch := l.maxN+1, l.epoch
	if epoch == 0 || start.Sub(l.epochStart) > m.cfg.Rebaseline {
		epoch++
	}
	defer func() { _ = os.RemoveAll(m.cfg.FrozenDir) }()
	f, err := m.freeze(ctx)
	if err != nil {
		return Manifest{}, err
	}
	defer f.close()
	if m.hooks.beforeCopy != nil {
		m.hooks.beforeCopy()
	}
	r := &runner{m: m, f: f, conditional: conditional, epoch: epoch, n: n, uploads: l.uploads[epoch],
		buf: make([]byte, 0, backup.PartBytes)}
	objects, err := r.copyAll(ctx)
	if err != nil {
		return Manifest{}, err
	}
	index, err := r.putIndex(ctx, objects)
	if err != nil {
		return Manifest{}, err
	}
	man := Manifest{Format: Format, Generation: n, Epoch: epoch, At: v1alpha1.NewTimestamp(start), Funcd: version.Version,
		Objects: len(objects), Index: index, Recipients: slices.Clone(m.cfg.Recipients)}
	data, err := yaml.Marshal(man)
	if err != nil {
		return Manifest{}, fault.Wrapf(err, fault.Internal, "blobmirror.Run", "encode the manifest")
	}
	if err := r.put(ctx, genDir(epoch, n)+manifestName, data); err != nil {
		return Manifest{}, err
	}
	return man, nil
}

// runner is one run's copy state.
type runner struct {
	m           *mirror
	f           *freezer
	conditional bool
	epoch, n    uint64
	uploads     map[string]uploadSet
	buf         []byte
}

func (r *runner) put(ctx context.Context, key string, data []byte) error {
	return r.m.cfg.Target.Put(ctx, key, data, blob.PutOptions{IfNotExist: r.conditional})
}

// copyAll uploads each frozen object in key order; a mismatch found at copy time freezes the key again, and a key
// gone by then is left out.
func (r *runner) copyAll(ctx context.Context) ([]Object, error) {
	var out []Object
	for _, key := range slices.Sorted(maps.Keys(r.f.img)) {
		for {
			e, ok := r.f.img[key]
			if !ok {
				break
			}
			obj, same, err := r.copyObject(ctx, e)
			if err != nil {
				return nil, err
			}
			if same {
				out = append(out, obj)
				break
			}
			if err := r.f.refreeze(key); err != nil {
				return nil, err
			}
			if _, err := r.f.freezeKey(ctx, key, e.livePath, false); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// copyObject returns e's Object: from the epoch's complete upload of its id unread, else uploaded under the run's n.
// same is false when the bytes are not the ones frozen: the MD5 the store recorded, or a live file's size and time.
func (r *runner) copyObject(ctx context.Context, e *frozen) (Object, bool, error) {
	const op = "blobmirror.Run"
	id := objectID(e.key, e.size, e.mod, e.md5, r.m.cfg.Recipients)
	obj := Object{Key: e.key, ID: id, ContentType: e.contentType, Size: e.size, Metadata: e.meta}
	if u := r.uploads[id].lowest(); u != nil {
		obj.Gen, obj.SHA256, obj.Parts = u.n, u.sha, u.parts
		return obj, true, nil
	}
	file, err := os.Open(e.path)
	if err != nil {
		if os.IsNotExist(err) && e.live {
			return obj, false, nil
		}
		return Object{}, false, fault.Wrapf(err, fault.Internal, op, "open the frozen %q", e.key)
	}
	defer func() { _ = file.Close() }()
	if same, err := e.matches(file); err != nil || !same {
		return obj, false, err
	}
	dir := objectDir(r.epoch, id, r.n)
	pw := &partWriter{ctx: ctx, put: r.put, prefix: dir, buf: r.buf[:0], h: sha256.New()}
	if err := pw.copyFrom(file, r.m.cfg.Seal); err != nil {
		return Object{}, false, err
	}
	obj.Gen, obj.SHA256, obj.Parts = r.n, hex.EncodeToString(pw.h.Sum(nil)), pw.parts
	if err := r.put(ctx, dir+shaPrefix+obj.SHA256, nil); err != nil {
		return Object{}, false, err
	}
	return obj, true, nil
}

// putIndex writes the objects as sealed JSON lines in parts.
func (r *runner) putIndex(ctx context.Context, objects []Object) (backup.StoreFile, error) {
	var lines bytes.Buffer
	enc := json.NewEncoder(&lines)
	for _, o := range objects {
		if err := enc.Encode(o); err != nil {
			return backup.StoreFile{}, fault.Wrapf(err, fault.Internal, "blobmirror.Run", "encode the index")
		}
	}
	pw := &partWriter{ctx: ctx, put: r.put, prefix: genDir(r.epoch, r.n) + "index/", buf: r.buf[:0], h: sha256.New()}
	if err := pw.copyFrom(&lines, r.m.cfg.Seal); err != nil {
		return backup.StoreFile{}, err
	}
	return backup.StoreFile{Name: "index", Parts: pw.parts, Bytes: pw.n, SHA256: hex.EncodeToString(pw.h.Sum(nil))}, nil
}

// partWriter puts what it is written in parts of PartBytes, part-00000 first, hashing and counting the stored bytes;
// an empty file is one empty part.
type partWriter struct {
	ctx    context.Context
	put    func(ctx context.Context, key string, data []byte) error
	prefix string
	buf    []byte
	h      hash.Hash
	parts  int
	n      int64
}

// copyFrom streams src through seal (nil ⇒ as is) into parts.
func (w *partWriter) copyFrom(src io.Reader, seal backup.Seal) error {
	var dst io.WriteCloser = nopCloser{w}
	if seal != nil {
		sealed, err := seal(w)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "blobmirror.Run", "seal")
		}
		dst = sealed
	}
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	if len(w.buf) > 0 || w.parts == 0 {
		return w.flush()
	}
	return nil
}

func (w *partWriter) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		take := min(backup.PartBytes-len(w.buf), len(p))
		w.buf = append(w.buf, p[:take]...)
		p = p[take:]
		if len(w.buf) == backup.PartBytes {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return written, nil
}

func (w *partWriter) flush() error {
	key := fmt.Sprintf("%spart-%05d", w.prefix, w.parts)
	if err := w.put(w.ctx, key, w.buf); err != nil {
		return err
	}
	w.h.Write(w.buf)
	w.n += int64(len(w.buf))
	w.parts++
	w.buf = w.buf[:0]
	return nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// objectID is hex SHA-256(key ‖ 0x00 ‖ size u64 BE ‖ ModTime ns i64 BE ‖ MD5, 16 zero bytes when none ‖ 0x00 ‖
// recipients joined by ","): unchanged metadata ⇒ the same id, so the object is never read again.
func objectID(key string, size int64, mod time.Time, md5 []byte, recipients []string) string {
	b := make([]byte, 0, len(key)+40)
	b = append(append(b, key...), 0)
	b = binary.BigEndian.AppendUint64(b, uint64(size))           //nolint:gosec // a size is not negative
	b = binary.BigEndian.AppendUint64(b, uint64(mod.UnixNano())) //nolint:gosec // the bits of the time, as ADR-0208 fixes
	var sum [16]byte
	copy(sum[:], md5)
	b = append(append(b, sum[:]...), 0)
	b = append(b, strings.Join(recipients, ",")...)
	id := sha256.Sum256(b)
	return hex.EncodeToString(id[:])
}

func genDir(epoch, n uint64) string { return fmt.Sprintf("%s%010d/gen/%010d/", rootPrefix, epoch, n) }

func objectDir(epoch uint64, id string, n uint64) string {
	return fmt.Sprintf("%s%010d/objects/%s/%010d/", rootPrefix, epoch, id, n)
}

// upload is one objects/<id>/<n>/: its parts seen and its sha256- key's digest, "" until complete.
type upload struct {
	n     uint64
	parts int
	have  map[int]bool
	sha   string
}

type uploadSet map[uint64]*upload

// lowest is the complete upload with the lowest n, nil when none.
func (s uploadSet) lowest() *upload {
	var best *upload
	for _, u := range s {
		if u.sha != "" && (best == nil || u.n < best.n) {
			best = u
		}
	}
	if best != nil {
		best.parts = len(best.have)
	}
	return best
}

// complete reports every part below parts present and the digest sha.
func (u *upload) complete(parts int, sha string) bool {
	if u == nil || u.sha != sha {
		return false
	}
	for i := range parts {
		if !u.have[i] {
			return false
		}
	}
	return true
}

type generation struct {
	epoch    uint64
	complete bool
	at       time.Time
	index    map[int]bool
}

// listing is what one listing of blob/ shows.
type listing struct {
	epoch      uint64
	epochStart time.Time
	maxN       uint64
	gens       map[uint64]*generation
	uploads    map[uint64]map[string]uploadSet
}

// list lists blob/ once.
func list(ctx context.Context, b blob.Bucket) (*listing, error) {
	items, err := b.List(ctx, rootPrefix)
	if err != nil {
		return nil, err
	}
	l := &listing{gens: map[uint64]*generation{}, uploads: map[uint64]map[string]uploadSet{}}
	starts := map[uint64]time.Time{}
	for _, a := range items {
		k, ok := parseKey(a.Key)
		if !ok {
			continue
		}
		if s, seen := starts[k.epoch]; !seen || a.ModTime.Before(s) {
			starts[k.epoch] = a.ModTime
		}
		l.epoch, l.maxN = max(l.epoch, k.epoch), max(l.maxN, k.n)
		if k.id == "" {
			g := l.gens[k.n]
			if g == nil {
				g = &generation{epoch: k.epoch, index: map[int]bool{}}
				l.gens[k.n] = g
			}
			if k.name == manifestName {
				g.complete, g.at = true, a.ModTime
			} else if k.part >= 0 {
				g.index[k.part] = true
			}
			continue
		}
		byID := l.uploads[k.epoch]
		if byID == nil {
			byID = map[string]uploadSet{}
			l.uploads[k.epoch] = byID
		}
		if byID[k.id] == nil {
			byID[k.id] = uploadSet{}
		}
		u := byID[k.id][k.n]
		if u == nil {
			u = &upload{n: k.n, have: map[int]bool{}}
			byID[k.id][k.n] = u
		}
		if sha, ok := strings.CutPrefix(k.name, shaPrefix); ok {
			u.sha = sha
		} else if k.part >= 0 {
			u.have[k.part] = true
		}
	}
	l.epochStart = starts[l.epoch]
	return l, nil
}

// key is a parsed key under blob/: an object's (id set) or a generation's; part is -1 for a non-part name.
type key struct {
	epoch, n uint64
	id, name string
	part     int
}

func parseKey(k string) (key, bool) {
	parts := strings.Split(strings.TrimPrefix(k, rootPrefix), "/")
	epoch, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || len(parts) < 4 {
		return key{}, false
	}
	var out key
	switch {
	case parts[1] == "objects" && len(parts) == 5:
		out.id, parts = parts[2], parts[3:]
	case parts[1] == "gen" && len(parts) == 4:
		parts = parts[2:]
	case parts[1] == "gen" && len(parts) == 5 && parts[3] == "index":
		parts = []string{parts[2], parts[4]}
	default:
		return key{}, false
	}
	n, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return key{}, false
	}
	out.epoch, out.n, out.name, out.part = epoch, n, parts[1], -1
	if p, ok := strings.CutPrefix(out.name, "part-"); ok {
		if i, err := strconv.Atoi(p); err == nil && i >= 0 {
			out.part = i
		}
	}
	return out, true
}

// List lists the generations under blob/ once, by generation; no read.
func List(ctx context.Context, src blob.Bucket) ([]Entry, error) {
	l, err := list(ctx, src)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(l.gens))
	for n, g := range l.gens {
		out = append(out, Entry{Generation: n, Epoch: g.epoch, Complete: g.complete, At: g.at})
	}
	slices.SortFunc(out, func(a, b Entry) int { return cmp.Compare(a.Generation, b.Generation) })
	return out, nil
}
