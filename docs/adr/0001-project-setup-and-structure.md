# ADR-0001: Project setup, file structure, and Nix dev environment

- **Status**: Implemented
- **Date**: 2026-06-13 (accepted 2026-06-13; pre-acceptance fix: flake shell ships `just`, not `gnumake`; implemented 2026-06-13 — bootstrap built, tree matches the Repository surface and `just ci` exits 0; reconciled directly from `Accepted` as a one-time migration of pre-existing work to the new gate model)
- **Deciders**: green-0-rabbit
- **Tags**: setup, repo, nix, tooling
- **Realizes**: [FEAT-0000/F01](../feat/0000-feat-v1.md)
- **Relates to**: [blueprint.md — Repository structure](../../blueprint.md), ADR-0000

## Context & Need

The repository currently contains only documents (blueprint, ADRs). Before any feature ADR
can be scaffolded we need: a Go module, a directory skeleton matching the blueprint's
target layout, a reproducible development environment (same toolchain on the macOS dev
machine and the Linux homebox/CI), a task runner, lint baseline, and a CI bootstrap. This
ADR decides all of that — and only that.

## Scenarios

- `scenario: fresh-clone-dev-shell` — **Given** a fresh clone on macOS (arm64) or Linux
  with Nix + direnv installed, **when** the developer runs `direnv allow`, **then** they
  land in a shell whose `go version` matches the flake pin — identical on every machine.
- `scenario: local-ci-parity` — **Given** the dev shell, **when** the developer runs
  `just ci`, **then** it passes and exercises the exact toolchain CI runs (CI invokes the
  same recipe through `nix develop --command`), so "green locally, red in CI" cannot be
  caused by version drift.
- `scenario: no-nix-degraded` — **Given** a contributor without Nix but with a recent
  system Go and `just` installed, **when** they run `just ci`, **then** it still works
  via `go tool`-pinned tools — versions best-effort, documented as degraded.

These scenarios are verified directly by the *Definition of done* (this ADR produces no
Go code, so there are no test skeletons; the e2e harness that hosts future scenario
skeletons arrives with FEAT-0000/F20).

## Scope

**In**: module identity, license, top-level directory skeleton, Nix flake + direnv dev
shell, dev-tool pinning strategy, justfile recipes, `.gitignore` / `.editorconfig` /
`.golangci.yml` baselines, minimal CI workflow.

**Out** (each gets its own ADR): OpenAPI/proto codegen toolchain (ADR-0002), any Go
interface or package below the top level, testing strategy, release/packaging, function
build pipeline.

## Constraints & Decision drivers

- **C1 — Blueprint rules**: single Go module; exactly one task runner (`just` — blueprint
  amended 2026-06-13); layout must converge to the blueprint's "Repository structure";
  depguard-enforced import discipline comes later with real packages.
- **C2 — Two platforms**: development on macOS (arm64), execution on Linux (amd64/arm64).
  The dev environment must be identical where it matters (toolchain versions), and must
  not pretend Linux-only things (containerd, CNI) work on macOS.
- **C3 — Gradual scaffolding**: only top-level directories now, kept in git via
  `.gitkeep`; deeper structure arrives per feature ADR.
- **C4 — License compatibility**: everything in the stack is Apache-2.0/MIT.
- **D1 — Reproducibility over convenience**, but the repo must remain usable without Nix
  (degraded mode) so contributors and CI fallbacks aren't blocked.
- **D2 — One pinning source per ecosystem** — avoid the same tool being version-pinned in
  two places.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Nix flake + direnv** | hermetic, pinned via `flake.lock`, auto-loading shell, CI runs the identical environment | Nix learning curve | **chosen** |
| Flake without direnv | same guarantees | manual `nix develop` every session | rejected (free win lost) |
| devenv.sh | nicer DX, declarative services | extra abstraction + dependency on top of Nix; services belong to the testing ADR anyway | rejected for now |
| No Nix (mise/asdf + bootstrap script) | lowest entry barrier | weak reproducibility, drift between machines | rejected |
| All tools from nixpkgs | strongest env pinning | nixpkgs lags releases; competes with `go.mod` as pinning source (violates D2) | rejected |
| **Go `tool` directive for Go-ecosystem tools** | versions live in `go.mod` next to library deps; `go tool <x>` everywhere; matches blueprint | needs Go ≥ 1.24; golangci-lint upstream prefers binary distribution | **chosen** (see workaround) |
| **just (justfile)** | clean recipe syntax, recipe arguments, no `.PHONY` ceremony, good recipe listing (`just --list`) | extra binary — but ships from nixpkgs in the dev shell, so no install burden | **chosen** |
| Makefile | ubiquitous, zero extra deps | clunky for task-running (phony targets, arg passing, tab pitfalls); it's a build tool pressed into task duty | rejected 2026-06-13 (was the original choice) |
| Taskfile | nicer than make | YAML verbosity; one runner only — lost to `just` | rejected |

