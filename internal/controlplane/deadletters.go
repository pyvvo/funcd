package controlplane

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
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
	Namespace v1.NamespaceName `path:"namespace" doc:"The namespace of the dead letters."`
}

type deadLetterListOutput struct {
	Body struct {
		Items []deadletter.DeadLetter `json:"items" doc:"The namespace's dead letters, newest first."`
	}
}

// deadLetterItemInput is the single-record DLQ request (describe / replay / discard).
type deadLetterItemInput struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The dead letter's namespace."`
	ID        string           `path:"id" doc:"The dead letter's ID."`
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

	read, write := forbidden("read Sensors in the namespace"), forbidden("delete Sensors in the namespace")
	missing := problem(fault.NotFound, "No dead letter of that ID exists in the namespace.",
		"Check the ID: funcdctl eventing dlq list lists the namespace's dead letters.")
	huma.Register(api, Describe(huma.Operation{
		OperationID: "listDeadLetters",
		Method:      http.MethodGet,
		Path:        base,
		Tags:        []string{"DeadLetter"},
	}, "List the dead letters of a namespace", unauthorized(), read, badPath("namespace"),
	), func(ctx context.Context, in *deadLetterListInput) (*deadLetterListOutput, error) {
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

	huma.Register(api, Describe(huma.Operation{
		OperationID: "getDeadLetter",
		Method:      http.MethodGet,
		Path:        base + "/{id}",
		Tags:        []string{"DeadLetter"},
	}, "Read a dead letter", unauthorized(), read, missing, badPath("namespace"),
	), func(ctx context.Context, in *deadLetterItemInput) (*deadLetterItemOutput, error) {
		if err := authorizeDeadLetters(ctx, authz, in.Namespace, auth.VerbGet); err != nil {
			return nil, wrapFaultError(err)
		}
		dl, err := store.Get(ctx, in.Namespace, in.ID)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &deadLetterItemOutput{Body: dl}, nil
	})

	huma.Register(api, Describe(huma.Operation{
		OperationID: "replayDeadLetter",
		Method:      http.MethodPost,
		Path:        base + "/{id}/replay",
		Tags:        []string{"DeadLetter"},
	}, "Replay a dead letter", slices.Concat([]ErrorCase{unauthorized(), write, missing, badPath("namespace"),
		badQuery()}, replayCases())...,
	), func(ctx context.Context, in *deadLetterItemInput) (*struct{}, error) {
		if err := authorizeDeadLetters(ctx, authz, in.Namespace, auth.VerbDelete); err != nil {
			return nil, wrapFaultError(err)
		}
		return nil, wrapFaultError(replayer.Replay(ctx, in.Namespace, in.ID))
	})

	huma.Register(api, Describe(huma.Operation{
		OperationID: "discardDeadLetter",
		Method:      http.MethodDelete,
		Path:        base + "/{id}",
		Tags:        []string{"DeadLetter"},
	}, "Discard a dead letter", unauthorized(), write, badPath("namespace"), badQuery(),
	), func(ctx context.Context, in *deadLetterItemInput) (*struct{}, error) {
		if err := authorizeDeadLetters(ctx, authz, in.Namespace, auth.VerbDelete); err != nil {
			return nil, wrapFaultError(err)
		}
		return nil, wrapFaultError(store.Delete(ctx, in.Namespace, in.ID))
	})
}

// replayCases are the answers of Replayer, whose one delivery attempt re-parks a dead letter that it fails to deliver.
func replayCases() []ErrorCase {
	return []ErrorCase{
		problem(fault.NotFound, "The Sensor that the dead letter names no longer exists.",
			"Apply the Sensor again, then retry, or discard the dead letter."),
		problem(fault.NotFound, "The Sensor no longer has the dead letter's action.",
			"Add the action back to the Sensor, then retry, or discard the dead letter."),
		problem(fault.Conflict, "Another replay of the dead letter is running.", "Retry once that replay has ended."),
		problem(fault.Conflict, "The action's Function serves HTTP, so only the edge calls it; the dead letter stays.",
			"Point the action at a Function that does not serve HTTP, then retry."),
		problem(fault.Invalid, "The action's input in the Sensor is not a JSON object, or holds an expression that "+
			"does not evaluate; the dead letter stays.", "Correct the action's input in the Sensor, then retry."),
		problem(fault.Unavailable, "The platform or the namespace is held, so funcd delivers no event.",
			"Retry once the hold is released, which funcdctl hold release does."),
		problem(fault.Unavailable, "The delivery failed again, such as on a Function that is missing, has no ready "+
			"worker or answers an error; the dead letter stays, with its attempts reset and the new reason.",
			"Correct what the dead letter's reason names, then retry."),
	}
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

func (stubDeadLetterStore) Put(context.Context, deadletter.DeadLetter) error    { return nil }
func (stubDeadLetterStore) Update(context.Context, deadletter.DeadLetter) error { return nil }
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
