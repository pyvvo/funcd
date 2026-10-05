// Package activator implements scale-to-zero (ADR-0016): an in-process data-path
// component backing a gateway route's upstream. For a request to a scaled-to-zero
// function it buffers the request, single-flights a wake (Scaler.ScaleTo(fn,1)),
// waits for a ready upstream to appear, then forwards the held request (or 503s on
// timeout, or at once for a Failed function); and it runs a periodic idle-reclaim
// pass that scales idle functions to zero. It emits a scale INTENT through two seams
// (Endpoints read / Scaler write); it does not provision workers (that is the P-M
// Function reconciler) and it does not register a Function reconciler
// (one-reconciler-per-gvk, ADR-0015).
package activator

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/store"
)

// FunctionRef identifies the function a request / scale-decision targets. A ref with a Revision names one revision
// of the Function with UID (ADR-0190), so single-flight, activity and idle reclaim keyed by it are per revision.
type FunctionRef struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	Revision  v1.ObjectName // "" ⇒ the serving revision
	UID       v1.UID        // set with Revision
}

// function is fn without its revision.
func (fn FunctionRef) function() FunctionRef {
	return FunctionRef{Namespace: fn.Namespace, Name: fn.Name}
}

// reclaims are the refs whose idle reclaim stops fn's worker: fn and, for a ref pinned to a revision, its Function,
// whose reclaim stops the serving revision a pinned call may reach.
func (fn FunctionRef) reclaims() []FunctionRef {
	if fn.Revision == "" {
		return []FunctionRef{fn}
	}
	return []FunctionRef{fn, fn.function()}
}

// heldIdleTimeout is the idle window of a held revision whose Function has none, the step Functions' (ADR-0094), so a
// paused run holds disk, never RAM (ADR-0190 Decision 10).
const heldIdleTimeout = 5 * time.Minute

// HeldRevision reports whether ref is pinned to a revision of f other than its current and serving ones, or, for a pooled
// f, other than its current one, which alone its pool holds (ADR-0190 Decisions 6 and 8): a wake of it is a demand on
// that Revision, which runs solo, and its Revision.status, not f's phase, judges it.
func HeldRevision(f *v1.Function, ref FunctionRef) bool {
	rev := string(ref.Revision)
	return rev != "" && f.UID == ref.UID && rev != f.Status.CurrentRevision && (f.Status.Pool != "" || rev != f.Status.ServingRevision)
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
	Store             store.Store   // lists Functions for idle reclaim; read for a Failed phase while activating
	Endpoints         Endpoints     // ready-upstream resolver (required)
	Scaler            Scaler        // scale-intent writer (required)
	Clock             clock.Clock   // default clock.System()
	Logger            *slog.Logger  // default slog.Default()
	ActivationTimeout time.Duration // cold-start hold bound; default 30s
	PollInterval      time.Duration // ready-poll cadence; default 25ms
	ReclaimInterval   time.Duration // idle-reclaim cadence for Run; default 30s
	Calls             *CallTracker  // counts the calls to each worker (ADR-0143), which idle reclaim also reads; nil ⇒ uncounted
}

// condReady is the Function condition whose reason says why a Failed function cannot serve.
const condReady v1.ConditionType = "Ready"

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
	lastActive map[FunctionRef]activity        // last-activity tracker feeding idle reclaim
	upstreams  map[FunctionRef]map[string]bool // upstreams Wake handed out, whose calls in flight idle reclaim spares
	reclaiming map[FunctionRef]chan struct{}   // a reclaim writing fn's scale-to-zero; closed when it is written
	stopped    bool                            // Run has returned: no new activation starts

	// life bounds every activation and is cancelled when Run returns: the platform that would start a woken worker
	// stops with it, so a held request could never be served. drives counts the running activations.
	life   context.Context
	cancel context.CancelFunc
	drives sync.WaitGroup
}

