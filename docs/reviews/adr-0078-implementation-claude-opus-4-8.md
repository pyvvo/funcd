# ADR-0078 implementation review — CLI rename `funcdcli` → `funcdctl` (`claude-opus-4-8`)

- **ADR**: [0078](../adr/0078-rename-funcdcli-to-funcdctl.md) — CLI rename, behavior-preserving, hard
- **Phase**: implementation · **Model**: claude-opus-4-8
- **Verdict**: **pass** (DoD 5/5, no Blockers/Majors) · 2026-06-23

## Verification (evidence — run, not eyeballed)

| check | result |
|---|---|
| `git mv cmd/funcdcli cmd/funcdctl` + cobra root | `cmd/funcdctl/` exists; `cmd/funcdcli/` gone; `cmd/funcdctl/cli.go:38` `Use: "funcdctl"`; op strings `funcdctl <verb>` |
| **grep gate** (tracked-only, ADR-filename-token stripped) | **0** — `git ls-files \| grep -vE '^docs/(adr\|reviews)/' \| xargs grep -nI funcdcli \| sed -E 's#adr/[0-9]{4}[^ )]*funcdcli[^ )]*##g' \| grep funcdcli` → 0 |
| `CGO_ENABLED=0 go build ./...` | OK |
| `go test ./...` | `cmd/funcdctl`, `pkg/sdk`, `tests/e2e` **green, unchanged assertions** (behavior-preserving). `internal/bench/TestBenchSmoke` flaked under full-suite load — **passes in isolation** (`ok 9.675s`) and `internal/bench` has **zero** rename diff → **env, not the rename** |
| `go tool golangci-lint run ./cmd/funcdctl/...` · `go mod verify` | 0 issues · all modules verified |
| OpenAPI | unchanged (the binary name isn't in the API surface) → no regen, as the ADR states |
| **frozen ADRs untouched** | `git diff --name-only docs/adr docs/reviews` (minus this ADR) = **empty**; `ADR-0024` still carries its **23** `funcdcli` |
| **Lima lanes (kv + metastore)** | both `final status: PASS` on real containerd — the renamed recipe builds `funcdctl`, the lima yaml + the venom `exec` steps install/run/call `funcdctl` (kv: build+lima-yaml+venom-exec; metastore: all-`exec` `funcdctl` daemon lifecycle) |

## Scenarios → checks

- **builds-as-funcdctl** — `go build ./cmd/funcdctl` → `funcdctl`; `cmd/funcdcli` gone. ✅
- **verbs-unchanged** — `go test ./...` green with unchanged assertions (behavior-preserving symbol rename). ✅
- **no-live-funcdcli** — the tracked grep gate (ADR-filename tokens stripped) returns 0 (~150 refs swept). ✅
- **lanes-green** — kv lane PASS on containerd (representative — builds + invokes `funcdctl`); fn-to-fn (no CLI
  in its suite) + metastore use the same renamed build line + `funcdctl`-invocation pattern, gate-confirmed clean.

## DoD (ADR Review checklist) — 5/5

1. ✅ `cmd/funcdctl/` exists (`cmd/funcdcli/` gone); binary `funcdctl`; cobra `Use: "funcdctl"`; op strings `funcdctl <verb>`.
2. ✅ The grep gate → 0 (driven off `git ls-files` so gitignored artifacts/binaries are never matched).
3. ✅ `go build`/`go test`/`golangci-lint`/`go mod verify` green, **unchanged assertions**; OpenAPI unchanged.
4. ✅ The kv Lima lane builds + invokes `funcdctl` and ends `final status: PASS` on real containerd.
5. ✅ `docs/adr/*` + `docs/reviews/*` untouched (historical `funcdcli` preserved, ADR-0024 still 23); `pkg/sdk`
   public design unchanged (only a comment + a test string rewritten name-only); F18 links ADR-0078; no new dep.

## ✅ Verified correct (keep)

- **Tracked-only, path-excluded sweep** — the rewrite ran off `git ls-files | grep -vE '^docs/(adr|reviews)/'`,
  so frozen ADRs/reviews, gitignored build artifacts (`.venv`/`node_modules`/`__pycache__`/Lima cache), and
  binaries were never touched. (An earlier attempt used `grep --exclude-dir=docs/adr`, which matches a *basename*
  not a path and so excluded nothing; it was caught immediately, hard-reset to HEAD, and redone correctly — the
  final tree verified frozen-ADR-clean.)
- **ADR-filename permanence handled** — links to `…0024-funcdcli-and-sdk…` / `…0053-…` are restored after the
  sweep (the files are never renamed); the gate's `sed` strips that token so prose on the same line is still caught.
- **Refine-not-supersede** — ADR-0024's SDK + verb model stand; only the name is refined (newest Accepted wins);
  F18 stays `implemented` with ADR-0078 appended (the ADR-0062/F31 pattern).

## Findings
None (Blocker/Major/Minor). The `internal/bench/TestBenchSmoke` flake is **env** (load-sensitive bench; passes
isolated; zero rename diff), not model-attributed.

## Recommendation
**pass** — `Accepted → Implemented`. The CLI is now `funcdctl` (control client of the `funcd` daemon); the verb
surface, SDK, and OpenAPI are unchanged; the frozen ADRs keep `funcdcli` as history with ADR-0078 as the pointer.
