# ADR-0039: Pin the Node runtime to node 22 (refines ADR-0032 + ADR-0037)

> **Superseded in part by [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)** — the node
> curated-image **base** only (`node:22-bookworm-slim` → `distroless/nodejs22`). The node **22 version** pin this ADR
> decided is **kept** (distroless/nodejs22 is still node 22).

- **Status**: Implemented
- **Date**: 2026-06-16 (**Accepted 2026-06-16** — judge: sound + complete, advanced as-is; folded the step-5 polish naming every test file. **Implemented 2026-06-16** — review gate: pass, DoD 4/4, all green.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, nodejs, curated-image, build
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime — the curated nodejs runtime)
- **Relates to / refines**: [ADR-0032](0032-curated-runtime-images-container-execution.md) — bumps the curated image base
  `node:20`→`node:22` and the runtime label `nodejs20`→`nodejs22`; [ADR-0037](0037-typescript-hono-runtime-shim.md) — bumps
  the shim's esbuild `--target=node20`→`node22`. Both are **additive version refinements**, not behavior changes — the
  contracts and APIs are unchanged.

## Context & Need

The curated nodejs runtime (ADR-0032) and the shim build (ADR-0037) were pinned to **node 20**. Node 20 leaves Active
LTS / enters Maintenance; **node 22** is the current Active LTS. Standardizing on 22 keeps the platform on a supported,
performance-improved base before it spreads further (the python shim + benchmark work build on this). This is a version
pin, not a redesign — no contract, API, or shim-behavior change.

## Scenarios

- **scenario: shim-targets-node22** — *when* the shim is rebuilt, *then* `esbuild --target=node22` produces `shim.mjs`
  and every existing shim/Go scenario test still passes unchanged (the runtime contract is version-independent).
- **scenario: curated-image-node22** — *when* the curated image is inspected, *then* its Dockerfile is `FROM node:22-…`
  and `ImageFor("nodejs22")` → `funcd/runtime-nodejs22:latest`.
- **scenario: runtime-label-nodejs22** — *given* a Function with `spec.runtime: nodejs22`, *when* it reconciles, *then*
  it deploys exactly as `nodejs20` did (the label is just the curated-runtime id).

## Scope

**In:** the shim esbuild target (`node22`); the curated image base (`node:22-bookworm-slim`) + dir/tag/label rename
`nodejs20`→`nodejs22`; the example + demo + tests updated to `nodejs22`/`@types/node@22`/`--target=node22`.

**Out:** any runtime-shim contract change; an *unversioned* `nodejs` label (a separate scheme decision — see Open
questions); python runtime (its own ADR); upgrading `@types/node` past the major that matches the shipped node.

## Constraints & Decision drivers

- **Supported LTS** — node 22 is current Active LTS; node 20 is aging out.
- **No contract churn** — the shim's HTTP contract + the function shape are version-independent; only the base + label move.
- **One reference version** — the shim target, the curated image, and the example must agree (they drifted: shim `node20`,
  example `node24`). Converge them on **22**.

## Alternatives considered

- **Stay on node 20** — rejected: aging out of Active LTS; the user mandated 22.
- **Jump to node 24** — node 24 is newer but the user chose 22 (the conservative Active-LTS pick); the example had drifted
  to 24 and is pulled **back** to 22 for one reference version.
- **Unversioned `nodejs` label pinned to a base major** — cleaner for users (no manifest churn on future bumps), but a
  *scheme* change beyond this version pin. Deferred to an open question, not bundled here.

## Decision

Pin the Node runtime to **node 22** everywhere it is referenced:
1. **Shim** — `esbuild --target=node22` (ADR-0037 build); rebuild the committed `shim.mjs`. `@types/node` stays `^22`.
2. **Curated image** — `images/runtime/nodejs22/Dockerfile` is `FROM node:22-bookworm-slim` (copies `shim/nodejs/shim.mjs`,
   same ENTRYPOINT); `just build-runtime-images` tags `funcd/runtime-nodejs22:latest`.
