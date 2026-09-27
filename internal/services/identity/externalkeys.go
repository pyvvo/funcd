package identity

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/store"
)

// storeExternalKeys is the production s3gateway.ExternalKeys implementation (ADR-0135): it resolves an
// Identity-issued access key ("FUNCID…") back to its stored secret by decoding it to (ns, name),
// reading the Identity and its owned credential Secret from the metastore. Restart-safe and
// index-free (the access key encodes the identity), so a deleted Identity/Secret naturally stops
// authenticating (Lookup returns ok=false). A non-Identity access key returns ok=false (the gateway
// then falls back to its other resolvers / Forbidden).
type storeExternalKeys struct {
	store store.Store
}

// NewExternalKeys builds the store-backed ExternalKeys for the S3 gateway's Deps.External seam.
func NewExternalKeys(s store.Store) s3gateway.ExternalKeys {
	return &storeExternalKeys{store: s}
}

// Lookup implements s3gateway.ExternalKeys. It has no context (the versitygw auth seam), so it reads the
// store with context.Background(); the reads are fast metastore Gets.
func (e *storeExternalKeys) Lookup(access string) (secret, namespace string, ok bool) {
	ns, name, isID := s3gateway.DecodeIdentityAccess(access)
	if !isID {
		return "", "", false
	}
	ctx := context.Background()
	idObj, err := e.store.Get(ctx, v1.KindIdentity.GVK(), v1.NamespaceName(ns), v1.ObjectName(name))
	if err != nil {
		return "", "", false
	}
	id, isIdentity := idObj.(*v1.Identity)
	if !isIdentity {
		return "", "", false
	}
	secretName := id.Spec.CredentialSecretName
	if secretName == "" {
		secretName = id.Name
	}
	secObj, err := e.store.Get(ctx, v1.KindSecret.GVK(), id.Namespace, secretName)
	if err != nil {
		return "", "", false
	}
	sec, isSecret := secObj.(*v1.Secret)
	if !isSecret {
		return "", "", false
	}
	raw, has := sec.Spec.Data[secretKeySecretAccessKey]
	if !has || len(raw) == 0 {
		return "", "", false
	}
	return string(raw), string(id.Namespace), true
}
