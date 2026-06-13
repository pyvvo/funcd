# ADR-0001: Project setup, file structure, and Nix dev environment

- **Status**: Proposed
- **Date**: 2026-06-13
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

## Scope

**In**: module identity, license, top-level directory skeleton, Nix flake + direnv dev
shell, dev-tool pinning strategy, Makefile targets, `.gitignore` / `.editorconfig` /
`.golangci.yml` baselines, minimal CI workflow.

**Out** (each gets its own ADR): OpenAPI/proto codegen toolchain (ADR-0002), any Go
interface or package below the top level, testing strategy, release/packaging, function
build pipeline.

## Constraints & Decision drivers

- **C1 — Blueprint rules**: single Go module; Makefile as the only task runner; layout
  must converge to the blueprint's "Repository structure"; depguard-enforced import
  discipline comes later with real packages.
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
| No Nix (mise/asdf + Makefile bootstrap) | lowest entry barrier | weak reproducibility, drift between machines | rejected |
| All tools from nixpkgs | strongest env pinning | nixpkgs lags releases; competes with `go.mod` as pinning source (violates D2) | rejected |
| **Go `tool` directive for Go-ecosystem tools** | versions live in `go.mod` next to library deps; `go tool <x>` everywhere; matches blueprint | needs Go ≥ 1.24; golangci-lint upstream prefers binary distribution | **chosen** (see workaround) |
| Taskfile | nicer syntax | blueprint already ruled it out (two runners drift) | rejected |

## Decision

1. **Module**: `github.com/green-0-rabbit/funcd`. **License**: Apache-2.0
   (patent grant; matches the entire dependency stack). Copyright "The funcd Authors".
2. **Dev environment**: `flake.nix` exposing a single `devShell` with the base toolchain
   only — `go` (latest stable from pinned nixpkgs; ≥ 1.24 required for the `tool`
   directive), `gopls`, `gnumake`, `git`. `flake.lock` committed. `.envrc` containing
   `use flake` (nix-direnv); direnv auto-loads the shell on `cd`.
3. **Dev tools**: Go-ecosystem tools are pinned in `go.mod` via the `tool` directive and
   invoked as `go tool <name>` (first occupant: `golangci-lint`; codegen tools arrive with
   ADR-0002). Nix never ships a tool that `go.mod` already pins (D2).
4. **Skeleton**: top-level directories only, each holding `.gitkeep` until a feature ADR
   populates it: `api/ cmd/ pkg/ internal/ tests/ configs/ deploy/ scripts/`, plus `docs/`
   (already real: `docs/adr/`, `docs/legacy/`).
5. **Task runner**: `Makefile` with phony targets `help` (default, self-documenting),
   `fmt`, `lint`, `test`, `build`, `tidy`, `ci` (= fmt-check + lint + test + build).
6. **CI bootstrap**: `.github/workflows/ci.yml` — single job on `ubuntu-latest`: install
   Nix (Determinate Systems installer action), then `nix develop --command make ci`. CI
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
├── Makefile
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
| Exposes | `make help/fmt/lint/test/build/tidy/ci` | the only sanctioned entry points; CI calls `make ci` |
| Exposes | `go.mod` module path + `tool` directives | downstream ADRs add tools here, never to the flake |

## Scaffold plan

Executable by an LLM with repo write access; no business logic anywhere.

1. `git init` is done; create the directory skeleton + `.gitkeep` files exactly as in
   *Repository surface*.
2. `go mod init github.com/green-0-rabbit/funcd`; set `go` to the toolchain the flake provides.
3. Write `flake.nix` (single devShell, inputs: `nixpkgs` pinned to the current
   `nixos-unstable`; outputs for `aarch64-darwin`, `x86_64-linux`, `aarch64-linux`;
   shell packages: `go`, `gopls`, `gnumake`, `git`); run `nix flake lock`.
4. Write `.envrc` = `use flake`.
5. `go get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`
   (record the resolved version in the PR description).
6. Write `Makefile` (targets from the Decision; `help` parses `##` comments), `.gitignore`
   (`/bin/`, `.direnv/`, `result`, `coverage.out`, editor cruft), `.editorconfig`,
   `.golangci.yml` (linter set from the Decision).
7. Write `.github/workflows/ci.yml` (trigger: push + PR; nix installer action →
   `nix develop --command make ci`).
8. Add `LICENSE` (Apache-2.0 text), update README *Status* to "scaffolded — ADR-0001".
9. **Definition of done**: fresh clone + `direnv allow` lands in a shell where
   `go version` matches the flake pin; `make ci` exits 0; `git status` clean after
   `make fmt tidy`; CI green; tree matches *Repository surface* exactly.

## Review checklist

- [ ] Tree matches the *Repository surface* — nothing extra, nothing missing, no second
      pinning of any tool in both `flake.nix` and `go.mod` (D2).
- [ ] `flake.lock` committed; shell works on `aarch64-darwin` and `x86_64-linux`.
- [ ] No Go source files besides `go.mod`/`go.sum` (C3 — skeleton only).
- [ ] Makefile targets exist, are `.PHONY`, and `make ci` is the union promised above.
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
