// Package sensor implements the F69 Sensor (ADR-0109): the reusable event→action binder. It subscribes a
// Sensor's named event dependencies to the ADR-0108 in-process Fanout and, on a firing, runs the matching
// kind-keyed actions — start a WorkflowRun (`workflow:`) or invoke a Function (`function:`) — projecting
// the firing CloudEvent into the target's input via the F73 engine, and recording an Invocation per action.
// It owns only KindSensor (one-reconciler-per-gvk, ADR-0015). Stateless in V1: every firing is independent.
package sensor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/expr"
	"github.com/pyvvo/funcd/internal/store"
)

const op = "sensor"

// defaultDeliveryAttempts is the platform-wide bounded-retry cap before an action-delivery is dead-lettered.
const defaultDeliveryAttempts = 3

const condReady = v1.ConditionType("Ready")

// Subscriber is the subscribe seam the Sensor needs from the ADR-0108 Fanout — a single-method port (the
// concrete *eventing.Fanout satisfies it; a value type would copy its lock). The returned cancel deregisters.
type Subscriber interface {
	Subscribe(ns v1.NamespaceName, source, event v1.ObjectName, fn func(context.Context, eventing.CloudEvent)) (cancel func())
}

// Invoker delivers a CloudEvent to a Function's upstream, waking a cold one (ADR-0033). The concrete impl
// (HTTPInvoker, built in pkg/funcd) holds the activator's Endpoints + Waker; the reconciler only calls it.
type Invoker interface {
	Invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error
}

// Deps wires the Sensor reconciler (ADR-0002 §1 — every dep a port).
type Deps struct {
	Store      store.Store
	Subscriber Subscriber
	Invoker    Invoker
	// DeadLetters is the DLQ (ADR-0118). Optional: a nil store disables dead-lettering — the pre-ADR
	// behavior stands (one delivery attempt, one Invocation, no retry loop).
	DeadLetters deadletter.Store
	// DeliveryAttempts is the bounded-retry cap before an action-delivery is dead-lettered (ADR-0118);
	// 0 ⇒ the default (3). Ignored when DeadLetters is nil.
	DeliveryAttempts int
	Logger           *slog.Logger
}

// subEntry is a Sensor's live subscription bookkeeping (ADR-0109 B1 idempotency): the cancels for its
// current subscriptions + the object (UID) and generation they were made for, so a resync re-reconcile is
// a no-op and a spec change or a re-create under the same name (Generation restarts at 1) cancels-and-replaces.
type subEntry struct {
	uid        v1.UID
	generation int64
	cancels    []func()
}

type sensorKey struct {
	ns   v1.NamespaceName
	name v1.ObjectName
}

// Reconciler owns KindSensor (ADR-0109).
type Reconciler struct {
	store   store.Store
	subr    Subscriber
	invoker Invoker
	logger  *slog.Logger

	deadletters      deadletter.Store // ADR-0118: the DLQ (nil ⇒ dead-lettering off)
	deliveryAttempts int              // ADR-0118: bounded-retry cap before dead-lettering
	retry            *retryQueue      // ADR-0118: the rate-limited action-delivery retry queue

	mu   sync.Mutex
	subs map[sensorKey]*subEntry
}

// NewReconciler builds the Sensor reconciler. Store/Subscriber/Invoker are required; DeadLetters +
// DeliveryAttempts are optional (ADR-0118 — nil DeadLetters keeps the pre-ADR one-attempt behavior).
func NewReconciler(d Deps) (*Reconciler, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("sensor.NewReconciler", "store is required")
	}
	if d.Subscriber == nil {
		return nil, fault.Invalidf("sensor.NewReconciler", "subscriber is required")
	}
	if d.Invoker == nil {
		return nil, fault.Invalidf("sensor.NewReconciler", "invoker is required")
	}
	l := d.Logger
	if l == nil {
		l = slog.Default()
	}
	attempts := d.DeliveryAttempts
	if attempts <= 0 {
		attempts = defaultDeliveryAttempts
	}
	return &Reconciler{
		store:            d.Store,
		subr:             d.Subscriber,
		invoker:          d.Invoker,
		logger:           l.With("component", "sensor"),
		deadletters:      d.DeadLetters,
		deliveryAttempts: attempts,
		retry:            newRetryQueue(retryBaseDelay, retryMaxDelay),
		subs:             map[sensorKey]*subEntry{},
	}, nil
}

