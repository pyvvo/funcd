package sensor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	dlmemory "github.com/pyvvo/funcd/internal/eventing/deadletter/memory"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

type targetMode int

const (
	answer   targetMode = iota // succeed at once
	refuse                     // fail at once
	hang                       // block until released or cut, then fail
	gated                      // block until the gate opens, then succeed
	failOnce                   // fail the first call, then succeed
)

// targets is a real Invoker stub with a mode per Function; it counts calls and the attempts in flight.
type targets struct {
	mu        sync.Mutex
	modes     map[v1.ObjectName]targetMode
	calls     map[v1.ObjectName]int
	inFlight  map[v1.ObjectName]int
	peak      map[v1.ObjectName]int
	total     int
	peakTotal int
	release   chan struct{} // closed: hung calls fail
	gate      chan struct{} // closed: gated calls succeed
	cuts      chan struct{} // one send per hung call its context cut
	hold      chan struct{} // non-nil: a cut call waits for it before returning
}

func newTargets() *targets {
	return &targets{
		modes:    map[v1.ObjectName]targetMode{},
		calls:    map[v1.ObjectName]int{},
		inFlight: map[v1.ObjectName]int{},
		peak:     map[v1.ObjectName]int{},
		release:  make(chan struct{}),
		gate:     make(chan struct{}),
		cuts:     make(chan struct{}, 64),
	}
}

func (tg *targets) set(fn v1.ObjectName, m targetMode) {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	tg.modes[fn] = m
}

func (tg *targets) Invoke(ctx context.Context, _ v1.NamespaceName, fn v1.ObjectName, _ eventing.CloudEvent) error {
	tg.mu.Lock()
	m := tg.modes[fn]
	tg.calls[fn]++
	call := tg.calls[fn]
	tg.inFlight[fn]++
	tg.total++
	tg.peak[fn] = max(tg.peak[fn], tg.inFlight[fn])
	tg.peakTotal = max(tg.peakTotal, tg.total)
	hold := tg.hold
	tg.mu.Unlock()
	defer func() {
		tg.mu.Lock()
		tg.inFlight[fn]--
		tg.total--
		tg.mu.Unlock()
	}()
	switch m {
	case refuse:
		return fault.Unavailablef("test.invoke", "%s returned 500", fn)
	case failOnce:
		if call == 1 {
			return fault.Unavailablef("test.invoke", "%s returned 503", fn)
		}
	case hang:
		select {
		case <-tg.release:
			return fault.Unavailablef("test.invoke", "%s timed out", fn)
		case <-ctx.Done():
			tg.cuts <- struct{}{}
			if hold != nil {
				<-hold
			}
			return fault.Unavailablef("test.invoke", "POST %s: %v", fn, ctx.Err())
		}
	case gated:
		select {
		case <-tg.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	case answer:
	}
	return nil
}

func (tg *targets) count(fn v1.ObjectName) int {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	return tg.calls[fn]
}

func (tg *targets) flying(fn v1.ObjectName) int {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	return tg.inFlight[fn]
}

// countingDLQ counts its Puts, so a test waits on parks without listing the store.
type countingDLQ struct {
	deadletter.Store
	puts atomic.Int64
}

func (c *countingDLQ) Put(ctx context.Context, dl deadletter.DeadLetter) error {
	err := c.Store.Put(ctx, dl)
	c.puts.Add(1)
	return err
}

type rig struct {
	st   store.Store
	fan  *eventing.Fanout
	dlq  *countingDLQ
	tg   *targets
	r    *Reconciler
	stop func()
}

func newRig(t *testing.T, d Deps) *rig {
	t.Helper()
	g := &rig{st: store.New(memory.New()), fan: eventing.NewFanout(), dlq: &countingDLQ{Store: dlmemory.New()}, tg: newTargets()}
	d.Store, d.Subscriber, d.Invoker, d.DeadLetters = g.st, g.fan, g.tg, g.dlq
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	r, err := NewReconciler(d)
	require.NoError(t, err)
	g.r = r
	return g
}

// start runs the delivery workers; stop cancels them and waits for RunRetryWorkers to return.
func (g *rig) start(t *testing.T, drain time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		g.r.RunRetryWorkers(ctx, drain)
		close(done)
	}()
	var once sync.Once
	g.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("RunRetryWorkers did not return")
			}
		})
	}
	t.Cleanup(g.stop)
}

