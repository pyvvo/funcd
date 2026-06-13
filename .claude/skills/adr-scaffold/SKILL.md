---
name: adr-scaffold
description: Scaffold an Accepted funcd ADR — ADR-0000 workflow gate "Scaffold". Turns an ADR's Contracts + Scaffold plan + Scenarios into the bare-minimum COMPILING skeleton (port interfaces, structs, functional-options facades, api/fault errors, go.mod deps, one skipped test per Scenario) with NO business logic, ending green on `just build` + `just lint`. Use whenever the user wants to scaffold, set up, stub out, bootstrap, start coding, build out the packages for, or "implement" an ADR — "scaffold ADR-0002", "set up the gateway ADR", "stub out the store port", "start on ADR-0003", "build the packages for the conventions ADR" — even if they say "implement" (the correct first phase is always the scaffold + review gate, before business logic). Reads the ADR, blueprint.md, ADR-0001 and ADR-0002. Stops at the scaffold; hands off to the review gate. Does NOT write business logic (that is the later implementation gate).
---

# ADR → Scaffold

Execute the **Scaffold** gate of the ADR workflow ([docs/adr/0000-adr-process.md](../../../docs/adr/0000-adr-process.md)):
turn one Accepted ADR into the *bare minimum* code that compiles, lints clean, and carries a
named, skipped test per Scenario — **declarations only, no business logic**. The next gate (a
high-capability review, then implementation) makes the skeletons pass; your job is to give that
gate something real and conventional to review.

You are an **executor of the ADR's own Scaffold plan**, not an inventor. The ADR already lists
the files, deps, and definition of done; the blueprint gives the layout; [ADR-0002](../../../docs/adr/0002-source-code-conventions-and-patterns.md)
gives the code shapes. Two different things, so they don't conflict:
- **Code shape** (how to write a port/driver/facade in Go): copy the matching template from
  `references/` rather than improvising a novel pattern — faithful-and-green beats clever.
- **Scope** (which files/types/deps to create): comes only from the ADR. If the ADR is silent or
  ambiguous about scope, **flag the gap** (Step 2) — never fill it by inventing files the ADR
  didn't sanction. Copying a code shape is not licence to invent scope.

The ADR usually arrives as the argument (`/adr-scaffold docs/adr/0002-...md` or `/adr-scaffold 0002`).
If none is given, ask which ADR.

## Step 0 — Preconditions (check before writing anything)

0. **The ADR must resolve to a real file.** Map the argument to a path (`0002` → the
   `docs/adr/0002-*.md` file). If nothing matches (typo, wrong number), stop and list the ADRs that
   do exist (`ls docs/adr/`) — never scaffold a guessed-at ADR.
1. **The ADR must be `Accepted`.** Read its header. If it is `Draft`/`Proposed`, stop and say so —
   scaffolding an unaccepted decision is wasted work; offer to run the judge / get acceptance first.
2. **The ADR must carry a usable `Scaffold plan` and `Contracts`.** If either is missing or empty,
   the ADR is malformed against the ADR-0000 template — stop and report it (hand back to the `adr`
   skill to complete those sections); do not reverse-engineer a plan from prose.
3. **Identity rules** (apply throughout, grep before finishing): module is
   `github.com/green-0-rabbit/funcd`; author "The funcd Authors"; never write the local machine
   username or local filesystem paths into any file.
4. **Prerequisite scaffolds exist.** Read the ADR's *Contracts → Dependencies & I/O* and its
   `Relates to`. If it consumes something not yet on disk (e.g. it uses `api/fault` but that package
   doesn't exist, or there is no Go module yet because ADR-0001 isn't scaffolded), say which
   prerequisite ADR must be scaffolded first and stop. ADR-0001 is the repo/module bootstrap;
   ADR-0002 provides `api/fault`, the facade shape, and the lint graph — most later scaffolds assume both.

## Step 1 — Orient

Read, in this order, and do not skip:
1. The target ADR in full — but weight **Contracts** (the interfaces/signatures to emit),
   **Scaffold plan** (the file list + deps + definition of done), **Scenarios** (each → one skipped
   test), and **Review checklist** (what the next gate will verify — pre-satisfy it).
2. `blueprint.md` — the repository layout the files slot into (where the package lives).
3. `docs/adr/0002-source-code-conventions-and-patterns.md` — **binding** code shapes (functional
   options vs deps-struct, ports/drivers, `api/fault`, typed enums/IDs, ctx-first, no globals,
   slog-only, the depguard import graph, one-file-per-driver, no mocks). Also `docs/adr/0001-*`
   for the module path, `just` recipes, and `.golangci.yml`.
4. `references/conventions.md` (in this skill) — the above distilled to a checklist + the
   not-implemented body rules that keep stubs lint-clean.
5. **Any existing review report for this ADR** — check `docs/reviews/` for a prior
   `adr-<NNNN>-*.md` report (and its row in `model-ledger.json`). The review gate writes one when it
   reviews a scaffold; if it returned `changes-requested`, that report is your work list. **Read it
   and follow it**: fix every open **`model`**-attributed finding (the report tags each finding's
   attribution), and don't regress anything it marked *Verified correct*. Leave **`adr`**-attributed
   findings alone — those loop back to a superseding ADR, not the scaffold — and note any **`env`**
   ones you can't act on. No prior report → this is a first scaffold; proceed normally.

