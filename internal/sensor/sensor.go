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
	"strings"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/eventing"
	"github.com/green-0-rabbit/funcd/internal/expr"
	"github.com/green-0-rabbit/funcd/internal/store"
)

const op = "sensor"

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
	Logger     *slog.Logger
}

// subEntry is a Sensor's live subscription bookkeeping (ADR-0109 B1 idempotency): the cancels for its
// current subscriptions + the generation they were made for, so a resync re-reconcile is a no-op and a
// spec change cancels-and-replaces.
type subEntry struct {
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

	mu   sync.Mutex
	subs map[sensorKey]*subEntry
}

// NewReconciler builds the Sensor reconciler. All deps are required.
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
	return &Reconciler{store: d.Store, subr: d.Subscriber, invoker: d.Invoker, logger: l.With("component", "sensor"), subs: map[sensorKey]*subEntry{}}, nil
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
	if e, present := r.subs[k]; present && e.generation == se.Generation {
		r.mu.Unlock() // already subscribed for this generation — nothing to do
	} else {
		if present {
			for _, c := range e.cancels {
				c()
			}
		}
		r.subs[k] = &subEntry{generation: se.Generation, cancels: r.subscribe(se)}
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
				r.runAction(ctx, ns, rg, name, a, ev)
			}
		})
		cancels = append(cancels, cancel)
	}
	return cancels
}

// runAction executes one action on a firing: build the input, run the kind (start a run / invoke a
// function), and record an Invocation (Ready on success, Failed + Error otherwise) — never silently dropped.
func (r *Reconciler) runAction(ctx context.Context, ns v1.NamespaceName, rg v1.ResourceGroupName, sensor v1.ObjectName, a v1.Action, ev eventing.CloudEvent) {
	start := time.Now().UTC()
	input, err := buildInput(a.Input, ev)
	if err == nil {
		switch {
		case a.Workflow != "":
			err = r.startWorkflow(ctx, ns, rg, sensor, a, input)
		case a.Function != "":
			err = r.invoker.Invoke(ctx, ns, a.Function, ev)
		}
	}
	if recErr := r.record(ctx, ns, rg, ev, start, err); recErr != nil {
		r.logger.WarnContext(ctx, "record invocation failed", "sensor", sensor, "action", a.Name, "error", recErr)
	}
	if err != nil {
		r.logger.WarnContext(ctx, "sensor action failed", "sensor", sensor, "action", a.Name, "error", err)
	}
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
		if len(a.Input) == 0 {
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
// evaluated against {"event": <the CloudEvent JSON>}.
func buildInput(raw json.RawMessage, ev eventing.CloudEvent) (json.RawMessage, error) {
	if len(raw) == 0 {
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
	out := make(map[string]json.RawMessage, len(fields))
	for field, val := range fields {
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
		res, eerr := e.Eval(docs)
		if eerr != nil {
			return nil, fault.Wrapf(eerr, fault.KindOf(eerr), op, "eval input field %q", field)
		}
		out[field] = res
	}
	return json.Marshal(out)
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
