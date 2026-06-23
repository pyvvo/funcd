# ADR-0078: CLI rename — `funcdcli` → `funcdctl`

- **Status**: Implemented (2026-06-23)
- **Date**: 2026-06-23 (judged 2026-06-23 — right decision, ADR-0045 model applied faithfully; folded 2 Blockers
  (the grep gate + the file enumeration were not mutually exhaustive — the gate now **strips the permanent
  ADR-filename token** per line, since `…0024-funcdcli-and-sdk…`/`…0053-…` links are forever, and the enumeration
  now covers all ~150 live refs incl. PROJECT-SUMMARY/install/demo/venom-skill/.gitignore) + 1 Major (the
  `pkg/sdk` design is unchanged; only its comment/test refs rewrite name-only) + minors. Behavior-preserving,
  zero-dep, refines (not supersedes) ADR-0024.)
- **Deciders**: green-0-rabbit
- **Tags**: cli, rename, tooling, ux
- **Realizes**: [FEAT-0000/F18](../feat/0000-feat-v1.md) (the CLI it renames)
- **Relates to**: [ADR-0024](0024-funcdcli-and-sdk.md) (named the CLI `funcdcli` — this **refines that name**;
  the SDK + kubectl-verb model are untouched), [ADR-0042](0042-cobra-cli-framework.md) (the cobra root command
  whose `Use` string changes), [ADR-0045](0045-rename-sandbox-to-worker.md) (the rename-ADR + **grep-gate**
  pattern this follows), [ADR-0053](0053-funcdcli-bench-subcommand.md) (the `bench` verb — its op strings change)

## Context & Need

The CLI is named **`funcdcli`** (ADR-0024). The `…cli` suffix is non-idiomatic; the platform daemon is `funcd`,
and the prevailing convention pairs a daemon with a `…ctl` client (`systemd`→`systemctl`, `kube…`→`kubectl`).
This ADR renames the CLI to **`funcdctl`** so it reads as the control client *of* `funcd`. It is a **hard
rename** (no alias/symlink — the project is in its design phase with no external users to deprecate for) and is
**behavior-preserving**: only the binary/command *name* changes — every verb, flag, output, and the Go SDK stay
identical.

Per ADR-0045 (the `sandbox`→`worker` rename), a rename is a recorded decision, not a silent edit. ADR-0024 (and
the **27 frozen `docs/adr/*`** that mention it) named `funcdcli` and are **Implemented/frozen** — their text is
immutable, so it stays as historical record; this ADR is their forward pointer, and the newest-Accepted-wins rule
carries the new name into the living surfaces. Two ADR **filenames** even embed the old name
(`0024-funcdcli-and-sdk.md`, `0053-funcdcli-bench-subcommand.md`) and are likewise permanent — so links to them
from living docs keep `funcdcli` in the path (the grep gate strips those tokens).

## Scenarios

- **scenario: builds-as-funcdctl** — Given the repo, When the CLI is built (`go build ./cmd/funcdctl`), Then the
  output binary is **`funcdctl`** and there is no `funcdcli` binary or `cmd/funcdcli` package.
- **scenario: verbs-unchanged** — Given `funcdctl`, When a user runs the kubectl-style verbs (`get`, `apply`,
  `push`, `bench`, …) with the same flags, Then behavior, output, and exit codes are **identical** to the former
  `funcdcli` (only the program name in usage/help differs).
- **scenario: no-live-funcdcli** — Given the implemented rename, When you `grep funcdcli` across the **live**
  surfaces (code, `justfile`, `scripts/`, `examples/`, `shim/`, `e2e/`, `blueprint.md`, `docs/feat/`), Then there
  are **zero** matches (the frozen `docs/adr/` + `docs/reviews/` and the permanent ADR-filename links are excepted).
- **scenario: lanes-green** — Given the rename, When the Lima lanes (`just lima-example-{kv,fn-to-fn,metastore}`)
  build + invoke the CLI, Then they build `funcdctl`, call `funcdctl`, and stay `final status: PASS`.

## Scope

