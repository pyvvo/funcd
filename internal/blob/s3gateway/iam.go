// Package s3gateway is the funcd S3-protocol frontend over the blob.Bucket
// substrate (ADR-0080), with in-platform identity via funcd-managed per-principal
// SigV4 keypairs resolved by an in-process versitygw auth.IAMService (ADR-0085).
//
// AuthN: every in-platform Function and CatalogService engine gets a DETERMINISTIC
// keypair derived from a node-local master secret over its (kind, namespace, name)
// (ADR-0175) — funcd injects it into the sandbox env; the worker never chooses its
// secret and cannot derive a peer's. AuthZ: every backend op is a PEP on the cedar PDP (s3::read / s3::write),
// exactly the spec.blob binding-as-grant model of slice 2.
package s3gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"strings"

	"github.com/versity/versitygw/auth"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// accessKeyPrefix marks a funcd-derived (in-platform) access key. A key without it
// is treated as an external/SigV4 client looked up in the ExternalKeys store.
const accessKeyPrefix = "FUNCD"

// refSeparator joins (ns, fn) inside the base32-encoded access-key body. NUL never
// occurs in a DNS-1123 name, so the decode is unambiguous.
const refSeparator = "\x00"

// accessEncoding is the access-key body codec: uppercase base32, no padding — every
// character is a valid AWS access-key character (A-Z2-7), and it round-trips (ns, fn).
//
//nolint:gochecknoglobals // an effectively-const codec handle (stdlib pattern)
var accessEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Keypair is a principal's deterministic S3 credentials (ADR-0085). Pure value; no I/O.
type Keypair struct{ AccessKey, SecretKey string }

// Owner-kind codes an in-platform access key carries (ADR-0175).
const (
	kindCodeFunction       = "F"
	kindCodeCatalogService = "C"
)

// kindCode returns the access-key code of kind; "" for a kind that holds no in-platform key, which
// decodeAccess refuses.
func kindCode(kind v1.Kind) string {
	switch kind {
	case v1.KindFunction:
		return kindCodeFunction
	case v1.KindCatalogService:
		return kindCodeCatalogService
	default:
		return ""
	}
}

// DeriveKeypair returns the stable per-(kind, namespace, name) keypair (ADR-0085, ADR-0175):
//   - AccessKey = "FUNCD" + base32-noPad-upper(code + "\x00" + ns + "\x00" + name), code "F" for a Function and
//     "C" for a CatalogService — decodable via decodeAccess; not secret (knowing it grants nothing).
//   - SecretKey = base64( HMAC-SHA256(master, "s3:" + kind + ":" + ns + "/" + name) ) — the full kind is in the
//     MAC input, so a Function cannot compute the secret of a same-named CatalogService.
//
// Pure and deterministic: the same (master, kind, ns, name) always yields the same keypair, across daemon
// restarts. No storage, no rotation.
func DeriveKeypair(master []byte, kind v1.Kind, ns, name string) Keypair {
	access := accessKeyPrefix + accessEncoding.EncodeToString([]byte(kindCode(kind)+refSeparator+ns+refSeparator+name))
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte("s3:" + string(kind) + ":" + ns + "/" + name))
	secret := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return Keypair{AccessKey: access, SecretKey: secret}
}

// identityAccessKeyPrefix marks a user-assigned managed Identity's access key (ADR-0135). It does NOT
// start with accessKeyPrefix ("FUNCD"), so decodeAccess rejects it and GetUserAccount falls through to the
// ExternalKeys store — which decodes it with DecodeIdentityAccess and reads the Identity's stored secret.
const identityAccessKeyPrefix = "FUNCID"

// IdentityAccessKey returns the stable SigV4 access key id for a user-assigned Identity (ADR-0135):
// "FUNCID" + base32-noPad-upper(ns + "\x00" + name). Deterministic in (ns, name); the SECRET is NOT
// derived (it is generated + stored in the Identity's owned Secret, so it is rotatable). Because the
// prefix is not "FUNCD", the Function decodeAccess skips it and the gateway resolves it via ExternalKeys.
func IdentityAccessKey(ns, name string) string {
	return identityAccessKeyPrefix + accessEncoding.EncodeToString([]byte(ns+refSeparator+name))
}

