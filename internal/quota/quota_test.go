package quota_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/quota"
)

const media = "media"

func subject() quota.Subject {
	return quota.Subject{Kind: v1.KindBucket, Namespace: "default", Name: media}
}

// store is a capped store: its objects by key, a total and the Source over it.
type store struct {
	mu       sync.Mutex
	limit    int64
	capped   bool
	objects  map[string]int64
	total    int64
	measures atomic.Int32
	failNext atomic.Bool
	gate     chan struct{} // when set, Measure lands the total it read only once it is closed
}

func newStore(limit int64, objects map[string]int64) *store {
	s := &store{limit: limit, capped: limit > 0, objects: map[string]int64{}}
	for k, n := range objects {
		s.objects[k] = n
		s.total += n
	}
	return s
}

func (s *store) source() quota.Source {
	return quota.Source{
		Limit: func(context.Context, quota.Subject) (int64, bool, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.limit, s.capped, nil
		},
		Measure: func(ctx context.Context, _ quota.Subject) (int64, error) {
			s.measures.Add(1)
			if s.failNext.CompareAndSwap(true, false) {
				return 0, errors.New("listing failed")
			}
			s.mu.Lock()
			total := s.total
			s.mu.Unlock()
			if s.gate != nil {
				select {
				case <-s.gate:
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			}
			return total, nil
		},
	}
}

func (s *store) size(key string) func(context.Context) (int64, error) {
	return func(context.Context) (int64, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.objects[key], nil
	}
}

// put stores key at n bytes and returns the total after it.
func (s *store) put(key string, n int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total += n - s.objects[key]
	s.objects[key] = n
	return s.total
}

func (s *store) delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total -= s.objects[key]
	delete(s.objects, key)
}

// del deletes key and reports the delete, as an enforcement point does.
func del(ctx context.Context, b *quota.Book, s *store, key string) {
	settle, err := b.Admit(ctx, quota.Request{Subject: subject(), Deletes: true})
	if err != nil {
		panic(err)
	}
	s.delete(key)
	settle(true)
}

func newBook(s *store) *quota.Book {
	b := quota.NewBook(clock.System(), nil)
	b.Register(v1.KindBucket, s.source())
	return b
}

// write admits, stores and settles one write, returning Admit's error.
func write(ctx context.Context, b *quota.Book, s *store, key string, n int64) error {
	settle, err := b.Admit(ctx, quota.Request{Subject: subject(), Bytes: n, Replaces: s.size(key)})
	if err != nil {
		return err
	}
	s.put(key, n)
	settle(true)
	return nil
}

func TestNoneAdmits(t *testing.T) {
	t.Parallel()
	settle, err := quota.None.Admit(context.Background(), quota.Request{Subject: subject(), Bytes: 1 << 40})
	require.NoError(t, err)
	settle(true)
}

// scenario: no-cap-unchanged — a store whose quota sets no size is never measured.
func TestScenarioNoCapUnchanged(t *testing.T) {
	t.Parallel()
	s := newStore(0, nil)
	b := newBook(s)
	ctx := context.Background()
	for i := range 50 {
		require.NoError(t, write(ctx, b, s, "k", int64(i)))
	}
	b.Refresh(ctx)
	require.Zero(t, s.measures.Load(), "an uncapped store is never measured")
}

func TestKindWithoutSourceAdmits(t *testing.T) {
	t.Parallel()
	b := quota.NewBook(clock.System(), nil)
	settle, err := b.Admit(context.Background(), quota.Request{Subject: subject(), Bytes: 1 << 40})
	require.NoError(t, err)
	settle(true)
	_, tracked := b.Usage(subject())
	require.False(t, tracked)
}

func TestRefusedPastTheLimit(t *testing.T) {
	t.Parallel()
	s := newStore(1024, map[string]int64{"a": 1000})
	b := newBook(s)
	err := write(context.Background(), b, s, "b", 100)
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "bucket default/media holds 1000 of its 1024 bytes (spec.quota): a write of 100 bytes would pass it")
	require.NoError(t, write(context.Background(), b, s, "b", 24), "a write that fits to the byte is admitted")
}

func TestUnknownSizeNeedsRoomAndDeletesPass(t *testing.T) {
	t.Parallel()
	s := newStore(1024, map[string]int64{"a": 1023})
	b := newBook(s)
	ctx := context.Background()
	settle, err := b.Admit(ctx, quota.Request{Subject: subject()})
	require.NoError(t, err, "one byte left admits a write of unknown size")
	settle(true)
	s.put("a", 1024)
	b.Refresh(ctx)
	_, err = b.Admit(ctx, quota.Request{Subject: subject()})
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(err))
	require.ErrorContains(t, err, "delete to make room")
	settle, err = b.Admit(ctx, quota.Request{Subject: subject(), Deletes: true})
	require.NoError(t, err, "a write that only deletes always passes")
	settle(true)
}