// Reconcile owns KindSensor: static-check the Sensor, (idempotently) subscribe its dependencies to the
// Fanout, and set Ready — or NotReady with a reason on a static defect; unsubscribe on delete.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	k := sensorKey{req.Namespace, req.Name}
	obj, err := r.store.Get(ctx, v1.KindSensor.GVK(), req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			r.cancelAll(k) // deleted → unsubscribe all
			return controller.Result{}, nil
		}
		return controller.Result{}, err
	}
	se := obj.(*v1.Sensor)

	// Static checks (ADR-0109): parse+Check each ${{ }} input field against the event resolver, before any
	// firing. A defect makes the Sensor NotReady (and drops any stale subscriptions) — fail at the gate.
	if reason, msg, ok := r.staticCheck(se); !ok {
		r.cancelAll(k)
		se.Status.Phase = v1.PhasePending
		se.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: capMsg(msg), ObservedGeneration: se.Generation})
		return controller.Result{}, r.updateStatus(ctx, se)
	}

	// Subscription idempotency (B1): a resync with an unchanged generation is a no-op; a spec change
	// cancels-and-replaces so a firing invokes each dependency's callback exactly once.
	r.mu.Lock()
	if e, present := r.subs[k]; present && e.uid == se.UID && e.generation == se.Generation {
		r.mu.Unlock() // already subscribed for this generation — nothing to do
	} else {
		if present {
			for _, c := range e.cancels {
				c()
			}
		}
		r.subs[k] = &subEntry{uid: se.UID, generation: se.Generation, cancels: r.subscribe(se)}
		r.mu.Unlock()
	}

	if cur, ok := se.Status.Conditions.Get(condReady); !ok || cur.Status != v1.ConditionTrue || se.Status.Phase != v1.PhaseReady {
		se.Status.Phase = v1.PhaseReady
		se.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "Bound", ObservedGeneration: se.Generation})
		return controller.Result{}, r.updateStatus(ctx, se)
	}
	return controller.Result{}, nil
}

// cancelAll cancels a Sensor's subscriptions and forgets it (delete / static defect).
func (r *Reconciler) cancelAll(k sensorKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.subs[k]; ok {
		for _, c := range e.cancels {
			c()
		}
		delete(r.subs, k)
	}
}

// subscribe registers one Fanout subscription per dependency; each callback runs the actions bound to that
// dependency (snapshotted here, so a firing uses the spec as of the subscribe — re-subscribed on a change).
func (r *Reconciler) subscribe(se *v1.Sensor) []func() {
	ns, rg, name := se.Namespace, se.ResourceGroup, se.Name
	byDep := make(map[v1.ObjectName][]v1.Action, len(se.Spec.Do))
	for _, a := range se.Spec.Do {
		byDep[a.On] = append(byDep[a.On], a)
	}
	var cancels []func()
	for i := range se.Spec.On {
		d := se.Spec.On[i]
		acts := byDep[d.Name]
		cancel := r.subr.Subscribe(ns, d.Source, d.Event, func(ctx context.Context, ev eventing.CloudEvent) {
			for _, a := range acts {
				r.runAction(ctx, ns, rg, name, d.Source, d.Event, a, ev)
			}
		})
		cancels = append(cancels, cancel)
	}
	return cancels
}

// runAction executes one action on a firing (ADR-0109 + ADR-0118). It delivers the action inline once; on
// a terminal delivery error, with dead-lettering enabled, the unit is handed to the bounded-retry loop and
// the Invocation is recorded at the TERMINAL outcome (success-after-retries or DLQ) — one per
// action-delivery. With DeadLetters nil the pre-ADR behavior stands: one attempt, one Invocation.
func (r *Reconciler) runAction(ctx context.Context, ns v1.NamespaceName, rg v1.ResourceGroupName, sensor, source, event v1.ObjectName, a v1.Action, ev eventing.CloudEvent) {
	d := delivery{ns: ns, rg: rg, sensor: sensor, source: source, event: event, action: a, ce: ev, firedAt: time.Now().UTC()}
	if r.deadletters == nil { // dead-lettering disabled — one attempt, one Invocation (pre-ADR-0118)
		err := r.deliver(ctx, d)
		if recErr := r.record(ctx, ns, rg, ev, d.firedAt, err); recErr != nil {
			r.logger.WarnContext(ctx, "record invocation failed", "sensor", sensor, "action", a.Name, "error", recErr)
		}
		if err != nil {
			r.logger.WarnContext(ctx, "sensor action failed", "sensor", sensor, "action", a.Name, "error", err)
		}
		return
	}
	r.attemptDelivery(ctx, newRetryID(), d, 1) // inline attempt #1; retries run async on the retry queue
}