**In** — rename `funcdcli` → `funcdctl` on **every live (tracked) surface the grep gate flags**. The authoritative
set (grep-confirmed):
- **Code** — `cmd/funcdcli/` → `cmd/funcdctl/` (dir + `package main` + the output binary); the cobra root
  `Use: "funcdcli"` → `"funcdctl"` + all op/usage strings (ADR-0042/0053); the Go refs
  `internal/artifact/artifact.go`, `internal/loadgen/loadgen.go`, `docs/demo/server/main.go`,
  `tests/e2e/{journey,linux_integration}_test.go`, and the name-only refs in `pkg/funcd/invoke_e2e_test.go`
  + `pkg/sdk/manifest_test.go`; `shim/python/src/funcd_shim/build.py`.
- **Build / lanes** — `justfile` (the three `go build -o …/funcdcli ./cmd/funcdcli` + every Lima-recipe
  invocation); `scripts/lima-{kv,fn-to-fn}.yaml`; `scripts/demo/{setup,journey}.sh`; `e2e/{kv-counter,metastore}.venom.yml`
  + `e2e/README.md`. (The metastore lane's CLI ref lives in `e2e/metastore.venom.yml` — there is no
  `scripts/lima-metastore.yaml`; the `fn-to-fn` lane uses no CLI.)
- **Examples** — `examples/**`: the manifests' comment refs (`*/counter.yaml`, `fn-to-fn/{front,greeter}.yaml`),
  every `README.md`, `build.{ts,py}`, `src/handler.{ts,py}`, `package.json`, `pyproject.toml`.
- **Docs (living)** — `blueprint.md`; `docs/install.md` (user-facing build + verb examples); `docs/demo/*`
  (`README.md`, `demo.yaml`, `function.yaml`); `docs/PROJECT-SUMMARY.md` (the CLI-name **prose**); the CLI-name
  **prose** in `docs/feat/{0000,0001}` + `docs/roadmap/v1-delivery-plan.md`; and the F18 link to this ADR.
- **Misc** — `.gitignore` (the `/funcdcli` ignore rule); `.claude/skills/venom-e2e/SKILL.md` (the playbook's CLI
  examples).

**Exception (kept, the gate strips it)**: **links to the permanent ADR filenames** (`docs/adr/00NN-…funcdcli….md`)
inside living docs (feat / roadmap / PROJECT-SUMMARY) — ADR files are never renamed, so those paths keep
`funcdcli`. `docs/PROJECT-SUMMARY.md` + `docs/roadmap/` are **derived/computed** — prefer regenerating the
CLI-name prose via their skills (`/project-summary`, `/roadmap-planner`) over hand-editing computed sections.

