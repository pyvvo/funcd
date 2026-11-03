package lifecycle_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/lifecycle"
)

// journal records the hooks the providers ran, in order.
type journal struct {
	mu    sync.Mutex
	calls []string
}

func (j *journal) add(call string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls = append(j.calls, call)
}

func (j *journal) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.calls...)
}

// full has every hook; a set err fails its Start.
type full struct {
	name     string
	j        *journal
	startErr error
	stopErr  error
}

var (
	_ lifecycle.Starter       = (*full)(nil)
	_ lifecycle.BeforeStopper = (*full)(nil)
	_ lifecycle.Stopper       = (*full)(nil)
)

func (f *full) Name() string { return f.name }

func (f *full) Start(context.Context) error {
	f.j.add("start " + f.name)
	return f.startErr
}

func (f *full) BeforeStop(context.Context) error {
	f.j.add("before stop " + f.name)
	return nil
}

func (f *full) Stop(context.Context) error {
	f.j.add("stop " + f.name)
	return f.stopErr
}

// stopOnly has a Stop and nothing else.
type stopOnly struct {
	name string
	j    *journal
}

var _ lifecycle.Stopper = (*stopOnly)(nil)

func (s *stopOnly) Name() string { return s.name }

func (s *stopOnly) Stop(context.Context) error {
	s.j.add("stop " + s.name)
	return nil
}

// none has no hook.
type none struct{ name string }

func (n none) Name() string { return n.name }

func TestPhases_StartInOrderThenStopInReverse(t *testing.T) {
	j := &journal{}
	ph := lifecycle.New(&full{name: "a", j: j}, none{name: "b"}, &full{name: "c", j: j})
	ctx := context.Background()
	require.NoError(t, ph.Start(ctx))
	require.NoError(t, ph.BeforeStop(ctx))
	require.NoError(t, ph.Stop(ctx))
	require.Equal(t, []string{
		"start a", "start c",
		"before stop c", "before stop a",
		"stop c", "stop a",
	}, j.list())
}

// A failed Start ends the boot: the providers after it never start, and BeforeStop and Stop still run for every
// provider, so each one releases what it holds.
func TestPhases_AFailedStartStillStopsEveryProvider(t *testing.T) {
	j := &journal{}
	boom := errors.New("bind: address in use")
	ph := lifecycle.New(&full{name: "a", j: j}, &full{name: "b", j: j, startErr: boom}, &full{name: "c", j: j})
	ctx := context.Background()
	err := ph.Start(ctx)
	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "start b")
	require.NoError(t, ph.BeforeStop(ctx))
	require.NoError(t, ph.Stop(ctx))
	require.Equal(t, []string{
		"start a", "start b",
		"before stop c", "before stop b", "before stop a",
		"stop c", "stop b", "stop a",
	}, j.list())
}

// A platform that never booted, such as one whose New failed, still gets every BeforeStop and every Stop.
func TestPhases_StopWithoutStartStopsEveryProvider(t *testing.T) {
	j := &journal{}
	ph := lifecycle.New(&full{name: "a", j: j}, &stopOnly{name: "b", j: j})
	ctx := context.Background()
	require.NoError(t, ph.BeforeStop(ctx))
	require.NoError(t, ph.Stop(ctx))
	require.Equal(t, []string{"before stop a", "stop b", "stop a"}, j.list())
}

// Every Stop runs when one fails, and the errors are joined.
func TestPhases_StopJoinsErrorsAndRunsEveryStop(t *testing.T) {
	j := &journal{}
	errA, errC := errors.New("a stuck"), errors.New("c stuck")
	ph := lifecycle.New(&full{name: "a", j: j, stopErr: errA}, &stopOnly{name: "b", j: j}, &full{name: "c", j: j, stopErr: errC})
	err := ph.Stop(context.Background())
	require.ErrorIs(t, err, errA)
	require.ErrorIs(t, err, errC)
	require.Equal(t, []string{"stop c", "stop b", "stop a"}, j.list())
}

// scenario: a-stop-during-boot-starts-nothing-more
// Once stopping has begun, Start starts no further provider.
// oracle: docs/design/platform/start-stop.fizz#NothingServesAfterStop
func TestPhases_StartAfterStopStartsNothing(t *testing.T) {
	j := &journal{}
	ph := lifecycle.New(&full{name: "a", j: j})
	ctx := context.Background()
	require.NoError(t, ph.BeforeStop(ctx))
	require.NoError(t, ph.Start(ctx))
	require.Equal(t, []string{"before stop a"}, j.list())
}

func TestPhases_HooksNamesWhatEachProviderHas(t *testing.T) {
	ph := lifecycle.New(&full{name: "a"}, &stopOnly{name: "b"}, none{name: "c"}, &runner{name: "d"})
	require.Equal(t, []string{"a: start, before stop, stop", "b: stop", "c: ", "d: run"}, ph.Hooks())
}

