package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

func todoRevision(mutate func(*AppRevision)) *AppRevision {
	r := &AppRevision{
		TypeMeta:   TypeMeta{APIVersion: KindAppRevision.GVK().APIVersion(), Kind: KindAppRevision},
		ObjectMeta: ObjectMeta{Name: "todo-1", Namespace: "default", ResourceGroup: "todo-rg"},
		Spec: AppRevisionSpec{
			App:    ObjectRef{Kind: KindApp, Namespace: "default", Name: "todo"},
			Number: 1,
			Spec:   todoApp(nil).Spec,
		},
	}
	if mutate != nil {
		mutate(r)
	}
	return r
}

func TestAppRevisionName(t *testing.T) {
	t.Parallel()
	if got := AppRevisionName("todo", 4); got != "todo-4" {
		t.Fatalf("AppRevisionName(todo, 4) = %q, want todo-4", got)
	}
	longest := AppRevisionName(ObjectName(strings.Repeat("a", maxAppNameLength)), maxAppRevisionNumber)
	if err := longest.Validate(); err != nil || len(longest) != 63 {
		t.Fatalf("the longest name %q (%d characters) is not a DNS label: %v", longest, len(longest), err)
	}
}

// ADR-0200 Decision 1: Validate checks the envelope, name and number, not the spec.
func TestAppRevisionValidate(t *testing.T) {
	t.Parallel()
	if err := todoRevision(nil).Validate(); err != nil {
		t.Fatalf("a valid AppRevision is refused: %v", err)
	}
	maxed := todoRevision(func(r *AppRevision) {
		r.Spec.Number = maxAppRevisionNumber
		r.Name = AppRevisionName("todo", maxAppRevisionNumber)
	})
	if err := maxed.Validate(); err != nil {
		t.Fatalf("the highest number is refused: %v", err)
	}
	invalidSpec := todoRevision(func(r *AppRevision) { r.Spec.Spec.Functions = []AppFunction{{}} })
	if err := invalidSpec.Validate(); err != nil {
		t.Fatalf("the spec copy was checked: %v", err)
	}

	for name, tc := range map[string]struct {
		mutate func(*AppRevision)
		want   string
	}{
		"wrong kind":       {func(r *AppRevision) { r.Kind = KindRevision }, "does not match"},
		"no namespace":     {func(r *AppRevision) { r.Namespace = "" }, "namespace is required"},
		"number zero":      {func(r *AppRevision) { r.Spec.Number, r.Name = 0, "todo-0" }, "spec.number 0 is outside 1 to 9999999999"},
		"number too high":  {func(r *AppRevision) { r.Spec.Number, r.Name = 10000000000, "todo-10000000000" }, "spec.number 10000000000"},
		"name not number":  {func(r *AppRevision) { r.Name = "todo-2" }, `metadata.name "todo-2" must be "todo-1"`},
		"name not the app": {func(r *AppRevision) { r.Name = "other-1" }, `metadata.name "other-1" must be "todo-1"`},
	} {
		err := todoRevision(tc.mutate).Validate()
		if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want fault.Invalid containing %q", name, err, tc.want)
		}
	}
}

func TestAppRevisionJSONRoundtrip(t *testing.T) {
	t.Parallel()
	started := NewTimestamp(time.Date(2026, 10, 8, 9, 30, 0, 123456789, time.UTC))
	in := todoRevision(func(r *AppRevision) {
		r.Status.Phase = PhaseDeploying
		r.Status.StartedAt = &started
	})
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"startedAt":"2026-10-08T09:30:00.123Z"`) {
		t.Fatalf("startedAt is not in the ADR-0196 form: %s", b)
	}
	obj, ok := NewObject(KindAppRevision)
	if !ok {
		t.Fatal("NewObject(AppRevision) failed")
	}
	if err := json.Unmarshal(b, obj); err != nil {
		t.Fatal(err)
	}
	out := obj.(*AppRevision)
	if out.Spec.Number != 1 || out.Spec.App.Name != "todo" || out.Spec.Spec.Functions[0].Image != "oci-layout://todo-api:1" {
		t.Fatalf("spec did not round-trip: %+v", out.Spec)
	}
	if out.Status.StartedAt == nil || !time.Time(*out.Status.StartedAt).Equal(time.Time(started)) {
		t.Fatalf("startedAt = %v, want %v", out.Status.StartedAt, started)
	}
	if out.GetStatus().Phase != PhaseDeploying {
		t.Fatalf("GetStatus().Phase = %q, want Deploying", out.GetStatus().Phase)
	}
	if !KindAppRevision.Namespaced() {
		t.Fatal("AppRevision must be namespaced")
	}
}
