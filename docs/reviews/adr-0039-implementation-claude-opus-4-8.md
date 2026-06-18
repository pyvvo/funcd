# Review — ADR-0039 (Pin the Node runtime to node 22) · implementation

- **ADR**: [0039](../adr/0039-pin-node-runtime-to-node22.md) — refines ADR-0032 + ADR-0037 (version pin, no contract change)
- **Realizes**: FEAT-0000/F12 (multi-ADR row, already `implemented` — not regressed)
- **Producing model**: claude-opus-4-8
- **Phase**: implementation
- **Verdict**: **pass** — DoD met, all green, no Blockers/Majors. One Minor (stale Dockerfile comment).

## What was verified (evidence)

| Check | Command | Result |
|---|---|---|
| Completeness (core) | `grep -rIn ... -e nodejs20 -e node:20 -e target=node20 -e target=node24 -e '@types/node":"^24'` (excl. node_modules/.git/adr/reviews/package-lock) | **empty** — no live stale refs |
| Image dir rename | `ls images/runtime/` | only `nodejs22/`; `nodejs20/` gone |
| Curated base | `cat images/runtime/nodejs22/Dockerfile` | `FROM node:22-bookworm-slim` |
| Shim target | `grep -- '--target=node22' shim/nodejs/package.json` | present |
| Shim rebuilt | `ls -la shim/nodejs/shim.mjs` | mtime 09:43 (rebuilt this session) |
| Shim tests | `npm test` (shim/nodejs) | **11 pass / 0 fail** |
| Example | `grep node22/@types/node package.json` | `--target=node22`, `@types/node ^22.0.0` |
| Example green | `npm run typecheck` + `npm test` + `npm run build` | typecheck clean, **1 pass**, build emits `handler.mjs` |
| Go build | `go build ./...` | exit 0 |
| function tests | `go test ./internal/function/ -run 'Shim|Timer|CuratedImage' -count=1` | ok |
| curated-image asserts | `grep node:22 / runtime-nodejs22 / nodejs22/Dockerfile shim_test.go` | asserts `funcd/runtime-nodejs22:latest` (L275), `FROM node:22` (L305), `nodejs22/Dockerfile` path (L302) |
| artifact | `go test ./internal/artifact/ -run SeamNode -count=1` | ok |
| pkg/funcd | `go test ./pkg/funcd/ -run DataPlane -count=1` | ok |
| cmd/funcd | `go test ./cmd/funcd/ -run DaemonExecutes -count=1` | ok |
| e2e | `go test ./tests/e2e/ -count=1` | ok |
| justfile | `grep nodejs22 justfile` | `-f images/runtime/nodejs22/Dockerfile -t funcd/runtime-nodejs22:latest` |
| demo | `grep runtime docs/demo/function.yaml` | `runtime: nodejs22` |
| Contract/API | `git diff api/types/v1alpha1/function.go` | only the doc-comment example `nodejs20`→`nodejs22`; no contract change |
| Identity | `grep -rIn green-0-rabbit / /Users/` (excl. node_modules/.git/package-lock) | only prior `docs/reviews/` prose; **no code leak** |
| Tree vs ADR surface | `git status --porcelain` | shim pkg, renamed Dockerfile, justfile, example, demo, the listed `*_test.go`, api doc-comment — matches ADR Contracts; no unexplained extras |

## Findings

### Minor (model-attributed)
- **Stale Dockerfile comment** — `images/runtime/nodejs22/Dockerfile:1` still reads
  `# Curated Node.js 20 runtime image (ADR-0032, P-V-2).` while `FROM node:22-bookworm-slim`.
  Cosmetic only (no build/test reads it; the ADR's grep patterns target `node:20`/`nodejs20`,
  not the "Node.js 20" prose, so it slipped the completeness sweep). Non-blocking; worth a
  one-line follow-up touch. Attribution: **model**.

## ✅ Verified correct (keep)
- Clean, complete version sweep: the core completeness grep is empty; dir rename, base bump,
  label, tag, justfile, example, demo, and all Go test fixtures converge on **22**.
- The `curated-image-builds` test was correctly tightened to assert all three node22 surfaces
  (`FROM node:22`, the `nodejs22` Dockerfile path, `funcd/runtime-nodejs22:latest`).
- Both bundles rebuilt (`shim.mjs`, `handler.mjs`); no stub paths; every scenario test green.
- No contract/API change, exactly as the ADR claims; F12 row untouched at `implemented`.

## Definition of done (ADR Review checklist)
1. No remaining `nodejs20`/`node:20`/`--target=node20`/`--target=node24` outside frozen files — **pass**
2. `shim_test.go` asserts `funcd/runtime-nodejs22:latest` + `FROM node:22` + nodejs22 path; passes — **pass**
3. Shim + all five Go launch paths + example green; `shim.mjs`/`handler.mjs` rebuilt — **pass**
4. No contract/API change; identity clean — **pass**

DoD: **4 / 4**.

## Recommendation
**pass.** Stamp ADR-0039 `Reviewing → Implemented`. F12 stays `implemented` (ADR-0039 already
linked). The lone Minor (stale Dockerfile comment) is a non-blocking cosmetic follow-up, not a
gate.