// quoter is a shared hook of the test package, as one beside lifecycle defines its own.
type quoter interface {
	lifecycle.Provider
	Quota() string
}

// quoted has the shared hook and a Stop.
type quoted struct{ stopOnly }

func (q *quoted) Quota() string { return "quota " + q.name }

// Each walks a shared hook in list order, and Hooks names it after the lifecycle's own hooks.
func TestPhases_EachWalksASharedHookInOrder(t *testing.T) {
	ph := lifecycle.New(&quoted{stopOnly{name: "a"}}, &full{name: "b"}, none{name: "c"}, &quoted{stopOnly{name: "d"}})
	var got []string
	for _, q := range lifecycle.Each[quoter](ph) {
		got = append(got, q.Quota())
	}
	require.Equal(t, []string{"quota a", "quota d"}, got)
	require.Equal(t, []string{"a: stop, quotas", "b: start, before stop, stop", "c: ", "d: stop, quotas"},
		ph.Hooks(lifecycle.Named[quoter]("quotas")))
}

// runner has a Run that serves until its context ends.
type runner struct {
	name  string
	ended chan struct{}
}

var _ lifecycle.Runner = (*runner)(nil)

func (r *runner) Name() string { return r.name }

func (r *runner) Run(ctx context.Context) {
	<-ctx.Done()
	close(r.ended)
}

// Run starts every Runner on the context it is given, and the wait group counts them until that context ends.
func TestPhases_RunServesEachRunnerUntilItsContextEnds(t *testing.T) {
	a, b := &runner{name: "a", ended: make(chan struct{})}, &runner{name: "b", ended: make(chan struct{})}
	ph := lifecycle.New(a, none{name: "n"}, b)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	ph.Run(ctx, &wg)
	cancel()
	wg.Wait()
	for _, r := range []*runner{a, b} {
		select {
		case <-r.ended:
		default:
			t.Fatalf("runner %s did not run until its context ended", r.name)
		}
	}
}

// scenario: a-stop-during-boot-starts-nothing-more
// Once stopping has begun, Run starts no Runner.
func TestPhases_RunAfterStopStartsNothing(t *testing.T) {
	r := &runner{name: "a", ended: make(chan struct{})}
	ph := lifecycle.New(r)
	require.NoError(t, ph.BeforeStop(context.Background()))
	var wg sync.WaitGroup
	ph.Run(context.Background(), &wg)
	wg.Wait()
	select {
	case <-r.ended:
		t.Fatal("a Runner started after stopping began")
	default:
	}
}

// reclaimer reclaims, failing with err when it is set; during, when set, runs inside its Reclaim.
type reclaimer struct {
	name   string
	j      *journal
	err    error
	during func()
}

var _ lifecycle.Reclaimer = (*reclaimer)(nil)

func (r *reclaimer) Name() string { return r.name }

func (r *reclaimer) Reclaim(context.Context) error {
	r.j.add("reclaim " + r.name)
	if r.during != nil {
		r.during()
	}
	return r.err
}

// scenario: a-failed-reclaim-warns-and-boots
// scenario: a-stop-during-boot-reclaims-nothing-more
// Reclaim runs every Reclaimer in list order, past a failed one, and joins the errors with their provider's name. Once
// a stop phase began, before the walk or during a reclaim, it reclaims nothing more.
func TestPhases_ReclaimRunsEveryReclaimerInOrder(t *testing.T) {
	j := &journal{}
	errA, errC := errors.New("disk full"), errors.New("store unreachable")
	ph := lifecycle.New(&reclaimer{name: "a", j: j, err: errA}, none{name: "n"}, &reclaimer{name: "b", j: j},
		&reclaimer{name: "c", j: j, err: errC})
	err := ph.Reclaim(context.Background())
	require.ErrorIs(t, err, errA)
	require.ErrorIs(t, err, errC)
	require.ErrorContains(t, err, "reclaim a: disk full")
	require.ErrorContains(t, err, "reclaim c: store unreachable")
	require.Equal(t, []string{"reclaim a", "reclaim b", "reclaim c"}, j.list())
	require.Equal(t, []string{"a: reclaim", "n: ", "b: reclaim", "c: reclaim"}, ph.Hooks())

	require.NoError(t, ph.Stop(context.Background()))
	require.NoError(t, ph.Reclaim(context.Background()))
	require.Equal(t, []string{"reclaim a", "reclaim b", "reclaim c"}, j.list(), "nothing is reclaimed once a stop began")

	cj := &journal{}
	var cut *lifecycle.Phases
	stop := func() { require.NoError(t, cut.BeforeStop(context.Background())) }
	cut = lifecycle.New(&reclaimer{name: "a", j: cj, during: stop}, &reclaimer{name: "b", j: cj})
	require.NoError(t, cut.Reclaim(context.Background()))
	require.Equal(t, []string{"reclaim a"}, cj.list(), "a stop that began during a reclaim reclaims nothing more")
}
