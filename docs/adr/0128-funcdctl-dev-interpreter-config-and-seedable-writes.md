# ADR-0128: funcdctl dev — manifest interpreter config + seedable (relaxed-write) dev blob

- **Status**: Implemented
- **Implemented**: 2026-07-12 — retroactive: the code + its three tests shipped this session and are green (both
  build tags, lint, `go mod verify`); the judge gate verified every Contract against the tree and the
  dev-only/prod-unchanged security claim. Per the ADR-0126 precedent, the judge's code-verification + the shipped
  tests are this ADR's coverage; no separate adr-impl-review gate ran. (Decision 4, `--cport`, was folded in
  *after* the judge — a trivial CLI flag parallel to ADR-0126's `--gport`/`--s3port`, not re-judged.)
- **Date**: 2026-07-12 (judged 2026-07-12 — pass-worthy, no Blockers; the judge verified every Contract against
  the shipped code and confirmed via `git show` that the pre-0128 `compile()` hard-coded the default registry's
  built-ins (the real ADR-0116 bug), and that `WithDevS3RelaxedWrites` is dev-tag-only. Folded the Major
  (this retroactive-doc note) + two Minors (added the no-owner-prefix assertion; corrected the env-precedence
  test claim). **RETROACTIVE documentation ADR** (ADR-0126 precedent): these two dev-only decisions
  were made interactively and shipped as additive dev-tooling *while running the releve-lakehouse example under
  ADR-0125's `funcdctl dev`*, on branch `feat/funcdctl-contract-codegen`. The implementation + its three unit
  tests (`TestScenarioDevRelaxedWritesDropOwnerGate`, `TestResolveInterpreter`, `TestEmbedIncludesEveryRuntimeModule`)
  already landed and are green (both build tags, lint, `go mod verify`); this ADR records the decisions so the
  doc trail is honest. The *Implementation plan* / *Definition of done* below read forward-tense as the template
  wants, but describe code that already exists — the coverage is the shipped tests, and the code predates this
  ADR rather than being implemented from it.)
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, funcdctl, dev, s3, blob, cedar, fidelity-boundary
- **Realizes**: [FEAT-0001/F93](../feat/0001-feat-v1.1.md) (funcdctl dev — manifest interpreter config + seedable dev blob)
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md) (the dev command these extend) · [ADR-0126](0126-funcdctl-dev-developer-experience.md)
  (the dev-UX lineage) · [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (the `funcdctl.yaml` +
  `dev:` block extended here) · [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md)/[ADR-0116](0116-capability-authorization-framework.md)
  (the S3 single-writer built-in + the capability framework the relaxation varies) · [ADR-0127](0127-context-blob-data-plane.md)
  (context.blob — the native path the seeded input feeds) · [ADR-0136](0136-roles-and-role-assignments.md)
  (Role/RolesAssignment — the IAM writer-role grant this amendment adopts in place of the relaxation)

> ## ⚠️ In-place amendment — 2026-07-14 (deliberate rules bypass)
>
> **This `Implemented` ADR was edited in place, which the repo's working agreement forbids.**
> [CLAUDE.md](../../.claude/CLAUDE.md) is explicit: an `Implemented` ADR is *fully frozen* and a correction is
> *always* a new superseding ADR, never an in-place edit. **That rule is knowingly bypassed here, at the
> decider's (green-0-rabbit) explicit direction** — recorded so the deviation is honest, not hidden.
>
> **What changed:** Decision 2 (the dev S3-write relaxation) is replaced. `funcdctl dev` no longer *drops* the
> single-writer forbid — it now **leverages the ADR-0136 IAM writer-role grant**: it auto-provisions a
> write-only blob-writer `Role` + `RolesAssignment` for the dev principal so the **real** single-writer authz runs in dev
> (an unassigned principal is still denied — a fidelity gain, not a bypass). Decisions 1 (interpreter config),
> 3 (registry-built-ins fix), and 4 (`--cport`) are unchanged. The superseded relaxation text below is kept and
> annotated rather than deleted, so the original decision stays legible. Because this is an in-place bypass, the
> normal propagation (a new feat row + roadmap follow-through a superseding ADR would carry) is intentionally
> skipped; the F93 feat row keeps its `implemented` status.

## Context & Need

Running the `examples/python/releve-lakehouse` workflow under `funcdctl dev` (ADR-0125) surfaced two friction
points that block a from-source run, both dev-only:

1. **The Python/Node interpreter is `FUNCD_PYTHON`/`FUNCD_NODE`-env-only.** A project whose handlers need
   third-party deps (pdfplumber, pyarrow, duckdb …) must run them with the project **virtualenv** — but the
   only way to point `funcdctl dev` at that venv was an env var passed on every invocation. `python3` on PATH
   (the fallback) lacks the deps, so the shim import fails and the function never becomes ready. There was no
   way to pin the interpreter in the **committable** `funcdctl.yaml`.

2. **A workflow's input blob cannot be seeded in dev.** The releve pipeline starts from a PDF dropped into the
   `landing` prefix, which has **no in-platform owner** (an external drop zone). funcd's S3 single-writer
   built-in (ADR-0080) *denies a write to any owner-less prefix* (the forbid fires unless `resource has owner
   && principal == resource.owner`), so `aws s3 cp` into `landing` returns AccessDenied. Worse, `funcdctl dev`
   auto-provisions each prefix's owner from **bindings alone** (ADR-0125), which cannot tell a *producer* from
   a *consumer* — so a prefix bound by several functions can get an owner that is not its writer, denying the
   real producer's own write. Neither is reproducible in dev without relaxing the single-writer rule.

Both are **dev-fidelity boundaries** in the spirit of ADR-0125 (no egress isolation, no sandbox): conveniences
that make a local from-source run possible while the **production** posture (vendored-deps runtime; single
writer per prefix) is unchanged.

Fixing #2 also surfaced a **latent correctness bug** in the capability framework (ADR-0116): the PDP compiled
its built-in PolicySet from a package-global `defaultRegistry`, **ignoring** the registry actually assembled at
the composition root — so any registry *variant* (like the dev S3 built-in) had its built-ins silently dropped.
That is fixed here (threaded through `cedar.Deps`) as a prerequisite of #2.

## Scenarios

- **scenario: dev-python-from-manifest** — Given a `funcdctl.yaml` with `dev.python: .venv/bin/python` and no
  `FUNCD_PYTHON` env, When `funcdctl dev` launches the python handler, Then it uses the project venv interpreter
  (resolved relative to the manifest dir) and the handler's deps import.
- **scenario: dev-interpreter-env-overrides** — Given both `FUNCD_PYTHON` set and `dev.python` in the manifest,
  When `funcdctl dev` resolves the interpreter, Then the env var wins (an ad-hoc override still works).
- **scenario: dev-seed-no-owner-prefix** — Given `funcdctl dev` with the relaxation, When a developer `aws s3 cp`s
  a file into a **no-owner** prefix (`landing`), Then the write succeeds (so a workflow's input can be seeded).
- **scenario: dev-producer-writes-inferred-owner** — Given a prefix whose dev-inferred owner is not its actual
  producer, When the producer writes it (via context.blob or the S3 frontend), Then the write succeeds in dev.
- **scenario: prod-single-writer-unchanged** — Given the PROD S3 built-in, When a non-owner (or
  anyone, for a no-owner prefix) writes, Then it is denied — the consistency invariant holds; reads stay
  binding-gated. *(amended 2026-07-14: this same real built-in now runs in dev too — see below.)*
- **scenario: variant-builtins-take-effect** — Given a PDP built from a registry carrying a variant built-in,
  When it authorizes, Then the variant's built-ins are the ones evaluated (not the package default).

> *(amended 2026-07-14)* The two seed scenarios (`dev-seed-no-owner-prefix`, `dev-producer-writes-inferred-owner`)
> now hold **through the real forbid**, because `funcdctl dev` grants the dev principal a write-only blob-writer Role
> (ADR-0136) — the write succeeds since the principal is in the prefix's `writers` set, not because the forbid
> was dropped. A new scenario **dev-unassigned-write-denied** holds: an Identity with **no** grant is denied
> the write locally (the fidelity gain the relaxation could not give).

## Scope

**In**: a `dev.python` + `dev.node` field on the ADR-0122 `Dev` block (interpreter path, relative to the manifest
dir or absolute; precedence env > manifest > PATH; GLOBAL per run, first-function-representative like
`dev.backends`); a **dev-only S3 built-in variant** (`S3CapabilityDevRelaxedWrites`) that drops the single-writer
*write* forbid (reads unchanged), selected by a `funcd` option (`WithDevS3RelaxedWrites`) that `funcdctl dev`
sets; threading the assembled registry's built-ins through `cedar.Deps.Builtins` so a variant takes effect (the
ADR-0116 fix); and a `--cport` flag (→ `WithListenAddr`) for a reproducible control-plane port, the parallel of
ADR-0126's `--gport`/`--s3port`.

**Out**: any change to the PROD S3 model (single writer per prefix is unchanged — the relaxation is dev-only and
never in the thin release client); the catalog **consumer-binding env** injection in dev (`FUNCD_CATALOG_*` for a
`catalogs:` binding — a separate dev gap, ADR-0091, not addressed here); a first-class external-identity owner
(tracked on the board — the prod-safe way to let an external client own a drop prefix); `funcdctl push`/`types`
(they ignore the `dev:` block, unchanged).

## Constraints & Decision drivers

- **Committable, zero-env dev** — the interpreter belongs in the `funcdctl.yaml` a project commits, not an env
  var re-typed per run; the env override stays for ad-hoc cases.
- **Dev-fidelity honesty** — the write relaxation is dev-only, documented as a boundary; reads stay binding-gated
  so a "forgot to bind" bug still surfaces locally; prod is untouched.
- **Reuse the capability framework** — the relaxation is a built-in *variant* of the existing S3 capability
  (ADR-0116), not a new mechanism; the `Deps.Builtins` threading is the minimal correctness fix that makes a
  variant real. Zero new deps.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| Keep `FUNCD_PYTHON` env-only | No schema change | Not committable; re-typed per run; a project can't declare its toolchain. **Rejected.** |
| A separate dev-config file (`.funcddev`) | Keeps the manifest lean | A second config to learn; the `dev:` block already exists for exactly this (backends/config/secrets). **Rejected** — extend `dev:`. |
| `dev.python`/`dev.node` on the `dev:` block ✅ | One committable config, mirrors `dev.backends` | Chosen — precedence env > manifest > PATH keeps the override. |
| Relax writes by giving no-owner prefixes a dev owner | No cedar change | The owner must be a *Function*; an external seed client is an `S3Identity` — types never match, and dev can't infer the true producer anyway. **Rejected.** |
| A dev-only built-in variant dropping the write forbid ✅ | Unblocks seeding AND mis-inferred owners; reads stay gated | Chosen — smallest honest relaxation; prod built-in unchanged. |
| Add the external-identity owner model now | Prod-safe, general | ADR-sized (typed `owner` + an `S3Identity`/`AccessKey` CRD + a PrincipalSource) — **deferred to the board**; the dev relaxation unblocks the demo today. |

## Decision

1. **Manifest interpreter config.** Add `Python` + `Node` to the `funcdctl.yaml` `Dev` block (`pkg/sdk`).
   `funcdctl dev` resolves the interpreter as **env (`FUNCD_PYTHON`/`FUNCD_NODE`) → `dev.python`/`dev.node`
   (relative to the manifest dir, or absolute) → the bare name on PATH**. GLOBAL per run — the first planned
   function's block is representative (like `dev.backends`). `push`/`types` ignore it (dev-only).

2. **Dev S3 writes via an IAM writer-role grant** *(amended 2026-07-14 — see the in-place-amendment banner)*.
   ~~Add `S3CapabilityDevRelaxedWrites()` — a dev-only built-in (`builtin_s3_dev.cedar`) that drops the
   single-writer write forbid, so any authenticated principal may write any prefix locally; `WithDevS3RelaxedWrites()`
   selects it.~~ **Superseded.** `funcdctl dev` keeps the **real** prod S3 built-in (`S3CapabilityWithWriters`,
   single-writer forbid — ADR-0136's generalized `writers` set) and instead **auto-provisions a write-only
   `Role` + a `RolesAssignment` per dev function**: a namespace-scoped grant to the **dev principal** (the dev
   function's derived keypair, ADR-0085) so it joins each prefix's `writers` set — its seeds and producer-writes
   then pass the *real* forbid, no bypass. The Role grants **`s3::write` only** (not the built-in `Blob Data
   Writer`, which also grants read) so **reads stay strictly binding-gated** in dev — the "forgot to bind → read
   Forbidden" fidelity ADR-0125 deliberately keeps. An **unassigned** principal is still denied, so dev exercises
   the actual default-deny + writer-grant path. `WithDevS3RelaxedWrites` and `builtin_s3_dev.cedar` are removed;
   prod is untouched (it always ran the real forbid).

3. **Registry built-ins reach the PDP (ADR-0116 fix).** `cedar.Deps` gains a `Builtins` field; the driver's
   policy cache compiles *that* (falling back to the default registry when empty) instead of the package-global
   `defaultRegistry.Builtins()`. The composition root passes the **assembled** registry's `Builtins()`, so a
   variant capability's built-ins actually take effect. Without this, #2 is inert.

4. **`--cport` — a fixed control-plane port.** `funcdctl dev` gains `--cport` (mapped to `WithListenAddr`), the
   control-plane parallel of ADR-0126's `--gport`/`--s3port`. Without it the control-plane port is ephemeral and
   only in the startup log, so `funcdctl workflow run --server …` / `apply` can't be scripted; with it the control
   URL is reproducible and printed in the banner. Any two fixed ports must differ.

## Temporary workarounds

- ~~**The dev write relaxation** is itself the workaround for the absence of a prod-safe external-prefix-owner.~~
  **Resolved 2026-07-14 by ADR-0136** (Role/RolesAssignment + the generalized `writers` set). The writer-role
  grant is the prod-safe mechanism the exit criterion called for: `funcdctl dev` auto-provisions a
  write-only blob-writer `Role` + `RolesAssignment` for the dev principal, so the **real** single-writer built-in authorizes
  the seed with **no** relaxation. `WithDevS3RelaxedWrites` and `builtin_s3_dev.cedar` are removed — no workaround
  remains.

## Contracts

```go
// pkg/sdk — the Dev block gains two interpreter fields (dev-only; push/types ignore the whole block).
type Dev struct {
    Python   string   `json:"python,omitempty"` // interpreter for python* handlers; rel-to-manifest-dir or abs
    Node     string   `json:"node,omitempty"`   // interpreter for nodejs* handlers
    Backends Backends `json:"backends,omitempty"`
    Config   map[string]map[string]string `json:"config,omitempty"`
    Secrets  map[string]map[string]string `json:"secrets,omitempty"`
}

// internal/auth/cedar — the built-ins wired through Deps (Decision 3, unchanged).
type Deps struct { Entities EntityProvider; Policies PolicySource; Builtins string; Logger *slog.Logger }
// New(): builtins := d.Builtins; if "" { builtins = defaultRegistry.Builtins() }; the policy cache compiles it.
// (amended 2026-07-14) S3CapabilityDevRelaxedWrites + builtin_s3_dev.cedar are REMOVED; dev uses the real
// S3CapabilityWithWriters, and the dev writer is a data-provisioned RolesAssignment (below), not a policy variant.

// cmd/funcdctl — interpreter resolution (Decision 1) + the dev IAM writer grant (amended Decision 2).
func resolveInterpreter(p, baseDir string) string // ""→""; abs→as-is; else filepath.Join(baseDir, p)
// devShimOptions(op, dev sdk.Dev, baseDir string): env > resolveInterpreter(dev.Python/Node, baseDir) > PATH
// bootDev: after auto-provisioning the bindings' Buckets/KVStores, also Create a RolesAssignment granting the
// dev principal WRITE (a write-only dev-blob-writer Role) @ the dev namespace (ADR-0136) — replaces WithDevS3RelaxedWrites().
```

```yaml
# What funcdctl dev auto-provisions (ADR-0136): a WRITE-ONLY Role + a RolesAssignment per function. The dev
# principal becomes a real writer, so the prod single-writer forbid authorizes its seeds/producer-writes. The
# Role grants s3::write ONLY, so reads stay binding-gated. No policy variant; an unassigned principal = denied.
apiVersion: funcd.io/v1alpha1
kind: Role
metadata:
  name: dev-blob-writer
  namespace: default
spec:
  actions:
    - "s3::write"
---
apiVersion: funcd.io/v1alpha1
kind: RolesAssignment
metadata:
  name: dev-blob-writer-<dev-function>
  namespace: default
spec:
  principal:
    kind: Function
    name: <dev-function>
  assignments:
    - roleRef:
        kind: Role
        name: dev-blob-writer
      scope:
        kind: Namespace
```

| consumes | exposes |
|---|---|
| the ADR-0122 `Dev` block; the ADR-0116 capability registry + cedar driver; the ADR-0080 real S3 built-in; the ADR-0136 `Role` + `RolesAssignment` (a write-only dev-blob-writer Role) | `dev.python`/`dev.node`; `cedar.Deps.Builtins`; `funcdctl dev`'s interpreter resolution + its auto-provisioned dev write-only blob-writer `Role` + `RolesAssignment` |

## Implementation plan

> *(amended 2026-07-14)* The relaxation files below (`builtin_s3_dev.cedar`, `S3CapabilityDevRelaxedWrites`,
> `c.s3DevRelaxedWrites`, `WithDevS3RelaxedWrites`, the registry swap) are **removed** by the amendment. The
> replacement work is: `cmd/funcdctl/dev.go` Creates a write-only blob-writer `Role` + `RolesAssignment` (ADR-0136) for the dev
> principal in `bootDev`. `Deps.Builtins` (Decision 3) and the interpreter files (Decision 1) stay as below.

**Files**
- `pkg/sdk/manifest.go` — `Dev.Python` + `Dev.Node`.
- ~~`internal/auth/cedar/builtin_s3_dev.cedar` (new) + `capabilities.go` (`S3CapabilityDevRelaxedWrites`).~~
  `cedar.go` (`Deps.Builtins`, default fallback) + `policies.go` (`policyCache.builtins`, `compile(builtins, …)`).
- ~~`pkg/funcd` — `c.s3DevRelaxedWrites`, `WithDevS3RelaxedWrites`, registry selection~~ + `Deps.Builtins:
  cedarRegistry.Builtins()`.
- `cmd/funcdctl/dev.go` — `resolveInterpreter`, `devShimOptions(op, dev, baseDir)`, `plannedFunc.manifestDir`,
  ~~`funcd.WithDevS3RelaxedWrites()`~~ the auto-provisioned dev write-only blob-writer `Role` + `RolesAssignment` in `bootDev`.

**Test plan** (named tests)
- `TestScenarioDevRelaxedWritesDropOwnerGate` (cedar) — the dev variant allows a non-owner write; prod denies;
  reads stay binding-gated in both (covers dev-seed-no-owner-prefix / dev-producer-writes / prod-single-writer /
  variant-builtins-take-effect at the PDP level).
- `resolveInterpreter` unit test — rel/abs path resolution against the manifest dir (dev-python-from-manifest).
  The env > manifest > PATH **precedence** is the resolution *order* in `devShimOptions` (env is read before the
  manifest value, which is read before the PATH lookup); dev-interpreter-env-overrides rides that ordering (and
  was verified manually running the releve example with and without `FUNCD_PYTHON`), not a separate assertion.
- A shim-embed completeness test — assert every `src/funcd_shim/*.py` is in the `go:embed` set (guards the
  ADR-0127 `blob.py` omission from recurring).

**Definition of done**: the four Go sub-checks green for both tags; the named tests pass; `funcdctl dev` runs the
releve blob steps from source with `dev.python` and no `FUNCD_PYTHON` env, and an `aws s3 cp` seed into `landing`
succeeds; prod S3 authz unchanged.

## Review checklist

- [ ] `dev.python`/`dev.node` resolve env > manifest (rel-to-dir/abs) > PATH; `push`/`types` still ignore `dev:`.
- [ ] ~~`S3CapabilityDevRelaxedWrites` drops ONLY the write forbid.~~ *(amended 2026-07-14)* `funcdctl dev` runs the
      **real** `S3CapabilityWithWriters` (single-writer forbid) and auto-provisions a write-only blob-writer `Role` +
      `RolesAssignment` (ADR-0136) for the dev principal; an unassigned principal is still denied; prod unchanged.
- [ ] `cedar.Deps.Builtins` compiled by the policy cache (default-registry fallback when empty); the composition
      root passes the assembled registry's `Builtins()`.
- [ ] `WithDevS3RelaxedWrites` is dev-only (set by `funcdctl dev`, never the release client); off by default.
- [ ] `--cport` fixes the control-plane port (→ `WithListenAddr`), prints the control URL in the banner, and
      rejects a collision with `--gport`/`--s3port`.
- [ ] Named tests present + passing; no `any` in exported signatures; `api/fault`; ctx-first.

## Consequences

**Positive**: a project pins its toolchain in the committable `funcdctl.yaml` (`funcdctl dev` needs no env); a
developer can seed a workflow's input blob locally; a from-source workflow whose prefix owners are
binding-inferred still runs; and the capability framework now honors a variant's built-ins (a latent bug fixed).
**Negative (accepted)**: ~~the dev S3 write relaxation is a real fidelity gap — dev is **not** where the
single-writer invariant is validated.~~ *(amended 2026-07-14)* Closed — dev now runs the **real** single-writer
forbid with the dev principal grant-listed as a writer (ADR-0136), so the single-writer invariant **is** exercised
in dev; the only accepted cost is one auto-provisioned `RolesAssignment` in the dev boot path.
**Neutral**: `to-gold`'s catalog consumer-binding env in dev remains unwired (separate); the prod path and the
thin release client are unaffected.

## Open questions

- **First-class external `owner`** (typed `owner` + `S3Identity`/`AccessKey` CRD + PrincipalSource) — the
  prod-safe replacement for the dev relaxation; on the Project #4 backlog.
- **Dev catalog consumer-binding env** (`FUNCD_CATALOG_*`) so a `catalogs:`-bound step (to-gold) runs under
  `funcdctl dev` — a separate dev-fidelity increment.

## References

- ADR-0125/0126 (funcdctl dev + dev UX), ADR-0122 (funcdctl.yaml + `dev:` block), ADR-0080/0116 (S3 built-in +
  capability framework), ADR-0127 (context.blob), the Project #4 external-identity/typed-owner card.