## Decision

1. **Module**: `github.com/green-0-rabbit/funcd`. **License**: Apache-2.0
   (patent grant; matches the entire dependency stack). Copyright "The funcd Authors".
2. **Dev environment**: `flake.nix` exposing a single `devShell` with the base toolchain
   only — `go` (latest stable from pinned nixpkgs; ≥ 1.24 required for the `tool`
   directive), `gopls`, `just`, `git`. `flake.lock` committed. `.envrc` containing
   `use flake` (nix-direnv); direnv auto-loads the shell on `cd`.
3. **Dev tools**: Go-ecosystem tools are pinned in `go.mod` via the `tool` directive and
   invoked as `go tool <name>` (first occupant: `golangci-lint`; codegen tools arrive with
   ADR-0002). Nix never ships a tool that `go.mod` already pins (D2).
4. **Skeleton**: top-level directories only, each holding `.gitkeep` until a feature ADR
   populates it: `api/ cmd/ pkg/ internal/ tests/ configs/ deploy/ scripts/`, plus `docs/`
   (already real: `docs/adr/`, `docs/legacy/`).
5. **Task runner**: `justfile` with recipes `help` (default = `just --list`), `fmt`,
   `lint`, `test`, `build`, `tidy`, `ci` (= fmt-check + lint + test + build). Recipes
   `e2e` and `integration` are reserved names and arrive with FEAT-0000/F20 (testing
   strategy) — they are not stubbed empty here.
6. **CI bootstrap**: `.github/workflows/ci.yml` — single job on `ubuntu-latest`: install
   Nix (Determinate Systems installer action), then `nix develop --command just ci`. CI
   and laptops execute byte-identical toolchains.
7. **Editor/format baselines**: `.editorconfig` (tabs for Go, LF, final newline),
   `.golangci.yml` starting set: govet, staticcheck, errcheck, ineffassign, misspell;
   depguard added when the first import-discipline rule exists (feature ADRs).
8. **Docs layout**: `blueprint.md` stays at the repo root (the architectural entry point);
   decisions live in `docs/adr/`; superseded working documents in `docs/legacy/`.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| `golangci-lint` via `go tool` despite upstream recommending binary installs | keeps D2 (one pinning source) | if `go tool golangci-lint` misbehaves (build flags, version skew), switch it — and only it — to the nixpkgs package and record the change here |
| No depguard rules yet | nothing to guard in an empty skeleton | first feature ADR that creates two packages with an import rule |
| CI has no Linux-VM integration lane | no code needs containerd yet | testing-strategy ADR |

## Contracts

No Go interfaces — this ADR's "contract" is the repository surface itself.

### Repository surface after scaffold

```text
funcd/
├── api/.gitkeep                  # public contracts (OpenAPI, proto, types) — ADR-0002+
├── cmd/.gitkeep                  # funcd, funcdcli entrypoints — later ADRs
├── pkg/.gitkeep                  # public library facade (pkg/funcd, pkg/sdk)
├── internal/.gitkeep             # private implementation
├── tests/.gitkeep                # e2e / integration / contract suites
├── configs/.gitkeep              # example configs
├── deploy/.gitkeep               # systemd, compose.dev
├── scripts/.gitkeep              # generate.sh, build.sh, … as they become real
├── docs/
│   ├── adr/                      # this process
│   └── legacy/IMPLEMENTATION.md  # raw material from the pre-ADR phase
├── .editorconfig
├── .envrc                        # `use flake`
├── .gitignore
├── .github/workflows/ci.yml
├── .golangci.yml
├── justfile
├── flake.nix
├── flake.lock                    # generated by `nix flake lock`, committed
├── go.mod                        # module github.com/green-0-rabbit/funcd + tool directives
├── go.sum
├── LICENSE                       # Apache-2.0
├── README.md
└── blueprint.md
```

### Dependencies & I/O

