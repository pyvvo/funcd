# Review report — ADR-0001 scaffold

- **ADR**: [ADR-0001 — Project setup, file structure, and Nix dev environment](../adr/0001-project-setup-and-structure.md)
- **Phase**: scaffold (ADR-0000 review gate #5)
- **Model reviewed**: `deepseek-v4-pro`
- **Date**: 2026-06-13
- **Reviewer**: `adr-impl-review` skill
- **Realizes**: [FEAT-0000/F01](../feat/0000-feat-v1.md)
- **Ledger entry**: [model-ledger.json](model-ledger.json) · rollup: [model-scorecard.md](model-scorecard.md)

## Verdict: pass — 0 blockers, 0 majors, 3 minors

ADR-0001 carries a *documented* self-contradiction — "no Go source files" **and** "`just ci`
exits 0" — which the empty-module trap normally makes impossible (`golangci-lint`/`go vet`/
`go test` all error on a zero-package module). The model **pre-empted that adr-Blocker**: it
guards `lint`/`test` behind a `_has-packages` check so they no-op while the module is empty,
leaving `go build` (which tolerates empty) plus tidy/verify to carry the gate. Result —
skeleton-only **and** green CI, both satisfied honestly. Zero model-attributed defects.

## Verification run (evidence)

| Check | Command | Result |
|---|---|---|
| toolchain | `go version` | `go1.26.4 darwin/arm64` — matches `go.mod` `go 1.26.4` |
| recipes | `just --list` | `build ci default fmt help lint test tidy` — matches ADR §5 (no `e2e`/`integration`) |
| compiles | `just build` | exit **0** (`matched no packages` warning — expected) |
| lints | `just lint` | exit **0** (no-op: `_has-packages` empty) |
| tests | `just test` | exit **0** (no-op: `_has-packages` empty) |
| full gate | `just ci` | exit **0** — `tidy → fmt → build → mod verify` all clean |
| tree clean | `git status --porcelain` | empty (clean after `just ci`) |
| lint tool | `go tool golangci-lint version` | `v2.12.2` resolves, exit **0** |
| lint config | `go tool golangci-lint config verify` | exit **0** — `.golangci.yml` is schema-valid |
| identity | `git grep green-0-rabbit / /Users/ / /home/` | **no leaks** |
| ADR immutable | `git log -- docs/adr/0001-*.md` | last touch = acceptance commit `9df36d6`; scaffold `3a2cd07` did not mutate it |

## 🔴 Blocker

None.

## 🟡 Major

None.

## Minor (all `model`-attributed, all non-defect / forward-looking)

- **M1 — `ci` recipe is a superset of the ADR spec** · [justfile](../../justfile#L38-L46). ADR §5
  defines `ci = fmt-check + lint + test + build`; the recipe also runs `go mod tidy` (mutating)
  + `go mod verify` + a go.mod/go.sum tidy-diff gate. Benign hardening — arguably an
  improvement; `go mod tidy -diff` would be the non-mutating form. Not blocking.
- **M2 — the `_has-packages` guard becomes vestigial once real packages exist** ·
  [justfile](../../justfile#L6), [justfile](../../justfile#L24-L30). Clean and self-healing now,
  but it should be removed in the first ADR that adds real packages (ADR-0002/F25 scaffold), and
  carries a tiny latent risk (if `go list ./...` ever fails wholesale, lint/test would silently
  skip). Note the exit path; not a defect today.
- **M3 — golangci-lint v2's default linters (incl. `unused`) are also active** beyond the five
  `enable`d in [.golangci.yml](../../.golangci.yml#L3-L9). Matches ADR intent (a superset of the
  named starting set) — flagged only so it's a conscious fact when ADR-0002 extends the config.

## ✅ Verified correct (keep it)

- **Full gate green**: `just ci` → exit 0; tree stays clean; `just build/lint/test` each exit 0.
  The `no-nix-degraded` scenario is *literally proven* — `go1.26.4` (Homebrew), `just ci` exit 0,
  no Nix.
- **Tree matches the Repository surface exactly**: all 8 top-level dirs with `.gitkeep`
  (`api cmd pkg internal tests configs deploy scripts`), every baseline file present, **zero
  `.go` files** (skeleton-only honored), and **no `e2e`/`integration` stubs** (correctly reserved
  for F20). The only files beyond the ADR's illustrative surface are planning/meta docs
  (`docs/feat`, `docs/roadmap`, `.claude`, `.github/copilot-instructions.md`) — expected.
- **Linter set = ADR-0001 §7 exactly**: govet, staticcheck, errcheck, ineffassign, misspell
  ([.golangci.yml](../../.golangci.yml#L3-L9)); depguard/forbidigo correctly **deferred to
  ADR-0002**. Config is schema-valid and matches the v2 tool.
- **`flake.nix`**: three systems (`aarch64-darwin x86_64-linux aarch64-linux`), packages
  `go gopls just git`, **no `gnumake`**; `flake.lock` committed; `.envrc` = `use flake`.
- **`go.mod`**: module `github.com/green-0-rabbit/funcd`, `go 1.26.4` matches the live toolchain,
  `tool golangci-lint/v2` resolves (v2.12.2). **No double-pinning (D2)** — `just/go/gopls/git` in
  the flake only, `golangci-lint` in `go.mod` only.
- **CI workflow** uses `nix develop --command just ci` (no `actions/setup-go` bypass), triggers
  push+PR on `main`.
- **LICENSE** = unmodified Apache-2.0; copyright "The funcd Authors"; **README** Status =
  "Scaffolded — ADR-0001", points to blueprint + ADRs.
- **Hygiene**: no identity leak; Accepted ADR-0001 unchanged since acceptance; feat row **F01
  advanced to `scaffolded`**.

## Definition of Done

**10 / 11** hold (ADR Review-checklist ×6 + DoD/scenario executable items ×5). The one miss —
`fresh-clone-dev-shell` (Nix dev shell + `direnv allow` parity) — is **`env`-attributed**: no Nix
on this machine (direnv shows `.envrc is blocked`), so the in-Nix path can't be executed here. The
flake *declares* all three systems and `flake.lock` is committed, so the verifiable substance holds.

## Recommendation

**Sign off.** Zero model-attributed defects; the scaffold is a clean, reproducible base. No builder
loop-back needed. The 3 minors are forward-notes for the ADR-0002 scaffold (drop the
`_has-packages` guard once real packages exist; fold the v2 default linters into the conscious set)
— not changes to request now. No superseding ADR needed: the model resolved ADR-0001's latent
contradiction rather than tripping on it.