// DecodeIdentityAccess maps an Identity access key back to its (namespace, name). ok=false for a
// non-Identity or malformed key. Used by the store-backed ExternalKeys to resolve the Identity + secret.
func DecodeIdentityAccess(access string) (ns, name string, ok bool) {
	if !strings.HasPrefix(access, identityAccessKeyPrefix) {
		return "", "", false
	}
	raw, err := accessEncoding.DecodeString(strings.TrimPrefix(access, identityAccessKeyPrefix))
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(raw), refSeparator, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// decodeAccess maps an in-platform access key back to its (kind, ns, name) (ADR-0175). ok=false for a
// non-funcd (external) access key, a malformed one, or a pre-ADR-0175 two-part key.
func decodeAccess(access string) (kind v1.Kind, ns, name string, ok bool) {
	if !strings.HasPrefix(access, accessKeyPrefix) {
		return "", "", "", false
	}
	raw, err := accessEncoding.DecodeString(strings.TrimPrefix(access, accessKeyPrefix))
	if err != nil {
		return "", "", "", false
	}
	parts := strings.SplitN(string(raw), refSeparator, 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	switch parts[0] {
	case kindCodeFunction:
		return v1.KindFunction, parts[1], parts[2], true
	case kindCodeCatalogService:
		return v1.KindCatalogService, parts[1], parts[2], true
	default:
		return "", "", "", false
	}
}

// ExternalKeys is the funcd store of issued external scoped keypairs — the one
// untrusted edge (ADR-0085). It resolves an access key to its secret + scoped
// namespace for the S3Identity principal. Lifecycle UX (issue/rotate/revoke) is a
// follow-on; this read-only seam is all the gateway needs.
type ExternalKeys interface {
	Lookup(access string) (secret string, namespace string, ok bool)
}

// iam is the funcd in-process versitygw auth.IAMService (ADR-0085). In-platform
// accounts are DERIVED (stateless) from the master secret; external accounts resolve
// from the ExternalKeys store. SigV4 verification in versitygw passes only for the
// holder of the matching secret — signs-as-self / cannot-forge-peer.
type iam struct {
	master   []byte
	external ExternalKeys // may be nil ⇒ no external clients
}

var _ auth.IAMService = (*iam)(nil)

// GetUserAccount resolves an access key to its versitygw account (ADR-0085): an
// in-platform access decodes to (kind, ns, name) and the secret is RE-DERIVED; an external
// access is looked up in the store. The returned Secret is what versitygw checks the
// SigV4 signature against — deriving it here is precisely what makes a function able
// to sign only as itself. Existence/authorization is NOT decided here (the Cedar PEP
// in the backend is the real gate); a decodable access for a missing function still
// yields a derivable secret the attacker cannot produce.
func (s *iam) GetUserAccount(access string) (auth.Account, error) {
	if kind, ns, name, ok := decodeAccess(access); ok {
		kp := DeriveKeypair(s.master, kind, ns, name)
		return auth.Account{Access: access, Secret: kp.SecretKey, Role: auth.RoleUser}, nil
	}
	if s.external != nil {
		if secret, _, ok := s.external.Lookup(access); ok {
			return auth.Account{Access: access, Secret: secret, Role: auth.RoleUser}, nil
		}
	}
	// An unknown/undecodable access key must fail closed with a CLEAN 403: versitygw's SigV4 middleware
	// maps the sentinel auth.ErrNoSuchUser → 403 InvalidAccessKeyId, but any other error → 500 InternalError
	// (which the AWS SDK then retries). Return the sentinel so a forged/unknown key gets an immediate 403.
	return auth.Account{}, auth.ErrNoSuchUser
}

// ListUserAccounts returns the external store's accounts (ADR-0085). In-platform
// accounts are derived on demand and not enumerable (there is no registry of them).
func (s *iam) ListUserAccounts() ([]auth.Account, error) { return nil, nil }

// CreateAccount is not supported for derived accounts (ADR-0085): in-platform
// keypairs are derived, not created. The external-keypair lifecycle is a follow-on.
func (s *iam) CreateAccount(auth.Account) error {
	return fault.Invalidf("s3gateway.iam.CreateAccount", "account creation is not supported (in-platform keypairs are derived)")
}

// UpdateUserAccount is not supported (ADR-0085): a derived account has no mutable state.
func (s *iam) UpdateUserAccount(string, auth.MutableProps) error {
	return fault.Invalidf("s3gateway.iam.UpdateUserAccount", "account update is not supported (in-platform keypairs are derived)")
}

// DeleteUserAccount is not supported (ADR-0085): a derived account has no stored state.
func (s *iam) DeleteUserAccount(string) error {
	return fault.Invalidf("s3gateway.iam.DeleteUserAccount", "account deletion is not supported (in-platform keypairs are derived)")
}

// Shutdown is a no-op (ADR-0085): the IAM holds no resources to release.
func (s *iam) Shutdown() error { return nil }
