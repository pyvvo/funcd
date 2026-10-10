package app_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// bareApp is an App of the namespace with only a version and, when given, its requirements.
func bareApp(name v1.ObjectName, version string, reqs ...v1.AppRequirement) *v1.App {
	return &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: v1.ResourceGroupName(name)},
		Spec:       v1.AppSpec{Version: version, Requires: reqs},
	}
}

func needs(app v1.ObjectName, rng string) v1.AppRequirement {
	return v1.AppRequirement{App: app, Version: rng}
}

// The dependent states the admission tells apart (ADR-0219 Decision 7).
func startedStatus(name v1.ObjectName) v1.AppStatus {
	rev := v1.AppRevisionName(name, 1)
	return v1.AppStatus{Status: v1.Status{Phase: v1.PhaseReady}, CurrentRevision: rev, LatestRevision: rev}
}

func deployingStatus(name v1.ObjectName, reason string) v1.AppStatus {
	s := v1.AppStatus{Status: v1.Status{Phase: v1.PhaseDeploying}, LatestRevision: v1.AppRevisionName(name, 1)}
	s.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: reason})
	return s
}

type requiresEnv struct {
	t  *testing.T
	st store.Store
	a  admission.Admission
}

func newRequiresEnv(t *testing.T) *requiresEnv {
	st := store.New(memory.New())
	return &requiresEnv{t: t, st: st, a: app.NewRequiresAdmission(reader{st})}
}

// put stores a with status.
func (e *requiresEnv) put(a *v1.App, status v1.AppStatus) *v1.App {
	e.t.Helper()
	ctx := context.Background()
	obj, err := e.st.Create(ctx, a)
	require.NoError(e.t, err)
	stored := obj.(*v1.App)
	stored.Status = status
	obj, err = e.st.Update(ctx, stored)
	require.NoError(e.t, err)
	return obj.(*v1.App)
}

func (e *requiresEnv) admit(op admission.Operation, obj, old v1.Object) error {
	req := admission.Request{Operation: op, GVK: v1.KindApp.GVK(), Object: obj, Old: old}
	_, err := e.a.Admit(context.Background(), req)
	return err
}

// upgrade admits an update of the stored App old to version, keeping its requirements.
func (e *requiresEnv) upgrade(old *v1.App, version string) error {
	return e.admit(admission.Update, bareApp(old.Name, version, old.Spec.Requires...), old)
}

func requireRefused(t *testing.T, err error, kind fault.Kind, msg string) {
	t.Helper()
	require.Equal(t, kind, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, msg)
}

func TestRequiresAdmissionHandles(t *testing.T) {
	a := newRequiresEnv(t).a
	require.Equal(t, "app-requires", a.Name())
	require.Equal(t, admission.Validating, a.Phase())
	for _, op := range []admission.Operation{admission.Create, admission.Update, admission.Delete} {
		require.True(t, a.Handles(v1.KindApp.GVK(), op), "%s", op)
		require.False(t, a.Handles(v1.KindFunction.GVK(), op), "%s", op)
		require.False(t, a.Handles(v1.KindAppRevision.GVK(), op), "%s", op)
	}
	nr, ok := a.(admission.NamespaceReading)
	require.True(t, ok)
	require.True(t, nr.ReadsNamespace())
}

// scenario: app-requires-upgrade-refused (the admission half) — an upgrade or a rollback that takes the required App
// out of a started dependent's range is a Conflict naming it and its range, not a dependent waiting on another version;
// an upgrade inside the range passes.
func TestRequiresAdmissionRefusesUpgrade(t *testing.T) {
	e := newRequiresEnv(t)
	lake := e.put(bareApp("lakehouse", "2.1.0"), startedStatus("lakehouse"))
	e.put(bareApp("billing", "1.3.0", needs("lakehouse", "^2.0.0")), startedStatus("billing"))
	e.put(bareApp("audit", "1.0.0", needs("lakehouse", "^3.0.0")), deployingStatus("audit", "RequirementNotMet"))

	requireRefused(t, e.upgrade(lake, "3.0.0"), fault.Conflict,
		`app "lakehouse" version "3.0.0" is outside the range its dependents require: billing (^2.0.0)`)
	requireRefused(t, e.upgrade(lake, "1.9.0"), fault.Conflict,
		`app "lakehouse" version "1.9.0" is outside the range its dependents require: billing (^2.0.0)`)
	requireRefused(t, e.upgrade(lake, "beta"), fault.Conflict, "billing (^2.0.0)")
	require.NoError(t, e.upgrade(lake, "2.2.0"))
	require.NoError(t, e.upgrade(lake, "2.1.0"), "an update that keeps the version")
}

