// Package activator implements scale-to-zero (ADR-0016): an in-process data-path
// component backing a gateway route's upstream. For a request to a scaled-to-zero
// function it buffers the request, single-flights a wake (Scaler.ScaleTo(fn,1)),
// waits for a ready upstream to appear, then forwards the held request (or 503s on
// timeout); and it runs a periodic idle-reclaim pass that scales idle functions to
// zero. It emits a scale INTENT through two seams (Endpoints read / Scaler write);
// it does not provision workeres (that is the P-M Function reconciler) and it does
// not register a Function reconciler (one-reconciler-per-gvk, ADR-0015).
package activator

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
)

// FunctionRef identifies the function a request / scale-decision targets.
type FunctionRef struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// Endpoints resolves a function's currently-ready upstream. P-M/scheduler provide the
// production driver (from the provisioned replica set); tests inject a fake.
type Endpoints interface {
	// Upstream returns the ready upstream URL for fn and whether one exists. The URL's path, if
	// any, is where fn's worker serves it (a pool worker's /function/<name>, ADR-0046).
	Upstream(ctx context.Context, fn FunctionRef) (upstream string, ready bool, err error)
}

// Scaler drives a function toward a target replica count (V1 uses only 0 and 1).
type Scaler interface {
	ScaleTo(ctx context.Context, fn FunctionRef, replicas int) error
}

// Deps configures the activator (internal component, ADR-0002 §1).
type Deps struct {
	Store             store.Store   // lists Functions for idle reclaim
	Endpoints         Endpoints     // ready-upstream resolver (required)
	Scaler            Scaler        // scale-intent writer (required)
	Clock             clock.Clock   // default clock.System()
	Logger            *slog.Logger  // default slog.Default()
	ActivationTimeout time.Duration // cold-start hold bound; default 30s
	PollInterval      time.Duration // ready-poll cadence; default 25ms
	ReclaimInterval   time.Duration // idle-reclaim cadence for Run; default 30s
	Calls             *CallTracker  // counts the calls to each worker (ADR-0143), which idle reclaim also reads; nil ⇒ uncounted
}

const (
	defaultActivationTimeout = 30 * time.Second
	defaultPollInterval      = 25 * time.Millisecond
	defaultReclaimInterval   = 30 * time.Second
)

// Activator is the scale-to-zero data path + idle-reclaim component.
type Activator struct {
	store             store.Store
	endpoints         Endpoints
	scaler            Scaler
	clock             clock.Clock
	logger            *slog.Logger
	activationTimeout time.Duration
	pollInterval      time.Duration
	reclaimInterval   time.Duration

	transport http.RoundTripper // shared, pooled upstream transport (ADR-0041), counted when Calls is set (ADR-0143)
	calls     *CallTracker

	mu         sync.Mutex
	inflight   map[FunctionRef]*activation     // singleflight: one activation per cold fn
	lastActive map[FunctionRef]time.Time       // last-activity tracker feeding idle reclaim
	upstreams  map[FunctionRef]map[string]bool // upstreams Wake handed out, whose calls in flight idle reclaim spares
	reclaiming map[FunctionRef]chan struct{}   // a reclaim writing fn's scale-to-zero; closed when it is written
	stopped    bool                            // Run has returned: no new activation starts

	// life bounds every activation and is cancelled when Run returns: the platform that would start a woken worker
	// stops with it, so a held request could never be served. drives counts the running activations.
	life   context.Context
	cancel context.CancelFunc
	drives sync.WaitGroup
}

// newPooledTransport returns the data-plane's shared upstream transport: it reuses keep-alive
// connections to each function worker (pool keyed by upstream host:port — a distinct, per-
// replica address, so connections never cross function/tenant boundaries) instead of dialing
// per request, which on http.DefaultTransport (2 idle conns/host) churns ports into TIME_WAIT
// and exhausts the ephemeral range under load (ADR-0041, surfaced by ADR-0040).
func newPooledTransport() *http.Transport {
	t, _ := http.DefaultTransport.(*http.Transport)
	tr := t.Clone()
	tr.MaxIdleConns = 512
	tr.MaxIdleConnsPerHost = 256 // ≫ the stdlib default of 2
	tr.IdleConnTimeout = 90 * time.Second
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return tr
}