// activity is a function's last-activity time and the UID of the Function a reclaim pass attributed it
// to ("" until one has), so a deleted-and-re-created name never inherits its predecessor's activity.
type activity struct {
	at  time.Time
	uid v1.UID
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
	var transport http.RoundTripper = httpx.NodeTransport()
	if d.Calls != nil {
		transport = d.Calls.Wrap(transport)
	}
	transport = DeadlineTransport(transport)
	transport = idleTunnel(transport, tunnelIdleTimeout)
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
		lastActive:        map[FunctionRef]activity{},
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
// timeout or for a Failed function → 503 problem+json; a request with no FunctionRef in
// context → 500 problem+json. A request with a ResponseDeadline gets the DeadlineOp 504 when the
// deadline passes before the upstream's headers: at once when already past (no wake), during the wake
// (the shared activation goes on, ADR-0016), or during the call (DeadlineTransport) — ADR-0151.
func (a *Activator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const op = "activator.ServeHTTP"
	fn, ok := functionFrom(r.Context())
	if !ok {
		fault.WriteProblem(w, fault.Internalf(op, "no function in request context"))
		return
	}
	ctx := r.Context()
	d, bounded := responseDeadlineFrom(ctx)
	var expired error
	if bounded {
		expired = deadlineFault(fn, d)
		if !time.Now().Before(d.At) {
			fault.WriteProblem(w, expired)
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadlineCause(ctx, d.At, expired)
		defer cancel()
	}
	upstream, err := a.Wake(ctx, fn)
	if err != nil {
		if bounded && r.Context().Err() == nil && errors.Is(context.Cause(ctx), expired) {
			err = expired
		}
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
	if fault.KindOf(err) == fault.NotFound && fn.Revision != "" {
		return "", fault.Wrapf(err, fault.NotFound, op, "resolve upstream for %s/%s", fn.Namespace, fn.Name) // a pin that is gone (ADR-0190)
	}
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

// touch records last-activity for fn (also seeds first observation). It first waits out a reclaim writing a
// scale-to-zero that stops fn's worker, so the caller then finds fn cold instead of a worker about to stop.
func (a *Activator) touch(fn FunctionRef) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range fn.reclaims() {
		for done := a.reclaiming[k]; done != nil; done = a.reclaiming[k] {
			a.mu.Unlock()
			<-done
			a.mu.Lock()
		}
	}
	a.active(fn)
}

// active restarts fn's idle window; the caller holds a.mu.
func (a *Activator) active(fn FunctionRef) {
	e := a.lastActive[fn]
	e.at = a.clock.Now()
	a.lastActive[fn] = e
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

// drive runs one shared activation: trigger the wake once, then poll until a ready
// upstream appears, the Function turns Failed, ActivationTimeout elapses or Run returns,
// and resolve all waiters. It uses its own bounded context (not a request's) so the shared
// wake is not tied to one caller.
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
		if fault.KindOf(err) == fault.NotFound && fn.Revision != "" { // the pinned revision is gone (ADR-0190)
			a.resolve(fn, act, "", err)
			return
		}
		if err != nil {
			a.logger.WarnContext(ctx, "endpoint resolve failed during activation",
				"namespace", string(fn.Namespace), "name", string(fn.Name), "error", err)
		}
		if ferr := a.failed(ctx, fn); ferr != nil {
			a.resolve(fn, act, "", ferr)
			return
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

// failed returns FailedFault for fn's Function, or RevisionFailedFault for a held revision fn is pinned to (ADR-0190
// Decision 5): once the wake is accepted, the reconciler may fail it during the activation (a shim that cannot load
// the handler). Without a Store, or when a read fails, it is nil and the poll goes on.
func (a *Activator) failed(ctx context.Context, fn FunctionRef) error {
	const op = "activator.activate"
	if a.store == nil {
		return nil
	}
	obj, err := a.store.Get(ctx, v1.KindFunction.GVK(), fn.Namespace, fn.Name)
	if err != nil {
		a.logger.DebugContext(ctx, "function read failed during activation",
			"namespace", string(fn.Namespace), "name", string(fn.Name), "error", err)
		return nil
	}
	f, ok := obj.(*v1.Function)
	if !ok {
		return nil
	}
	if !HeldRevision(f, fn) {
		return FailedFault(op, f)
	}
	obj, err = a.store.Get(ctx, v1.KindRevision.GVK(), fn.Namespace, fn.Revision)
	if err != nil {
		a.logger.DebugContext(ctx, "revision read failed during activation",
			"namespace", string(fn.Namespace), "revision", string(fn.Revision), "error", err)
		return nil
	}
	rev, ok := obj.(*v1.Revision)
	if !ok {
		return nil
	}
	return RevisionFailedFault(op, fn, rev)
}

// RevisionFailedFault is the fault.Unavailable a call pinned to the held revision rev is answered with when rev is
// Failed, naming its Ready reason, else nil (ADR-0190 Decision 5).
func RevisionFailedFault(op string, fn FunctionRef, rev *v1.Revision) error {
	if rev.Status.Phase != v1.PhaseFailed {
		return nil
	}
	ready, _ := rev.Status.Conditions.Get(condReady)
	return fault.Unavailablef(op, "revision %q of function %s/%s is Failed (%s)", rev.Name, fn.Namespace, fn.Name, ready.Reason)
}

// FailedFault is the fault.Unavailable a call to f is answered with when f is Failed, naming
// its Ready reason, else nil. A wake does not leave Failed (ADR-0016 C2) and the reconciler
// does not restart a ShapeInvalid worker (ADR-0142), so a held call could only time out
// (issue #142).
func FailedFault(op string, f *v1.Function) error {
	if f.Status.Phase != v1.PhaseFailed {
		return nil
	}
	ready, _ := f.Status.Conditions.Get(condReady)
	return fault.Unavailablef(op, "function %s/%s is Failed (%s)", f.Namespace, f.Name, ready.Reason)
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
		a.active(fn)
	}
	a.mu.Unlock()
	act.upstream = upstream
	act.err = err
	close(act.done)
}

// forward reverse-proxies r to upstream, streaming each write immediately
// (FlushInterval = -1) so SSE / token streams are not buffered (ADR-0013 parity). A failed
// upstream call is an Unavailable problem+json logged through slog (ADR-0002), not the
// ReverseProxy default (a bare 502 logged through the stdlib log package); the cause, like a malformed
// upstream, names the worker's address, so it stays in the log. ErrorLog sends ReverseProxy's own
// errors (a failed body copy) through the same logger (#511).
func (a *Activator) forward(w http.ResponseWriter, r *http.Request, upstream string) {
	const op = "activator.forward"
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		a.logger.ErrorContext(r.Context(), "worker upstream is not a valid URL", "upstream", upstream)
		fault.WriteProblem(w, fault.Internalf(op, "worker upstream is misconfigured"))
		return
	}
	if target.Path != "" {
		r = rebase(r, target.Path)
		target = &url.URL{Scheme: target.Scheme, Host: target.Host}
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1
	rp.Transport = a.transport // reuse pooled upstream connections (ADR-0041)
	logger := a.logger.With("upstream", upstream)
	rp.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, perr error) {
		if fault.KindOf(perr) == fault.DeadlineExceeded {
			fault.WriteProblem(w, perr)
			return
		}
		logger.WarnContext(r.Context(), "upstream call failed", "error", perr)
		fault.WriteProblem(w, fault.Unavailablef(op, "upstream call failed"))
	}
	KeepEdgeHeaders(rp, w)
	rp.ServeHTTP(w, r)
}

// KeepEdgeHeaders makes rp put the headers w carries now back before the final response or the error
// response: httputil.ReverseProxy clears the whole header map after it relays an upstream 1xx, which
// would drop the X-Request-Id and CORS headers the edge set before proxying (#417). ReverseProxy
// appends the upstream's values, so the upstream's copy of an edge header is dropped and the edge's
// value wins, like the shaping header rules; Vary is a list, so both sides' values are kept (#720). It
// sets rp.ModifyResponse and wraps rp.ErrorHandler, which must be set first.
func KeepEdgeHeaders(rp *httputil.ReverseProxy, w http.ResponseWriter) {
	edge := w.Header().Clone()
	rp.ModifyResponse = func(res *http.Response) error {
		for k := range edge {
			if k != "Vary" {
				res.Header.Del(k)
			}
		}
		maps.Copy(w.Header(), edge)
		return nil
	}
	onError := rp.ErrorHandler
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		maps.Copy(w.Header(), edge)
		onError(w, r, err)
	}
}

// rebase addresses r under base, the path an upstream serves its function at: r's root is base
// itself, since a pool worker serves a member at /function/<name> and not at /function/<name>/
// (ADR-0046), and any other path of r is appended to base. The appended path is resolved as if
// rooted at "/" first, so a dot segment cannot climb out of base: a pool worker resolves
// /function/<name>/../<other> to the sibling member <other>.
func rebase(r *http.Request, base string) *http.Request {
	out := r.WithContext(r.Context())
	u := *r.URL
	if rest := rootedClean(u.Path); rest == "/" {
		u.Path = base
	} else {
		u.Path = strings.TrimSuffix(base, "/") + rest
	}
	u.RawPath = ""
	out.URL = &u
	return out
}

// rootedClean resolves the dot segments of p as a path rooted at "/" and keeps a trailing slash.
func rootedClean(p string) string {
	c := path.Clean("/" + p)
	if c != "/" && strings.HasSuffix(p, "/") {
		c += "/"
	}
	return c
}

// ReclaimIdle scales to zero every minReplicas==0 function in a Reclaimable phase whose last activity is older
// than its IdleTimeout, that has no call in flight and no wake in progress. Functions with recent
// activity, MinReplicas != 0, a zero IdleTimeout (reclaim disabled), or any other phase — Pending, Failed,
// Deploying, Terminating, Idle — are skipped. A function not yet seen, or with a call in flight, is
// given a full grace window from the current time. Entries for functions that no longer exist are dropped. A held
// revision is reclaimed by its pinned ref after its Function's IdleTimeout, or heldIdleTimeout when that is zero,
// whatever the replica floor (ADR-0190 Decisions 6 and 10).
func (a *Activator) ReclaimIdle(ctx context.Context) error {
	const op = "activator.ReclaimIdle"
	list, err := a.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list functions")
	}
	now := a.clock.Now()
	live := make(map[FunctionRef]struct{}, len(list.Items))
	fns := make(map[FunctionRef]*v1.Function, len(list.Items))
	for _, obj := range list.Items {
		fn, ok := obj.(*v1.Function)
		if !ok {
			continue
		}
		ref := FunctionRef{Namespace: fn.Namespace, Name: fn.Name}
		live[ref], fns[ref] = struct{}{}, fn
		sc := fn.Spec.Scaling
		if sc.MinReplicas != 0 || sc.IdleTimeout <= 0 {
			continue // scale-to-zero / reclaim not enabled for this function
		}
		if Reclaimable(fn) {
			a.reclaim(ctx, ref, fn.UID, now, sc.IdleTimeout, func(p FunctionRef) bool { return p.UID == fn.UID && !HeldRevision(fn, p) })
		}
	}
	for _, ref := range a.pinnedRefs() {
		fn, ok := fns[ref.function()]
		if !ok || fn.UID != ref.UID {
			continue // the pinned Function is gone: forgotten
		}
		if !HeldRevision(fn, ref) {
			live[ref] = struct{}{} // a call pinned to the current or serving revision counts for its Function's reclaim
			continue
		}
		obj, err := a.store.Get(ctx, v1.KindRevision.GVK(), ref.Namespace, ref.Revision)
		if err != nil {
			continue
		}
		live[ref] = struct{}{}
		// a held revision is reclaimed whatever the Function's replica floor, which is its current revision's
		// (ADR-0190 Decision 6)
		idle := fn.Spec.Scaling.IdleTimeout
		if idle <= 0 {
			idle = heldIdleTimeout
		}
		if rev, ok := obj.(*v1.Revision); ok && ReclaimablePhase(rev.Status.Phase) {
			a.reclaim(ctx, ref, ref.UID, now, idle, nil)
		}
	}
	a.forgetAllBut(live)
	return nil
}

// reclaim scales ref to zero when claimIdle finds it idle.
func (a *Activator) reclaim(ctx context.Context, ref FunctionRef, uid v1.UID, now time.Time, idleTimeout time.Duration, serves func(FunctionRef) bool) {
	done, idle := a.claimIdle(ref, uid, now, idleTimeout, serves)
	if !idle {
		return
	}
	err := a.scaler.ScaleTo(ctx, ref, 0)
	a.mu.Lock()
	delete(a.reclaiming, ref)
	a.mu.Unlock()
	close(done)
	if err != nil {
		a.logger.WarnContext(ctx, "idle reclaim scale-to-zero failed",
			"namespace", string(ref.Namespace), "name", string(ref.Name), "revision", string(ref.Revision), "error", err)
	}
}

// pinnedRefs are the tracked refs pinned to a revision, whose activity idle reclaim judges per revision.
func (a *Activator) pinnedRefs() []FunctionRef {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []FunctionRef
	for ref := range a.lastActive {
		if ref.Revision != "" {
			out = append(out, ref)
		}
	}
	return out
}

// Reclaimable reports whether idle reclaim may move fn to Idle (ADR-0016 C2, ADR-0169, ADR-0185): a gate-held
// Pending Function runs no worker, so it is never reclaimed.
func Reclaimable(fn *v1.Function) bool {
	return ReclaimablePhase(fn.Status.Phase)
}

// ReclaimablePhase reports whether idle reclaim may move a Function or a held Revision in phase p to Idle.
func ReclaimablePhase(p v1.Phase) bool {
	switch p {
	case v1.PhaseReady, v1.PhaseDegraded, "":
		return true
	}
	return false
}

// claimIdle reports whether fn has seen no activity for idleTimeout and has no call in flight to an upstream Wake
// handed out, and no wake in progress (its held requests are traffic, ADR-0016 C3); the activity of a ref pinned to a
// revision of fn that serves reports counts as fn's (ADR-0190 Decision 5). It then marks fn reclaiming, and the caller
// closes done once the scale-to-zero is written. A fn not yet seen under this uid, or one with a call in flight, is
// given a full grace window from now.
func (a *Activator) claimIdle(fn FunctionRef, uid v1.UID, now time.Time, idleTimeout time.Duration, serves func(FunctionRef) bool) (done chan struct{}, idle bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := []FunctionRef{fn}
	for ref := range a.lastActive {
		if serves != nil && ref.Revision != "" && ref.function() == fn && serves(ref) {
			keys = append(keys, ref)
		}
	}
	for _, k := range keys {
		if _, waking := a.inflight[k]; waking {
			a.lastActive[fn] = activity{at: now, uid: uid}
			return nil, false
		}
	}
	e, ok := a.lastActive[fn]
	seen := ok && e.uid == uid
	recent := seen && now.Sub(e.at) <= idleTimeout
	for _, k := range keys[1:] {
		recent = recent || now.Sub(a.lastActive[k].at) <= idleTimeout
	}
	if recent {
		return nil, false
	}
	busy := false
	for _, k := range keys {
		for up := range a.upstreams[k] {
			if a.calls.Idle(up, 0) {
				delete(a.upstreams[k], up)
			} else {
				busy = true
			}
		}
	}
	if !seen || busy {
		a.lastActive[fn] = activity{at: now, uid: uid}
		return nil, false
	}
	done = make(chan struct{})
	a.reclaiming[fn] = done
	return done, true
}

// forgetAllBut drops the tracker entries of every function not in live.
func (a *Activator) forgetAllBut(live map[FunctionRef]struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for fn := range a.lastActive {
		if _, ok := live[fn]; !ok {
			delete(a.lastActive, fn)
			delete(a.upstreams, fn)
		}
	}
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
