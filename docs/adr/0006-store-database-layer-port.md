# ADR-0006: Store / database-layer port (`store.Store`, slatedb via UniFFI + cgo)

- **Status**: Implemented — **engine choice superseded by [ADR-0065](0065-metastore-badger-engine.md)**
- **Superseded in part by**: [ADR-0202](0202-platform-store-snapshot-and-timeline.md) (2026-10-10) — Decision §2's decimal-string resource version, now `<timeline>-<n>`, and the `Store` and `Engine` interfaces, which gain the snapshot methods.
  (slatedb/cgo → pure-Go Badger; this ADR's `store.Store` port, RV/generation/watch/keying semantics, and
  the memory engine are retained unchanged)
- **Date**: 2026-06-14 (revised same day post-judge: "one binary" now requires static-linking
  `slatedb_uniffi` [operator-vs-developer impact + feasibility flagged]; lib build/acquisition pinned for
  deterministic CI; at-rest `Encryptor` seam added for `Secret`; `Engine` batch-isolation clarified;
  slatedb version pinned; **spike-validated 2026-06-14** — build recipe, static-link to a ~24 MB binary,
  and the required `staticlib` patch all confirmed and recorded in §5; **Accepted 2026-06-14**; **Implemented 2026-06-14** — review gate pass, see docs/reviews/adr-0006-implementation-claude-opus-4-8.md)
- **Deciders**: green-0-rabbit
- **Tags**: store, metastore, database-layer, slatedb, uniffi, cgo, watch, generations, port
- **Realizes**: [FEAT-0000/F05](../feat/0000-feat-v1.md), [FEAT-0000/F21](../feat/0000-feat-v1.md) (database layer)
- **Relates to**: [ADR-0003](0003-resource-model-and-api-typing.md) (the typed `Object`/`GVK`/registry the
  store persists; **resolves** its deferred *"when `Generation` bumps, how `ResourceVersion` is minted"*),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (the `internal/store` + `storecontract`
  skeletons this fills; ports/drivers, `api/fault`; **refines** its §2 store-engine example),
  [ADR-0001](0001-project-setup-and-structure.md) (build/`just`/CI — **refined**: this ADR turns on
  `CGO_ENABLED=1` and a native build artifact, the first such in funcd),
  [blueprint.md — Metastore / Database layer / Single-binary / Embed-first](../../blueprint.md)
  (**refined**: the embed-first "pure-Go static single binary" rule gets a recorded exception — see Decision §0)

## Context & Need

Every control-plane component sits on the **metastore**: the API server writes resources to it, the
controller watches it (watch → diff → act → status), and crash-only recovery rebuilds funcd's world on
restart. The blueprint puts it behind the **`store.Store` port** (the *database layer* — the records
substrate, mirroring `blob` for bytes), with `slatedb` as the eventual object-storage-backed engine.
ADR-0003 made resources typed `Object`s and deferred *who mints `ResourceVersion`/when `Generation`
bumps* to this ADR; ADR-0002 scaffolded `internal/store/store.go` + `storecontract/` and deferred their
bodies to "the store ADR". Nothing persists yet — without this port the controller (P-J), API server
(P-L), function lifecycle (P-M), and KV/service pattern (P-N) cannot be built.

