package cedar

import (
	"context"
	"log/slog"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
)

// Deps configures the cedar driver (ADR-0074).
type Deps struct {
	// Entities resolves the request-relevant Cedar entities per call (principal + resource + parents).
	Entities EntityProvider
	// Policies supplies the user Policy resources (compiled + cached with the built-ins).
	Policies PolicySource
	// Builtins is the always-on built-in Cedar policy text (a Registry's Builtins()) compiled into the
	// PolicySet with the user Policies. Empty ⇒ the default registry's built-ins — so the compose root can
	// supply a variant built-in set (e.g. funcdctl dev's relaxed S3 writes) without a package global.
	Builtins string
	// Logger is the driver's logger; nil ⇒ slog.Default().
	Logger *slog.Logger
}

// driver is the cedar-go Authorizer (ADR-0074): default-deny, per-object decisions over a cached
// PolicySet + per-call request-relevant entities.
type driver struct {
	entities EntityProvider
	policies *policyCache
	logger   *slog.Logger
}

// New builds the cedar driver behind the auth.Authorizer port (ADR-0074). Entities + Policies
// are required.
func New(d Deps) (auth.Authorizer, error) {
	if d.Entities == nil {
		return nil, fault.Invalidf("cedar.New", "entity provider is required")
	}
	if d.Policies == nil {
		return nil, fault.Invalidf("cedar.New", "policy source is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	builtins := d.Builtins
	if builtins == "" {
		builtins = defaultRegistry.Builtins()
	}
	return driver{
		entities: d.Entities,
		policies: &policyCache{src: d.Policies, builtins: builtins},
		logger:   logger.With("component", "auth.cedar"),
	}, nil
}

// Authorize evaluates a per-object Cedar decision (ADR-0074). It requires an Action + Resource +
// the Identity.Principal (the connection-scoped per-function principal); a request missing them is
// a wiring bug routed here by mistake. DEFAULT-DENY: cedar-go returns Deny unless a permit matches
// and no forbid wins.
func (d driver) Authorize(ctx context.Context, req auth.Request) (auth.Decision, error) {
	const op = "cedar.Authorize"
	if req.Action == "" {
		return auth.Decision{}, fault.Internalf(op, "cedar driver requires a per-object Action")
	}
	if !KnownAction(string(req.Action)) {
		return auth.Decision{Allowed: false, Reason: "unknown action " + string(req.Action)}, nil
	}
	if req.Identity.Principal == nil {
		// Fail closed: no principal ⇒ no permit can match.
		return auth.Decision{Allowed: false, Reason: "no Cedar principal on the request"}, nil
	}
	if req.Resource == nil {
		return auth.Decision{Allowed: false, Reason: "no Cedar resource on the request"}, nil
	}

	principal := *req.Identity.Principal
	resource := *req.Resource

	em, err := d.entities.EntitiesFor(ctx, principal, resource)
	if err != nil {
		return auth.Decision{}, fault.Wrapf(err, fault.KindOf(err), op, "resolve entities")
	}
	ps, err := d.policies.For(ctx, principal.Namespace, resource.Namespace)
	if err != nil {
		return auth.Decision{}, fault.Wrapf(err, fault.KindOf(err), op, "load policy set")
	}

	pUID, err := principalUID(principal)
	if err != nil {
		return auth.Decision{}, err
	}
	rUID, err := resourceUID(resource)
	if err != nil {
		return auth.Decision{}, err
	}
	cedarReq := cedartypes.Request{
		Principal: pUID,
		Action:    cedartypes.NewEntityUID("Action", cedartypes.String(req.Action)),
		Resource:  rUID,
		Context:   cedartypes.NewRecord(cedartypes.RecordMap{}),
	}

	decision, diag := cedar.Authorize(ps, em, cedarReq)
	allowed := bool(decision)
	reason := "cedar: permitted"
	if !allowed {
		reason = denyReason(diag, sameNamespace(principal.Namespace, resource.Namespace))
	}
	d.logger.Debug("cedar decision",
		"principal", pUID.String(), "action", string(req.Action), "resource", rUID.String(), "allowed", allowed)
	return auth.Decision{Allowed: allowed, Reason: reason}, nil
}

// denyReason explains a deny (ADR-0177 Decision 4): a matched forbid first (on Deny cedar-go's Reasons
// holds the forbids, collected in set-iteration order, so the lowest PolicyID is named for a stable
// reason), then an evaluation error, then the missing permit.
func denyReason(diag cedar.Diagnostic, sameNS bool) string {
	if len(diag.Reasons) > 0 {
		id := diag.Reasons[0].PolicyID
		for _, r := range diag.Reasons[1:] {
			id = min(id, r.PolicyID)
		}
		return "cedar: forbidden by policy " + string(id)
	}
	if len(diag.Errors) > 0 {
		return "cedar: denied (" + diag.Errors[0].String() + ")"
	}
	const noPermit = "cedar default-deny: no permitting policy"
	if !sameNS {
		return noPermit + " (cross-namespace request: built-in policies only)"
	}
	return noPermit
}
