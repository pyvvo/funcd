# ADR-0153: The Identity catalog token carries its owner — two store reads, never a scan

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: iam, identity, catalog, credential, security
- **Realizes**: [FEAT-0008/F102](../feat/0008-feat-iam.md) (per-caller `catalog::query` RBAC — the per-`Identity`
  catalog token)
- **Supersedes (in part)**: [ADR-0137](0137-per-caller-catalog-query-rbac.md) Decision 2, only its words about the
  per-`Identity` token "minted, random, rotatable catalog token (no structure to forge)": the token gains a public
  owner prefix, and its "store lookup" becomes "decode the owner, two Gets". The secret part stays minted, random and
  rotatable. Everything else in ADR-0137 stands, including the per-function token.
- **Relates to**: [ADR-0135](0135-managed-identity.md) (`IdentityAccessKey`/`DecodeIdentityAccess` and
  `storeExternalKeys.Lookup`, the shape reused) · [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md)
  (the edge that exposes the proxy before auth) · Proposed [ADR-0170](0170-owner-garbage-collector.md) (its Decision 4
  namesake re-issue; either ADR may land first, and a re-issue mints in the format then in force)

## Context & Need

The catalog PEP proxy resolves each Quack handshake's token to a principal before it authorizes (ADR-0137).
`PrincipalFor` (`internal/catalog/gateway/token.go`) first verifies a master-signed HS256 JWT, with no store read. Any
other token goes to `identityFor`, which Lists every `Identity` in every namespace and Gets each one's credential
`Secret` to compare its `catalogToken` (#194): the minted token (`randomSecret()`,
`internal/services/identity/reconcile.go`) names no owner to look up by. Every Identity or garbage token costs one
List plus one Get per Identity: 0.19 ms at 0 Identities, 9.82 ms at 2000, linear (#194 probe, memory store), and with
`spec.ingress` (ADR-0138) an unauthenticated client can loop it. The same Identity's S3 credential has no such cost:
`storeExternalKeys.Lookup` (`internal/services/identity/externalkeys.go`) decodes `(ns, name)` from the access key and
does two Gets.

The purpose: an Identity catalog token resolves in a fixed number of store reads, whatever the number of Identities,
and a token that does not carry a canonical owner costs no store read.

## Scenarios

- `scenario: identity-token-lookup-cost-constant` — Given 1 Identity, and separately 500 Identities, each with an
  issued catalog token, When the last-created Identity's token is presented, Then it resolves to that Identity with
  exactly two store reads and no List, at both sizes.
- `scenario: garbage-token-refused-without-reads` — Given 500 Identities, When a token is presented that is neither a
  funcd JWT nor prefixed by a canonical owner (`garbage-unresolvable-token`, a 44-character token in the old format, a
  bare access key id, a `FUNCID` prefix over a non-DNS name, a non-canonical encoding of a real owner), Then no
  principal resolves (the proxy answers 403) and the store is not read.
- `scenario: identity-token-resolves` — Given Identity `analyst` in `data` reconciled by the identity controller, When
  the `catalogToken` from its owned Secret is presented, Then it resolves to `Identity::"data/analyst"`.
- `scenario: forged-owner-prefix-denied` — Given issued Identities `data/analyst` and `ops/admin`, When `analyst`
  presents its own random part, or a guessed one, behind a prefix naming `ops/admin`, Then no principal resolves (403).
- `scenario: identity-token-rotation` — Given an issued token T1, When `spec.rotate` is bumped and the Identity is
  reconciled, Then the Secret holds T2 with the same prefix and a new random part, T2 resolves, and T1 is refused.

## Scope

In: the per-Identity catalog token's format, its minting by the identity reconciler, its resolution in `identityFor`.

Out:
- the per-function token (`DeriveCatalogToken`, an HS256 JWT) and the Identity's S3 SigV4 credential, unchanged;
- rate limiting handshakes at the edge (FEAT-0006/F75: only `key: clientIP` limits them; under `key: function`
  ADR-0164 leaves Upstream matches unlimited, and the in-flight cap still applies);
- the handshake read, already bounded (`proxy.go` reads at most `handshakeHeadMax` before it decides, #39);
- a live Identity-token query on the `duckdb` Lima lane.

## Constraints & Decision drivers

- Least work before auth on an open edge: a token without a canonical owner costs no store read, and any other
  token costs at most two.
- One shape for both Identity credentials: ADR-0135's access key already decodes to `(ns, name)` and resolves in two Gets.
- A Quack token is a bearer credential (ADR-0137 Decision 2): the whole token is compared in constant time, and only
  its 32 random bytes (`crypto/rand`) prove possession. The owner part is public; it equals `Identity.status.accessKeyId`.
- Stateless and restart-safe, as today: every handshake reads the store; no in-memory state.
- The token is a length-prefixed handshake-body field, parsed up to 4 KiB (`handshakeHeadMax`); the `duckdb` lane
  already carries a 132-byte per-function JWT (a 2-byte length) through DuckDB's Quack client.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Owner prefix + random part, two Gets** | O(1); no new component; the S3 Identity credential's shape; restart-safe | changes a credential format an Implemented ADR fixed; older tokens stop resolving; the token shows its (public) owner | **chosen** |
| Opaque token + an in-memory `sha256(token)` → Identity index fed by a store Watch, each hit confirmed by two Gets | the token stays opaque | a token minted moments ago, or any token after a restart, is denied until the index catches up; a new long-running component; no longer "resolved by a store lookup" | rejected |
| Opaque token + a persisted metastore record `sha256(token)` → `(ns, name)` written at mint | the token stays opaque; restart-safe; no Watch | the token names no namespace, so the record lives outside any namespace: no owner-reference cascade, orphan cleanup on delete; a mint becomes two writes with no cross-object atomicity | rejected |
| Owner prefix, with the scan kept as a fallback for prefix-less tokens | older tokens keep working | every garbage token takes the fallback, so the scan stays reachable before auth | rejected; existing tokens are re-issued by a `spec.rotate` bump instead |
| A master-signed JWT `{ns, name, rotate}` per Identity | a forged signature costs no read | rotation and revocation still need the same two reads; possession rests on the node master, not a per-Identity secret | rejected |

## Decision

1. **Format**: `IdentityAccessKey(ns, name) + "." + random`. The prefix is the Identity's access key id
   (`"FUNCID" + base32NoPad(ns "\x00" name)`, ADR-0135); `random` is `randomSecret()` (standard base64 of 32
   `crypto/rand` bytes, 44 characters). `"."` is in neither alphabet; a JWT has two dots, so neither resolver path
   accepts the other's token. Length 56–255 bytes: namespace and name are DNS labels of at most 63
   (`api/types/v1alpha1/ids.go`), so at most 6 + 204 + 1 + 44.
2. **Lookup**: `PrincipalFor` keeps its order: a valid JWT ⇒ `Function`, no store read. Otherwise `identityFor` decodes
   the prefix. It is refused at once, with no store read and no fallback, unless the separator is present, the random
   part has 44 characters, `DecodeIdentityAccess` accepts the prefix, both names are valid DNS labels, and the prefix
   is the canonical `IdentityAccessKey` of that owner. Otherwise it Gets that `Identity` and its credential `Secret`
   and compares the whole presented token with the stored `catalogToken` by `hmac.Equal` ⇒ that `Identity`; anything
   else ⇒ refused. `PrincipalFor` makes at most two Gets and no List; authorization after it is unchanged.
3. **A prefix grants nothing**: the principal is the Identity whose stored token equals the presented one. An Identity
   in namespace A that presents a token naming namespace B gets 403, unless it holds B's random part — B's credential.
4. **Rotation**: every re-issue by `ensureSecret` mints a new random part under the same prefix, together with the
   SigV4 secret. That covers today's triggers (Secret absent, or `spec.rotate > status.observedRotate`) and any trigger
   a later ADR adds, such as ADR-0170 Decision 4 once it lands; this ADR adds no trigger. The old token is refused from
   the Secret update on.
5. **Clients see an opaque string**: the token is delivered only as `catalogToken` in the Identity's owned Secret and
   documented to users only as an opaque string; no dedicated command parses or prints its parts. funcd promises
   nothing about it beyond "opaque string".
6. **No migration**: a token minted before this ADR (44 characters, no prefix) is refused. An Identity issued before
   it gets a working token by a `spec.rotate` bump, or, once ADR-0170 is implemented, by being recreated (until then,
   a recreated Identity keeps its namesake's Secret and its old token).

## Temporary workarounds

None.

## Contracts

```go
// internal/catalog/gateway/token.go — imports gain "strings" and internal/blob/s3gateway.

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

// identityFor resolves an Identity catalog token in two store reads (ADR-0153).
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
	if !isSecret {
		return auth.EntityRef{}, false
	}
	stored := sec.Spec.Data[catalogTokenSecretKey]
	if len(stored) == 0 || !hmac.Equal(stored, []byte(token)) {
		return auth.EntityRef{}, false
	}
	return auth.EntityRef{Type: v1.KindIdentity, Namespace: id.Namespace, Name: id.Name}, true
}
```

```go
// internal/services/identity/reconcile.go, ensureSecret — imports internal/catalog/gateway as cataloggw.
random, err := randomSecret()
if err != nil {
	return err
}
catalogToken := cataloggw.IdentityCatalogToken(id.Namespace, id.Name, random)
```

| Consumes | Exposes |
|---|---|
| `s3gateway.IdentityAccessKey`/`DecodeIdentityAccess` (ADR-0135); metastore Gets of the `Identity` and its credential `Secret` (`catalogToken` key) | `gateway.IdentityCatalogToken` (called by the identity reconciler); `PrincipalFor` resolving an Identity token in ≤ 2 reads and refusing an undecodable one with none |

New acyclic import edges (`go list -deps`): `internal/catalog/gateway` → `internal/blob/s3gateway`;
`internal/services/identity` → `internal/catalog/gateway` (as `internal/function` does for `DeriveCatalogToken`).

## Implementation plan

1. `internal/catalog/gateway/token.go`: add `identityTokenSep`, `IdentityCatalogToken` and
   `decodeIdentityCatalogToken`; replace the scan in `identityFor`; reword the package doc and the
   `CatalogKeys`/`macKeys`/`identityFor` comments that call the token opaque or the lookup a scan, and credit the
   access-key shape to ADR-0135 (not ADR-0088).
2. `internal/catalog/gateway/proxy.go`: the `handshakeHeadMax` comment's "a 44-byte minted Identity token" becomes "an
   Identity token of at most 255 bytes"; the constant is unchanged.
3. `internal/services/identity/reconcile.go`: mint with `IdentityCatalogToken`; update the `secretKeyCatalogToken`
   comment.
4. Tests:
   - `internal/catalog/gateway/scenario_test.go`: a `countingStore` (embeds `store.Store` over the memory driver,
     counts `Get` and `List`; the counting tests call `NewCatalogKeys(...).PrincipalFor` directly, not the proxy,
     whose authorization Lists role assignments); `TestScenarioIdentityTokenLookupCostConstant` (N = 1 and 500: 2
     Gets, 0 Lists), `TestScenarioGarbageTokenRefusedWithoutReads` (0 Gets, 0 Lists),
     `TestScenarioForgedOwnerPrefixDenied`. `TestCatalogKeysResolvesMintedIdentityToken` builds its token with
     `IdentityCatalogToken` (its old-format token is now refused).
   - `internal/catalog/gateway/token_test.go` (new): `TestIdentityCatalogTokenRoundTrip` — decode returns the owner; a
     63/63 owner gives 255 bytes; refused: no separator, a random part not 44 characters long, a Function access key
     (`FUNCD…`), bad base32, a non-DNS owner, a non-canonical encoding, a bare access key id.
   - `internal/catalog/gateway/handshake_test.go`: `TestSwapHandshakeToken` gains a 255-byte (maximum) token
     case (the 200-byte case already covers the 2-byte length).
   - `internal/services/identity/reconcile_test.go`: `TestScenarioIdentityTokenResolves` and
     `TestScenarioIdentityTokenRotation` (the reconciler mints, `gateway.NewCatalogKeys` resolves);
     `TestScenarioIdentityIssuesCredential` also asserts the token starts with `status.accessKeyId + "."`.
5. `scripts/agent/d go test -race ./internal/catalog/gateway/ ./internal/services/identity/`, plus vet and lint on both.
   The PR gate runs `just ci-full` and the `duckdb` lane once: its edge garbage-token step must still answer 403, and
   the suite needs no change (it presents no Identity token).
6. Documents: at Draft, the FEAT-0008/F102 row adds `(+ ADR-0153 Identity token format)` to its ADR cell and splits
   its status into `catalog::query: implemented · Identity token: adr`, as F13 does (ADR-0148 also edits this cell).
   At acceptance, ADR-0137 gains `Superseded in part by ADR-0153`; no blueprint change ("a minted per-Identity token"
   stays true). The PR carries `Fixes #194`, a `!` title and a `BREAKING CHANGE:` footer: Identity catalog tokens
   minted before ADR-0153 are refused, so operators bump `spec.rotate` on every Identity whose token is in use before
   external Quack/DuckDB clients reconnect.

**Definition of done**: every scenario has a named, passing test; `identityFor` makes no List; the four Go sub-checks
are green; the `duckdb` lane is green.

## Review checklist

- [ ] The Identity catalog token is built only by `IdentityCatalogToken`: access key id, `"."`, `randomSecret()`.
- [ ] `PrincipalFor` makes no `List` and at most two `Get`s; a token without a canonical owner makes none; no fallback.
- [ ] The whole token is compared by `hmac.Equal`; the principal comes from the stored Identity.
- [ ] The JWT path runs first and is unchanged.
- [ ] Rotation keeps the prefix, replaces the random part, and the old token is refused.
- [ ] No token value is logged; no doc or `funcdctl` output describes the token's format.
- [ ] Each scenario has a named, passing test; the counting test asserts 2 Gets and 0 Lists at N = 1 and N = 500.
- [ ] The PR is marked breaking (`!` and a `BREAKING CHANGE:` footer naming the `spec.rotate` re-issue).
- [ ] The `handshakeHeadMax` comment is updated; a 255-byte token round-trips `swapHandshakeToken`.

## Consequences

- Positive: an Identity token resolves in at most two reads and garbage in none, whatever the number of Identities;
  the pre-auth scan of #194 is gone; both Identity credentials resolve the same way, with no new state.
- Negative: the token shows its owner (as `status.accessKeyId` already does). Anyone can build a canonical owner from
  public names, so such a token still costs up to two reads, and timing tells an existing Identity from a missing one,
  as with the S3 lookup. Tokens minted before this ADR stop working.
  `internal/catalog/gateway` now imports `internal/blob/s3gateway`.

## Open questions

None.

## References

- Issue [#194](https://github.com/pyvvo/funcd/issues/194) (the scan and its probe numbers)
- `internal/catalog/gateway/token.go` (`PrincipalFor`, `identityFor`), `internal/catalog/gateway/proxy.go`
  (`handshakeHeadMax`), `internal/services/identity/reconcile.go` and `externalkeys.go`,
  `internal/blob/s3gateway/iam.go` (`IdentityAccessKey`, `DecodeIdentityAccess`)
- `e2e/duckdb.venom.yml` (the edge garbage-token step)