func (g *rig) sensor(t *testing.T, name string, on []v1.Dependency, do []v1.Action) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindSensor)
	require.True(t, ok)
	se := obj.(*v1.Sensor)
	se.Name, se.Namespace, se.ResourceGroup = v1.ObjectName(name), "team-a", "rg1"
	se.Spec.On, se.Spec.Do = on, do
	_, err := g.st.Create(context.Background(), se)
	require.NoError(t, err)
	g.reconcile(t, name)
}

func (g *rig) reconcile(t *testing.T, name string) {
	t.Helper()
	_, err := g.r.Reconcile(context.Background(), controller.Request{GVK: v1.KindSensor.GVK(), Namespace: "team-a", Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

func (g *rig) fire(t *testing.T, source, event string, n int) {
	t.Helper()
	for range n {
		ev, err := eventing.NewNamedEvent("team-a", v1.ObjectName(source), v1.ObjectName(event))
		require.NoError(t, err)
		require.NoError(t, g.fan.Publish(context.Background(), ev))
	}
}

func (g *rig) parked() int { return int(g.dlq.puts.Load()) }

func (g *rig) deadLetters(t *testing.T) []deadletter.DeadLetter {
	t.Helper()
	items, err := g.dlq.List(context.Background(), "team-a")
	require.NoError(t, err)
	return items
}

// invocations returns the recorded Invocations' errors: "" for each Ready one.
func (g *rig) invocations(t *testing.T) (ready int, failed []string) {
	t.Helper()
	list, err := g.st.List(context.Background(), v1.KindInvocation.GVK(), store.ListOptions{})
	require.NoError(t, err)
	for _, o := range list.Items {
		inv := o.(*v1.Invocation)
		switch inv.Status.Phase {
		case v1.PhaseReady:
			ready++
		case v1.PhaseFailed:
			failed = append(failed, inv.Status.Error)
		}
	}
	return ready, failed
}

func (g *rig) workflowRuns(t *testing.T) int {
	t.Helper()
	list, err := g.st.List(context.Background(), v1.KindWorkflowRun.GVK(), store.ListOptions{})
	require.NoError(t, err)
	return len(list.Items)
}

// units returns the ids of the queued units in state s, with their attempts made.
func (g *rig) units(s unitState, action v1.ObjectName) map[string]int {
	q := g.r.retry
	q.mu.Lock()
	defer q.mu.Unlock()
	out := map[string]int{}
	for id, u := range q.units {
		if u.state == s && u.d.action.Name == action {
			out[id] = u.attempts
		}
	}
	return out
}

func countReason(dls []deadletter.DeadLetter, substr string, attempts int) int {
	n := 0
	for _, dl := range dls {
		if strings.Contains(dl.Reason, substr) && dl.Attempts == attempts {
			n++
		}
	}
	return n
}

func depOn(name, source, event string) v1.Dependency {
	return v1.Dependency{Name: v1.ObjectName(name), Source: v1.ObjectName(source), Event: v1.ObjectName(event)}
}

const (
	fullText    = "is full"
	changedText = "changed with the delivery still queued"
	downText    = "daemon shut down with the delivery still queued"
)

func eventSource(t *testing.T, st store.Store, name string, set func(*v1.EventSource)) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindEventSource)
	require.True(t, ok)
	es := obj.(*v1.EventSource)
	es.Name, es.Namespace, es.ResourceGroup = v1.ObjectName(name), "team-a", "rg1"
	set(es)
	_, err := st.Create(context.Background(), es)
	require.NoError(t, err)
}

func reconcileSource(t *testing.T, src *eventing.Source, name string) {
	t.Helper()
	_, err := src.Reconcile(context.Background(), controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "team-a", Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

// scenario: stuck-target-spares-timer
func TestScenarioStuckTargetSparesTimer(t *testing.T) {
	t.Parallel()
	g := newRig(t, Deps{})
	g.tg.set("slow", hang)
	g.start(t, time.Second)
	src, err := eventing.NewSource(eventing.Deps{Store: g.st, Publisher: g.fan})
	require.NoError(t, err)
	for _, name := range []string{"slowclock", "fastclock"} {
		eventSource(t, g.st, name, func(es *v1.EventSource) {
			es.Spec.Timer = &v1.TimerSource{Events: []v1.TimerEvent{{Name: "tick", Interval: v1.Duration(500 * time.Millisecond)}}}
		})
		reconcileSource(t, src, name)
	}
	g.sensor(t, "a", []v1.Dependency{depOn("d", "slowclock", "tick")}, []v1.Action{{Name: "call", On: "d", Function: "slow"}})
	g.sensor(t, "b", []v1.Dependency{depOn("d", "fastclock", "tick")}, []v1.Action{{Name: "run", On: "d", Workflow: "wf"}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = src.Run(ctx) }()

	require.Eventually(t, func() bool { return g.workflowRuns(t) >= 6 && g.tg.flying("slow") > 0 }, 6*time.Second, 50*time.Millisecond,
		"sensor b started %d WorkflowRuns in 6 s beside a stuck target", g.workflowRuns(t))
}

// growingLister lists one more object per call, for every bucket.
type growingLister struct {
	mu   sync.Mutex
	objs map[v1.ObjectName][]blob.Attributes
}

func (l *growingLister) List(_ context.Context, _ v1.NamespaceName, bucket v1.ObjectName, _ string) ([]blob.Attributes, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.objs[bucket])
	l.objs[bucket] = append(l.objs[bucket], blob.Attributes{Key: fmt.Sprintf("obj-%d", n), Size: 1, ModTime: time.Unix(1_700_000_000+int64(n), 0)})
	return append([]blob.Attributes(nil), l.objs[bucket]...), nil
}

// countingPublisher counts the CloudEvents each source published once Publish returned.
type countingPublisher struct {
	next eventing.Publisher
	mu   sync.Mutex
	n    map[v1.ObjectName]int
}

func (p *countingPublisher) Publish(ctx context.Context, ev eventing.CloudEvent) error {
	err := p.next.Publish(ctx, ev)
	_, source, _ := eventing.ParseSourceURI(ev.Source)
	p.mu.Lock()
	p.n[source]++
	p.mu.Unlock()
	return err
}

func (p *countingPublisher) count(source v1.ObjectName) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n[source]
}

// scenario: stuck-target-spares-blob-source
func TestScenarioStuckTargetSparesBlobSource(t *testing.T) {
	t.Parallel()
	g := newRig(t, Deps{})
	g.tg.set("slow", hang)
	g.start(t, time.Second)
	pub := &countingPublisher{next: g.fan, n: map[v1.ObjectName]int{}}
	watcher, err := eventing.NewBlobWatcher(&growingLister{objs: map[v1.ObjectName][]blob.Attributes{}}, pub, eventing.NewMemWatermark(), 200*time.Millisecond, nil)
	require.NoError(t, err)
	src, err := eventing.NewSource(eventing.Deps{Store: g.st, Publisher: pub, Blob: watcher})
	require.NoError(t, err)
	for _, name := range []string{"a", "b"} {
		obj, ok := v1.NewObject(v1.KindBucket)
		require.True(t, ok)
		b := obj.(*v1.Bucket)
		b.Name, b.Namespace, b.ResourceGroup = v1.ObjectName("bucket-"+name), "team-a", "rg1"
		_, err := g.st.Create(context.Background(), b)
		require.NoError(t, err)
		eventSource(t, g.st, "source-"+name, func(es *v1.EventSource) {
			es.Spec.Blob = &v1.BlobSource{Bucket: v1.ObjectName("bucket-" + name), Events: []v1.BlobEvent{{Name: "created"}}}
		})
		reconcileSource(t, src, "source-"+name)
	}
	require.Equal(t, 2, watcher.ActiveWatches())
	g.sensor(t, "a", []v1.Dependency{depOn("d", "source-a", "created")}, []v1.Action{{Name: "call", On: "d", Function: "slow"}})
	g.sensor(t, "b", []v1.Dependency{depOn("d", "source-b", "created")}, []v1.Action{{Name: "call", On: "d", Function: "fast"}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = watcher.Run(ctx) }()

	require.Eventually(t, func() bool { return pub.count("source-b") >= 15 && g.tg.flying("slow") > 0 }, 6*time.Second, 50*time.Millisecond,
		"source b published %d objects in 6 s beside a stuck target", pub.count("source-b"))
}

// scenario: stuck-target-holds-at-most-cap
func TestScenarioStuckTargetHoldsAtMostCap(t *testing.T) {
	t.Parallel()
	g := newRig(t, Deps{})
	g.tg.set("slow", hang)
	g.tg.set("flaky", failOnce)
	g.start(t, time.Second)
	g.sensor(t, "stuck", []v1.Dependency{depOn("d", "git", "push")}, []v1.Action{{Name: "call", On: "d", Function: "slow"}})
	g.sensor(t, "other", []v1.Dependency{depOn("d", "git", "tag")}, []v1.Action{{Name: "call", On: "d", Function: "flaky"}})
	g.fire(t, "git", "push", 40)
	require.Eventually(t, func() bool { return g.tg.flying("slow") == 4 }, 2*time.Second, 5*time.Millisecond)

	fired := time.Now()
	g.fire(t, "git", "tag", 1)
	require.Eventually(t, func() bool { ready, _ := g.invocations(t); return ready == 1 }, 500*time.Millisecond, 5*time.Millisecond,
		"the second target's retry did not succeed within 500 ms")
	require.WithinDuration(t, fired, time.Now(), 500*time.Millisecond)
	require.Equal(t, 2, g.tg.count("flaky"), "one failure, then the retry")
	require.Equal(t, 4, g.tg.count("slow"), "the stuck target got only its cap")
	g.tg.mu.Lock()
	defer g.tg.mu.Unlock()
	require.LessOrEqual(t, g.tg.peak["slow"], 4)
}

// scenario: full-sensor-queue-dead-letters
func TestScenarioFullSensorQueueDeadLetters(t *testing.T) {
	t.Parallel()
	g := newRig(t, Deps{})
	g.tg.set("slow", hang)
	g.start(t, 0)
	g.sensor(t, "s", []v1.Dependency{depOn("d", "git", "push")}, []v1.Action{{Name: "call", On: "d", Function: "slow"}})
	g.fire(t, "git", "push", 4096)
	require.Eventually(t, func() bool { return g.tg.flying("slow") == 4 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, 4096, g.r.retry.queuedFor(sensorKey{"team-a", "s"}))

	g.fire(t, "git", "push", 1)
	require.Equal(t, 4, g.tg.count("slow"), "the overflowing firing never reached the target")
	dls := g.deadLetters(t)
	require.Len(t, dls, 1)
	require.Equal(t, 0, dls[0].Attempts)
	require.Contains(t, dls[0].Reason, "delivery queue of sensor team-a/s is full (4096 deliveries pending, the bound set by eventing.maxQueuedPerSensor); not attempted")
	_, failed := g.invocations(t)
	require.Equal(t, []string{dls[0].Reason}, failed, "one Failed Invocation with the same text")
}

// scenario: healthy-delivery-first-attempt
func TestScenarioHealthyDeliveryFirstAttempt(t *testing.T) {
	t.Parallel()
	g := newRig(t, Deps{})
	g.tg.set("fn", gated)
	g.start(t, time.Second)
	g.sensor(t, "s", []v1.Dependency{depOn("d", "git", "push")}, []v1.Action{{Name: "call", On: "d", Function: "fn"}})
	g.fire(t, "git", "push", 1)
	ready, failed := g.invocations(t)
	require.Zero(t, ready, "Publish returned before the target answered")
	require.Empty(t, failed)

	close(g.tg.gate)
	require.Eventually(t, func() bool { ready, _ := g.invocations(t); return ready == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, 1, g.tg.count("fn"), "one attempt")
	_, failed = g.invocations(t)
	require.Empty(t, failed)
	require.Empty(t, g.deadLetters(t))
}

// scenario: sensor-edit-releases-backlog
func TestScenarioSensorEditReleasesBacklog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	g := newRig(t, Deps{})
	g.r.retry = newRetryQueue(time.Hour, time.Hour, defaultMaxInFlightPerTarget, defaultMaxQueuedPerSensor)
	g.start(t, 0)
	g.sensor(t, "s", []v1.Dependency{depOn("da", "git", "a"), depOn("db", "git", "b")},
		[]v1.Action{{Name: "a", On: "da", Function: "f"}, {Name: "b", On: "db", Function: "h"}})
	g.sensor(t, "s2", []v1.Dependency{depOn("dc", "git", "c")}, []v1.Action{{Name: "x", On: "dc", Function: "f"}})
	backingOff := func(action v1.ObjectName, attempts int) []string {
		var ids []string
		for id, n := range g.units(unitBackingOff, action) {
			if n == attempts {
				ids = append(ids, id)
			}
		}
		return ids
	}
	wait := func(cond func() bool) {
		t.Helper()
		require.Eventually(t, cond, 30*time.Second, 5*time.Millisecond)
	}

	g.tg.set("f", refuse)
	g.fire(t, "git", "a", 1)
	wait(func() bool { return len(backingOff("a", 1)) == 1 })
	third := backingOff("a", 1)[0]
	g.r.retry.ready(third)
	wait(func() bool { return len(backingOff("a", 2)) == 1 })
	g.tg.set("f", hang)
	g.r.retry.ready(third)
	wait(func() bool { return g.tg.flying("f") == 1 })

	g.tg.set("f", refuse)
	g.fire(t, "git", "a", 3)
	wait(func() bool { return len(backingOff("a", 1)) == 3 })
	g.tg.set("f", hang)
	g.fire(t, "git", "a", 3)
	wait(func() bool { return g.tg.flying("f") == 4 })
	g.fire(t, "git", "a", 4088)
	g.tg.set("h", refuse)
	g.fire(t, "git", "b", 1)
	wait(func() bool { return len(backingOff("b", 1)) == 1 })
	bID := backingOff("b", 1)[0]
	g.fire(t, "git", "c", 6)
	require.Equal(t, 4096, g.r.retry.queuedFor(sensorKey{"team-a", "s"}), "sensor s is full")
	require.Equal(t, 6, g.r.retry.queuedFor(sensorKey{"team-a", "s2"}))
	require.Equal(t, 9, g.tg.count("f"))

	g.tg.set("g", answer)
	g.tg.set("h", answer)
	obj, err := g.st.Get(ctx, v1.KindSensor.GVK(), "team-a", "s")
	require.NoError(t, err)
	se := obj.(*v1.Sensor)
	se.Spec.Do[0].Function = "g"
	_, err = g.st.Update(ctx, se)
	require.NoError(t, err)
	g.reconcile(t, "s")
	s2, err := g.st.Get(ctx, v1.KindSensor.GVK(), "team-a", "s2")
	require.NoError(t, err)
	require.NoError(t, g.st.Delete(ctx, v1.KindSensor.GVK(), "team-a", "s2", s2.GetObjectMeta().ResourceVersion))
	g.reconcile(t, "s2")

	wait(func() bool { return g.parked() == 4091+6 })
	dls := g.deadLetters(t)
	require.Equal(t, 4088+6, countReason(dls, changedText, 0), "the waiting deliveries are parked unattempted")
	require.Equal(t, 3, countReason(dls, changedText, 1), "the backing-off ones keep their attempt count")
	_, failed := g.invocations(t)
	require.Len(t, failed, 4097, "one Failed Invocation each")
	require.Equal(t, 9, g.tg.count("f"), "f got no further attempt")

	g.r.retry.ready(bID)
	wait(func() bool { ready, _ := g.invocations(t); return ready == 1 })
	require.Equal(t, 2, g.tg.count("h"), "b's delivery reached h as attempt 2")

	g.fire(t, "git", "a", 1)
	wait(func() bool { ready, _ := g.invocations(t); return ready == 2 })
	require.Equal(t, 1, g.tg.count("g"), "the next firing reached g")
	require.Zero(t, countReason(g.deadLetters(t), fullText, 0), "no overflow after the edit")

	close(g.tg.release)
	wait(func() bool { return g.parked() == 4097+4 })
	dls = g.deadLetters(t)
	require.Equal(t, 4088+6+3+3, countReason(dls, changedText, 0)+countReason(dls, changedText, 1))
	require.Equal(t, 1, countReason(dls, "f timed out", 3), "the last attempt is parked with its own error")
	require.Equal(t, 9, g.tg.count("f"), "no hung attempt was retried")

	var replay string
	for _, dl := range dls {
		if dl.Action == "a" && strings.Contains(dl.Reason, changedText) {
			replay = dl.ID
			break
		}
	}
	require.NoError(t, g.r.Replay(ctx, "team-a", replay))
	require.Equal(t, 2, g.tg.count("g"), "the replay went to g")
}

// scenario: shutdown-parks-queued
func TestScenarioShutdownParksQueued(t *testing.T) {
	t.Parallel()
	g := newRig(t, Deps{})
	g.tg.set("slow", hang)
	g.start(t, time.Second)
	g.sensor(t, "s", []v1.Dependency{depOn("d", "git", "push")}, []v1.Action{{Name: "call", On: "d", Function: "slow"}})
	g.fire(t, "git", "push", 10)
	require.Eventually(t, func() bool { return g.tg.flying("slow") == 4 }, 2*time.Second, 5*time.Millisecond)

	go g.stop()
	require.Eventually(t, func() bool { return g.parked() == 6 }, 900*time.Millisecond, 5*time.Millisecond)
	g.fire(t, "git", "push", 1)
	g.stop()

	dls := g.deadLetters(t)
	require.Len(t, dls, 11)
	require.Equal(t, 7, countReason(dls, downText, 0), "the 6 waiting and the late one")
	require.Equal(t, 4, countReason(dls, "POST slow: context canceled", 1), "the cut attempts, with their own error")
	_, failed := g.invocations(t)
	require.Len(t, failed, 11)
	require.Equal(t, 4, g.tg.count("slow"), "the late firing never reached the target")
}

// syncBuffer is a log sink the workers and the test share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) lines(substr string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for line := range strings.SplitSeq(b.buf.String(), "\n") {
		if strings.Contains(line, substr) {
			out = append(out, line)
		}
	}
	return out
}

// scenario: shutdown-past-bound-drops-with-count
func TestScenarioShutdownPastBoundDropsWithCount(t *testing.T) {
	t.Parallel()
	logs := &syncBuffer{}
	g := newRig(t, Deps{Logger: slog.New(slog.NewTextHandler(logs, nil))})
	g.tg.set("slow", hang)
	g.tg.hold = make(chan struct{})
	g.start(t, 0)
	g.sensor(t, "s", []v1.Dependency{depOn("d", "git", "push")}, []v1.Action{{Name: "call", On: "d", Function: "slow"}})
	g.fire(t, "git", "push", 10)
	require.Eventually(t, func() bool { return g.tg.flying("slow") == 4 }, 2*time.Second, 5*time.Millisecond)

	go g.stop()
	for range 4 {
		select {
		case <-g.tg.cuts:
		case <-time.After(5 * time.Second):
			t.Fatal("the attempts in flight were not cut at the bound")
		}
	}
	g.fire(t, "git", "push", 1)
	require.Empty(t, g.deadLetters(t), "past the bound nothing queued is parked")
	close(g.tg.hold)
	g.stop()

	dls := g.deadLetters(t)
	require.Len(t, dls, 4)
	require.Equal(t, 4, countReason(dls, "POST slow: context canceled", 1))
	_, failed := g.invocations(t)
	require.Len(t, failed, 4)
	dropped := logs.lines("dropped at shutdown")
	require.Len(t, dropped, 1)
	require.Contains(t, dropped[0], "count=7")

	g.fire(t, "git", "push", 1)
	require.Len(t, logs.lines("sensor delivery dropped after shutdown"), 1, "a firing after the count log logs its own line")
	require.Len(t, g.deadLetters(t), 4)
	require.Equal(t, 4, g.tg.count("slow"))
}

// scenario: delivery-sizes-from-settings
func TestScenarioDeliverySizesFromSettings(t *testing.T) {
	t.Parallel()
	g := newRig(t, Deps{MaxDeliveriesInFlight: 5, MaxInFlightPerTarget: 2, MaxQueuedPerSensor: 10})
	g.start(t, 0)
	for _, name := range []string{"a", "b", "c"} {
		fn := v1.ObjectName("target-" + name)
		g.tg.set(fn, hang)
		g.sensor(t, name, []v1.Dependency{depOn("d", "git", name)}, []v1.Action{{Name: "call", On: "d", Function: fn}})
	}
	g.fire(t, "git", "a", 11)
	dls := g.deadLetters(t)
	require.Len(t, dls, 1, "the 11th delivery is dead-lettered at once")
	require.Equal(t, 0, dls[0].Attempts)
	require.Contains(t, dls[0].Reason, "is full (10 deliveries pending")
	g.fire(t, "git", "b", 4)
	g.fire(t, "git", "c", 4)

	require.Eventually(t, func() bool {
		g.tg.mu.Lock()
		defer g.tg.mu.Unlock()
		return g.tg.total == 5
	}, 2*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool {
		g.tg.mu.Lock()
		defer g.tg.mu.Unlock()
		return g.tg.total > 5
	}, 200*time.Millisecond, 10*time.Millisecond)
	g.tg.mu.Lock()
	defer g.tg.mu.Unlock()
	require.Equal(t, 5, g.tg.peakTotal, "the peak in flight is the worker count")
	for _, fn := range []v1.ObjectName{"target-a", "target-b", "target-c"} {
		require.LessOrEqual(t, g.tg.peak[fn], 2, "at most the per-target cap to %s", fn)
	}
}
