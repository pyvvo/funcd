// Package identity is the controller for KindIdentity (ADR-0135, FEAT-0008/F100): a user-assigned
// managed identity whose reconciler issues a revocable SigV4 credential — a stable access key
// (IdentityAccessKey) plus a generated, rotatable secret written to an owned Secret. The S3 gateway's
// store-backed ExternalKeys (this package's storeExternalKeys) resolves an Identity-issued key back to
// its secret so the caller authenticates and resolves to the Identity::"<ns>/<name>" Cedar principal.
// Issuing a credential is authentication only; authorization is a separate RolesAssignment (ADR-0136).
package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

// condReady is the readiness condition the Identity reconciler raises: True once the credential Secret
// is issued.
const condReady = "Ready"

// secretKeyAccessKeyID and secretKeySecretAccessKey are the owned Secret's data keys (shared with
// storeExternalKeys, which reads secretKeySecretAccessKey).
const (
	secretKeyAccessKeyID     = "accessKeyId"
	secretKeySecretAccessKey = "secretAccessKey"
	// secretKeyCatalogToken is the minted per-Identity catalog bearer token (ADR-0137): a random,
	// rotatable value the catalog PEP proxy resolves back to this Identity by store lookup (the query-path
	// analog of the SigV4 secret). Rotated on the same trigger as the keypair secret.
	secretKeyCatalogToken = "catalogToken"
)

// ReconcilerDeps configures the Identity reconciler. Store is required.
type ReconcilerDeps struct {
	Store  store.Store
	Logger *slog.Logger
}

// Reconciler is the controller.Reconciler for KindIdentity.
type Reconciler struct {
	store  store.Store
	logger *slog.Logger
}

// NewReconciler builds the Identity reconciler. Store is required.
func NewReconciler(d ReconcilerDeps) (*Reconciler, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("services.identity.NewReconciler", "store is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{store: d.Store, logger: logger.With("component", "services.identity")}, nil
}

// Reconcile issues/rotates an Identity's credential (idempotent, at-least-once). A deleted Identity
// (NotFound) needs no teardown — its owned Secret cascades via the OwnerReference GC, and the
// store-backed ExternalKeys lookup naturally fails for a missing Identity, so the credential stops
// authenticating.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "services.identity.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return controller.Result{}, nil
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get identity")
	}
	id, ok := obj.(*v1.Identity)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not an Identity", req.Namespace, req.Name)
	}

	access := s3gateway.IdentityAccessKey(string(id.Namespace), string(id.Name))
	if err := r.ensureSecret(ctx, id, access); err != nil {
		id.Status.Phase = v1.PhasePending
		id.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "SecretIssueFailed", Message: err.Error()})
		if _, uerr := r.store.Update(ctx, id); uerr != nil {
			return controller.Result{}, retryOnConflict(uerr, op)
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "issue credential secret")
	}

	id.Status.Phase = v1.PhaseReady
	id.Status.AccessKeyID = access
	id.Status.ObservedRotate = id.Spec.Rotate
	id.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
	if _, uerr := r.store.Update(ctx, id); uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	return controller.Result{}, nil
}

// ensureSecret idempotently creates/rotates the Identity's owned credential Secret. It generates a new
// random secret when the Secret is absent OR spec.rotate advanced past status.observedRotate (rotation);
// otherwise it leaves the existing secret in place. The access key id is stable.
func (r *Reconciler) ensureSecret(ctx context.Context, id *v1.Identity, access string) error {
	name := id.Spec.CredentialSecretName
	if name == "" {
		name = id.Name
	}
	existing, gerr := r.store.Get(ctx, v1.KindSecret.GVK(), id.Namespace, name)
	if gerr != nil && fault.KindOf(gerr) != fault.NotFound {
		return gerr
	}
	rotate := gerr != nil || id.Spec.Rotate > id.Status.ObservedRotate
	if !rotate {
		return nil // secret already issued for this rotation generation
	}
	secret, err := randomSecret()
	if err != nil {
		return err
	}
	// ADR-0137: mint a fresh per-Identity catalog token alongside the SigV4 secret — a bearer credential
	// the catalog PEP proxy resolves to this Identity. Rotated together with the secret (same trigger).
	catalogToken, err := randomSecret()
	if err != nil {
		return err
	}
	data := map[string][]byte{
		secretKeyAccessKeyID:     []byte(access),
		secretKeySecretAccessKey: []byte(secret),
		secretKeyCatalogToken:    []byte(catalogToken),
	}
	if gerr != nil { // NotFound → create
		sobj, _ := v1.NewObject(v1.KindSecret)
		sec := sobj.(*v1.Secret)
		sec.Namespace, sec.Name = id.Namespace, name
		sec.ResourceGroup = id.ResourceGroup
		sec.OwnerReferences = []v1.OwnerReference{ownerRef(id)}
		sec.Spec = v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: data}
		_, cerr := r.store.Create(ctx, sec)
		return cerr
	}
	sec := existing.(*v1.Secret) // rotate → update in place (preserve UID/RV)
	sec.Spec.Data = data
	if sec.Spec.Type == "" {
		sec.Spec.Type = v1.SecretTypeOpaque
	}
	_, uerr := r.store.Update(ctx, sec)
	return uerr
}

// ownerRef ties the credential Secret to its Identity so it cascades on delete (revocation).
func ownerRef(id *v1.Identity) v1.OwnerReference {
	return v1.OwnerReference{
		ObjectRef:          v1.ObjectRef{Kind: v1.KindIdentity, Namespace: id.Namespace, Name: id.Name},
		UID:                id.UID,
		Controller:         true,
		BlockOwnerDeletion: true,
	}
}

// randomSecret returns a base64-encoded 32-byte cryptographically-random secret.
func randomSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fault.Internalf("services.identity.randomSecret", "read random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// retryOnConflict swallows an optimistic-concurrency conflict (the next reconcile re-converges).
func retryOnConflict(err error, op string) error {
	if fault.KindOf(err) == fault.Conflict {
		return nil
	}
	return fault.Wrapf(err, fault.KindOf(err), op, "status write-back")
}