// scenario: overwrite-not-refused — a Bucket within 1 KiB of its quota that holds a 4 KiB object takes an overwrite of
// it with 4 KiB after one fresh measure.
// oracle: docs/design/storage/quota-usage.fizz#RefusalsRestOnAFreshMeasure
func TestScenarioOverwriteNotRefused(t *testing.T) {
	t.Parallel()
	s := newStore(10<<10, map[string]int64{"photo": 4 << 10, "doc": 5 << 10})
	b := newBook(s)
	ctx := context.Background()
	require.NoError(t, write(ctx, b, s, "photo", 4<<10), "the first overwrite fits on the credit, no bytes settled yet")
	measures := s.measures.Load()
	require.NoError(t, write(ctx, b, s, "photo", 4<<10), "the second waits on one fresh measure, then fits on the credit")
	require.Equal(t, measures+1, s.measures.Load(), "one fresh measure before the decision")
	usage, _ := b.Usage(subject())
	require.Equal(t, int64(13<<10), usage, "the credit decided; the count kept the full 4 KiB")
}

// A refusal waits on a measure that lands after the write arrived: deletes made since then count.
func TestRefusalWaitsForAFreshMeasure(t *testing.T) {
	t.Parallel()
	s := newStore(10, map[string]int64{"a": 5})
	b := newBook(s)
	ctx := context.Background()
	require.NoError(t, write(ctx, b, s, "b", 5), "fills the quota; 5 bytes settled since the measure")
	s.delete("a")
	require.NoError(t, write(ctx, b, s, "c", 5), "the fresh measure sees the delete")
	b.Refresh(ctx)
	measures := s.measures.Load()
	require.Error(t, write(ctx, b, s, "d", 1))
	require.Equal(t, measures, s.measures.Load(), "nothing settled or deleted since the last measure: refused at once")
}

// A reported delete makes room at once: the next refusal waits on a fresh measure, which sees it.
// oracle: docs/design/storage/quota-usage.fizz#RefusalsRestOnAFreshMeasure
func TestDeleteMakesRoomAtOnce(t *testing.T) {
	t.Parallel()
	s := newStore(10, map[string]int64{"a": 5, "b": 5})
	b := newBook(s)
	ctx := context.Background()
	require.Error(t, write(ctx, b, s, "c", 5), "at the cap")
	del(ctx, b, s, "a")
	require.NoError(t, write(ctx, b, s, "c", 5))
}

// A delete reported while a measure runs, after its read, still makes the next refusal measure again.
// oracle: docs/design/storage/quota-usage.fizz#RefusalsRestOnAFreshMeasure
func TestDeleteDuringAMeasureCounts(t *testing.T) {
	t.Parallel()
	s := newStore(10, map[string]int64{"a": 5, "b": 5})
	b := newBook(s)
	ctx := context.Background()
	require.Error(t, write(ctx, b, s, "c", 5), "tracks the subject at the cap")
	s.gate = make(chan struct{})
	done := make(chan struct{})
	go func() {
		b.Refresh(ctx)
		close(done)
	}()
	require.Eventually(t, func() bool { return s.measures.Load() == 2 }, time.Second, time.Millisecond, "the measure read 10 bytes")
	del(ctx, b, s, "a")
	close(s.gate)
	<-done
	s.gate = nil
	require.NoError(t, write(ctx, b, s, "c", 5))
}

func TestOneMeasureForManyWaiters(t *testing.T) {
	t.Parallel()
	s := newStore(10, map[string]int64{"a": 5})
	b := newBook(s)
	ctx := context.Background()
	require.NoError(t, write(ctx, b, s, "b", 5))
	s.gate = make(chan struct{})
	before := s.measures.Load()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { _ = write(ctx, b, s, "c", 3) })
	}
	time.Sleep(50 * time.Millisecond)
	close(s.gate)
	wg.Wait()
	require.Equal(t, before+1, s.measures.Load(), "the waiters share one measure")
}

func TestFirstRefreshFailureIs503(t *testing.T) {
	t.Parallel()
	s := newStore(10, nil)
	s.failNext.Store(true)
	b := newBook(s)
	ctx := context.Background()
	err := write(ctx, b, s, "a", 1)
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "%v", err)
	require.NoError(t, write(ctx, b, s, "a", 1), "the next write runs the first refresh again")
}