// activation is one in-progress cold-start wake, shared by all waiters for a fn. Its
// result fields are written once (by the driving goroutine) before done is closed; the
// close→receive on done is the happens-before that makes them visible to waiters.
type activation struct {
	done     chan struct{}
	upstream string
	err      error
}

// New builds the activator. Endpoints and Scaler are required.
func New(d Deps) (*Activator, error) {
	if d.Endpoints == nil {
		return nil, fault.Invalidf("activator.New", "endpoints resolver is required")
	}
	if d.Scaler == nil {
		return nil, fault.Invalidf("activator.New", "scaler is required")
	}
	c := d.Clock
	if c == nil {
		c = clock.System()
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	activationTimeout := d.ActivationTimeout
	if activationTimeout <= 0 {
		activationTimeout = defaultActivationTimeout
	}
	pollInterval := d.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	reclaimInterval := d.ReclaimInterval
	if reclaimInterval <= 0 {
		reclaimInterval = defaultReclaimInterval
	}
	var transport http.RoundTripper = newPooledTransport()
	if d.Calls != nil {
		transport = d.Calls.Wrap(transport)
	}
	life, cancel := context.WithCancel(context.Background())
	return &Activator{
		store:             d.Store,
		endpoints:         d.Endpoints,
		scaler:            d.Scaler,
		clock:             c,
		logger:            logger.With("component", "activator"),
		activationTimeout: activationTimeout,
		pollInterval:      pollInterval,
		reclaimInterval:   reclaimInterval,
		transport:         transport,
		calls:             d.Calls,
		inflight:          map[FunctionRef]*activation{},
		lastActive:        map[FunctionRef]time.Time{},
		life:              life,
		cancel:            cancel,
		upstreams:         map[FunctionRef]map[string]bool{},
		reclaiming:        map[FunctionRef]chan struct{}{},
	}, nil
}

// --- request-context plumbing for the FunctionRef ---

type ctxKey struct{}

// WithFunction returns r carrying fn for ServeHTTP. The gateway route wiring (P-I) sets
// it from the matched route; tests set it directly.
func WithFunction(r *http.Request, fn FunctionRef) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, fn))
}

func functionFrom(ctx context.Context) (FunctionRef, bool) {
	fn, ok := ctx.Value(ctxKey{}).(FunctionRef)
	return fn, ok
}

// ServeHTTP serves one request: warm → proxy now; cold → single-flight ScaleTo(1),
// wait for a ready upstream (bounded by ActivationTimeout), then forward. On activation
// timeout → 503 problem+json; a request with no FunctionRef in context → 500 problem+json.
func (a *Activator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const op = "activator.ServeHTTP"
	fn, ok := functionFrom(r.Context())
	if !ok {
		fault.WriteProblem(w, fault.Internalf(op, "no function in request context"))
		return
	}
	upstream, err := a.Wake(r.Context(), fn)
	if err != nil {
		fault.WriteProblem(w, err)
		return
	}
	a.forward(w, r, upstream)
}

// Wake returns fn's ready upstream, single-flight-activating (ScaleTo(1) + poll, bounded by
// ActivationTimeout) if it is cold — the shared wake primitive (ADR-0033) used by both the
// data-plane cold path (ServeHTTP) and the eventing trigger path. It records activity (touch,
// feeding idle reclaim); an Endpoints error → fault.Unavailable; a ready function returns its
// upstream immediately; a not-ready one is woken via the existing per-fn singleflight.
func (a *Activator) Wake(ctx context.Context, fn FunctionRef) (string, error) {
	const op = "activator.Wake"
	a.touch(fn)
	upstream, ready, err := a.endpoints.Upstream(ctx, fn)
	if err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "resolve upstream for %s/%s", fn.Namespace, fn.Name)
	}
	if !ready {
		if upstream, err = a.activate(ctx, fn); err != nil {
			return "", err
		}
	}
	a.handedOut(fn, upstream)
	return upstream, nil
}

