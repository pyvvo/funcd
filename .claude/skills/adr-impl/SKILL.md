---
name: adr-impl
description: Implement an Accepted funcd ADR — ADR-0000 workflow gate "Implement". Turns an ADR's Contracts + Implementation plan + Scenarios into the WORKING implementation (port interfaces + drivers, functional-options facades, api/fault errors, go.mod deps, and the scenario tests written AND passing) — real business logic, ending green on `just ci`. On exit it sets the ADR `Accepted → Reviewing` and the feat row → `reviewing`, then hands off to the review gate. Use whenever the user wants to implement, build, code, realize, or "scaffold" an ADR — "implement ADR-0002", "build the gateway ADR", "code the store port", "start on ADR-0003", "realize the conventions ADR". Reads the ADR, blueprint.md, ADR-0001 and ADR-0002. Hands off to the review gate (adr-impl-review), which stamps it `Implemented` on a pass.
---

# ADR → Implementation

Execute the **Implement** gate of the ADR workflow ([docs/adr/0000-adr-process.md](../../../docs/adr/0000-adr-process.md)):
turn one Accepted ADR into the *working* code that compiles, lints clean, and makes every Scenario
test pass — **real business logic, conforming to the ADR's Contracts**. The review gate then *runs*
the verification and, on a pass, stamps the ADR `Implemented`; your job is to give that gate a
complete, conventional implementation to verify.

You are an **executor of the ADR's own Implementation plan**, not an inventor. The ADR already lists
the files, deps, contracts, and definition of done; the blueprint gives the layout; [ADR-0002](../../../docs/adr/0002-source-code-conventions-and-patterns.md)
gives the code shapes. Two different things, so they don't conflict:
- **Code shape** (how to write a port/driver/facade in Go): copy the matching template from
  `references/` rather than improvising a novel pattern — faithful-and-conventional beats clever.
- **Scope** (which files/types/deps to create, what behavior to build): comes only from the ADR. If
  the ADR is silent or ambiguous about scope, **flag the gap** (Step 2) — never fill it by inventing
  files or behavior the ADR didn't sanction. Copying a code shape is not licence to invent scope.

The ADR usually arrives as the argument (`/adr-impl docs/adr/0002-...md` or `/adr-impl 0002`).
If none is given, ask which ADR.

> **Section name note.** The ADR's build plan is its **Implementation plan** section. The two
> pre-existing ADRs (0001–0002) were written when this section was called *Scaffold plan* — treat
> that older name as the same section.

## Step 0 — Preconditions (check before writing anything)

0. **The ADR must resolve to a real file.** Map the argument to a path (`0002` → the
   `docs/adr/0002-*.md` file). If nothing matches (typo, wrong number), stop and list the ADRs that
   do exist (`ls docs/adr/`) — never implement a guessed-at ADR.
1. **The ADR must be `Accepted` (fresh) or `Reviewing` (rework).** Read its header. `Accepted` means
   a first implementation; `Reviewing` means a prior review returned `changes-requested` and you are
   reworking (Step 1 picks up its report). If it is `Draft`/`Proposed`, stop and say so —
   implementing an unaccepted decision is wasted work; offer to run the judge / get acceptance first.
   If it is already `Implemented`, stop — that decision is frozen; a correction is a new superseding ADR.
2. **The ADR must carry a usable `Implementation plan` and `Contracts`.** (In ADR-0001/0002 the plan
   section is named *Scaffold plan* — same thing.) If either is missing or empty, the ADR is
   malformed against the ADR-0000 template — stop and report it (hand back to the `adr` skill to
   complete those sections); do not reverse-engineer a plan from prose.
3. **Identity rules** (apply throughout, grep before finishing): module is
   `github.com/pyvvo/funcd`; author "The funcd Authors"; never write the local machine
   username or local filesystem paths into any file.
4. **Prerequisite implementations exist.** Read the ADR's *Contracts → Dependencies & I/O* and its
   `Relates to`. If it consumes something not yet on disk (e.g. it uses `api/fault` but that package
   doesn't exist, or there is no Go module yet because ADR-0001 isn't implemented), say which
   prerequisite ADR must be implemented first and stop. ADR-0001 is the repo/module bootstrap;
   ADR-0002 provides `api/fault`, the facade shape, and the lint graph — most later work assumes both.

## Step 1 — Orient

Read, in this order, and do not skip:
1. The target ADR in full — but weight **Contracts** (the interfaces/signatures + behavior to
   build), **Implementation plan** (the file list + deps + definition of done), **Scenarios** (each
   → one passing test), and **Review checklist** (what the next gate will verify — pre-satisfy it).
2. `blueprint.md` — the repository layout the files slot into (where the package lives).
3. `docs/adr/0002-source-code-conventions-and-patterns.md` — **binding** code shapes (functional
   options vs deps-struct, ports/drivers, `api/fault`, typed enums/IDs, ctx-first, no globals,
   slog-only, the depguard import graph, one-file-per-driver, no mocks). Also `docs/adr/0001-*`
   for the module path, `just` recipes, and `.golangci.yml`.
4. `references/conventions.md` (in this skill) — the above distilled to a checklist + the code-shape
   rules every implementation must satisfy.
5. **Any existing review report for this ADR** — check `docs/reviews/` for a prior
   `adr-<NNNN>-*.md` report (and its row in `model-ledger.json`). The review gate writes one when it
   reviews an implementation; if it returned `changes-requested`, that report is your work list. **Read
   it and follow it**: fix every open **`model`**-attributed finding (the report tags each finding's
   attribution), and don't regress anything it marked *Verified correct*. Leave **`adr`**-attributed
   findings alone — those loop back to a superseding ADR, not the code — and note any **`env`** ones
   you can't act on. No prior report → this is a first implementation; proceed normally.

