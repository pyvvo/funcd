// Package quota is the storage quota framework (ADR-0292): one decision point, Checker, that every capped storage
// write asks before it stores, as every PEP asks the one Cedar PDP (internal/auth). Book implements it over the
// Sources each store kind registers; None admits everything and is every enforcement point's default.
package quota

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

// refreshInterval is how often a subject's limit and usage are read again off the write path (ADR-0292 Decision 3).
const refreshInterval = time.Minute

// idleAfter drops a subject no write asked about for this long; its next write reads it again.
const idleAfter = 10 * time.Minute

// measureTimeout bounds one measure.
const measureTimeout = 2 * time.Minute

// Subject is what a quota caps: a store, or a prefix of a Bucket.
type Subject struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	Prefix    string
}

func (s Subject) String() string {
	name := fmt.Sprintf("%s %s/%s", strings.ToLower(string(s.Kind)), s.Namespace, s.Name)
	if s.Prefix != "" {
		return fmt.Sprintf("prefix %s of %s", s.Prefix, name)
	}
	return name
}

// Request is one write a Checker decides.
type Request struct {
	Subject Subject
	// Bytes is the write's size; 0 when unknown, which needs room left under the limit.
	Bytes int64
	// Replaces is the size the write overwrites, asked only before a refusal; nil is 0. It is credited to the
	// decision, never to the count, since a concurrent delete can make it wrong.
	Replaces func(ctx context.Context) (int64, error)
	// Deletes marks a write that only deletes: always admitted, never measured for; once stored, the subject's next
	// refusal waits on a fresh measure, so the room it made counts at once.
	Deletes bool
}

// Settle ends an admitted write: stored turns its reservation into settled bytes, false releases it. Call it once.
type Settle func(stored bool)

// Checker is the quota counterpart of auth.Authorizer: Admit before a write, the returned Settle after it.
type Checker interface {
	// Admit reserves r.Bytes, or refuses with fault.InsufficientStorage when the write would pass the limit.
	Admit(ctx context.Context, r Request) (Settle, error)
}

// None admits every write.
//
//nolint:gochecknoglobals // ADR-0292 contract: every enforcement point's default
var None Checker = none{}

type none struct{}

func (none) Admit(context.Context, Request) (Settle, error) { return settled, nil }

func settled(bool) {}

// Source reads one kind's quotas: Limit is a subject's cap (ok=false: none), Measure what it holds.
type Source struct {
	Limit   func(ctx context.Context, s Subject) (limit int64, ok bool, err error)
	Measure func(ctx context.Context, s Subject) (int64, error)
}

// Book is the Checker over the registered Sources. Usage is the last measure plus the bytes settled since and the
// bytes reserved in flight, so for a write of known size it never counts less than the store holds.
type Book struct {
	clk    clock.Clock
	logger *slog.Logger

	mu       sync.Mutex
	sources  map[v1.Kind]Source
	subjects map[Subject]*usage
}

// NewBook returns an empty Book; nil logger is slog.Default.
func NewBook(clk clock.Clock, logger *slog.Logger) *Book {
	if logger == nil {
		logger = slog.Default()
	}
	return &Book{clk: clk, logger: logger, sources: map[v1.Kind]Source{}, subjects: map[Subject]*usage{}}
}

// Register sets the Source of kind.
func (b *Book) Register(kind v1.Kind, src Source) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sources[kind] = src
}

// usage is one subject's state, as the design model has it (docs/design/storage/quota-usage.fizz).
type usage struct {
	src   Source
	first sync.Mutex // held while the first refresh runs

	mu        sync.Mutex
	ready     bool
	capped    bool
	limit     int64
	measured  int64
	settled   int64 // bytes settled since the last measure landed
	reserved  int64
	measuring bool
	during    int64 // bytes settled while the running measure runs
	freed     bool  // a delete was stored since the last measure's read, maybe
	freedMid  bool  // a delete was stored while the running measure runs
	landed    chan struct{}
	epoch     uint64
	used      time.Time
}

func (b *Book) usage(s Subject) (*usage, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if u, ok := b.subjects[s]; ok {
		return u, true
	}
	src, ok := b.sources[s.Kind]
	if !ok {
		return nil, false
	}
	u := &usage{src: src, used: b.clk.Now()}
	b.subjects[s] = u
	return u, true
}

// Admit implements Checker (ADR-0292 Decisions 3 and 4).
func (b *Book) Admit(ctx context.Context, r Request) (Settle, error) {
	const op = "quota.Admit"
	if r.Deletes {
		return b.deletes(r.Subject), nil
	}
	u, ok := b.usage(r.Subject)
	if !ok {
		return settled, nil
	}
	if err := b.ready(ctx, u, r.Subject); err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.used = b.clk.Now()
	if !u.capped {
		u.mu.Unlock()
		return settled, nil
	}
	if u.room(r.Bytes, 0) {
		defer u.mu.Unlock()
		return u.reserve(r.Bytes), nil
	}
	epoch, dirty := u.epoch, u.settled > 0 || u.freed
	u.mu.Unlock()
	if dirty {
		if err := b.awaitMeasure(ctx, u, r.Subject, epoch); err != nil {
			return nil, err
		}
	}
	credit := b.credit(ctx, u, r)
	defer u.mu.Unlock()
	if u.room(r.Bytes, credit) {
		return u.reserve(r.Bytes), nil
	}
	used := u.measured + u.settled + u.reserved
	if r.Bytes == 0 {
		return nil, fault.InsufficientStoragef(op, "%s holds %d of its %d bytes (spec.quota): delete to make room", r.Subject, used, u.limit)
	}
	return nil, fault.InsufficientStoragef(op, "%s holds %d of its %d bytes (spec.quota): a write of %d bytes would pass it",
		r.Subject, used, u.limit, r.Bytes)
}