// touch records last-activity for fn (also seeds first observation). It first waits out a reclaim writing fn's
// scale-to-zero, so the caller then finds fn cold instead of a worker about to stop.
func (a *Activator) touch(fn FunctionRef) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for done := a.reclaiming[fn]; done != nil; done = a.reclaiming[fn] {
		a.mu.Unlock()
		<-done
		a.mu.Lock()
	}
	a.lastActive[fn] = a.clock.Now()
}

// handedOut records that Wake returned upstream for fn, so idle reclaim can ask Calls about it.
func (a *Activator) handedOut(fn FunctionRef, upstream string) {
	if a.calls == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.upstreams[fn] == nil {
		a.upstreams[fn] = map[string]bool{}
	}
	a.upstreams[fn][upstream] = true
}

// activate single-flights a cold-start wake for fn: the first caller (leader) drives
// ScaleTo(fn,1) + the ready-poll; followers attach to the same activation. All waiters
// are released together when a ready upstream appears or the activation times out. A
// caller whose own request ctx is cancelled returns promptly without aborting the shared
// activation (it outlives any single request).
func (a *Activator) activate(ctx context.Context, fn FunctionRef) (string, error) {
	a.mu.Lock()
	act, existed := a.inflight[fn]
	if !existed {
		if a.stopped {
			a.mu.Unlock()
			return "", errStopped(fn)
		}
		act = &activation{done: make(chan struct{})}
		a.inflight[fn] = act
		a.drives.Add(1)
	}
	a.mu.Unlock()

	if !existed {
		go func() {
			defer a.drives.Done()
			a.drive(fn, act)
		}()
	}

	select {
	case <-act.done:
		return act.upstream, act.err
	case <-ctx.Done():
		return "", fault.Wrapf(ctx.Err(), fault.Unavailable, "activator.activate",
			"request cancelled while activating %s/%s", fn.Namespace, fn.Name)
	}
}

// drive runs one shared activation: trigger the wake once, then poll Endpoints until a
// ready upstream appears, ActivationTimeout elapses or Run returns, and resolve all waiters. It
// uses its own bounded context (not a request's) so the shared wake is not tied to one caller.
func (a *Activator) drive(fn FunctionRef, act *activation) {
	ctx, cancel := context.WithTimeout(a.life, a.activationTimeout)
	defer cancel()

	if err := a.scaler.ScaleTo(ctx, fn, 1); err != nil {
		a.resolve(fn, act, "", fault.Wrapf(err, fault.Unavailable, "activator.activate",
			"scale up %s/%s", fn.Namespace, fn.Name))
		return
	}

	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		upstream, ready, err := a.endpoints.Upstream(ctx, fn)
		if err == nil && ready {
			a.resolve(fn, act, upstream, nil)
			return
		}
		if err != nil {
			a.logger.WarnContext(ctx, "endpoint resolve failed during activation",
				"namespace", string(fn.Namespace), "name", string(fn.Name), "error", err)
		}
		select {
		case <-ctx.Done():
			var err error = fault.Unavailablef("activator.activate",
				"function %s/%s did not become ready within %s", fn.Namespace, fn.Name, a.activationTimeout)
			if a.life.Err() != nil {
				err = errStopped(fn)
			}
			a.resolve(fn, act, "", err)
			return
		case <-ticker.C:
		}
	}
}

// resolve records the activation result, removes it from the in-flight map (so a later
// cold request starts a fresh activation — no stuck state), and releases all waiters. A
// successful wake restarts fn's idle window, so a boot longer than IdleTimeout is not
// reclaimed before its held requests are forwarded.
func (a *Activator) resolve(fn FunctionRef, act *activation, upstream string, err error) {
	a.mu.Lock()
	if a.inflight[fn] == act {
		delete(a.inflight, fn)
	}
	if err == nil {
		a.lastActive[fn] = a.clock.Now()
	}
	a.mu.Unlock()
	act.upstream = upstream
	act.err = err
	close(act.done)
}

// forward reverse-proxies r to upstream, streaming each write immediately
// (FlushInterval = -1) so SSE / token streams are not buffered (ADR-0013 parity).
func (a *Activator) forward(w http.ResponseWriter, r *http.Request, upstream string) {
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		fault.WriteProblem(w, fault.Internalf("activator.forward", "invalid upstream %q", upstream))
		return
	}
	if target.Path != "" {
		r = rebase(r, target.Path)
		target = &url.URL{Scheme: target.Scheme, Host: target.Host}
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1
	rp.Transport = a.transport // reuse pooled upstream connections (ADR-0041)
	rp.ServeHTTP(w, r)
}