## Step 2 — Plan the file set (write it down before creating files)

From the ADR's Implementation plan, list exactly: every file to create (path + one-line purpose),
every `go.mod` dependency/tool to add, and the **Scenario → test** map (one named test per Scenario,
the test name echoing the `scenario: <name>`). Confirm the list matches the ADR — if the ADR's plan
is thin or ambiguous, that is a gap to flag, not to silently invent around. Present the plan
briefly, then proceed.

## Step 3 — Implement (real behavior)

Create the files using the copy-paste shapes in **`references/templates.md`** — port interface,
one-file driver in its own subpackage, functional-options facade, internal deps-struct constructor,
`api/fault` usage, typed IDs/enums, contract suite, scenario test, and the `go.mod` edit commands —
then fill each body with the **real behavior the ADR's Contracts specify**. The rules below are
**independent constraints — every one must hold at once, and satisfying one never breaks another**,
so there is no order to resolve them in: write each file to pass all of them. `references/conventions.md`
has each in full with rationale; consult it when a rule is unclear rather than guessing.

- **Implement the behavior.** Bodies do the real work the ADR's Contracts describe — no
  `not implemented` placeholders left in the shipped path. Where a method genuinely cannot fail,
  return real values, not stub errors.
- **Tests pass, not skip.** Each Scenario gets a named test that actually exercises the behavior and
  asserts the outcome (no `t.Skip`); the contract suite carries real assertions and every driver's
  test runs it. The test name echoes `scenario: <name>` so traceability stays grep-able.
- **One driver = one file in its own subpackage** (dependency isolation), never a fan-out of files.
- **Honor the import graph**: `api/**` imports nothing from `internal/`/`pkg/`; features never import
  sibling features; `internal/platform/**` is a leaf; no mock frameworks; `log/slog` only.
- **Typed surface**: no `interface{}`/`any`/`map[string]any` in hand-written exported or port
  signatures; typed enums/IDs; `ctx context.Context` first on every blocking call.
- **Deps**: add with `go get` / tools with `go get -tool`; pin and record the resolved versions. If
  the environment is offline/sandboxed and `go get` can't fetch, **do not hand-edit `go.mod` with a
  guessed version** — stop and report it; an implementation that can't fetch its deps isn't done.
- Keep the code lint-clean as you go (`forbidigo` bans `panic`/`fmt.Print*` outside `main`;
  `staticcheck`'s `unused` fails on dead unexported code) — don't leave dead helpers behind.

## Step 4 — Verify green (this is the definition of done)

Run the ADR's definition of done, at minimum:
- `just build` (or `go build ./...`) — compiles.
- `just lint` — clean, including the depguard/forbidigo rules the conventions ADR added. If the ADR
  defines known-bad lint fixtures, confirm they still fail as intended.
- `just test` / `go test ./...` — **passes**, with every Scenario test running (none skipped) and the
  contract suite green against every driver.
- `just ci` exits 0 end-to-end.
- Every Scenario has exactly one named, passing test; none silently dropped or left skipped.

When `just build`/`just lint`/`just test` fail, distinguish the cause:
- **Your implementation has a defect** (wrong import path, mismatched signature, unused import, a
  failing assertion, an unexported helper tripping `unused`): fix it and iterate until green. This is
  normal.
- **It genuinely cannot go green within the ADR's scope** (e.g. the Contracts are internally
  contradictory, or a Scenario can't be satisfied by the sanctioned files): stop and flag it for the
  review gate as an **`adr`**-attributed gap — do not invent scope the ADR didn't sanction to force
  green.

## Step 5 — Handoff (stop here)

- **Advance the ADR `Accepted → Reviewing`.** This is the one forward status edit this gate makes:
  set the ADR header to `Reviewing` (the implementation is done and awaiting the review gate). Leave
  every other part of the ADR untouched — Context, Scenarios, Decision, Contracts stay frozen. If the
  ADR was already `Reviewing` (you were reworking), leave the status as-is.
- **Advance the realizing feature row to status → `reviewing`.** Find it via the ADR's header
  `Realizes: FEAT-NNNN/Fxx` line — open `docs/feat/NNNN-*.md` and the `Fxx` row directly. If the ADR
  has no `Realizes` line, or that file/row doesn't exist, or it matches more than one row, don't
  guess — note it in the handoff and leave the feat doc untouched. (The "active" feat doc, when you
  otherwise need it, is the highest-numbered file in `docs/feat/` whose status is `Active`.)
- **Board card: no move.** A feature ADR's Project #4 card is already `In Progress` (moved at acceptance);
  `Reviewing` maps to the same board state, so this gate touches no card — the `→ Done` move belongs to the
  review gate at `Implemented`. (Pure-infra / process / refactor ADRs have no card.)
- Grep the changed files for the local username/paths to confirm no identity leak.
- Close with a short **implementation report**: files created, deps/tools added (with versions), the
  Scenario→test results (all passing), the green `just ci` result, the ADR → `Reviewing` and feat
  row → `reviewing` moves, and any ADR gaps you hit. If you were acting on a prior review report,
  list which `model` findings you closed. Point to the next gate: the high-capability **review** of
  this implementation against the ADR's *Review checklist* and *Contracts*, which stamps the ADR
  `Implemented` on a pass (or returns it to you with `changes-requested`, leaving it `Reviewing`).

## When the implementation is large or multi-package

Build in the dependency order the ADR implies (port before drivers before facade wiring), running
`just build` / the relevant tests after each package so a break is localized. See the end-to-end
illustration in **`references/worked-example.md`** — a complete minimal implementation (port +
one-file driver + facade option + one passing scenario test) that compiles, lints, and passes, to
copy the *whole shape* from when the fragments in `templates.md` aren't enough on their own.
