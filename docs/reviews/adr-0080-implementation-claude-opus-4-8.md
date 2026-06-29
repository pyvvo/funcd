# ADR-0080 + ADR-0085 implementation review — S3-protocol frontend on the blob substrate (`claude-opus-4-8`)

- **ADRs**: [0080](../adr/0080-s3-protocol-frontend-blob-substrate.md) (S3 frontend, FEAT-0003/F47) + [0085](../adr/0085-s3-in-platform-identity-funcd-keypair.md) (in-platform identity, partial supersession of 0080 §AuthN)
- **Phase**: implementation · **Model**: claude-opus-4-8 · built in 3 slices (CRD → Cedar → s3gateway)
- **Verdict**: **pass** (DoD met across both ADRs; no Blockers/Majors; one accepted license-policy exception + minor follow-ons) · 2026-06-30

## Verification (evidence — run, not eyeballed)

| check | result |
|---|---|
| `go build ./...` · `CGO_ENABLED=0 go build ./...` | **OK** · **OK** (pure-Go gate passes despite the heavy versitygw tree) |
| `go test ./...` | green **except** `internal/testkit/bench` `TestPythonPoolSmoke` (py pool shim readiness) — **`env`-attributed**, unrelated to F47 (no s3/blob/Bucket reference) |
| `go test ./internal/blob/s3gateway/... -v` | **ok** — all scenarios pass, run directly: `signs-as-self`, **`cannot-forge-peer` (403)**, `binding-grants-read`, `owner-writes` (+ non-owner denied), `unbound-denied`, `cross-namespace-rejected`, `external-sigv4`, `listobjects-glob`, `rangereader-fallback`, `owner-multipart-write`, the spine, + IAM units (derive/decode/mutators) |
| `go test ./internal/auth/... ./internal/function/... ./pkg/funcd/... ./internal/platform/config/... ./api/...` | **ok** — Cedar s3 authz units; keypair-env injection; the enabled/disabled gateway wiring (real listener opens iff enabled); Bucket Validate + admissions; kind-count |
| `go tool golangci-lint run` (all changed pkgs) · `go mod verify` | **0 issues** · all modules verified |
| **OpenAPI** | regenerated — `/buckets` + `v1alpha1.Bucket` on the wire (golden test green) |
| **identity/path grep** | clean on all changed/new files; the master secret is never logged |

## Conformance to the ADR Review checklists

**ADR-0080:** s3gateway opens no port unless `s3gateway.enabled` ✓; backend embeds `BackendUnsupported`, only the DuckDB/DuckLake subset overridden ✓; `RangeReader` optional with a working `Get`+slice fallback, `blobcontract` additive (mem + file) ✓; **every S3 op is a PEP → cedar PDP** (`s3::read`/`s3::write`), default-deny, the `BlobPrefix` resource materialized from the `Bucket` CRD ✓; `spec.blob` **binding IS the read grant**, write requires `caller == prefix.owner`, unbound Forbidden ✓; **cross-namespace denied** ✓; path-style, S3-bucket→namespace mapping ✓; listener node-private ✓; pure-Go (no cgo) ✓; one passing test per Scenario ✓.

**ADR-0085:** in-platform keypair is **derived** (HMAC over the Ref), **deterministic across restart**, never stored per function; master secret `0600`, never logged ✓; keypair **injected into the sandbox env** only when enabled && `spec.blob` ✓; `iam` implements the **full 6-method `auth.IAMService`**; built on **`s3api.New`** + `ServeMultiPort`/`ShutDown`, not `embedgw.RunVersityGW` ✓; **SigV4 passes only for the secret holder** — `signs-as-self` ✓ / `cannot-forge-peer` → 403 ✓; account→principal (in-platform → Ref, external → S3Identity); the **ADR-0080 Cedar PEP unchanged** ✓; partial supersession touches only AuthN + entry point ✓.

## ✅ Verified correct (what's strong — keep it)

- **The security model holds end-to-end, run not asserted.** `cannot-forge-peer` proves a request signed with the wrong secret for another function's access key gets **403**; `owner-writes` + non-owner-denied prove single-writer; `unbound-denied` + `cross-namespace-rejected` prove default-deny tenancy. SigV4 is verified by versitygw against the secret `iam.GetUserAccount` derives, so a function can sign only as itself.
- **The "one substrate, two surfaces" goal is real** — the backend targets the `blob.Bucket` port (via a `blob.Prefixed` `s3/<ns>/<bucket>/` view), so the S3 bytes are the same governed bytes functions use; swap the blob driver → the gateway is memory / file / remote-S3 unchanged.
- **Cedar is the sole authz gate** — the implementer discovered versitygw runs its *own* ACL/policy/lock/versioning gates in front of the backend and correctly neutralized them (caller-owned ACL, no bucket policy, lock/versioning unset) so funcd's PEP is authoritative. A non-obvious, correct integration call.
- **The versitygw fail-closed defaults were retired properly** (concurrency limiter, multipart max-parts, the account-via-fiber-locals, the SDK checksum trailer) — documented, not hacked.
- **Disciplined slicing** — CRD (modeled verbatim on KVStore, no Status) → Cedar (one `blobPrefixUID` builder so `blobBindings.contains(resource)` matches) → s3gateway, each its own green commit.

## Findings

- **Blockers / Majors**: none.
- **Accepted exception (decider-approved, recorded):**
  - **L1 — MPL-2.0 transitive deps.** versitygw transitively requires 5 **MPL-2.0** modules (`hashicorp/vault-client-go`, `go-retryablehttp`, `go-rootcerts`, `go-cleanhttp`, `go-secure-stdlib/strutil`) from its **unused** Vault IAM backend. This deviates from the literal "Apache-2.0/MIT only" rule but **not its intent** (no GPL/AGPL — MinIO was rejected for AGPL; MPL-2.0 is weak file-level copyleft, link/redistribute-unmodified explicitly permitted, §3.3). **Accepted by the decider** as a permitted exception for transitive, unmodified deps. Not `model`-attributed.
- **Minor (follow-ons, recorded):**
  - **m1 — deletion-protection data-emptiness prober is nil-wired** (slice 1) — binding-protection is fully active; the per-prefix non-empty-data check is a thin follow-on now that the s3 data plane exists.
  - **m2 — external-keypair lifecycle** (issue/rotate/revoke UX) is the ADR-0085-deferred follow-on; the `ExternalKeys` read seam is implemented.
  - **m3 — multipart buffers to the cap** (no streaming blob seam) — ADR-0080 Temporary workaround with its exit criterion.
  - **m4 — the node-gated real-DuckDB httpfs e2e** is deferred per the ADR (the in-process real-AWS-SDK scenarios cover the contract).

## Recommendation

**pass.** The S3 frontend is implemented end-to-end on funcd's own primitives — versitygw does the protocol, the backend bridges to `blob.Bucket` with the Cedar `spec.blob` PEP on every op, and in-platform identity is funcd-derived per-function keypairs (ADR-0085) whose isolation property is verified (`cannot-forge-peer` 403). 12 scenarios green via the real AWS SDK client; build/lint/CGO-free/mod-verify clean; OpenAPI regenerated. The single suite failure is the env python-shim flake. The MPL-2.0 transitive deps are a decider-accepted license exception. Stamp ADR-0080 and ADR-0085 `Reviewing → Implemented` and FEAT-0003/F47 → `implemented`.