// rebase addresses r under base, the path an upstream serves its function at: r's root is base
// itself, since a pool worker serves a member at /function/<name> and not at /function/<name>/
// (ADR-0046), and any other path of r is appended to base.
func rebase(r *http.Request, base string) *http.Request {
	out := r.WithContext(r.Context())
	u := *r.URL
	if u.Path == "" || u.Path == "/" {
		u.Path = base
	} else {
		u.Path = strings.TrimSuffix(base, "/") + u.Path
	}
	u.RawPath = ""
	out.URL = &u
	return out
}

// ReclaimIdle scales to zero every minReplicas==0 function whose last activity is older
// than its IdleTimeout, that has no call in flight and no wake in progress. Functions with recent
// activity, MinReplicas != 0, or a zero IdleTimeout (reclaim disabled) are skipped. A function not
// yet seen, or with a call in flight, is given a full grace window from the current time.
func (a *Activator) ReclaimIdle(ctx context.Context) error {
	const op = "activator.ReclaimIdle"
	list, err := a.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list functions")
	}
	now := a.clock.Now()
	for _, obj := range list.Items {
		fn, ok := obj.(*v1.Function)
		if !ok {
			continue
		}
		sc := fn.Spec.Scaling
		if sc.MinReplicas != 0 || sc.IdleTimeout <= 0 {
			continue // scale-to-zero / reclaim not enabled for this function
		}
		ref := FunctionRef{Namespace: fn.Namespace, Name: fn.Name}
		done, idle := a.claimIdle(ref, now, sc.IdleTimeout)
		if !idle {
			continue
		}
		err := a.scaler.ScaleTo(ctx, ref, 0)
		a.mu.Lock()
		delete(a.reclaiming, ref)
		a.mu.Unlock()
		close(done)
		if err != nil {
			a.logger.WarnContext(ctx, "idle reclaim scale-to-zero failed",
				"namespace", string(ref.Namespace), "name", string(ref.Name), "error", err)
		}
	}
	return nil
}

// claimIdle reports whether fn has seen no activity for idleTimeout and has no call in flight to an upstream Wake
// handed out, and no wake in progress (its held requests are traffic, ADR-0016 C3); it then marks fn reclaiming, and the caller closes done once the scale-to-zero is written. A never-seen
// fn, or one with a call in flight, is given a full grace window from now.
func (a *Activator) claimIdle(fn FunctionRef, now time.Time, idleTimeout time.Duration) (done chan struct{}, idle bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, waking := a.inflight[fn]; waking {
		a.lastActive[fn] = now
		return nil, false
	}
	last, seen := a.lastActive[fn]
	if seen && now.Sub(last) <= idleTimeout {
		return nil, false
	}
	busy := false
	for up := range a.upstreams[fn] {
		if a.calls.Idle(up, 0) {
			delete(a.upstreams[fn], up)
		} else {
			busy = true
		}
	}
	if !seen || busy {
		a.lastActive[fn] = now
		return nil, false
	}
	done = make(chan struct{})
	a.reclaiming[fn] = done
	return done, true
}

// Run calls ReclaimIdle every ReclaimInterval until ctx is cancelled. On return it ends every
// in-flight activation (its waiters get Unavailable) and waits for them.
func (a *Activator) Run(ctx context.Context) error {
	defer a.halt()
	ticker := time.NewTicker(a.reclaimInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := a.ReclaimIdle(ctx); err != nil {
				a.logger.WarnContext(ctx, "idle reclaim pass failed", "error", err)
			}
		}
	}
}

// errStopped is what a waiter gets once Run has returned.
func errStopped(fn FunctionRef) error {
	return fault.Unavailablef("activator.activate", "activator stopped before %s/%s became ready", fn.Namespace, fn.Name)
}

// halt stops new activations, cancels the running ones and waits for them to resolve their waiters.
func (a *Activator) halt() {
	a.mu.Lock()
	a.stopped = true
	a.mu.Unlock()
	a.cancel()
	a.drives.Wait()
}