// Decision 7: every blocking dependent is named, sorted by name.
func TestRequiresAdmissionNamesEveryDependent(t *testing.T) {
	e := newRequiresEnv(t)
	lake := e.put(bareApp("lakehouse", "2.1.0"), startedStatus("lakehouse"))
	e.put(bareApp("billing", "1.3.0", needs("lakehouse", "^2.0.0")), startedStatus("billing"))
	e.put(bareApp("audit", "1.0.0", needs("lakehouse", "~2.1.0")), startedStatus("audit"))
	requireRefused(t, e.upgrade(lake, "3.0.0"), fault.Conflict,
		`app "lakehouse" version "3.0.0" is outside the range its dependents require: audit (~2.1.0), billing (^2.0.0)`)
}

// Decision 7: a dependent that has not started never blocks an upgrade: one waiting on another version, one waiting
// for readiness at a matching version, one created paused or not reconciled yet (no phase); one in its first rollout
// does; a dependent without a range never does.
func TestRequiresAdmissionStartedDependentsOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rng    string
		status v1.AppStatus
		blocks bool
	}{
		{"waiting on another version", "^3.0.0", deployingStatus("billing", "RequirementNotMet"), false},
		{"waiting for readiness at a matching version", "^2.0.0", deployingStatus("billing", "RequirementNotMet"), false},
		{"created paused", "^2.0.0", v1.AppStatus{}, false},
		{"in its first rollout", "^2.0.0", deployingStatus("billing", "Progressing"), true},
		{"current", "^2.0.0", startedStatus("billing"), true},
		{"current, without a range", "", startedStatus("billing"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRequiresEnv(t)
			lake := e.put(bareApp("lakehouse", "2.1.0"), startedStatus("lakehouse"))
			e.put(bareApp("billing", "1.3.0", needs("lakehouse", tc.rng)), tc.status)
			err := e.upgrade(lake, "3.0.0")
			if tc.blocks {
				requireRefused(t, err, fault.Conflict, "billing")
				return
			}
			require.NoError(t, err)
		})
	}
}

// scenario: app-requires-delete-refused and app-requires-any-version (the admission half) — the delete of a required
// App, with or without a range, is a Conflict naming its dependents; with none it passes.
func TestRequiresAdmissionRefusesDelete(t *testing.T) {
	e := newRequiresEnv(t)
	lake := e.put(bareApp("lakehouse", ""), startedStatus("lakehouse"))
	require.NoError(t, e.admit(admission.Delete, nil, lake), "no dependent")

	e.put(bareApp("billing", "1.3.0", needs("lakehouse", "")), startedStatus("billing"))
	requireRefused(t, e.admit(admission.Delete, nil, lake), fault.Conflict,
		`app "lakehouse" is required by billing; remove the requirement first`)
	e.put(bareApp("audit", "1.0.0", needs("lakehouse", "^3.0.0")), deployingStatus("audit", "RequirementNotMet"))
	requireRefused(t, e.admit(admission.Delete, nil, lake), fault.Conflict,
		`app "lakehouse" is required by audit, billing; remove the requirement first`)
	billing := bareApp("billing", "1.3.0", needs("lakehouse", ""))
	require.NoError(t, e.admit(admission.Delete, nil, billing), "a dependent is deleted freely")
}

// scenario: app-requires-cycle-refused (the admission half) — an update or a create closing a requirement cycle, and
// a self-requirement, are Invalid naming the cycle; a missing required App is never refused.
func TestRequiresAdmissionRefusesCycle(t *testing.T) {
	e := newRequiresEnv(t)
	require.NoError(t, e.admit(admission.Create, bareApp("billing", "1.3.0", needs("lakehouse", "^2.0.0")), nil),
		"a missing required App waits")
	e.put(bareApp("billing", "1.3.0", needs("lakehouse", "^2.0.0")), v1.AppStatus{})
	requireRefused(t, e.admit(admission.Create, bareApp("lakehouse", "2.1.0", needs("billing", "")), nil), fault.Invalid,
		"spec.requires would create a requirement cycle (lakehouse → billing → lakehouse)")

	lake := e.put(bareApp("lakehouse", "2.1.0"), startedStatus("lakehouse"))
	requireRefused(t, e.admit(admission.Update, bareApp("lakehouse", "2.1.0", needs("billing", "")), lake), fault.Invalid,
		"spec.requires would create a requirement cycle (lakehouse → billing → lakehouse)")
	requireRefused(t, e.admit(admission.Update, bareApp("lakehouse", "2.1.0", needs("lakehouse", "")), lake), fault.Invalid,
		"spec.requires would create a requirement cycle (lakehouse → lakehouse)")
	requireRefused(t, e.admit(admission.Create, bareApp("solo", "", needs("solo", "^1.0.0")), nil), fault.Invalid,
		"(solo → solo)")

	e.put(bareApp("audit", "1.0.0", needs("billing", "")), v1.AppStatus{})
	requireRefused(t, e.admit(admission.Update, bareApp("lakehouse", "2.1.0", needs("audit", "")), lake), fault.Invalid,
		"(lakehouse → audit → billing → lakehouse)")
	require.NoError(t, e.admit(admission.Update, bareApp("lakehouse", "2.1.0", needs("catalog", "")), lake),
		"a requirement outside every cycle")
}
