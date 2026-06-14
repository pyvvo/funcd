# ADR-0007: Blob / storage-layer port (`blob.Bucket` over `gocloud.dev/blob`)

- **Status**: Implemented
- **Date**: 2026-06-14 (Accepted + **Implemented 2026-06-14** — review pass, see docs/reviews/adr-0007-implementation-claude-opus-4-8.md; post-judge: typed `SignMethod`, §3 mapping note, encryptor-asymmetry note)
- **Deciders**: green-0-rabbit
- **Tags**: blob, storage-layer, gocloud, bytes-substrate, port, presign
- **Realizes**: [FEAT-0000/F21](../feat/0000-feat-v1.md) (storage layer — the `blob` half; the database
  half is [ADR-0006](0006-store-database-layer-port.md))
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (ports/drivers, `api/fault`,
  one-file drivers, ctx-first, no-`any`), [ADR-0006](0006-store-database-layer-port.md) (the **sibling
  substrate** — `blob` is bytes, `store` is records; deliberately distinct names), [blueprint.md — Storage
  layer / Substrate layers](../../blueprint.md). **No cgo** (contrast ADR-0006): `gocloud.dev/blob` is
  pure-Go.

## Context & Need

Every function-facing **blob storage** service (F23/P-O), and the S3-backed object backend that
`Secret`/`Config` drivers reuse, sits on the **storage layer**: an opaque-bytes object store behind the
**`blob.Bucket` port** (the bytes substrate, mirroring `store` for records). The blueprint fixes the
implementation — **`gocloud.dev/blob`** (Apache-2.0, Google's go-cloud) with its `memblob`/`fileblob`/`s3blob`
backends — so funcd reuses a mature library instead of hand-rolling an S3 client per service. Nothing in
the data plane can store bytes until this port exists; P-O (blob service), and the S3-backed
secrets/config drivers, are built *on* it.

**Purpose**: define and implement the `blob.Bucket` port — **opaque keyed byte objects with get / put /
delete / exists / list-by-prefix / signed-URL**, backed by one **gocloud** driver that spans
**memory / file / S3 by URL** (`mem://`, `file:///path`, `s3://bucket`). Callers: the blob service
(P-O) exposes it function-facing; secrets/config (P-P/F15, P-N) use it as an at-rest backend; the facade
(P-I) wires the in-memory bucket for the e2e harness. Conformance is mechanical: bytes round-trip,
absent keys are `fault.NotFound`, list filters by prefix, and the **same contract suite passes against the
memory and file backends** (the pure-Go `memblob` is the in-memory driver — no separately hand-written one,
per the substrate's in-memory-from-the-library rule).

## Scenarios

- `scenario: blob-roundtrip` — **Given** a bucket, **when** a key is `Put` then `Get`, **then** the bytes
  round-trip equal and `Exists` reports true.
- `scenario: not-found` — **Given** no such key, **when** `Get`/`Delete`, **then** `fault.NotFound`; **when**
  `Exists`, **then** `(false, nil)`.
- `scenario: delete-removes` — **Given** a stored key, **when** `Delete` then `Get`, **then**
  `fault.NotFound` and `Exists` is false.
- `scenario: list-by-prefix` — **Given** keys `a/1`, `a/2`, `b/1`, **when** `List("a/")`, **then** only
  `a/1`, `a/2` (with size), in sorted key order.
- `scenario: signed-url-unsupported-locally` — **Given** the memory/file backend (no signer), **when**
  `SignedURL`, **then** `fault.Unavailable` ("signing not supported by this backend") — the S3 happy path
  (a real presigned URL) is exercised in the S3 integration lane, not the default unit lane.
- `scenario: driver-conformance-parity` — **Given** the `blobcontract` suite, **when** it runs against the
  **memory** backend and the **file** backend, **then** both pass the identical assertions.

## Scope

**In**:
- The **`blob.Bucket` port** in `internal/blob`: `Get`/`Put`/`Delete`/`Exists`/`List`/`SignedURL`/`Close`
  over `[]byte` values keyed by an opaque path string; `api/fault` errors; ctx-first.
- One **gocloud driver** (`internal/blob/gocloud`): `Open(ctx, url)` wrapping `blob.OpenBucket`, spanning
  memory/file/S3 by URL — gocloud's backends are the "drivers"; the pure-Go `memblob` is the in-memory one.
- The **`blobcontract` conformance suite** run against the memory and file backends.

**Out**:
- **The function-facing blob *service*** (CRUD/binding via the `Service` CRD, presigned-download flows) —
  P-O/F23, built *on* this port.
- **Real S3 presign / S3 round-trip happy-path** — an **integration lane** (needs an S3 endpoint /
  credentials); the unit lane covers the port shape + the local-unsupported signing path.
- **Object lifecycle / retention / versioning / multipart** — follow-ups; gocloud exposes them but V1
  needs the five core verbs.
- **Encryption-at-rest of blob contents** — the secrets service (P-P) layers envelope encryption on top
  (encrypt-before-`Put`); the port stores opaque bytes. *(Asymmetry with ADR-0006 by design: the store
  offers an `Encryptor` seam because it serializes typed objects itself; the blob port is opaque bytes the
  caller controls, so the caller encrypts before `Put` — no seam needed here.)*

## Constraints & Decision drivers

- **C1 — library-first, pure-Go**: the blueprint names `gocloud.dev/blob`; it already abstracts
  memory/file/S3, so funcd writes a thin port + one driver, **no cgo** (unlike the store's slatedb).
- **C2 — ADR-0002 conventions**: port-in-its-own-package (`internal/blob/blob.go`, driver-dep-free), the
  driver in its own subpackage (`gocloud/gocloud.go`) to keep go-cloud out of the port; `api/fault`,
  ctx-first, no globals, no `any` leak.
- **C3 — name `blob`, not `storage`**: deliberately distinct from `store` (records) — a two-letter
  near-homonym would be a defect in an LLM-implemented codebase (blueprint note).
- **C4 — in-memory from the library**: `memblob` is pure-Go; it **is** the in-memory driver — funcd does
  **not** hand-write a second memory bucket (the substrate rule; the store is the exception because its
  library's memory mode is cgo).
- **D1 — trustworthy fake**: the memory backend must be provably equivalent to the file/S3 backends —
  enforced by the shared contract suite (the e2e-on-library + `InMemory()` strategy uses `memblob`).

## Alternatives considered

**Object-store library** (driver: maturity vs scope vs cgo):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **`gocloud.dev/blob`** (memblob/fileblob/s3blob) | mature (Google), **pure-Go**, one API spans mem/file/S3 + GCS/Azure; Apache-2.0; the blueprint's pick | a broad dep tree; URL-mux indirection | **chosen** (blueprint-aligned, pure-Go, one lib spans backends) |
| hand-rolled `aws-sdk-go-v2` S3 client + a local-fs + a map | no go-cloud indirection | reimplements what gocloud gives; a second memory/file impl to test; more code | rejected (reinvents the substrate) |
| `minio-go` | good S3 client | S3-only (no file/memory abstraction); would still need a local + memory impl | rejected (no multi-backend abstraction) |

**In-memory driver**: `memblob` (pure-Go, from the library) — **chosen**; a hand-written map bucket is
rejected (redundant; `memblob` is the real thing and cgo-free, so the store's "keep a tiny pure-Go engine"
exception does not apply here).

**Key type**: an opaque `string` path (not a typed ID) — blob keys are caller-chosen content paths, not
identity/enums; ADR-0002's typed-ID rule targets identity, not opaque content keys. (Open questions.)

## Decision

### 1. The `blob.Bucket` port — opaque keyed bytes (driver-independent)
`internal/blob` exposes `Bucket`: `Get`/`Put`/`Delete`/`Exists`/`List`/`SignedURL`/`Close` over `[]byte`
keyed by a path `string`. Errors are `api/fault` kinds; every method is ctx-first. The port imports no
driver library (go-cloud lives only in the `gocloud` subpackage).

### 2. One gocloud driver spanning memory/file/S3 by URL
`internal/blob/gocloud.Open(ctx, url)` opens a `*blob.Bucket` via `blob.OpenBucket(ctx, url)` and adapts
it to the port. `mem://` → in-memory (`memblob`), `file:///var/lib/funcd/blobs` → local files
(`fileblob`), `s3://bucket?region=…` → S3 (`s3blob`). gocloud's per-backend behavior is mapped to
`api/fault` via `gcerrors.Code` (§3). One file in its own subpackage (ADR-0002 §8).

### 3. Error mapping (gocloud → `api/fault`)
The driver translates `gcerrors.Code(err)`: `NotFound → fault.NotFound`; `Unimplemented` (e.g. `SignedURL`
on memory/file) `→ fault.Unavailable`; everything else `→ fault.Internal` (wrapping the cause). `Exists`
returns `(false, nil)` for a missing key, never an error. *(Mapping note: there is no `fault.Unsupported`
kind, so the backend-can't-sign capability gap maps to `Unavailable` — the closest stable kind — not to a
transient outage; callers treat it as "this backend has no signer".)*

### 4. List + signing
`List(ctx, prefix)` walks the gocloud list iterator under the prefix and returns `[]Attributes`
(`Key`, `Size`, `ModTime`), sorted by key. `SignedURL(ctx, key, opts)` delegates to gocloud's
`SignedURL`; memory/file backends report `fault.Unavailable` (no signer), S3 returns a presigned URL.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **`SignedURL` unsupported on memory/file** | `memblob`/`fileblob` ship no URL signer | the **S3 backend** (and a configured `fileblob` signer, later) supports it; the port returns a clear `fault.Unavailable` meanwhile; S3 presign verified in the integration lane |
| **S3 round-trip tested only in an integration lane** | unit CI has no S3 endpoint/creds | P-S adds the S3 integration lane (localstack/minio); the unit lane proves the port over memory+file |
| **No lifecycle/retention/multipart** | V1 needs the five verbs | a follow-up if a service needs them (gocloud exposes them) |

## Contracts

### The port (`internal/blob/blob.go`)
```go
package blob

import (
	"context"
	"time"
)

// Bucket is the storage-layer port: opaque byte objects keyed by a path string.
// Errors are api/fault kinds; every method is ctx-first. Implementations adapt a
// driver library (gocloud) — this package imports none.
type Bucket interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix string) ([]Attributes, error)
	SignedURL(ctx context.Context, key string, opts SignOptions) (string, error)
	Close() error
}

// Attributes is the listing metadata for one object.
type Attributes struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// SignMethod is the typed HTTP method a SignedURL grants (ADR-0002: typed over magic strings).
type SignMethod string

const (
	SignGet    SignMethod = "GET"
	SignPut    SignMethod = "PUT"
	SignDelete SignMethod = "DELETE"
)

// SignOptions configures a SignedURL request.
type SignOptions struct {
	Method SignMethod    // SignGet (default if zero)
	Expiry time.Duration // default 15m if zero
}
```

### The driver (`internal/blob/gocloud/gocloud.go`)
```go
// Open adapts a gocloud bucket to blob.Bucket.
//   Open(ctx, "mem://")                       → in-memory (memblob; the cgo-free in-mem driver)
//   Open(ctx, "file:///var/lib/funcd/blobs")  → local files (fileblob)
//   Open(ctx, "s3://bucket?region=us-east-1") → S3 (s3blob)
func Open(ctx context.Context, url string) (blob.Bucket, error)
```

### The contract suite (`internal/blob/blobcontract/contract.go`)
```go
func RunContract(t *testing.T, newBucket func(t *testing.T) blob.Bucket) // memory + file tests both call it
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/fault` (ADR-0002) | fault kinds; gocloud `gcerrors` mapped to them |
| Adds (lib) | `gocloud.dev/blob` (+ `memblob`, `fileblob`, `s3blob` driver imports) | **Apache-2.0**, pure-Go — no cgo |
| Exposes | `blob.Bucket` + the gocloud driver + the contract suite | consumed by P-O (blob service), P-P/P-N (s3-backed drivers), P-I (facade `InMemory`) |

## Implementation plan

No business logic beyond adapting gocloud to the port; all backends are pure-Go (cgo-free unit tests).

1. **`internal/blob/blob.go`** — the `Bucket` interface, `Attributes`, `SignOptions` (driver-dep-free).
2. **`internal/blob/gocloud/gocloud.go`** — `Open(ctx, url)`; adapt `Get`/`Put`/`Delete`/`Exists`/`List`/
   `SignedURL`/`Close`; map `gcerrors.Code` → `api/fault` (§3); blank-import `memblob`/`fileblob`/`s3blob`
   so their URL schemes register.
3. **`internal/blob/blobcontract/contract.go`** — `RunContract` with real assertions for the scenarios.
4. **Deps** — `go get gocloud.dev/blob`; `go mod tidy`. No cgo, no build tags.
5. **Test plan** (one named test per Scenario, all passing):
   - `internal/blob/gocloud/gocloud_test.go` → `RunContract` against **memory** (`mem://`) and **file**
     (`file://` + `t.TempDir()`) — covering `blob-roundtrip`, `not-found`, `delete-removes`,
     `list-by-prefix`, `signed-url-unsupported-locally`, and `driver-conformance-parity` (both backends).
6. **Definition of done** (= Scenarios executed):
   - `just ci` exits 0 (pure-Go): build, lint (no-`any`), test, mod verify.
   - `RunContract` passes against **both** the memory and file backends.
   - `SignedURL` returns `fault.Unavailable` on memory/file; `Exists` returns `(false,nil)` for absent keys;
     `Get`/`Delete` absent → `fault.NotFound`.
   - Only `gocloud.dev/blob` added (Apache-2.0); `go.mod`/`go.sum` tidy; **no cgo, no build tag**.

## Review checklist

- [ ] `internal/blob/blob.go` defines `Bucket` (+ `Attributes`/`SignOptions`) and imports **no** driver lib.
- [ ] `internal/blob/gocloud` is one driver file; spans `mem://`/`file://`/`s3://` by URL; `memblob` is the
      in-memory driver (no hand-written memory bucket).
- [ ] gocloud errors mapped to `api/fault`: `NotFound`, `Unimplemented→Unavailable`, else `Internal`;
      `Exists` never errors on a missing key.
- [ ] `RunContract` passes against **memory and file**; `list-by-prefix` sorted; `SignedURL` unsupported
      locally returns `fault.Unavailable`.
- [ ] Errors are `api/fault`; ctx-first; no `any` in port sigs; no globals; `slog` only.
- [ ] Only `gocloud.dev/blob` (Apache-2.0) added; `go.mod`/`go.sum` tidy; **no cgo**.
- [ ] Every Scenario has a named, passing test; no identity/path leak; the port stays driver-dep-free.

## Consequences

- (+) The data plane gets a **pure-Go** bytes substrate spanning **memory/file/S3** from one mature library,
  cgo-free — the blueprint's storage layer, reused not reinvented.
- (+) `memblob` is the in-memory driver, so `funcd.InMemory()` and unit tests stay **cgo-free and fast** —
  no second memory implementation to keep in sync (contrast the store's deliberate pure-Go engine).
- (+) The `blob.Bucket` port keeps go-cloud swappable (a future minio/native driver is one new file) and
  the secrets/config S3-backed drivers reuse it instead of each embedding an S3 client.
- (−) **`SignedURL` is backend-dependent** — unsupported on memory/file in V1; the port surfaces a clear
  `fault.Unavailable`, and real presign is an S3 (integration-lane) capability.
- (−) **S3 round-trip is unit-untested** (no endpoint in unit CI) — covered by the P-S integration lane;
  the memory+file parity is the unit guarantee.
- (risk) go-cloud's dependency tree is broad (binary-size); acceptable for a platform binary, and only the
  used backends link in.

## Open questions

| Question | Where it gets answered |
|---|---|
| A typed `Key`/`BucketName` vs opaque `string` keys | revisit if the blob *service* (P-O) needs typed addressing; the port stays opaque for V1 |
| `fileblob` URL signer (local presign) + the S3 integration lane | the blob service ADR (P-O/F23) + testing ADR (P-S/F20) |
| Streaming (`io.Reader`/`io.Writer`) vs `[]byte` for large objects | a follow-up if a service stores large blobs (gocloud exposes readers/writers) |
| Bucket lifecycle/retention/multipart | a follow-up, per service need |

## References

- [gocloud.dev/blob](https://pkg.go.dev/gocloud.dev/blob) (Apache-2.0) — the **pure-Go** object-store
  abstraction; `memblob`/`fileblob`/`s3blob`; `blob.OpenBucket`, `gcerrors.Code`, `SignedURL`.
- [ADR-0006](0006-store-database-layer-port.md) — the sibling **records** substrate (`store`); this is the
  **bytes** substrate (`blob`). Distinct names by design.
- [ADR-0002](0002-source-code-conventions-and-patterns.md), [blueprint.md](../../blueprint.md) — "Storage
  layer", "Substrate layers", the `internal/blob` layout.
- Substrate in-memory rule (applied here): reuse the library's pure-Go memory mode (`memblob`) as the
  in-memory driver rather than hand-writing one — the store is the exception (its memory mode is cgo).