// deliver runs ONE delivery attempt of a unit: build the projected input, then start a WorkflowRun with it
// (`workflow:`) or invoke a Function with the CloudEvent carrying it as its data (`function:`). Returns the
// delivery error (nil on success).
func (r *Reconciler) deliver(ctx context.Context, d delivery) error {
	input, err := buildInput(d.action.Input, d.ce)
	if err != nil {
		return err
	}
	switch {
	case d.action.Workflow != "":
		return r.startWorkflow(ctx, d.ns, d.rg, d.sensor, d.action, input)
	case d.action.Function != "":
		ce := d.ce
		ce.Data = input
		return r.invoker.Invoke(ctx, d.ns, d.action.Function, ce)
	}
	return nil
}

// attemptDelivery performs delivery attempt `attempt` of a unit and drives its terminal handling: on
// success it records a Ready Invocation and forgets the key; on failure past DeliveryAttempts it
// dead-letters (Store.Put) + records a Failed Invocation and forgets the key; otherwise it re-enqueues the
// unit on the retry queue with backoff. Invoked from the inline firing (attempt 1) and each retry worker.
func (r *Reconciler) attemptDelivery(ctx context.Context, id string, d delivery, attempt int) {
	err := r.deliver(ctx, d)
	if err == nil {
		r.recordTerminal(ctx, d, nil)
		r.retry.forget(id)
		return
	}
	if attempt >= r.deliveryAttempts { // retries exhausted — park it + record the terminal Failed line
		r.deadLetter(ctx, d, attempt, err)
		r.recordTerminal(ctx, d, err)
		r.retry.forget(id)
		r.logger.WarnContext(ctx, "sensor action dead-lettered", "sensor", d.sensor, "action", d.action.Name, "attempts", attempt, "error", err)
		return
	}
	r.retry.reschedule(id, d, attempt) // schedule attempt+1 after per-key backoff
}

// deadLetter parks a terminally-undeliverable unit in the DLQ (ADR-0118): a fresh ULID id, the full
// CloudEvent payload, the dependency's source/event provenance, the attempt count and terminal reason.
func (r *Reconciler) deadLetter(ctx context.Context, d delivery, attempts int, cause error) {
	payload, err := json.Marshal(d.ce)
	if err != nil {
		r.logger.WarnContext(ctx, "marshal cloudevent for dead-letter failed", "sensor", d.sensor, "action", d.action.Name, "error", err)
		return
	}
	dl := deadletter.DeadLetter{
		ID:        newRetryID(),
		Namespace: d.ns,
		Sensor:    d.sensor,
		Source:    d.source,
		Event:     d.event,
		Action:    string(d.action.Name),
		Payload:   payload,
		Attempts:  attempts,
		Reason:    cause.Error(),
		FailedAt:  time.Now().UTC(),
	}
	if perr := r.deadletters.Put(ctx, dl); perr != nil {
		r.logger.WarnContext(ctx, "dead-letter put failed", "sensor", d.sensor, "action", d.action.Name, "error", perr)
	}
}

// recordTerminal records the single Invocation for a completed action-delivery (Ready on success, Failed +
// Error on dead-letter). StartTime is the firing time so the record spans all attempts.
func (r *Reconciler) recordTerminal(ctx context.Context, d delivery, actionErr error) {
	if recErr := r.record(ctx, d.ns, d.rg, d.ce, d.firedAt, actionErr); recErr != nil {
		r.logger.WarnContext(ctx, "record invocation failed", "sensor", d.sensor, "action", d.action.Name, "error", recErr)
	}
}