**Purpose**: define and implement the `store.Store` port — a **generic, watchable, versioned record
store over ADR-0003 `Object`s** — backed by **slatedb** (the cloud-native LSM on object storage), so the
metastore spans **memory / local-file / S3 from one engine**. Its callers: the API server
persists/serves objects through it; the controller lists-and-watches it; the facade wires the in-memory
path for e2e. Conformance is mechanical: an object round-trips with a minted `resourceVersion`;
optimistic concurrency rejects stale writes; `generation` bumps on spec change only; a watcher receives
ordered change events and replays from a `resourceVersion`; bbolt-style crash recovery holds; and the
**same contract suite passes against the in-memory and slatedb engines** — proving the fake matches the
real one (ADR-0002's `driver-conformance-parity`, now real).

## Scenarios

- `scenario: crud-roundtrip` — **Given** the store, **when** an `Object` is `Create`d then `Get`,
  **then** it round-trips equal, with a server-minted non-empty `resourceVersion` and `generation: 1`.
- `scenario: not-found` — **Given** no such object, **when** `Get`/`Update`/`Delete`, **then**
  `fault.NotFound`.
- `scenario: optimistic-concurrency` — **Given** an object at `resourceVersion` R, **when** two
  `Update`s race (both carrying R), **then** exactly one succeeds and the other returns `fault.Conflict`.
- `scenario: generation-bumps-on-spec-change` — **Given** a stored object, **when** an `Update` changes
  its `spec`, **then** `generation` increments; **when** an `Update` changes only `status`/`metadata`,
  **then** `generation` is unchanged (the K8s semantics ADR-0003 deferred here).
- `scenario: list-by-namespace-and-filter` — **Given** objects of a kind across namespaces and resource
  groups, **when** `List(gvk, {namespace, resourceGroup, tag})`, **then** only matching objects + the
  collection `resourceVersion`.
- `scenario: watch-streams-changes` — **Given** an open `Watch(gvk)`, **when** objects are
  created/updated/deleted, **then** the watcher receives ordered `Added`/`Modified`/`Deleted`; **when**
  `ctx` is cancelled (or `Stop()`), the watch ends promptly and leaks no goroutine.
- `scenario: watch-replays-from-resourceversion` — **Given** changes since `resourceVersion` R, **when**
  `Watch(gvk, {sinceResourceVersion: R})`, **then** it replays the missed changes then streams live; an R
  older than the retained window returns `fault.Unavailable` (caller re-lists).
- `scenario: crash-recovery` — **Given** objects persisted by the **slatedb (file backend)** engine,
  **when** the store is closed and re-opened on the same path (a restart), **then** every object and the
  monotonic `resourceVersion` counter are recovered — no state lived only in memory.
- `scenario: driver-conformance-parity` — **Given** the `storecontract` suite, **when** it runs against
  both the **memory** engine and the **slatedb** engine, **then** both pass the identical assertions
  (resolves ADR-0002's deferred scenario).
- `scenario: secret-encrypted-at-rest` — **Given** a store configured `WithEncryptor` for the `Secret`
  kind, **when** a `Secret` is `Create`d, **then** the bytes the engine stores are ciphertext (not the
  plaintext value) and `Get` round-trips the decrypted object; with no encryptor, the stored bytes are
  the plaintext JSON (the recorded V1 gap).

## Scope

**In**:
- The **`store.Store` port** in `internal/store`: generic CRUD over ADR-0003 `Object`s keyed by
  `(GVK, namespace, name)`, `List` with namespace/resourceGroup/tag filters, in-process **`Watch`**, and
  `resourceVersion`/`generation` semantics.
- **`resourceVersion` minting** (store-wide monotonic counter) + **`generation` bumping** (spec change
  only) — ADR-0003's deferred items.
- A **wrapper + thin `Engine`** design (semantics written once over a minimal KV `Engine`), with two
  engines: **slatedb** (`slatedb.io/slatedb-go`, UniFFI/cgo → the Rust engine; object backends
  memory/file/S3) and a pure-Go **memory** engine (the cgo-free in-memory driver for unit tests + the
  `InMemory()` e2e harness; ADR-0002's two-driver rule + the e2e-on-library strategy).
- The **build changes** to make this compile/test: `CGO_ENABLED=1`, the `slatedb_uniffi` shared library
  on the loader path, the `just` recipes + CI to build/fetch it (Decision §0).
- The **`storecontract` conformance suite** with real assertions, run against both engines.

**Out**:
- **Release packaging of the cgo binary + native lib** (static-link the `.a` vs ship the `.so`,
  cross-compilation, the systemd unit) — the **packaging ADR (P-T/F19)**; this ADR only makes
  `just ci` green locally/CI.
- **The function-facing `kvstore`/KV service** (F14) — P-N, built *on* this layer.
- **Bus integration** (store changes → NATS) — the controller/app (P-J/P-I) bridges the in-process
  `Watch`; the store takes no bus dep.
- **Wiring slatedb's object backend to funcd's `blob` port** — slatedb ships its own object store;
  reusing the storage layer is a future refinement (Open questions).
- **List pagination, schema migration, backup/restore, compaction policy** — follow-ups.

## Constraints & Decision drivers

- **C1 — ADR-0003 is the model**: generic over `Object`/`GVK` via `NewObject`; `ObjectMeta` for
  identity/version/generation; imports `api/types/v1alpha1` + `api/fault`.
- **C2 — ADR-0002 conventions**: fills `internal/store` + `storecontract/`; port-in-its-own-package,
  one-file engines, `api/fault`, ctx-first, no globals, no `any` leak.
- **C3 — DELIBERATE deviation from "pure-Go static single binary / embed-first"**: the decider chose the
  **mature Rust slatedb engine via cgo** over a pure-Go engine, *accepting* that funcd is no longer a
  pure-Go static binary (it links `slatedb_uniffi`). This trade buys a proven object-storage engine
  (memory/file/S3 from one library) at the cost of cgo + a native build/runtime artifact. No cgo ban is
  frozen in ADR-0001 (the `justfile` is plain `go build`), so this *refines* the blueprint's embed-first
  rule rather than contradicting a frozen decision.
- **C4 — single source of semantics**: watch, generations, optimistic concurrency, list-then-watch are
  funcd's, written once over a thin `Engine`, so the engine is swappable (slatedb today; bbolt is the
  documented fallback).
- **D1 — trustworthy fake**: the memory engine must be provably equivalent to slatedb (the
  e2e-on-library + `InMemory()` strategy depends on a **cgo-free** in-memory store) — enforced by the
  shared contract suite.

## Alternatives considered

### §0 — the cgo deviation, recorded
funcd's blueprint asserts an embed-first **pure-Go static single binary** (`build.sh` "static single
binary"; the embed-first rule lists store drivers as embedded Go libraries). Choosing slatedb via
`slatedb.io/slatedb-go` (UniFFI/cgo) **deliberately deviates** — the **decider's explicit choice**
(maturity of the Rust engine over the pure-Go ideal). Who it impacts:
- **Developer (unconditional)**: `CGO_ENABLED=1` + a C **and** Rust toolchain to build `slatedb_uniffi`;
  cross-compilation is hard (a cross C/Rust toolchain + the native lib per target); heavier CI.
- **Operator — the "one binary" promise (funcd's selling point)**: [FEAT-0000](../feat/0000-feat-v1.md)'s
  exit criterion is *"one binary + systemd unit"*. Shipping the `.so` loose breaks it (binary + lib). **So
  release packaging MUST statically link `libslatedb_uniffi.a` into the funcd binary** (P-T/F19) — ideally
  a **musl-static** build, because even with the Rust archive linked a cgo binary keeps a dynamic **libc**
  dependency and is no longer the "runs on `scratch`/any Linux" pure-Go binary. **Whether the UniFFI
  archive can be statically (and musl-) linked is a release-blocking feasibility item** (Open questions).
The embed-first rule knows only "embedded pure-Go lib" vs "supervised child process"; slatedb is a new,
third category — a **cgo-linked native library** — which the blueprint sync adds at acceptance.

**Store engine** (driver: object-storage span vs the pure-Go/static-binary cost vs maturity):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **slatedb via `slatedb.io/slatedb-go` (UniFFI/cgo)** | the **mature, proven Rust engine**; one engine spans **memory/file/S3** (`ObjectStoreResolve`); object-storage-native, bottomless; Apache-2.0 | **cgo** + the `slatedb_uniffi` shared lib → **not a pure-Go static binary**; Rust/C toolchain in CI; harder cross-compile; v0.13 **pre-v1.0 (not stable)**; object-storage latency on S3 | **chosen** (decider's call) |
| slatedb native pure-Go port (`github.com/slatedb/slatedb-go`) | pure-Go (no cgo), Apache-2.0, spans memory/file/S3 | **★0 / early / unproven** for a metastore | rejected (maturity; decider preferred the proven engine even at cgo cost) |
| **bbolt (`go.etcd.io/bbolt`)** | pure-Go, mature, etcd's engine, local-disk-fast | local-only (no S3) | **fallback** if slatedb can't meet the contract at implement (workarounds) |
| sqlite (cgo `mattn`, or pure-Go `modernc`) | mature SQL | cgo (mattn) or heavy pure-Go port; SQL overkill for a KV metastore | rejected |

**Watch mechanism** — in-process channel stream + bounded replay log (no bus dep); identical reasoning to
the prior draft. Bus-via-NATS rejected (forces a P-C→P-E edge, untestable without NATS); polling rejected.

## Decision

### 1. The `store.Store` port — generic over `Object` (engine-independent)
`internal/store` exposes `Store`: `Get`/`List`/`Create`/`Update`/`Delete`/`Watch`/`Close` over ADR-0003
`v1alpha1.Object` keyed by `(GroupVersionKind, NamespaceName, ObjectName)`, decoding stored JSON via
`v1alpha1.NewObject(kind)`. Errors are `api/fault` kinds; every method is ctx-first.

### 2. `resourceVersion` minting + `generation` bumping (ADR-0003's deferral, resolved)
- **`resourceVersion`**: a **store-wide monotonic uint64** (etcd-style revision), incremented on every
  mutation, stamped as its decimal string onto `ObjectMeta.ResourceVersion`; it is the
  optimistic-concurrency token *and* the watch cursor.
- **`generation`**: `1` on `Create`; on `Update`, the store compares the **`spec` sub-tree** (via JSON)
  of new vs stored and increments `generation` **iff the spec changed**; `Update` requires the caller's
  `ResourceVersion` to match (`fault.Conflict` otherwise).

### 3. In-process `Watch` — list-then-watch
`Watch(ctx, gvk, opts)` returns `Watch{ ResultChan() <-chan Event; Stop() }`: no `sinceResourceVersion`
→ current matching set as `Added` then live; with one → replay the change log then live (old R →
`fault.Unavailable`). `ctx`-cancel/`Stop()` closes the channel and releases the subscriber (no leak). The
store keeps a bounded in-memory replay ring; **the bus is not involved**.

### 4. Wrapper + thin `Engine`; slatedb (cgo) + memory (pure-Go)
The store semantics live **once** over a minimal KV **`Engine`** (`View`/`Update` txns over
bucket/key/value). Two engines:
- **`internal/store/slatedb`** — wraps `slatedb.io/slatedb-go` (UniFFI/cgo → the Rust engine). The
  object backend is chosen by URL via `slatedb.ObjectStoreResolve(...)`: `memory:///`, a local-file URL,
  or an `s3://…` URL — **one engine, memory/file/S3**. slatedb has no native watch (LSM), so funcd's
  in-process watch (§3) sits above it; atomic writes use slatedb write-batches, with the wrapper
  serializing writers for the `resourceVersion` compare-and-set.
- **`internal/store/memory`** — a pure-Go map + `sync.RWMutex`; the **cgo-free** engine for unit tests and
  the `InMemory()` e2e harness. (slatedb is funcd's *real/persistent* store engine per the decider; this
  memory engine is the conventional in-memory test double, not a competing persistent engine.)
Each engine is one file in its own subpackage (ADR-0002 §8).

### 5. The cgo build (the concrete cost of §0) — validated by spike (2026-06-14)
- `go.mod` adds `slatedb.io/slatedb-go` **pinned to `v0.13.1`** (Apache-2.0). The **generated Go binding is
  published in the module** — `go get` is enough; funcd needs no `uniffi-bindgen-go` step.
- The native **`slatedb_uniffi` lib is not a downloadable prebuilt — it is cargo-built** from the slatedb
  source at the matching tag. A pinned **`just slatedb-lib`** recipe does this deterministically:
  ```bash
  git clone --depth 1 --branch bindings/go/v0.13.1 https://github.com/slatedb/slatedb .cache/slatedb
  cargo build --release --manifest-path .cache/slatedb/Cargo.toml -p slatedb-uniffi
  # -> .cache/slatedb/target/release/libslatedb_uniffi.{dylib,so,a}   (release .a ~52 MB)
  ```
  `build`/`test` export `CGO_ENABLED=1` + the loader path (`DYLD_LIBRARY_PATH`/`LD_LIBRARY_PATH`) to it; CI
  caches the artifact or re-runs the recipe (Rust+C toolchain). The **memory-engine** unit tests +
  `InMemory()` stay **cgo-free**; only the slatedb tests need the lib.
- **Release = static-link to keep FEAT-0000's "one binary" (§0).** Validated, with one caveat: the upstream
  crate is **`crate-type = ["cdylib"]` only**, so a static archive needs a **one-line patch** funcd's build
  applies (and upstreams — Workarounds):
  ```toml
  # slatedb  bindings/uniffi/Cargo.toml
  crate-type = ["cdylib", "staticlib"]
  ```
  The Go binary then links the `.a` plus the macOS **frameworks** the Rust deps pull (the rest —
  `libSystem`/`libobjc`/`libiconv` — Go's cgo already adds):
  ```bash
  CGO_LDFLAGS="-L<dir-holding-only-the.a> -framework IOKit -framework CoreFoundation -framework Security" \
    go build -ldflags "-s -w" -o funcd .
  ```
  **Result (spike, macOS arm64): one self-contained ~24 MB binary; `otool -L` shows no slatedb dylib; runs
  with no loader path.** A **musl** *fully*-static build (drop the libc dep too) is a Linux-only
  confirmation left to P-T. **Size lever**: slatedb is pulled with `features = ["all"]` (S3+GCS+Azure) —
  trimming to memory/file/S3 shrinks the binary.

### 6. Keying, filtering, crash-recovery
Bucket = `GVK.String()`, key = `<namespace>/<name>` (cluster-scoped kinds use a fixed empty-namespace
segment); `List` scans the bucket and filters by namespace/resourceGroup/tag. The slatedb file backend
persists objects + the revision counter (a `__meta__` key); re-opening recovers them (crash-only). The
memory engine is volatile by construction.

### 7. At-rest encryption seam (for `Secret`)
The blueprint requires secrets *"encrypted in the metastore"*. The store takes an **optional `Encryptor`**
(`WithEncryptor(kinds, enc)`) and applies it to stored **values of the named kinds** (e.g. `Secret`)
before `Put` / after `Get` — transparent at-rest encryption. **P-P/F15 supplies the tink/envelope impl**;
this ADR defines only the seam so P-P plugs in **without retrofitting a frozen store**. With no encryptor
configured (V1 before P-P) those values are **plaintext at rest** — a recorded, owned V1 gap, not a silent
one. The seam keeps the store generic (it encrypts opaque value bytes; it does not parse `Secret`).

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **slatedb is v0.13.x (pre-v1.0, "not stable")** | the engine is young | pin the version; **fall back to the bbolt engine** (one new `Engine` file behind this port) if slatedb can't satisfy the store contract / proves unstable at implement |
| **cgo + a native library** (funcd is no longer a pure-Go static binary) | the decider chose the proven Rust engine over the pure-Go ideal | **P-T/F19 static-links `libslatedb_uniffi.a` to keep FEAT-0000's "one binary"** — **validated** (spike: ~24 MB binary, no slatedb dylib, §5); **musl** fully-static on Linux still to confirm; revisit if a pure-Go slatedb matures |
| **slatedb-uniffi ships `cdylib` only** (no static archive) | upstream's crate-type omits `staticlib` | funcd's `just slatedb-lib` patches `crate-type` to add `staticlib` (one line, §5); **exit**: upstream the patch (PR) so no fork is carried |
| **Object-storage latency on the S3 backend** for a hot metastore | slatedb batches to object storage | single-node V1 uses the **file** backend (local disk); S3 is for durability/multi-node |
| **No `List` pagination**; bounded watch replay | small V1 collections; unbounded history leaks | callers re-list on `fault.Unavailable`; pagination is a follow-up |
| **`generation` compares `spec` via JSON** | generic `Object` exposes no typed `GetSpec()` | revisit if a typed spec-accessor is added |

## Contracts

### The port + engine seam (`internal/store/store.go`)
```go
package store

import (
	"context"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

type Store interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
	List(ctx context.Context, gvk v1.GroupVersionKind, opts ListOptions) (List, error)
	Create(ctx context.Context, obj v1.Object) (v1.Object, error)             // stamps uid, generation=1, resourceVersion
	Update(ctx context.Context, obj v1.Object) (v1.Object, error)             // RV precondition; bumps generation iff spec changed
	Delete(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, rv string) error
	Watch(ctx context.Context, gvk v1.GroupVersionKind, opts WatchOptions) (Watch, error)
	Close() error
}
type ListOptions struct { Namespace v1.NamespaceName; ResourceGroup v1.ResourceGroupName; Tags v1.Tags }
type List struct { Items []v1.Object; ResourceVersion string }
type WatchOptions struct { Namespace v1.NamespaceName; SinceResourceVersion string }
type Watch interface { ResultChan() <-chan Event; Stop() }
type EventType string
const ( Added EventType = "Added"; Modified EventType = "Modified"; Deleted EventType = "Deleted" )
type Event struct { Type EventType; Object v1.Object }

// New wraps an Engine with the store semantics (versioning, generation, watch, concurrency, filtering).
// Options: WithEncryptor applies at-rest encryption to stored values of the named kinds (e.g. Secret).
func New(e Engine, opts ...Option) Store
type Option func(*config) error

// Encryptor is the at-rest encryption seam (Decision §7). P-P/F15 supplies the impl (tink/envelope);
// the store stays generic — it encrypts opaque value bytes. With no encryptor, values are stored as-is.
type Encryptor interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}
func WithEncryptor(kinds []v1.Kind, enc Encryptor) Option

// Engine — the minimal KV the store is built on. memory + slatedb implement it; bbolt is the fallback.
// Update is an ATOMIC WRITE-BATCH (all-or-nothing), NOT a serializable transaction: slatedb gives batch
// atomicity, not in-txn read-your-writes isolation. Cross-writer serialization (the resourceVersion
// compare-and-set) is the store wrapper's job (a write mutex), not the engine's — bbolt happens to be
// serializable, but the store must not rely on that.
type Engine interface {
	View(ctx context.Context, fn func(Txn) error) error
	Update(ctx context.Context, fn func(Txn) error) error
	Close() error
}
type Txn interface {
	Get(bucket, key string) ([]byte, bool, error)
	Put(bucket, key string, val []byte) error
	Delete(bucket, key string) error
	Scan(bucket string, fn func(key string, val []byte) error) error
}
```

### The engines
```go
// internal/store/memory/memory.go — pure-Go, cgo-free; tests + InMemory().
func New() store.Engine

// internal/store/slatedb/slatedb.go — UniFFI/cgo → the Rust engine; backend by URL.
//   Open("memory:///")            → in-memory object store
//   Open("file:///var/lib/funcd") → local file object store (single-node default)
//   Open("s3://bucket/prefix")    → S3 object store (durable)
func Open(objectStoreURL string) (store.Engine, error)   // resolves via slatedb.ObjectStoreResolve
```

### The contract suite (`internal/store/storecontract/contract.go`)
```go
func RunContract(t *testing.T, newStore func(t *testing.T) store.Store)   // memory + slatedb tests both call it
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/types/v1alpha1` (ADR-0003), `api/fault` (ADR-0002) | generic over `Object`/`GVK`; fault kinds |
| Consumes / refines | ADR-0001 build + `.golangci.yml`; blueprint embed-first | turns on `CGO_ENABLED=1` + the native lib |
| Adds (lib) | `slatedb.io/slatedb-go` (**v0.13.1**, pinned) | **Apache-2.0** — UniFFI/cgo binding to the Rust engine |
| Adds (build) | `slatedb_uniffi` shared library + a C toolchain (+ Rust/cargo to build it) | runtime loader-path dependency; **not a Go module** |
| Exposes | `store.Store` + `store.Engine` + memory/slatedb engines + the contract suite | consumed by P-J, P-L, P-M, P-N, P-I |

## Implementation plan

No business logic beyond the store semantics; memory-engine scenarios are cgo-free unit tests; the
slatedb scenarios need the `slatedb_uniffi` lib.

1. **`internal/store/store.go`** — fill the skeleton: `Store`/`Engine`/`Txn`, options/events, and
   `New(Engine) Store` implementing CRUD, `resourceVersion` minting, `generation` (spec-JSON compare),
   optimistic concurrency, in-process watch (subscriber registry + bounded replay), and filtering.
2. **`internal/store/memory/memory.go`** — the pure-Go map engine.
3. **`internal/store/slatedb/slatedb.go`** — the slatedb engine over `slatedb.io/slatedb-go`
   (`ObjectStoreResolve` by URL; write-batches; `__meta__` revision).
4. **`internal/store/storecontract/contract.go`** — real `RunContract` assertions for every scenario.
5. **Build** — `go get slatedb.io/slatedb-go@v0.13.1`; add a **`just slatedb-lib`** recipe that checks out
   `slatedb/slatedb` at a pinned commit, `cargo build --release -p slatedb-uniffi`, caches
   `libslatedb_uniffi.*` under `.cache/`, and exports the loader path for `build`/`test`; set
   `CGO_ENABLED=1`; CI restores the cache or runs the recipe (Rust+C toolchain). `go mod tidy`.
6. **Test plan** (one named test per Scenario, all passing):
   - `internal/store/store_test.go` (memory engine, cgo-free) → `crud-roundtrip`, `not-found`,
     `optimistic-concurrency`, `generation-bumps-on-spec-change`, `list-by-namespace-and-filter`,
     `watch-streams-changes`, `watch-replays-from-resourceversion`, `secret-encrypted-at-rest`
     (a fake `Encryptor` → `Secret` value stored ciphertext, round-trips; no encryptor → plaintext).
   - `internal/store/slatedb/slatedb_test.go` → `crash-recovery` (file backend: write/close/reopen) +
     `RunContract` against slatedb.
   - `internal/store/memory/memory_test.go` → `RunContract` against memory.
   - `driver-conformance-parity` = `RunContract` passing in **both**.
7. **Definition of done** (= Scenarios executed):
   - `just ci` exits 0 (with `CGO_ENABLED=1` + the lib available): build, lint (no-`any`), test, mod verify.
   - `RunContract` passes against **both** engines; `resourceVersion` monotonic + enforced; `generation`
     bumps on spec change only; slatedb file backend survives close/reopen.
   - `Watch` streams ordered events, replays from an RV, ends on ctx-cancel with **no goroutine leak**.
   - The memory engine + `InMemory()` path are **cgo-free**; only the slatedb tests need the lib.
   - Only `slatedb.io/slatedb-go` (Apache-2.0) added; `go.mod`/`go.sum` tidy; the lib build is documented.

## Review checklist

- [ ] `internal/store/store.go` defines `Store` (generic over `Object`/`GVK`) + `Engine`/`Txn`; `New(Engine)`
      holds the shared semantics; no per-engine duplication of watch/generation.
- [ ] Two engines: `internal/store/slatedb` (UniFFI/cgo, memory/file/S3 by URL) + `internal/store/memory`
      (pure-Go, cgo-free); `RunContract` passes against **both**.
- [ ] `resourceVersion` store-minted + monotonic; `Update`/`Delete` honor it (`fault.Conflict`);
      `generation` is 1 on create and bumps **only** on spec change.
- [ ] `Watch` is in-process (no bus import), list-then-watch, ordered; ctx-cancel/`Stop()` → **no leak**;
      old RV → `fault.Unavailable`.
- [ ] slatedb **file backend** crash-recovery (reopen recovers objects + revision).
- [ ] `CGO_ENABLED=1` + the `slatedb_uniffi` lib are wired into `just`/CI via a **pinned** build/acquisition
      recipe (deterministic CI) and **documented**; the memory engine + `InMemory()` remain cgo-free.
- [ ] The store exposes the optional `WithEncryptor` at-rest seam (§7); with none configured, values are
      stored as-is (the recorded V1 plaintext-`Secret` gap until P-P).
- [ ] Errors are `api/fault`; ctx-first; no `any` in port sigs; no globals; `slog` only.
- [ ] Only `slatedb.io/slatedb-go` (Apache-2.0) added (no native-port/sqlite); `go.mod`/`go.sum` tidy.
- [ ] Every Scenario has a named, passing test; no identity/path leak; ADR substance unchanged.

## Consequences

- (+) The control plane gets persistence with an **object-storage-native** engine: memory/file/**S3** from
  one library, in V1 — the blueprint's database-on-object-storage vision, now.
- (+) Resolves ADR-0003's deferred `resourceVersion`/`generation` semantics; the wrapper+`Engine` split
  keeps a pure-Go in-memory store for the e2e harness and makes bbolt a one-file fallback.
- (+) **Spike-validated** (2026-06-14, a standalone bench against the real lib): memory + file backends
  drive funcd's scenarios (CRUD / not-found / delete / write-batch / scan-prefix, **file crash-recovery**),
  and the "one binary" holds — a **~24 MB** release binary with the `.a` statically linked, **no slatedb
  dylib dependency** (needs the §5 `staticlib` patch). The 439 MB debug `.a` was ~90% debuginfo (52 MB at
  release); archive size does **not** translate to binary size.
- (−) **funcd is no longer a pure-Go static single binary** — it links `slatedb_uniffi` via cgo:
  **developers** need a C+Rust toolchain and lose easy cross-compile; **operators** keep "one binary" only
  if P-T static-links the archive (and still lose pure-Go "runs-anywhere" — a cgo binary needs a compatible
  libc). The deliberate, recorded cost of the engine choice (§0).
- (−) slatedb is **pre-v1.0 (not stable)** — a real risk for the metastore; mitigated by the version pin
  + the bbolt fallback + funcd being in design phase.
- (−) The blueprint's embed-first/pure-Go rule + ADR-0002's store example are refined (synced at
  acceptance); slatedb is funcd's first cgo-linked native dependency.
- (risk) S3-backend latency on a hot store — mitigated by using the file backend single-node.

## Open questions

| Question | Where it gets answered |
|---|---|
| Static-link **validated** (spike: 24 MB binary, no slatedb dylib) — remaining: **upstream the `staticlib` crate-type patch** (vs vendor it); confirm **musl** fully-static on Linux; cross-compile; the systemd unit | packaging ADR (P-T/F19) |
| Wiring slatedb's object backend to funcd's `blob` port (one object-store stack) | a follow-up, if `slatedb-go` exposes a pluggable object store |
| `List` pagination + deep watch replay | a follow-up (ties to the API `watch`/pagination follow-up, F02) |
| Whether the controller fans store changes onto the bus, and the subject scheme | controller ADR (P-J/F08) + bus ADR (P-E/F06) |
| The `Encryptor` *implementation* (tink/envelope) for the §7 at-rest seam | secrets ADR (P-P/F15) — the seam is defined here |

## References

- [slatedb.io/slatedb-go](https://pkg.go.dev/slatedb.io/slatedb-go) (Apache-2.0) — the **official** Go
  binding: UniFFI + cgo to the Rust engine; `ObjectStoreResolve` (memory/file/S3); **v0.13.x, pre-v1.0**;
  build needs `CGO_ENABLED=1` + the `slatedb_uniffi` shared library on the loader path.
- [slatedb/slatedb](https://github.com/slatedb/slatedb) (Apache-2.0, Rust) — the engine;
  [design](https://materializedview.io/p/slatedb-an-embedded-storage-engine) (object-storage LSM, latency).
- [Go binding DEVELOPMENT.md](https://github.com/slatedb/slatedb/blob/bindings/go/v0.13.1/bindings/go/DEVELOPMENT.md)
  — the native-lib build steps (cargo + loader path) the §5 recipe follows; **validated by a local spike, 2026-06-14**
  (memory/file backends, crash-recovery, and a ~24 MB static-linked binary).
- [go.etcd.io/bbolt](https://github.com/etcd-io/bbolt) (MIT) — the pure-Go **fallback** engine.
- Kubernetes API conventions — `resourceVersion`, `generation`, list-then-watch.
- [ADR-0003](0003-resource-model-and-api-typing.md), [ADR-0002](0002-source-code-conventions-and-patterns.md),
  [ADR-0001](0001-project-setup-and-structure.md), [blueprint.md](../../blueprint.md) — "Metastore",
  "Database layer", "Single-binary process model", "Embed-first rule".