3. **Runtime label** — `fn.Spec.Runtime = "nodejs22"`; `ImageFor("nodejs22") → funcd/runtime-nodejs22:latest` (the
   `<prefix><runtime>:latest` convention, ADR-0032/0036 — derives automatically). All tests/example/demo updated.

## Temporary workarounds

None.

## Contracts

No interface or API change. The touched surfaces:
```
shim/nodejs/package.json build      : --target=node20 → --target=node22
images/runtime/nodejs22/Dockerfile  : FROM node:22-bookworm-slim   (dir renamed from nodejs20/)
justfile build-runtime-images       : -f images/runtime/nodejs22/… -t funcd/runtime-nodejs22:latest
fn.Spec.Runtime                     : "nodejs20" → "nodejs22"  (curated-runtime id; ImageFor maps it)
examples/js/hello-world/package.json: --target=node24 → node22 ; @types/node ^24 → ^22
docs/demo/function.yaml             : runtime: nodejs22
```

## Implementation plan

1. **Rename** `images/runtime/nodejs20/` → `nodejs22/`; Dockerfile `FROM node:20-bookworm-slim` → `node:22-bookworm-slim`.
2. **`justfile`** `build-runtime-images` → the `nodejs22` path + `funcd/runtime-nodejs22:latest` tag.
3. **`shim/nodejs/package.json`** build → `--target=node22`; **rebuild** `shim.mjs` (`npm run build`).
4. **`examples/js/hello-world/package.json`** → `--target=node22`, `@types/node@^22`; rebuild `handler.mjs`.
5. **Tests/config** (grep-driven across all `*_test.go`) — replace `"nodejs20"`→`"nodejs22"` in `cmd/funcd/main_test.go`,
   `tests/e2e/{controlplane,journey}_test.go`, `internal/function/{digest,function,shim}_test.go`,
   `internal/artifact/seam_test.go`, `internal/dataplane/dataplane_test.go`, `pkg/funcd/dataplane_e2e_test.go`; and in
   `internal/function/shim_test.go` the `funcd/runtime-nodejs20`→`nodejs22`, `FROM node:20`→`node:22`, and Dockerfile path.
   Also `docs/demo/function.yaml`. (Frozen ADR text is left as-is — history; the `api/.../function.go` doc-comment example
   is updated for accuracy.)
6. **Verify**: shim `npm test`; all five Go launch paths + the curated-image-builds test green; `go build ./...`; example
   typecheck/test/build; identity clean.
7. **Definition of done**: every `nodejs20`/`node:20`/`target=node20` reference (outside frozen ADRs) reads `22`; all tests green.

## Review checklist

- [ ] No remaining `nodejs20`/`node:20`/`--target=node20`/`--target=node24` outside frozen ADR/review files.
- [ ] `internal/function/shim_test.go` asserts `funcd/runtime-nodejs22:latest` + `FROM node:22` + the `nodejs22` Dockerfile path; passes.
- [ ] Shim + all five Go launch paths + example green; `shim.mjs`/`handler.mjs` rebuilt.
- [ ] No contract/API change; identity clean.

## Consequences

- (+) Platform on current Active-LTS node; one reference version (shim, image, example all on 22 — the drift to 24 is fixed).
- (−) `spec.runtime: nodejs20` manifests must become `nodejs22` (no real users yet; design phase). Future major bumps
  repeat this churn until/unless the unversioned-label scheme is adopted.

## Open questions

- **Unversioned `nodejs` label** (pinned to a base major, version as an image detail) to end manifest churn on future
  bumps — a labeling-scheme ADR, deferred.

## References

- Node.js release schedule (node 22 = Active LTS; node 20 → Maintenance). · [ADR-0032](0032-curated-runtime-images-container-execution.md) · [ADR-0037](0037-typescript-hono-runtime-shim.md).