// Replay performs ONE synchronous delivery attempt of a stored DeadLetter's CloudEvent through the LIVE
// Sensor's action path (ADR-0118 §3) — it does NOT re-enter the async bounded-retry loop. The replay is an
// action-delivery, so it records its Invocation (Ready or Failed). On success the entry is Deleted and nil
// returned; on failure the entry is re-Put with Attempts reset (never lost) and the delivery error
// returned, marked as re-parked once the re-Put succeeded. A missing DeadLetter / Sensor / action ⇒
// NotFound (the operator discards).
// Idempotent: a repeat replay of a still-broken target re-parks with a fresh attempt count.
func (r *Reconciler) Replay(ctx context.Context, ns v1.NamespaceName, id string) error {
	if r.deadletters == nil {
		return fault.Unavailablef("sensor.Replay", "dead-lettering is not enabled")
	}
	dl, err := r.deadletters.Get(ctx, ns, id) // NotFound propagates
	if err != nil {
		return err
	}
	obj, err := r.store.Get(ctx, v1.KindSensor.GVK(), ns, dl.Sensor)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return fault.NotFoundf("sensor.Replay", "sensor %q/%q no longer exists", ns, dl.Sensor)
		}
		return err
	}
	se := obj.(*v1.Sensor)
	action, ok := findAction(se, dl.Action)
	if !ok {
		return fault.NotFoundf("sensor.Replay", "action %q is no longer defined on sensor %q/%q", dl.Action, ns, dl.Sensor)
	}
	var ce eventing.CloudEvent
	if uerr := json.Unmarshal(dl.Payload, &ce); uerr != nil {
		return fault.Invalidf("sensor.Replay", "stored payload is not a CloudEvent: %v", uerr)
	}
	d := delivery{ns: ns, rg: se.ResourceGroup, sensor: dl.Sensor, source: dl.Source, event: dl.Event, action: action, ce: ce, firedAt: time.Now().UTC()}
	derr := r.deliver(ctx, d)
	r.recordTerminal(ctx, d, derr)
	if derr != nil {
		dl.Attempts = 0 // reset — a fresh attempt count for the re-parked entry
		dl.Reason = derr.Error()
		dl.FailedAt = time.Now().UTC()
		if perr := r.deadletters.Put(ctx, dl); perr != nil {
			r.logger.WarnContext(ctx, "re-park after failed replay failed", "sensor", dl.Sensor, "id", id, "error", perr)
			return derr
		}
		return fault.Wrapf(derr, fault.KindOf(derr), "sensor.Replay", "delivery failed (entry re-parked)")
	}
	return r.deadletters.Delete(ctx, ns, id)
}

// findAction returns the Sensor action named `name` on the live spec (ADR-0118 replay resolves the current
// spec, so a fixed Sensor makes replay succeed).
func findAction(se *v1.Sensor, name string) (v1.Action, bool) {
	for _, a := range se.Spec.Do {
		if string(a.Name) == name {
			return a, true
		}
	}
	return v1.Action{}, false
}

// newRetryID mints a unique, time-sortable id for a retry-queue delivery unit / DLQ record (ULID over
// crypto entropy — safe for concurrent minting across the retry workers). On the near-impossible entropy
// error it falls back to a random-hex id (still unique, just not ULID-sortable).
func newRetryID() string {
	id, err := ulid.New(ulid.Timestamp(time.Now()), rand.Reader)
	if err != nil {
		return randHex(16)
	}
	return id.String()
}

// startWorkflow creates a WorkflowRun for a `workflow:` action in the Sensor's namespace + ResourceGroup,
// with the projected input. Created on the internal store (admission-skipping, as the engine's own child
// runs are) — the ADR-0098 input-contract check + ADR-0094 payload cap are the run-start-gate backstop.
func (r *Reconciler) startWorkflow(ctx context.Context, ns v1.NamespaceName, rg v1.ResourceGroupName, sensor v1.ObjectName, a v1.Action, input json.RawMessage) error {
	obj, ok := v1.NewObject(v1.KindWorkflowRun)
	if !ok {
		return fault.Internalf(op, "unknown kind %s", v1.KindWorkflowRun)
	}
	run := obj.(*v1.WorkflowRun)
	run.Name = runName(sensor, a.Name)
	run.Namespace = ns
	run.ResourceGroup = rg
	run.Spec.Workflow = a.Workflow
	run.Spec.Input = input
	if _, err := r.store.Create(ctx, run); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "create workflow run for action %q", a.Name)
	}
	return nil
}

// record persists an Invocation for one action firing (Ready on success, Failed + Error otherwise).
func (r *Reconciler) record(ctx context.Context, ns v1.NamespaceName, rg v1.ResourceGroupName, ev eventing.CloudEvent, start time.Time, actionErr error) error {
	obj, ok := v1.NewObject(v1.KindInvocation)
	if !ok {
		return fault.Internalf(op, "unknown kind %s", v1.KindInvocation)
	}
	inv := obj.(*v1.Invocation)
	inv.Name = v1.ObjectName("inv-" + randHex(10)) // unique per action (a firing runs several)
	inv.Namespace = ns
	inv.ResourceGroup = rg
	inv.Status.StartTime = start
	inv.Status.EndTime = time.Now().UTC()
	if actionErr != nil {
		inv.Status.Error = actionErr.Error()
		inv.Status.Phase = v1.PhaseFailed
	} else {
		inv.Status.Phase = v1.PhaseReady
	}
	_, err := r.store.Create(ctx, inv)
	return err
}

