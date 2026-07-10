package controlplane

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/eventing/deadletter"
)

// Replayer is the replay seam the DLQ route needs from the Sensor reconciler (ADR-0118 §4). It performs ONE
// synchronous delivery attempt against the LIVE Sensor spec — the deliberate imperative departure from the
// ADR-0094 store-CRUD-only stance, scoped to this one method (a DeadLetter is not a CRD, so there is no
// reconcile loop to observe a declarative replay marker).
type Replayer interface {
	Replay(ctx context.Context, ns v1.NamespaceName, id string) error
}

// deadLetterListInput is the namespaced DLQ list request; the typed path param confines the namespace at the
// huma boundary (DNS-label schema), so the store prefix is always within the authorized namespace.
type deadLetterListInput struct {
	Namespace v1.NamespaceName `path:"namespace"`
}

type deadLetterListOutput struct {
	Body struct {
		Items []deadletter.DeadLetter `json:"items"`
	}
}

// deadLetterItemInput is the single-record DLQ request (describe / replay / discard).
type deadLetterItemInput struct {
	Namespace v1.NamespaceName `path:"namespace"`
	ID        string           `path:"id"`
}

type deadLetterItemOutput struct {
	Body deadletter.DeadLetter
}

// RegisterDeadLetters registers the DLQ read + replay/discard routes (ADR-0118 §4), mirroring the ADR-0106
// run-logs route shape:
//
//	GET    …/namespaces/{ns}/deadletters        → list    (read — authorizes get/Sensor)
//	GET    …/namespaces/{ns}/deadletters/{id}   → describe (read — authorizes get/Sensor)
//	POST   …/namespaces/{ns}/deadletters/{id}/replay → replay (imperative — authorizes delete/Sensor)
//	DELETE …/namespaces/{ns}/deadletters/{id}   → discard  (CRUD delete — authorizes delete/Sensor)
//
// The read routes stay purely read-only (GET), following the ADR-0106 precedent — replay is the sole
// imperative departure. Read authorizes a get verb; replay/discard authorize a DLQ-write (delete) verb.
func RegisterDeadLetters(api huma.API, store deadletter.Store, replayer Replayer, authz auth.Authorizer) {
	const base = "/apis/funcd.io/v1alpha1/namespaces/{namespace}/deadletters"

	huma.Register(api, huma.Operation{
		OperationID: "listDeadLetters",
		Method:      http.MethodGet,
		Path:        base,
		Tags:        []string{"DeadLetter"},
	}, func(ctx context.Context, in *deadLetterListInput) (*deadLetterListOutput, error) {
		if err := authorizeDeadLetters(ctx, authz, in.Namespace, auth.VerbGet); err != nil {
			return nil, wrapFaultError(err)
		}
		items, err := store.List(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		out := &deadLetterListOutput{}
		out.Body.Items = items
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getDeadLetter",
		Method:      http.MethodGet,
		Path:        base + "/{id}",
		Tags:        []string{"DeadLetter"},
	}, func(ctx context.Context, in *deadLetterItemInput) (*deadLetterItemOutput, error) {
		if err := authorizeDeadLetters(ctx, authz, in.Namespace, auth.VerbGet); err != nil {
			return nil, wrapFaultError(err)
		}
		dl, err := store.Get(ctx, in.Namespace, in.ID)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &deadLetterItemOutput{Body: dl}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replayDeadLetter",
		Method:      http.MethodPost,
		Path:        base + "/{id}/replay",
		Tags:        []string{"DeadLetter"},
	}, func(ctx context.Context, in *deadLetterItemInput) (*struct{}, error) {
		if err := authorizeDeadLetters(ctx, authz, in.Namespace, auth.VerbDelete); err != nil {
			return nil, wrapFaultError(err)
		}
		return nil, wrapFaultError(replayer.Replay(ctx, in.Namespace, in.ID))
	})

	huma.Register(api, huma.Operation{
		OperationID: "discardDeadLetter",
		Method:      http.MethodDelete,
		Path:        base + "/{id}",
		Tags:        []string{"DeadLetter"},
	}, func(ctx context.Context, in *deadLetterItemInput) (*struct{}, error) {
		if err := authorizeDeadLetters(ctx, authz, in.Namespace, auth.VerbDelete); err != nil {
			return nil, wrapFaultError(err)
		}
		return nil, wrapFaultError(store.Delete(ctx, in.Namespace, in.ID))
	})
}

// authorizeDeadLetters runs the ADR-0018 PEP for a DLQ operation: reads require get/Sensor in the
// namespace; replay/discard require a write (delete) verb on Sensor — the DeadLetter is a failed Sensor
// action, so the Sensor is the resource the caller must be entitled to (query-time tenant scoping).
func authorizeDeadLetters(ctx context.Context, authz auth.Authorizer, ns v1.NamespaceName, verb auth.Verb) error {
	const op = "controlplane.deadletters"
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return fault.Unauthorizedf(op, "no authenticated identity")
	}
	dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: verb, Kind: v1.KindSensor, Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "authorize dead-letter %s", verb)
	}
	if !dec.Allowed {
		return fault.Forbiddenf(op, "dead-letter %s in %q denied: %s", verb, ns, dec.Reason)
	}
	return nil
}

// stubReplayer backs RegisterStubDeadLetters (spec generation) — inert, shape only.
type stubReplayer struct{}

func (stubReplayer) Replay(context.Context, v1.NamespaceName, string) error { return nil }

// RegisterStubDeadLetters registers the DLQ operations with inert deps for spec generation, so the routes
// are documented in the committed OpenAPI even though they mount at runtime only when the store is present.
func RegisterStubDeadLetters(api huma.API) {
	RegisterDeadLetters(api, stubDeadLetterStore{}, stubReplayer{}, stubLogAuthorizer{})
}

// stubDeadLetterStore is the inert deadletter.Store for spec generation (shape only, never called).
type stubDeadLetterStore struct{}

func (stubDeadLetterStore) Put(context.Context, deadletter.DeadLetter) error { return nil }
func (stubDeadLetterStore) List(context.Context, v1.NamespaceName) ([]deadletter.DeadLetter, error) {
	return nil, nil
}
func (stubDeadLetterStore) Get(context.Context, v1.NamespaceName, string) (deadletter.DeadLetter, error) {
	return deadletter.DeadLetter{}, nil
}
func (stubDeadLetterStore) Delete(context.Context, v1.NamespaceName, string) error { return nil }
func (stubDeadLetterStore) SweepExpired(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}
func (stubDeadLetterStore) Close() error { return nil }