## Step 2 — Plan the file set (write it down before creating files)

From the ADR's Scaffold plan, list exactly: every file to create (path + one-line purpose), every
`go.mod` dependency/tool to add, and the **Scenario → test-skeleton** map (one named, skipped test
per Scenario, the test name echoing the `scenario: <name>`). Confirm the list matches the ADR — if
the ADR's plan is thin or ambiguous, that is a gap to flag, not to silently invent around. Present
the plan briefly, then proceed.

## Step 3 — Scaffold (declarations only)

Create the files using the copy-paste shapes in **`references/templates.md`** — port interface,
one-file driver in its own subpackage, functional-options facade, internal deps-struct constructor,
`api/fault` usage, typed IDs/enums, contract-suite stub, scenario test skeleton, and the `go.mod`
edit commands. The rules below are **independent constraints — every one must hold at once, and
satisfying one never breaks another**, so there is no order to resolve them in: write each file to
pass all of them. `references/conventions.md` has each in full with rationale; consult it when a
rule is unclear rather than guessing.

- **No business logic.** Bodies are not-implemented stubs that still lint clean: return zero values
  + `errors.New("not implemented: ADR-NNNN")` (never `panic` — forbidigo bans it outside `main`).
  Test bodies are `t.Skip("scaffold ADR-NNNN — scenario: <name>")`.
- **One driver = one file in its own subpackage** (dependency isolation), never a fan-out of files.
- **Honor the import graph**: `api/**` imports nothing from `internal/`/`pkg/`; features never import
  sibling features; `internal/platform/**` is a leaf; no mock frameworks; `log/slog` only.
- **Typed surface**: no `interface{}`/`any`/`map[string]any` in hand-written exported or port
  signatures; typed enums/IDs; `ctx context.Context` first on every blocking call.
- **Deps**: add with `go get` / tools with `go get -tool`; pin and record the resolved versions. If
  the environment is offline/sandboxed and `go get` can't fetch, **do not hand-edit `go.mod` with a
  guessed version** — leave the dep out, note it in the handoff for the implementer to add in a
  networked run, and scaffold the rest.
- Avoid unexported stubs that nothing in the built code references — `staticcheck`'s `unused` check
  (whole-package) will fail the build. Scaffold the exported API + skipped tests; add an unexported
  helper only once something calls it.

## Step 4 — Verify green (this is the definition of done)

Run the ADR's definition of done, at minimum:
- `just build` (or `go build ./...`) — compiles.
- `just lint` — clean, including the depguard/forbidigo rules the conventions ADR added. If the ADR
  defines known-bad lint fixtures, confirm they still fail as intended.
- `just test` / `go test ./...` — passes with the Scenario tests reported as **skipped**.
- Every Scenario has exactly one named, skipped skeleton; none silently dropped.

When `just build`/`just lint` fail, distinguish the cause:
- **Your scaffold has a defect** (wrong import path, mismatched stub signature, unused import, an
  unexported stub tripping `unused`): fix the *declaration* — correct the path/signature, drop the
  dead stub. This is normal; iterate until green. **Never add business logic to make it compile** —
  that is the one move this gate must not make.
- **It genuinely cannot go green without logic** (e.g. an interface can't be satisfied by a stub):
  that is a signal the ADR put implementation work in the scaffold gate — stop and flag it for the
  review gate; do not write the logic to force green.

## Step 5 — Handoff (stop here)

- Update the realizing feature row to status → `scaffolded`. Find it via the ADR's header
  `Realizes: FEAT-NNNN/Fxx` line — open `docs/feat/NNNN-*.md` and the `Fxx` row directly. If the ADR
  has no `Realizes` line, or that file/row doesn't exist, or it matches more than one row, don't
  guess — note it in the handoff and leave the feat doc untouched. (The "active" feat doc, when you
  otherwise need it, is the highest-numbered file in `docs/feat/` whose status is `Active`.)
- Do **not** modify the ADR (Accepted = immutable) and do **not** write business logic.
- Grep the changed files for the local username/paths to confirm no identity leak.
- Close with a short **scaffold report**: files created, deps/tools added (with versions), the
  Scenario→skeleton map, the green `just build`/`just lint`/`test` result, and any ADR gaps you hit.
  If you were acting on a prior review report, list which `model` findings you closed. Point to the
  next gate: the high-capability **review** of this scaffold against the ADR's *Review
  checklist* and *Contracts*, then implementation (making the skipped skeletons pass).

## When the scaffold is large or multi-package

Scaffold in the dependency order the ADR implies (port before drivers before facade wiring), running
`just build` after each package so a break is localized. See the end-to-end illustration in
**`references/worked-example.md`** — a complete minimal scaffold (port + one-file driver + facade
option + one skipped scenario test) that compiles and lints, to copy the *whole shape* from when the
fragments in `templates.md` aren't enough on their own.