func (r *Reconciler) updateStatus(ctx context.Context, se *v1.Sensor) error {
	if _, err := r.store.Update(ctx, se); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "update sensor status %q", se.Name)
	}
	return nil
}

// staticCheck validates every action's ${{ }} input fields at reconcile (parse + Check against the event
// resolver), so a bad static overlay makes the Sensor NotReady before any event fires. ok=false ⇒ reason+msg.
func (r *Reconciler) staticCheck(se *v1.Sensor) (reason, msg string, ok bool) {
	for i := range se.Spec.Do {
		a := &se.Spec.Do[i]
		if inputAbsent(a.Input) {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(a.Input, &fields); err != nil {
			return "InvalidInput", fmt.Sprintf("action %q input is not a JSON object", a.Name), false
		}
		for field, val := range fields {
			src, isExpr := asExprString(val)
			if !isExpr {
				continue // a literal
			}
			e, err := expr.Parse(src, expr.Select)
			if err != nil {
				return "InvalidInput", fmt.Sprintf("action %q field %q: %v", a.Name, field, err), false
			}
			if err := e.Check(eventResolver{}); err != nil {
				return "InvalidInput", fmt.Sprintf("action %q field %q: %v", a.Name, field, err), false
			}
		}
	}
	return "", "", true
}

// buildInput projects an action's input over a fired CloudEvent (ADR-0109): absent ⇒ the event data
// verbatim; else each field is a literal (passed through) or a `${{ event.* }}` Select expression
// evaluated against {"event": <the CloudEvent JSON>}. The expressions share one expr.Budget, so an input
// split into many fields costs no more than one expression may.
func buildInput(raw json.RawMessage, ev eventing.CloudEvent) (json.RawMessage, error) {
	if inputAbsent(raw) {
		if len(ev.Data) == 0 {
			return json.RawMessage("{}"), nil
		}
		return ev.Data, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fault.Invalidf(op, "action input is not a JSON object: %v", err)
	}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return nil, fault.Internalf(op, "marshal cloudevent: %v", err)
	}
	docs := map[string]json.RawMessage{"event": evJSON}
	budget := expr.NewBudget()
	out := make(map[string]json.RawMessage, len(fields))
	// In a fixed order, so the same field spends the budget on every firing.
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		val := fields[field]
		src, isExpr := asExprString(val)
		if !isExpr {
			out[field] = val // literal
			continue
		}
		e, perr := expr.Parse(src, expr.Select)
		if perr != nil {
			return nil, fault.Wrapf(perr, fault.KindOf(perr), op, "input field %q", field)
		}
		if cerr := e.Check(eventResolver{}); cerr != nil {
			return nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "input field %q", field)
		}
		res, eerr := e.EvalWithin(budget, docs)
		if eerr != nil {
			return nil, fault.Wrapf(eerr, fault.KindOf(eerr), op, "eval input field %q", field)
		}
		out[field] = res
	}
	return json.Marshal(out)
}

// inputAbsent reports whether an action has no input: the key is omitted, or set to null (a bare `input:`,
// `input: null` or `input: ~` in YAML).
func inputAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// asExprString reports whether a JSON value is a `${{ … }}` expression string (and returns it).
func asExprString(val json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(val, &s) != nil {
		return "", false // not a JSON string
	}
	if strings.HasPrefix(strings.TrimSpace(s), "${{") {
		return s, true
	}
	return "", false
}

// runName builds a DNS-1123-label WorkflowRun name for a `workflow:` action, bounded to 63 chars: a stable
// <sensor>-<action> prefix (truncated) + a random hex suffix for uniqueness across firings.
func runName(sensor, action v1.ObjectName) v1.ObjectName {
	suffix := "-" + randHex(4) // "-" + 8 hex = 9 chars
	base := string(sensor) + "-" + string(action)
	if max := 63 - len(suffix); len(base) > max {
		base = base[:max]
	}
	base = strings.TrimRight(base, "-")
	if base == "" {
		base = "run"
	}
	return v1.ObjectName(base + suffix)
}

// randHex returns n random bytes as 2n hex chars ("" on the near-impossible crypto/rand error).
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b)
}

// capMsg bounds a NotReady condition message so a huge expression error can't bloat status.
func capMsg(s string) string {
	const max = 512
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
