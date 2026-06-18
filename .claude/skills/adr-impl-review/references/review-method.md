# Review method — run it, attribute it, report it

The bar is in `definition-of-done.md`. This file is *how* to measure against it: the verification to
run, how to attribute findings (fair model scoring depends on this), and the verdict format.

## 1. Run the verification — evidence beats opinion

The findings that matter come from *executing*, not reading. Run these and **paste real output**
(exit codes, `file:line`, transcripts). A "looks green" with no captured exit code is not a finding.

| Check | Command / action | What a failure means |
|---|---|---|
| compiles | `just build` / `go build ./...` | Blocker |
| lints | `just lint` (the ADR/ADR-0002 linter set) | Blocker (but check empty-module trap, §3) |
| tests | `just test` / `go test ./...` | green, with every scenario test passing (none skipped) |
| full gate | `just ci` — capture the **exit code** | the single source of "is it done" |
| tree | diff produced tree vs the ADR's *Repository surface* | missing file = Blocker; unexplained extra = Major |
| conventions | grep for `any`/`interface{}` in exported sigs; `panic(` outside main; logging imports ≠ `log/slog`; import-graph violations | ADR-0002 breach = Major/Blocker |
| behavior | real logic present (no `not implemented` stubs shipped); scenarios un-skipped + passing, none weakened/deleted | stub-in-shipped-path or skipped scenario = Blocker |
| tracking | module path; ADR at `Reviewing` with substance unchanged (`git diff` it); feat row at `reviewing`; **silent** no-dev-machine-leak check (absolute path / local username / personal email) — do NOT write it as a report section, never transcribe the value | identity leak / mutated ADR substance = Blocker |

Empty-module trap worth knowing cold: on a module with **zero `.go` files**, `golangci-lint`,
`go vet`, and `go test ./...` exit non-zero ("no packages"); only `go build` tolerates it. So a
"just ci fails" on a pure skeleton is often an **ADR defect** (the DoD is unsatisfiable as written),
not the model's error — see §3.

## 2. Severity tiers

- **🔴 Blocker** — the work does not meet the DoD and cannot ship: doesn't compile/lint, a Scenario
  test is missing or fails, a required file is missing, the ADR's substance was mutated, an identity
  leak, a weakened/deleted scenario assertion.
- **🟡 Major** — meets the letter but breaks a real contract/convention or the blueprint; should fix
  before sign-off.
- **Minor** — nit, polish, non-blocking deviation.
- **✅ Verified correct** — what was checked and *passed*. Not optional: naming what's right tells
  the builder what not to regress, and it's the honest other half of a review.

## 3. Attribution — the crux for fair model scoring

Every finding gets an owner. Only **`model`** findings count against the model's scorecard.

- **`model`** — the implementer's error: missed a file, broke a convention, left a failing stub,
  skipped or weakened a scenario, leaked identity. → counts against the model.
- **`adr`** — the ADR itself is wrong/contradictory: a Definition of done that can't be satisfied as
  written, a Contract that doesn't compile, two checklist items in conflict. The model did the right
  thing and still couldn't pass. → recorded, **not** scored; loops back to a *superseding ADR*.
- **`env`** — tooling/environment: no Nix to run `nix flake lock`, no network for `go get`, no
  `/dev/kvm`. → recorded, **not** scored.

Worked example (real): ADR-0001's checklist demanded both *"no Go source files"* and *"`just ci`
exits 0"*. On an empty module the linter errors — so `just ci` can't be green. The model that
produced the empty skeleton followed the ADR faithfully; that Blocker is **`adr`-attributed**.
Whereas a **missing `flake.lock`** (the implementation plan said to run `nix flake lock` and it wasn't) is
**`model`-attributed** (or `env`, if Nix was unavailable — judge honestly). Mislabeling the first as
`model` would unfairly tank that model's score and corrupt every cross-model comparison.

When you record the scorecard, `--blockers/--majors/--minors` are the *totals*; `--model-attributed`
is how many of those are the model's fault. Keep the split visible in `--notes`.

## 4. Verdict template

Write the verdict to a standalone file — `docs/reviews/adr-<NNNN>-<phase>-<model>.md` — and link it
from the ledger with `scorecard.py … --report <that-path>` (the scorecard renders the link per row).
The `--notes` line is the at-a-glance summary; this doc is the full record. Use this shape:

```markdown
## Verdict: <pass | changes requested | fail> — N blockers, M majors  (ADR-<NNNN> <phase>, model: <name>)

### 🔴 Blocker 1 — <title>  ·  attribution: <model|adr|env>
<evidence: captured command + exit code, or file:line>
<the fix, and who owns it (builder / superseding ADR / env)>

### 🟡 Major / Minor
- <finding · attribution · evidence · fix>

### ✅ Verified correct (keep it)
- <what was checked and passed — tree, conventions, specific scenarios>
  <!-- Do NOT add a Hygiene / identity-grep line here. The no-dev-machine-leak check is silent
       (pass = say nothing); a leak is a Blocker described generically — never transcribe the value. -->

### Definition of Done
<X / Y items hold> (ADR Review-checklist + generic phase DoD). Misses: <which, and attribution>.

### Model scorecard
Recorded: <model> on ADR-<NNNN> (<phase>) → <verdict>, <b/m/m>, <model-attributed> model-attributed,
DoD <passed>/<total>. See docs/reviews/model-scorecard.md.

### Recommendation
<one or two lines: what unblocks sign-off; what loops back to the builder vs a superseding ADR>
```

## 5. Independence

Review and record — don't fix. A reviewer that rewrites the work can't impartially grade it, and the
model scorecard would measure the reviewer, not the model. Hand `model` findings back to the builder
(`adr-impl`); `adr` findings trigger a superseding ADR via `/adr`. Apply
fixes only as a separate step the user explicitly asks for, after the verdict is recorded.
