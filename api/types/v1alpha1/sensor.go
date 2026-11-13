package v1alpha1

import (
	"encoding/json"

	"github.com/pyvvo/funcd/api/fault"
)

// Sensor is the reusable event→action binder (ADR-0109, F69): it subscribes named event dependencies
// (`spec.on`, tuple-addressed `(source, event)`) and, on a firing, runs kind-keyed actions (`spec.do` —
// `workflow:` start a run · `function:` invoke), projecting the firing CloudEvent into the target's input.
// Namespaced, status-bearing. Shared by Functions and Workflows.
type Sensor struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       SensorSpec   `json:"spec"`
	Status     SensorStatus `json:"status,omitempty"`
}

// SensorSpec binds event dependencies to actions (ADR-0109).
type SensorSpec struct {
	// On lists the named event dependencies this Sensor subscribes to (≥1, unique names).
	On []Dependency `json:"on" doc:"The events the Sensor subscribes to, as named dependencies: at least one, with unique names."`
	// Do lists the named actions (≥1, unique names); each is bound to one dependency via On.
	Do []Action `json:"do" doc:"The actions the Sensor runs: at least one, with unique names. An event runs every action whose on names its dependency; a failed action is tried eventing.deliveryAttempts times in all, then stored as a dead letter, which funcdctl eventing dlq list shows; while eventing.maxQueuedPerSensor deliveries wait, a new one becomes a dead letter with no attempt."`
}

// Dependency is a named, tuple-addressed event dependency: (source, event) in this namespace.
type Dependency struct {
	Name   ObjectName `json:"name" doc:"The dependency's name, unique among the Sensor's dependencies; an action's on names it."`
	Source ObjectName `json:"source" doc:"The name of an EventSource in the Sensor's namespace."`                                                      // the EventSource name
	Event  ObjectName `json:"event" doc:"The name of one of that EventSource's events, which is the type of each CloudEvent the dependency receives."` // the named event of that source
}

// Action is a named, kind-keyed reaction bound to one dependency (ADR-0109). Exactly one of
// Workflow/Function is set. Input (optional) is a JSON object whose values are literals or `${{ event.* }}`
// projections over the firing CloudEvent (the F73 engine); absent ⇒ the event data verbatim.
type Action struct {
	Name     ObjectName      `json:"name" doc:"The action's name, unique among the Sensor's actions. Dead letters record it, and a WorkflowRun it starts is named after the Sensor and this name, with a random suffix."`
	On       ObjectName      `json:"on" doc:"The name of the dependency whose events run this action, one of the Sensor's on entries."`                                                                                                                                                                                                                                                                                                                                                                                                            // the dependency (Dependency.Name) this fires on
	Workflow ObjectName      `json:"workflow,omitempty" doc:"Each event starts a WorkflowRun of this Workflow, in the Sensor's namespace and resource group, with the action's input. The action succeeds once the run is created; a run whose input breaks the Workflow's contract or passes workflow.payloadLimit fails at its start. Exactly one of workflow and function."`                                                                                                                                                                    // start a WorkflowRun of this Workflow
	Function ObjectName      `json:"function,omitempty" doc:"Each event calls this Function, in the Sensor's namespace, with the CloudEvent, whose data is the action's input; funcd starts a worker first when none runs. A Function whose protocol is http is never called: the Sensor is not Ready, with reason TargetServesHTTP, and each event for this action ends as a dead letter. Exactly one of workflow and function."`                                                                                                                 // invoke this Function with the CloudEvent
	Input    json.RawMessage `json:"input,omitempty" doc:"What the action passes: the run's input, or the data of the CloudEvent the Function receives. Absent or null, it is the event's data; otherwise it is a JSON object whose top-level values are literals, or strings that are a single ${{ }} expression reading event.id, event.source, event.type, event.time or event.data. An input that is not an object, or holds an invalid expression, makes the Sensor not Ready, with reason InvalidInput, and then none of its actions runs."` // object; literals or ${{ event.* }} (checked at reconcile)
}

// SensorStatus holds the observed state (Ready/NotReady with a reason).
type SensorStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Sensor.
func (s *Sensor) GroupVersionKind() GroupVersionKind { return KindSensor.GVK() }

// GetStatus returns the shared Status pointer (the controller write-back seam).
func (s *Sensor) GetStatus() *Status { return &s.Status.Status }

// Validate enforces the wiring rules JSON Schema can't express (ADR-0109/ADR-0048): unique dependency and
// action names; each action bound to a declared dependency and exactly one action kind; DNS-1123 labels
// for source/event/workflow/function. (The `${{ }}` input parse+check is a reconcile-time step — it needs
// the F73 engine — not a Validate rule.)
func (s *Sensor) Validate() error {
	if err := validateMeta(s.TypeMeta, &s.ObjectMeta, KindSensor); err != nil {
		return err
	}
	const op = "Sensor.Validate"
	if len(s.Spec.On) == 0 {
		return fault.Invalidf(op, "spec.on must declare at least one event dependency")
	}
	deps := make(map[ObjectName]bool, len(s.Spec.On))
	for i := range s.Spec.On {
		d := &s.Spec.On[i]
		if !dnsLabel.MatchString(string(d.Name)) {
			return fault.Invalidf(op, "spec.on[%d].name %q is not a valid DNS-1123 label", i, d.Name)
		}
		if deps[d.Name] {
			return fault.Invalidf(op, "duplicate dependency name %q", d.Name)
		}
		deps[d.Name] = true
		if !dnsLabel.MatchString(string(d.Source)) {
			return fault.Invalidf(op, "spec.on[%d].source %q is not a valid DNS-1123 label", i, d.Source)
		}
		if !dnsLabel.MatchString(string(d.Event)) {
			return fault.Invalidf(op, "spec.on[%d].event %q is not a valid DNS-1123 label", i, d.Event)
		}
	}
	if len(s.Spec.Do) == 0 {
		return fault.Invalidf(op, "spec.do must declare at least one action")
	}
	actions := make(map[ObjectName]bool, len(s.Spec.Do))
	for i := range s.Spec.Do {
		a := &s.Spec.Do[i]
		if !dnsLabel.MatchString(string(a.Name)) {
			return fault.Invalidf(op, "spec.do[%d].name %q is not a valid DNS-1123 label", i, a.Name)
		}
		if actions[a.Name] {
			return fault.Invalidf(op, "duplicate action name %q", a.Name)
		}
		actions[a.Name] = true
		if !deps[a.On] {
			return fault.Invalidf(op, "spec.do[%d] (%q) is bound to undeclared dependency %q", i, a.Name, a.On)
		}
		kinds := 0
		if a.Workflow != "" {
			kinds++
			if !dnsLabel.MatchString(string(a.Workflow)) {
				return fault.Invalidf(op, "spec.do[%d].workflow %q is not a valid DNS-1123 label", i, a.Workflow)
			}
		}
		if a.Function != "" {
			kinds++
			if !dnsLabel.MatchString(string(a.Function)) {
				return fault.Invalidf(op, "spec.do[%d].function %q is not a valid DNS-1123 label", i, a.Function)
			}
		}
		if kinds != 1 {
			return fault.Invalidf(op, "spec.do[%d] (%q) must set exactly one action kind (workflow or function), got %d", i, a.Name, kinds)
		}
	}
	return nil
}