**Out**: **`pkg/sdk`'s public design / package name / generated client** — unchanged (only the CLI binary is
renamed; the old-name hits under `pkg/*` are a doc-comment + a test string, rewritten name-only); the CLI's
**behavior** (verbs, flags, output, exit codes, the OpenAPI surface — **no regen**, the name isn't in the API);
the **frozen ADRs' text** (`docs/adr/*`) + `docs/reviews/*` (historical scorecards); any **alias/back-compat**.

## Constraints & Decision drivers

- **Behavior-preserving** — a pure symbol/name rename; no verb, flag, or output changes (the review confirms
  `go test ./...` stays green with unchanged assertions, per ADR-0045).
- **Frozen ADRs are immutable** — `docs/adr/*` (and `docs/reviews/*`) keep the old name; the gate excludes them.
- **Hard rename** — design-phase, no external users; a deprecated alias is needless surface.
- **Grep-gate discipline (ADR-0045)** — the authoritative DoD is a clean gate over the live surfaces.
- **Sweep tracked files only** — drive the rewrite off `git ls-files` so gitignored build artifacts
  (`.venv`, `node_modules`, `__pycache__`, the Lima cache) and binaries are never touched.
- **Zero new deps.**

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| The name | **`funcdctl`** | **`funcdcli`** (status quo) — non-idiomatic `…cli` suffix. **`fnctl`** — drops the `funcd` family name; the daemon is `funcd`, not `fn`. **`funcd <verb>`** (fold into the daemon binary) — conflates daemon + client, breaks the single-purpose-binary split (ADR-0024). |
| Back-compat | **Hard rename** (no alias) | **A `funcdcli` symlink/shim** — no external users in the design phase, so a deprecated alias is surface to carry for nobody. |
| Recording | **A rename ADR refining ADR-0024** (ADR-0045 pattern) | **Silent find-replace** — impossible/forbidden anyway (the frozen ADRs can't be edited; the divergence must be *recorded*, with this ADR as the pointer). |

## Decision

Rename the CLI `funcdcli` → `funcdctl`, behavior-preserving, hard, across the live surfaces only.

1. **`cmd/funcdcli/` → `cmd/funcdctl/`** (`git mv` the dir; it stays `package main`); the built binary is
   **`funcdctl`** (the dir name == the binary name).
2. **Cobra** (ADR-0042): the root `Use: "funcdcli"` → `"funcdctl"`; every `"funcdcli <verb>"` op/help/usage
   string (incl. ADR-0053's `bench`) → `"funcdctl <verb>"`.
3. **Replace every old-name ref in the live (tracked) surfaces** (Scope-In) with `funcdctl`.
4. **Hard rename** — no `funcdcli` binary, package, or alias remains in the live tree.
5. **Frozen ADRs untouched** — `docs/adr/*` + `docs/reviews/*` keep the old name as historical record, and
   **links to the permanent ADR filenames** (`…0024-funcdcli-and-sdk…`, `…0053-funcdcli-bench-subcommand…`) keep
   it in the path; this ADR **refines** ADR-0024's naming (newest Accepted wins); the rest of ADR-0024 (the SDK,
   the kubectl-verb model) stands.
6. **Behavior unchanged** — verbs, flags, output, exit codes, the OpenAPI surface, and `pkg/sdk` are identical.

## Temporary workarounds

None.

## Contracts

```go
// cmd/funcdctl  (was cmd/funcdcli) — package main, output binary `funcdctl`.
// the cobra root command: newRootCmd → &cobra.Command{ Use: "funcdctl", ... }   // was "funcdcli"
// every fault op string "funcdcli <verb>" → "funcdctl <verb>". The verb set, flags, client wiring,
// and pkg/sdk are UNCHANGED — name-only.
```

**Grep gate (the authoritative DoD, ADR-0045 pattern)** — must return **zero** matches:

```bash
git ls-files | grep -vE '^docs/(adr|reviews)/' | xargs grep -nI "funcdcli" \
  | sed -E 's#adr/[0-9]{4}[^ )]*funcdcli[^ )]*##g' \
  | grep "funcdcli"
# → 0.  Driven off `git ls-files` (tracked only) so gitignored build artifacts + binaries are never matched.
#   Two classes are EXCEPTED: (1) FROZEN text — docs/adr/* + docs/reviews/* (excluded by path);
#   (2) PERMANENT ADR FILENAMES — links to `…0024-funcdcli-and-sdk…`, `…0053-…`, this ADR. ADR files are NEVER
#   renamed, so a live doc's LINK keeps `funcdcli` in the path; the `sed` strips that filename TOKEN from each
#   line (not the whole line), so prose `funcdcli` that SHARES a line with an ADR link — e.g. the F18 feat row —
#   is still caught and must be rewritten. (Pre-impl: ~150 hits.)
```

| consumes | exposes |
|---|---|
| nothing new (a name-only rename) | the `funcdctl` binary + the unchanged kubectl-style verb surface |
| `cmd/funcdctl` (was `cmd/funcdcli`) · the cobra root (ADR-0042) | the renamed Lima-lane build/invoke (`funcdctl`) |

## Implementation plan

**Files / steps**:
1. `git mv cmd/funcdcli cmd/funcdctl`.
2. Sweep the old name → `funcdctl` across the **tracked** live surfaces — the gate is the authoritative file
   list. Practical command (portable, tracked-only, skips frozen + binaries):
   `git ls-files | grep -vE '^docs/(adr|reviews)/' | xargs grep -lI funcdcli | while read f; do perl -i -pe
   's/funcdcli/funcdctl/g' "$f"; done`, then **restore the permanent ADR-filename stems**: `perl -i -pe
   's/funcdctl-and-sdk/funcdcli-and-sdk/g; s/funcdctl-bench-subcommand/funcdcli-bench-subcommand/g;
   s/rename-funcdctl-to-funcdctl/rename-funcdcli-to-funcdctl/g'` over the same set. **Never touch `docs/adr/*` or
   `docs/reviews/*`.**
3. For the **derived docs** (`docs/PROJECT-SUMMARY.md`, `docs/roadmap/v1-delivery-plan.md`) rewrite only the
   CLI-name **prose**; prefer regenerating via `/project-summary` + `/roadmap-planner` over hand-editing their
   computed sections (the slate table / graph / waves are `plan.json`/analyzer output, which contains no CLI name).

**go.mod / deps**: none.

**Test plan** — one check per scenario: **builds-as-funcdctl** — `go build ./cmd/funcdctl` yields `funcdctl`;
`cmd/funcdcli` is gone. **verbs-unchanged** — `go test ./...` green with **unchanged assertions** (the renamed
`cmd/funcdctl/bench_test.go` + the e2e still pass; behavior-preserving). **no-live-funcdcli** — the grep gate
returns 0. **lanes-green** — `just lima-example-{kv,fn-to-fn,metastore}` build + invoke `funcdctl` and end
`final status: PASS` (containerd lanes — run locally, like ADR-0052/0077). Plus `go build ./...`,
`go tool golangci-lint run`, `go mod verify`.

**Definition of done**: `cmd/funcdctl` builds the `funcdctl` binary (no `funcdcli`); the grep gate is clean over
the live surfaces; `go build`/`go test`/lint/`mod verify` green with unchanged test assertions; the Lima lanes
build + invoke `funcdctl` green; frozen ADRs untouched; `pkg/sdk` + the verb surface unchanged; no new dep.

## Review checklist

- [ ] `cmd/funcdctl/` exists (`cmd/funcdcli/` gone); the built binary is `funcdctl`; the cobra root `Use` is
      `"funcdctl"` and the op/usage strings read `funcdctl <verb>`.
- [ ] `git ls-files | grep -vE '^docs/(adr|reviews)/' | xargs grep -nI funcdcli | sed -E 's#adr/[0-9]{4}[^ )]*funcdcli[^ )]*##g' | grep funcdcli` → **0**.
- [ ] `go build ./...` · `go test ./...` (unchanged assertions, behavior-preserving) · `golangci-lint` ·
      `go mod verify` all green; OpenAPI unchanged (the name isn't in the API surface) → no regen.
- [ ] The Lima lanes (kv/fn-to-fn/metastore) build + invoke `funcdctl` and stay `final status: PASS`.
- [ ] `docs/adr/*` + `docs/reviews/*` are **untouched** (historical `funcdcli` preserved); `pkg/sdk` unchanged;
      F18 links ADR-0078; no new dep; no identity/path leak.

## Consequences

**Positive**: the CLI reads as the control client of the `funcd` daemon (`funcd`/`funcdctl`, systemctl-style);
idiomatic `…ctl`; one clear name. **Negative (accepted)**: a hard rename breaks any existing muscle memory /
local script referencing `funcdcli` — acceptable in the design phase (no external users; no published artifacts).
**Neutral**: the verb surface, flags, output, OpenAPI, and `pkg/sdk` are unchanged; the frozen ADRs keep the old
name as history, with this ADR as the forward pointer (newest Accepted wins on naming).

## Open questions

- **Other CLIs?** Only the one client binary exists (`cmd/funcdcli`); `cmd/funcd` (the daemon) is **not** renamed.
  If a second client surfaces later, it follows the `…ctl` convention — not a question for this ADR.

## References

- [ADR-0024](0024-funcdcli-and-sdk.md) (the CLI + SDK — naming refined here) · [ADR-0042](0042-cobra-cli-framework.md)
  (cobra root) · [ADR-0045](0045-rename-sandbox-to-worker.md) (rename-ADR + grep-gate pattern) ·
  [ADR-0053](0053-funcdcli-bench-subcommand.md) (`bench` verb).
- [FEAT-0000](../feat/0000-feat-v1.md) — F18. Convention: `systemd`→`systemctl`, `kube…`→`kubectl`.