func TestFailedRefreshKeepsTheLastValues(t *testing.T) {
	t.Parallel()
	s := newStore(10, map[string]int64{"a": 9})
	b := newBook(s)
	ctx := context.Background()
	require.NoError(t, write(ctx, b, s, "b", 1))
	s.failNext.Store(true)
	b.Refresh(ctx)
	usage, _ := b.Usage(subject())
	require.Equal(t, int64(10), usage)
	err := write(ctx, b, s, "c", 1)
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(err), "a fresh measure after the failure refuses: %v", err)
}

func TestIdleSubjectDropped(t *testing.T) {
	t.Parallel()
	s := newStore(10, nil)
	clk := clock.NewManual(time.Unix(0, 0))
	b := quota.NewBook(clk, nil)
	b.Register(v1.KindBucket, s.source())
	ctx := context.Background()
	require.NoError(t, write(ctx, b, s, "a", 1))
	clk.Advance(11 * time.Minute)
	b.Refresh(ctx)
	_, tracked := b.Usage(subject())
	require.False(t, tracked)
}

// A credit read before a measure that already counts a shrink of the same object is read again: the write is
// decided on the object's size after that measure, so the store cannot pass the cap.
// oracle: docs/design/storage/quota-usage.fizz#CapHolds
func TestStaleCreditReadAgain(t *testing.T) {
	t.Parallel()
	s := newStore(64, map[string]int64{"k": 10, "j": 50})
	b := newBook(s)
	ctx := context.Background()
	require.NoError(t, write(ctx, b, s, "x", 0), "tracks the subject")
	read, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	replaces := func(ctx context.Context) (int64, error) {
		n, err := s.size("k")(ctx)
		if reads.Add(1) == 1 {
			close(read)
			<-release
		}
		return n, err
	}
	refused := make(chan error, 1)
	go func() {
		settle, err := b.Admit(ctx, quota.Request{Subject: subject(), Bytes: 22, Replaces: replaces})
		if err == nil {
			s.put("k", 22)
			settle(true)
		}
		refused <- err
	}()
	<-read
	require.NoError(t, write(ctx, b, s, "k", 1), "shrinks k after the credit was read")
	b.Refresh(ctx)
	close(release)
	err := <-refused
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(err), "51 held, 22 more on a credit of 1 passes 64: %v", err)
	require.Equal(t, int32(2), reads.Load(), "the credit was read again after the measure")
	s.mu.Lock()
	defer s.mu.Unlock()
	require.LessOrEqual(t, s.total, int64(64))
}

// Writers overwrite and add keys, some stores fail, a deleter and the refresh run beside them; at each store the
// store holds no more than its cap, and the usage counts no less than the store holds.
// oracle: docs/design/storage/quota-usage.fizz#UsageNeverBelowTotal
// oracle: docs/design/storage/quota-usage.fizz#CapHolds
func TestBookUsageNeverBelowTotalAndCapHolds(t *testing.T) {
	t.Parallel()
	const limit = 64
	s := newStore(limit, map[string]int64{"k0": 8})
	b := newBook(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var violations atomic.Int32
	check := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		usage, _ := b.Usage(subject())
		if s.total > limit || usage < s.total {
			violations.Add(1)
			t.Logf("total %d, usage %d, limit %d", s.total, usage, limit)
		}
	}
	var bg sync.WaitGroup
	bg.Go(func() {
		for ctx.Err() == nil {
			b.Refresh(ctx)
			time.Sleep(time.Millisecond)
		}
	})
	bg.Go(func() {
		r := rand.New(rand.NewPCG(1, 2))
		for ctx.Err() == nil {
			del(ctx, b, s, []string{"k0", "k1", "k2", "k3"}[r.IntN(4)])
			time.Sleep(time.Millisecond)
		}
	})
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			r := rand.New(rand.NewPCG(uint64(w), 7))
			for range 300 {
				key := []string{"k0", "k1", "k2", "k3"}[r.IntN(4)]
				n := int64(1 + r.IntN(24))
				settle, err := b.Admit(ctx, quota.Request{Subject: subject(), Bytes: n, Replaces: s.size(key)})
				if err != nil {
					continue
				}
				stored := r.IntN(5) > 0
				if stored {
					s.put(key, n)
					check()
				}
				settle(stored)
			}
		})
	}
	wg.Wait()
	cancel()
	bg.Wait()
	require.Zero(t, violations.Load())
}
