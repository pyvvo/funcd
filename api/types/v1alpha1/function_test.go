package v1alpha1

import (
	"reflect"
	"strings"
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
		{"spec.bindings.links[0].timeout", reflect.TypeFor[FunctionLink](), "Timeout", 0, MaxLinkTimeout,
			func(d Duration) interface{ Validate() error } {
				return fnWith(FunctionSpec{Bindings: FunctionBindings{Links: []FunctionLink{{Alias: "a", Target: "b", Timeout: d}}}})
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

// scenario: wait-literal-checked-at-apply — a literal builtin.wait follows the duration grammar, checked by Validate
// (funcdctl apply's offline pre-flight and every store write); an expression is checked when it runs.
func TestScenarioWaitLiteralCheckedAtApply(t *testing.T) {
	wait := func(w string) *Workflow {
		return wfWith(WorkflowStep{Name: "pause", Builtin: &BuiltinStep{Wait: w}}, 0)
	}
	for _, bad := range []string{"1.5s", "30", "500us", "-1s"} {
		err := wait(bad).Validate()
		require.Equal(t, fault.Invalid, fault.KindOf(err), bad)
		require.ErrorContains(t, err, `step "pause"`, bad)
		require.ErrorContains(t, err, "units h, m, s and ms", bad)
	}
	for _, ok := range []string{"1s500ms", "0s", "${{ input.wait }}", "  ${{ input.wait }}"} {
		require.NoError(t, wait(ok).Validate(), ok)
	}
}

// ADR-0248 Decision 1: a Function's root entries are checked as an App's: a name or only a ref, no name used twice,
// each defined object valid as its kind, and the Secret declarations.
func TestFunctionRootEntriesValidate(t *testing.T) {
	fn := func(mutate func(*FunctionSpec)) *Function {
		f := &Function{TypeMeta: TypeMeta{APIVersion: KindFunction.GVK().APIVersion(), Kind: KindFunction}}
		f.Name, f.Namespace, f.ResourceGroup = "counter", "default", "rg1"
		f.Spec.KVStores = []KVStoreEntry{{Name: "counters", KVStoreSpec: KVStoreSpec{Tables: []KVTable{{Name: "counts"}}}}, {Ref: "sessions"}}
		mutate(&f.Spec)
		return f
	}
	if err := fn(func(*FunctionSpec) {}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	for name, tc := range map[string]struct {
		mutate func(*FunctionSpec)
		want   string
	}{
		"neither":      {func(s *FunctionSpec) { s.Buckets = []BucketEntry{{}} }, "spec.buckets[0] sets neither name nor ref"},
		"ref and spec": {func(s *FunctionSpec) { s.KVStores[1].Tables = []KVTable{{Name: "t"}} }, "spec.kv[1] sets ref and another field"},
		"repeat":       {func(s *FunctionSpec) { s.KVStores[1] = KVStoreEntry{Ref: "counters"} }, `spec.kv[1] repeats the name "counters" of spec.kv[0]`},
		"bad table":    {func(s *FunctionSpec) { s.KVStores[0].Tables[0].Name = "Bad_Name" }, "spec.kv[0].tables[0].name"},
		"bad deletion": {func(s *FunctionSpec) { s.SQLDatabases = []SQLDatabaseEntry{{Name: "notes", Deletion: "purge"}} }, `spec.sql[0].deletion "purge"`},
		"secret keys":  {func(s *FunctionSpec) { s.SecretDeclarations = []SecretDeclaration{{Name: "api-keys"}} }, "spec.secrets[0].keys is empty"},
	} {
		err := fn(tc.mutate).Validate()
		if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate = %v, want an Invalid fault containing %q", name, err, tc.want)
		}
	}
}

// ADR-0349 Decision 1: a Function's job bindings each have a DNS-1123 alias, unique in the list, and name a Job by a
// DNS-1123 name; a Job's spec refuses the list.
func TestFunctionJobBindingValidate(t *testing.T) {
	t.Parallel()
	withJobs := func(jobs ...FunctionJob) *Function {
		return fnWith(FunctionSpec{Bindings: FunctionBindings{Job: jobs}})
	}
	require.NoError(t, withJobs(FunctionJob{Alias: "stack", Job: "tofu"}, FunctionJob{Alias: "backfill", Job: "tofu"}).Validate(),
		"two aliases may bind one Job")
	for name, c := range map[string]struct {
		jobs []FunctionJob
		want string
	}{
		"alias not a label": {[]FunctionJob{{Alias: "Stack", Job: "tofu"}}, `spec.bindings.job alias "Stack" is not a valid DNS-1123 label`},
		"alias duplicated":  {[]FunctionJob{{Alias: "stack", Job: "tofu"}, {Alias: "stack", Job: "other"}}, `spec.bindings.job alias "stack" is duplicated`},
		"job not a name":    {[]FunctionJob{{Alias: "stack", Job: "Tofu"}}, `spec.bindings.job[stack].job "Tofu" is not a valid Job name`},
		"job empty":         {[]FunctionJob{{Alias: "stack"}}, `spec.bindings.job[stack].job "" is not a valid Job name`},
	} {
		err := withJobs(c.jobs...).Validate()
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.ErrorContains(t, err, c.want, name)
	}
	j := job(func(s *JobSpec) { s.Bindings.Job = []FunctionJob{{Alias: "stack", Job: "tofu"}} })
	err := j.Validate()
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "spec.bindings.job: a Job starts no run of a Job")
}
