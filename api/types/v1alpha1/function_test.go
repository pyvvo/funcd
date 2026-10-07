package v1alpha1

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

func wfWith(step WorkflowStep, timeout Duration) *Workflow {
	w := &Workflow{Spec: WorkflowSpec{Steps: []WorkflowStep{step}, Timeout: timeout}}
	w.TypeMeta = TypeMeta{APIVersion: KindWorkflow.GVK().APIVersion(), Kind: KindWorkflow}
	w.Name, w.Namespace, w.ResourceGroup = "wf", "default", "rg1"
	return w
}

// durationField is one Contracts row of ADR-0194: the field, its bounds and an object carrying a value in it.
type durationField struct {
	path   string
	owner  reflect.Type
	name   string
	lo, hi Duration
	with   func(Duration) interface{ Validate() error }
}

func durationFields() []durationField {
	fnStep := func(d Duration) WorkflowStep {
		return WorkflowStep{Name: "a", Function: &FunctionStep{Image: "oci:x", Timeout: d}}
	}
	return []durationField{
		{"spec.timer.events[0].interval", reflect.TypeFor[TimerEvent](), "Interval", MinTimerInterval, MaxTimerInterval,
			func(d Duration) interface{ Validate() error } {
				return esWith(EventSourceSpec{Timer: &TimerSource{Events: []TimerEvent{{Name: "t", Interval: d}}}})
			}},
		{"spec.timeout", reflect.TypeFor[FunctionSpec](), "Timeout", 0, Duration(MaxInvokeTimeout),
			func(d Duration) interface{ Validate() error } { return fnWith(FunctionSpec{Timeout: d}) }},
		{"spec.links[0].timeout", reflect.TypeFor[FunctionLink](), "Timeout", 0, MaxLinkTimeout,
			func(d Duration) interface{ Validate() error } {
				return fnWith(FunctionSpec{Links: []FunctionLink{{Alias: "a", Target: "b", Timeout: d}}})
			}},
		{"spec.scaling.idleTimeout", reflect.TypeFor[Scaling](), "IdleTimeout", 0, MaxIdleTimeout,
			func(d Duration) interface{ Validate() error } {
				return fnWith(FunctionSpec{Scaling: Scaling{IdleTimeout: d}})
			}},
		{"spec.timeout", reflect.TypeFor[WorkflowSpec](), "Timeout", 0, MaxWorkflowTimeout,
			func(d Duration) interface{ Validate() error } { return wfWith(fnStep(0), d) }},
		{"spec.steps[0].function.timeout", reflect.TypeFor[FunctionStep](), "Timeout", 0, MaxStepTimeout,
			func(d Duration) interface{ Validate() error } { return wfWith(fnStep(d), 0) }},
		{"spec.steps[0].function.retry.backoff", reflect.TypeFor[StepRetry](), "Backoff", 0, MaxRetryBackoff,
			func(d Duration) interface{ Validate() error } {
				s := fnStep(0)
				s.Function.Retry = &StepRetry{Backoff: d}
				return wfWith(s, 0)
			}},
	}
}

// Each Contracts row of ADR-0194 is bounded in Validate at lo − 1ms, lo, hi and hi + 1ms, and a sub-millisecond
// value set from Go is refused; the message names the field, the value and [lo, hi].
func TestDurationFieldBounds(t *testing.T) {
	const ms = Duration(time.Millisecond)
	for _, f := range durationFields() {
		t.Run(f.owner.Name()+"."+f.name, func(t *testing.T) {
			require.NoError(t, f.with(f.lo).Validate())
			require.NoError(t, f.with(f.hi).Validate())
			for _, bad := range []Duration{f.lo - ms, f.hi + ms, f.lo + 1} {
				err := f.with(bad).Validate()
				require.Equal(t, fault.Invalid, fault.KindOf(err), "%s = %s: %v", f.path, bad, err)
				require.ErrorContains(t, err, f.path+" "+bad.String())
				require.ErrorContains(t, err, "["+f.lo.String()+", "+f.hi.String()+"]")
			}
		})
	}
}

// Each field's doc tag names its bounds from the same constants Validate uses, and no numeric tag is left for huma
// to copy onto the string schema (ADR-0194 Decision 6).
func TestDurationFieldDocNamesBounds(t *testing.T) {
	for _, f := range durationFields() {
		sf, ok := f.owner.FieldByName(f.name)
		require.True(t, ok, f.path)
		require.Equal(t, reflect.TypeFor[Duration](), sf.Type, f.path)
		doc := sf.Tag.Get("doc")
		require.Contains(t, doc, f.lo.String()+" to "+f.hi.String(), "%s doc %q", f.path, doc)
		for _, tag := range []string{"minimum", "maximum", "format"} {
			_, has := sf.Tag.Lookup(tag)
			require.False(t, has, "%s carries a %s tag", f.path, tag)
		}
	}
}
