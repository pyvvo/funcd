// Package gateway is the funcd catalog PEP proxy over the Quack/DuckLake serving path (ADR-0137,
// FEAT-0008/F102). It brings catalog under the same per-caller, per-query Cedar PEP as blob/kv/S3, for
// BOTH caller classes, by putting funcd on the query path via a proxy that fronts the engine — the S3
// gateway shape (ADR-0085/0088): one proxy door, a per-caller credential resolved to a Cedar principal,
// a per-op PEP, the real backend credential (the shared engine token) used only after allow.
//
// A Quack token is a BARE BEARER credential (unlike an S3 access key, which is inert without the SigV4
// secret), so the internal per-function token must be unforgeable on its own. It is a standard HS256 JWT
// (go-jose) signed with the node master: DeriveCatalogToken signs it, PrincipalFor verifies the signature
// (alg pinned to HS256) before trusting its {ns, fn} claims. The external Identity gets a minted token: its
// public access key id (ADR-0135), ".", and a random, rotatable part. The prefix names the owner, so the
// lookup is two store reads (ADR-0153), the shape of the Identity's S3 credential lookup.
package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/store"
)

// hs256Key derives the fixed 32-byte HS256 signing key from the node master via SHA-256. HS256 (RFC 7518
// §3.2, enforced by go-jose) needs a key at least the hash-output size; the node master can be any length
// (operator-supplied or generated), so we hash it to exactly 32 bytes. Deterministic; used identically on
// sign and verify.
func hs256Key(master []byte) []byte {
	sum := sha256.Sum256(master)
	return sum[:]
}

// tokenAlg is the JWT signing algorithm: HS256 (HMAC-SHA256) — symmetric, keyed by the node master
// (funcd both mints and verifies, one party). Pinned on BOTH sign and verify so an attacker cannot swap
// the alg (the classic JWT alg-confusion attack); go-jose/v4's ParseSigned requires the allow-list.
const tokenAlg = jose.HS256

// catalogTokenSecretKey is the credential Secret data key the minted per-Identity catalog token lives
// under (written by the identity reconciler, ADR-0135/0137). It is the query-path analog of the SigV4
// secretAccessKey key.
const catalogTokenSecretKey = "catalogToken"

// identityTokenSep joins an Identity catalog token's owner prefix and its random part (ADR-0153).
const identityTokenSep = "."

// randomPartLen is the length of randomSecret's output: standard base64 of 32 bytes.
const randomPartLen = 44

// IdentityCatalogToken builds an Identity's catalog token (ADR-0153): its access key id, ".", random.
func IdentityCatalogToken(ns v1.NamespaceName, name v1.ObjectName, random string) string {
	return s3gateway.IdentityAccessKey(string(ns), string(name)) + identityTokenSep + random
}

// decodeIdentityCatalogToken returns the owner a token's prefix names. ok=false: not an Identity token.
func decodeIdentityCatalogToken(token string) (v1.NamespaceName, v1.ObjectName, bool) {
	prefix, random, found := strings.Cut(token, identityTokenSep)
	if !found || len(random) != randomPartLen {
		return "", "", false
	}
	ns, name, ok := s3gateway.DecodeIdentityAccess(prefix)
	if !ok || v1.NamespaceName(ns).Validate() != nil || v1.ObjectName(name).Validate() != nil ||
		s3gateway.IdentityAccessKey(ns, name) != prefix {
		return "", "", false
	}
	return v1.NamespaceName(ns), v1.ObjectName(name), true
}

// fnClaims is the per-function catalog token's payload — the Function principal. NO iat/exp: the token is
// deterministic (re-derived each reconcile, stable across daemon restarts; rotation = master rotation),
// mirroring the derived, non-expiring S3 keypair (ADR-0085).
type fnClaims struct {
	NS string `json:"ns"`
	Fn string `json:"fn"`
}

// DeriveCatalogToken mints the per-(namespace, function) catalog token as an HS256 JWT signed with the
// node master (ADR-0137). A Quack token is a bare bearer credential, so it must be unforgeable on its own:
// only the master holder can produce the signature, so a function cannot forge a peer's token. Deterministic
// — the same (master, ns, fn) always yields the same compact JWT (no time claims), so injection re-derives
// it without stored state.
func DeriveCatalogToken(master []byte, ns v1.NamespaceName, fn v1.ObjectName) (string, error) {
	const op = "gateway.DeriveCatalogToken"
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: tokenAlg, Key: hs256Key(master)}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "new HS256 signer")
	}
	token, err := jwt.Signed(sig).Claims(fnClaims{NS: string(ns), Fn: string(fn)}).Serialize()
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "sign catalog token")
	}
	return token, nil
}

