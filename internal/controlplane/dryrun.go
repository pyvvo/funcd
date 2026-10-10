package controlplane

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// DryRunParams is embedded in every create and replace input of a writable kind (ADR-0220 Decision 1). It is exported
// because huma skips unexported embedded fields.
type DryRunParams struct {
	DryRun bool `query:"dryRun" doc:"admit the write and store nothing (ADR-0220)"`
}

// AppPlanner plans the rollout of an App write (ADR-0220 Decision 5); internal/app implements it, so the control plane
// does not import the App package.
type AppPlanner interface {
	PlanApp(ctx context.Context, app *v1.App) (v1.AppPlan, error) // reads the store, writes nothing
}

type dryRunKey struct{}

// withDryRun marks ctx as a dry run when on: createObj and replaceObjIf then answer the admitted object instead of
// storing it.
func withDryRun(ctx context.Context, on bool) context.Context {
	if !on {
		return ctx
	}
	return context.WithValue(ctx, dryRunKey{}, true)
}

func dryRunFrom(ctx context.Context) bool {
	on, _ := ctx.Value(dryRunKey{}).(bool)
	return on
}

// rejectUnknownQuery makes every write operation answer 422 to a query parameter it does not declare, so a dryRun
// sent to an operation without one never becomes a real write (ADR-0220 Decision 1); reads accept unknown parameters
// as before. NewAPI registers it as the OpenAPI's OnAddOperation hook, which runs for every registered operation
// before its handler reads the field.
func rejectUnknownQuery(_ *huma.OpenAPI, op *huma.Operation) {
	if op.Method != http.MethodGet && op.Method != http.MethodHead {
		op.RejectUnknownQueryParameters = true
	}
}

// dryRunCreate is a dry-run create's store step (ADR-0220 Decisions 2 and 3): the store's refusal when the name is
// taken, else obj with the fields only the store sets cleared.
func (h *storeHandlers) dryRunCreate(ctx context.Context, obj v1.Object) (v1.Object, error) {
	m := obj.GetObjectMeta()
	kind := obj.GroupVersionKind()
	_, err := h.store.Get(ctx, kind, m.Namespace, m.Name)
	switch fault.KindOf(err) {
	case "":
		return nil, fault.Conflictf("store.Create", "%s %q already exists", kind.Kind, m.Name)
	case fault.NotFound:
	default:
		return nil, err
	}
	m.UID, m.Generation, m.ResourceVersion, m.CreationTime = "", 0, "", v1.Timestamp{}
	return obj, nil
}

// dryRunReplace is a dry-run replace's store step: obj with the uid, generation and creationTimestamp of the stored
// object, as the store's update keeps them; its resourceVersion is already the stored one.
func dryRunReplace(obj, cur v1.Object) v1.Object {
	m, c := obj.GetObjectMeta(), cur.GetObjectMeta()
	m.UID, m.Generation, m.CreationTime = c.UID, c.Generation, c.CreationTime
	return obj
}

// withPlan returns the step that sets status.plan on a dry-run App answer. It runs on the write's result, so after the
// write released its lock (ADR-0220 Decisions 4 and 5).
func (h *storeHandlers) withPlan(ctx context.Context) func(v1.Object, error) (v1.Object, error) {
	return func(obj v1.Object, err error) (v1.Object, error) {
		if err != nil || !dryRunFrom(ctx) {
			return obj, err
		}
		if h.planner == nil {
			return nil, fault.Unavailablef("controlplane.dryRun", "no App planner is wired")
		}
		a := obj.(*v1.App)
		plan, err := h.planner.PlanApp(ctx, a)
		if err != nil {
			return nil, err
		}
		a.Status.Plan = &plan
		return a, nil
	}
}