// deletes is the Settle of a delete on s: once stored, it marks a tracked subject freed; an untracked one needs nothing.
func (b *Book) deletes(s Subject) Settle {
	b.mu.Lock()
	u, ok := b.subjects[s]
	b.mu.Unlock()
	if !ok {
		return settled
	}
	return func(stored bool) {
		if !stored {
			return
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		u.freed = true
		if u.measuring {
			u.freedMid = true
		}
	}
}

// credit reads the size r replaces and returns with u.mu held. A measure that lands between the read and the decision
// may already count a shrink of that object, which the credit would count again, so the size is read again then.
func (b *Book) credit(ctx context.Context, u *usage, r Request) int64 {
	for {
		u.mu.Lock()
		epoch := u.epoch
		u.mu.Unlock()
		var credit int64
		if r.Replaces != nil && r.Bytes > 0 {
			if n, err := r.Replaces(ctx); err == nil {
				credit = n
			}
		}
		u.mu.Lock()
		if u.epoch == epoch {
			return credit
		}
		u.mu.Unlock()
	}
}

// room reports whether n more bytes fit, credit deducted; a write of unknown size needs a byte left.
func (u *usage) room(n, credit int64) bool {
	used := u.measured + u.settled + u.reserved
	if n == 0 {
		return used < u.limit
	}
	return used+n-credit <= u.limit
}

// reserve counts n bytes in flight and returns their Settle; u.mu is held.
func (u *usage) reserve(n int64) Settle {
	u.reserved += n
	var once sync.Once
	return func(stored bool) {
		once.Do(func() {
			u.mu.Lock()
			defer u.mu.Unlock()
			u.reserved -= n
			if stored {
				u.settled += n
				if u.measuring {
					u.during += n
				}
			}
		})
	}
}

// ready runs a subject's first refresh, once; while it fails, the write gets 503.
func (b *Book) ready(ctx context.Context, u *usage, s Subject) error {
	u.mu.Lock()
	ok := u.ready
	u.mu.Unlock()
	if ok {
		return nil
	}
	u.first.Lock()
	defer u.first.Unlock()
	u.mu.Lock()
	ok = u.ready
	u.mu.Unlock()
	if ok {
		return nil
	}
	if err := b.refresh(ctx, u, s); err != nil {
		return fault.Wrapf(err, fault.Unavailable, "quota.Admit", "read the quota of %s", s)
	}
	return nil
}

// refresh reads s's limit, then, when capped, its usage; an uncapped subject is never measured.
func (b *Book) refresh(ctx context.Context, u *usage, s Subject) error {
	limit, capped, err := u.src.Limit(ctx, s)
	if err != nil {
		return err
	}
	u.mu.Lock()
	u.limit, u.capped = limit, capped
	if !capped {
		u.ready = true
		u.mu.Unlock()
		return nil
	}
	epoch := u.epoch
	u.mu.Unlock()
	if err := b.awaitMeasure(ctx, u, s, epoch); err != nil {
		return err
	}
	u.mu.Lock()
	u.ready = true
	u.mu.Unlock()
	return nil
}

// awaitMeasure waits until a measure lands after epoch, starting one unless one runs; a measure that fails is 503.
func (b *Book) awaitMeasure(ctx context.Context, u *usage, s Subject, epoch uint64) error {
	u.mu.Lock()
	if u.epoch > epoch {
		u.mu.Unlock()
		return nil
	}
	if !u.measuring {
		u.measuring, u.during, u.freedMid, u.landed = true, 0, false, make(chan struct{})
		go b.measure(u, s, u.landed)
	}
	landed := u.landed
	u.mu.Unlock()
	select {
	case <-landed:
	case <-ctx.Done():
		return fault.Wrapf(ctx.Err(), fault.Unavailable, "quota.Admit", "measure %s", s)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.epoch > epoch {
		return nil
	}
	return fault.Unavailablef("quota.Admit", "could not measure %s; retry", s)
}

// measure reads s's total and lands it, keeping the bytes settled while it ran.
func (b *Book) measure(u *usage, s Subject, landed chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), measureTimeout)
	total, err := u.src.Measure(ctx, s)
	cancel()
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		b.logger.Warn("could not measure a store's quota usage; keeping the last measure", "subject", s.String(), "error", err)
	} else {
		u.measured, u.settled, u.freed = total, u.during, u.freedMid
		u.epoch++
	}
	u.measuring, u.during, u.freedMid = false, 0, false
	close(landed)
}

// Run refreshes every subject each refreshInterval until ctx ends, and drops those unused for idleAfter.
func (b *Book) Run(ctx context.Context) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Refresh(ctx)
		}
	}
}

// Refresh reads every subject's limit and usage again, as Run does each refreshInterval, and drops those unused for
// idleAfter.
func (b *Book) Refresh(ctx context.Context) {
	now := b.clk.Now()
	b.mu.Lock()
	live := make(map[Subject]*usage, len(b.subjects))
	for s, u := range b.subjects {
		u.mu.Lock()
		idle := now.Sub(u.used) > idleAfter && u.reserved == 0 && !u.measuring
		u.mu.Unlock()
		if idle {
			delete(b.subjects, s)
			continue
		}
		live[s] = u
	}
	b.mu.Unlock()
	for s, u := range live {
		if err := b.refresh(ctx, u, s); err != nil {
			b.logger.Warn("could not refresh a store's quota; keeping the last values", "subject", s.String(), "error", err)
		}
	}
}