// CatalogKeys resolves a presented catalog token to its Cedar principal (ADR-0137). It mirrors
// s3gateway.principalFor, with the JWT signature standing in for SigV4's per-request possession proof.
type CatalogKeys interface {
	// PrincipalFor resolves token to its principal. First: verify it as an HS256 JWT signed with the node
	// master (a forged {ns, fn} fails the signature) ⇒ a Function principal. Else: decode the owner an
	// Identity token's prefix names and read that Identity and its Secret (two Gets) ⇒ an Identity
	// principal. Else: default-deny (ok=false).
	PrincipalFor(token string) (auth.EntityRef, bool)
}

// macKeys is the production CatalogKeys (ADR-0137): a node master (for the per-function JWT verify) plus
// the metastore (for the per-Identity token's two-Get lookup). A nil store ⇒ Function-only resolution.
type macKeys struct {
	master []byte
	store  store.Store
}

var _ CatalogKeys = (*macKeys)(nil)

// NewCatalogKeys builds the production CatalogKeys over the node master and the metastore.
func NewCatalogKeys(master []byte, s store.Store) CatalogKeys {
	return &macKeys{master: master, store: s}
}

// PrincipalFor implements CatalogKeys. Verify-JWT ⇒ Function; else owner lookup ⇒ Identity; else deny.
func (k *macKeys) PrincipalFor(token string) (auth.EntityRef, bool) {
	if ref, ok := k.functionFor(token); ok {
		return ref, true
	}
	if k.store != nil {
		if ref, ok := k.identityFor(token); ok {
			return ref, true
		}
	}
	return auth.EntityRef{}, false
}

// functionFor verifies a per-function token as an HS256 JWT signed with the node master and returns the
// Function principal from its verified {ns, fn} claims. An unparseable token, a wrong/other algorithm, a
// bad signature (a forged token under the wrong key), or empty claims all fail closed (ok=false). The
// alg allow-list ([HS256]) is what makes go-jose reject an alg-confusion attack.
func (k *macKeys) functionFor(token string) (auth.EntityRef, bool) {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{tokenAlg})
	if err != nil {
		return auth.EntityRef{}, false
	}
	var c fnClaims
	if err := parsed.Claims(hs256Key(k.master), &c); err != nil { // verifies the HMAC signature with the master
		return auth.EntityRef{}, false
	}
	if c.NS == "" || c.Fn == "" {
		return auth.EntityRef{}, false
	}
	return auth.EntityRef{Type: v1.KindFunction, Namespace: v1.NamespaceName(c.NS), Name: v1.ObjectName(c.Fn)}, true
}

// identityFor resolves an Identity catalog token in two store reads (ADR-0153): it decodes the owner the
// prefix names, refusing with no read a token that does not carry a canonical owner, then compares the
// whole token with that Identity's stored catalogToken in constant time. The prefix grants nothing on its
// own. A deleted Identity/Secret stops resolving, like storeExternalKeys. It has no context (the resolver
// seam), so it reads with context.Background().
func (k *macKeys) identityFor(token string) (auth.EntityRef, bool) {
	ns, name, ok := decodeIdentityCatalogToken(token)
	if !ok {
		return auth.EntityRef{}, false
	}
	ctx := context.Background()
	idObj, err := k.store.Get(ctx, v1.KindIdentity.GVK(), ns, name)
	if err != nil {
		return auth.EntityRef{}, false
	}
	id, isIdentity := idObj.(*v1.Identity)
	if !isIdentity {
		return auth.EntityRef{}, false
	}
	secretName := id.Spec.CredentialSecretName
	if secretName == "" {
		secretName = id.Name
	}
	secObj, err := k.store.Get(ctx, v1.KindSecret.GVK(), id.Namespace, secretName)
	if err != nil {
		return auth.EntityRef{}, false
	}
	sec, isSecret := secObj.(*v1.Secret)
	if !isSecret || !v1.ControlledBy(sec.OwnerReferences, v1.KindIdentity, id.UID) {
		return auth.EntityRef{}, false
	}
	stored := sec.Spec.Data[catalogTokenSecretKey]
	if len(stored) == 0 || !hmac.Equal(stored, []byte(token)) {
		return auth.EntityRef{}, false
	}
	return auth.EntityRef{Type: v1.KindIdentity, Namespace: id.Namespace, Name: id.Name}, true
}