| | Item | Notes |
|---|---|---|
| Consumes (dev machine) | Nix ≥ 2.18 with flakes enabled; direnv + nix-direnv | documented in README |
| Consumes (CI) | GitHub Actions, Determinate Systems nix-installer action | latest major at scaffold time, pinned by the scaffolder |
| Exposes | `nix develop` shell; `direnv allow` auto-shell | identical toolchain everywhere |
| Exposes | `just fmt/lint/test/build/tidy/ci` (+ `e2e`/`integration` from F20) | the only sanctioned entry points; CI calls `just ci` |
| Exposes | `go.mod` module path + `tool` directives | downstream ADRs add tools here, never to the flake |

## Scaffold plan

Executable by an LLM with repo write access; no business logic anywhere.

1. `git init` is done; create the directory skeleton + `.gitkeep` files exactly as in
   *Repository surface*.
2. `go mod init github.com/green-0-rabbit/funcd`; set `go` to the toolchain the flake provides.
3. Write `flake.nix` (single devShell, inputs: `nixpkgs` pinned to the current
   `nixos-unstable`; outputs for `aarch64-darwin`, `x86_64-linux`, `aarch64-linux`;
   shell packages: `go`, `gopls`, `just`, `git` — matching Decision §2, no `gnumake`);
   run `nix flake lock`.
4. Write `.envrc` = `use flake`.
5. `go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`
   (record the resolved version in the PR description).
6. Write `justfile` (recipes from the Decision; default recipe lists all others),
   `.gitignore` (`/bin/`, `.direnv/`, `result`, `coverage.out`, editor cruft),
   `.editorconfig`, `.golangci.yml` (linter set from the Decision).
7. Write `.github/workflows/ci.yml` (trigger: push + PR; nix installer action →
   `nix develop --command just ci`).
8. Add `LICENSE` (Apache-2.0 text), update README *Status* to "scaffolded — ADR-0001".
9. **Definition of done** (= the *Scenarios* executed): fresh clone + `direnv allow`
   lands in a shell where `go version` matches the flake pin (`fresh-clone-dev-shell`);
   `just ci` exits 0 locally and in CI through the same Nix shell (`local-ci-parity`);
   `just ci` also passes outside Nix with a recent system Go and `just` installed
   (`no-nix-degraded`); `git status` clean after `just fmt tidy`; tree matches
   *Repository surface* exactly.

## Review checklist

- [ ] Tree matches the *Repository surface* — nothing extra, nothing missing, no second
      pinning of any tool in both `flake.nix` and `go.mod` (D2).
- [ ] `flake.lock` committed; shell works on `aarch64-darwin` and `x86_64-linux`.
- [ ] No Go source files besides `go.mod`/`go.sum` (C3 — skeleton only).
- [ ] justfile recipes exist as decided, `just --list` shows them, and `just ci` is the
      union promised above (no `e2e`/`integration` stubs — those belong to F20).
- [ ] CI workflow uses the Nix shell (no `actions/setup-go` bypass).
- [ ] LICENSE is the unmodified Apache-2.0 text; README points to blueprint + ADRs.

## Consequences

- (+) Every future ADR scaffold starts from a reproducible, lint-ready, CI-covered base.
- (+) Toolchain drift between laptop, homebox, and CI is structurally impossible while
  everyone is inside the flake.
- (−) Contributors without Nix get a degraded path (system Go + `go tool`), documented
  but not guaranteed.
- (−) `.gitkeep` placeholders mean the tree shows intent, not content — readers must look
  at the blueprint for what each directory will hold.
- (risk) Go-version skew between nixpkgs and `go.mod`'s `go` directive → mitigated:
  the `go` directive is set to the flake's version at scaffold time and bumped in lockstep.

## Open questions

| Question | Where it gets answered |
|---|---|
| Add formatter orchestration (treefmt / nixfmt for the flake itself)? | revisit when non-Go file types accumulate |
| Pre-commit hooks (lefthook)? | testing-strategy ADR; CI is the gate until then |
| GitHub remote creation + branch protection | manual, before first push |
| Exact Go / golangci-lint versions | recorded by the scaffolder in the scaffold PR |

## References

- Go `tool` directive (Go 1.24 release notes); golangci-lint installation docs.
- nix-direnv; Determinate Systems nix-installer-action.
- [blueprint.md — Repository structure / Go best practices](../../blueprint.md).
