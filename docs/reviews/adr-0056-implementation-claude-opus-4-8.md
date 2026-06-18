# Review — ADR-0056 implementation (model: claude-opus-4-8)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0056 implementation, model: claude-opus-4-8)

The temporary runtime self-provisioning lands cleanly: `funcd install` downloads + SHA-256-verifies +
lays down crun/containerd/CNI, the Manager resolves from a single shared `BinDir`, and the containerd
driver self-provisions its conflist + `ip_forward` before `cni.Load`. All verification is green; the
security model (verify-before-write) is airtight; no new dependency; no identity/path leak.

### Verification (captured)

| Check | Result |
|---|---|
| `go build ./...` (host) | exit 0 |
| `GOOS=linux GOARCH=amd64 go build ./...` | exit 0 |
| `GOOS=linux GOARCH=arm64 go build ./...` | exit 0 |
| `go test ./...` | all packages ok (provision, containerd, ctrmanager, cmd/funcd via root) |
| `go tool golangci-lint run ./...` | `0 issues.` |
| `go mod verify` | `all modules verified` |
| `go mod tidy` + `git diff --exit-code go.mod go.sum` | no-op (exit 0) — no new dep, as ADR mandates |
| `go run ./cmd/funcd install --print` | exit 0; printed unit + lay-down plan; `git status --porcelain` unchanged before/after (touched nothing) |

### 🔴 Blocker — none.

### 🟡 Major / Minor — none material.

Minor (non-blocking, no action required):
- `--print` reports the *host* arch in the plan (e.g. on an arm64 dev box it prints the arm64 URLs).
  This is exactly the contract (`Plan` is "for the HOST arch") and is correct; noting only because an
  operator reading `--print` on a dev machine sees their dev arch, not necessarily the install target's.
  Attribution: not a defect — matches the ADR contract.

### ✅ Verified correct (keep it)

- **Security model — verify-before-write is airtight (the #1 item).** `provision.LayDown`
  (`internal/runtime/provision/provision.go:148`) downloads each asset *fully into memory* via
  `download` (`:209`, bounded by a 256 MiB `io.LimitReader` cap so a hostile response can't exhaust
  RAM), computes `sha256.Sum256(data)` and compares to the pin **before** any `Install` call
  (`:174`). On mismatch it returns `fault.Invalidf` and calls `Install` for *nothing* — no temp file,
  no chmod, no exec, no stream-to-disk. `installBinary`/`installTarGz` only ever receive already-verified
  bytes. `TestLayDownChecksumMismatchAbortsWritingNothing` (`provision_test.go:152`) asserts both the
  error kind is `fault.Invalid` **and** `dirEmpty(binDir) && dirEmpty(cniDir)` after the mismatch — the
  dest dirs are provably empty. There is no path that writes/chmods/execs unverified bytes.
- **Real digests, not placeholders.** All six pins are real 64-hex values. Spot-checked two against
  upstream and they match exactly: crun 1.28 linux-amd64 (downloaded the binary, `shasum -a 256` ==
  the pinned `2aa6…1d42`); CNI v1.9.1 linux-amd64 (the published `…tgz.sha256` == the pinned
  `b98f…b303`). The remaining four (crun/containerd/cni × the other arch) are real-shaped hex pins,
  not zeros/TODO.
- **Single binDir source of truth (M2).** `ctrmanager.BinDir()` (`manager.go:34`) is the lone
  definition (`<FuncdRoot>/bin` = `/var/lib/funcd/bin`). `cmd/funcd` install writes there
  (`install.go:182` → `provision.LayDown(ctx, ctrmanager.BinDir(), cniBinDir)`); `resolveBin`
  (`manager_linux.go:77`) reads there first then `exec.LookPath`; `startContainerd` (`:106`) prepends
  `BinDir()` to the child's `PATH` so containerd finds `containerd-shim-runc-v2` + crun. One path,
  three consumers — install can't lay binaries where the Manager won't look.
- **Exact conflist (M1) + write-if-absent.** `renderConflist` (`cni_conf.go:47`) emits the
  ADR-0011/0032 form via typed structs (no `any`): cniVersion `1.0.0`, name `funcd`, bridge `funcd0`
  with `isGateway`+`ipMasq`+host-local IPAM over `SubnetCIDR`+default route `0.0.0.0/0`, then the
  `firewall` plugin. `ensureConflist` (`:70`) writes `10-funcd.conflist` **only if absent** and
  `TestEnsureConflistWritesIfAbsentLeavesExistingUntouched` (`cni_conf_test.go:51`) proves an existing
  operator conflist is left byte-for-byte (`string(after) == operator`). Both run **before** `cni.Load`
  in `New` (`containerd_linux.go:95-99` precede `:112`).
- **No new dependency.** stdlib only — `net/http`, `crypto/sha256`, `archive/tar`, `compress/gzip`,
  `encoding/hex`. `go mod tidy` is a no-op (captured). crun stays exec'd, not linked (GPL boundary
  intact).
- **`install --print` no-touch.** Prints the version-stamped unit + the lay-down plan and writes
  nothing; `git status` identical before/after the run; `TestPlanListsEveryAssetAndTouchesNothing`
  (`provision_test.go:243`) asserts neither dest dir is created, and `TestOneServiceInstallPrint`
  (`install_test.go:14`) now also asserts the plan elements (crun/containerd/cni-plugins) are present.
- **Conventions (ADR-0002).** No `any`/`interface{}` in signatures (conflist is typed structs);
  `api/fault` for the package's surfaced errors (`fault.Invalidf`/`Wrapf`); `log/slog` for the
  best-effort `ip_forward` warning; ctx-first on `LayDown`/`download`; the `assets` global carries a
  justified `//nolint:gochecknoglobals` (read-only pinned table). The cross-platform helpers
  (`renderConflist`/`ensureConflist`/`setIPForward`, `provision.*`) carry **no build tag** so they are
  exercised by `just ci` on darwin — confirmed by the passing darwin test run.
- **Deferral honesty.** `install-lays-down-runtime` and `manager-finds-laid-down-binaries` (real
  root+network+containerd) are correctly deferred to the `FUNCD_IT=1` Linux lane per the ADR Test
  plan; their absence as host-runnable tests is sequencing, not a model gap. The non-gated scenarios
  each have a named, un-skipped, passing test: checksum-mismatch
  (`TestLayDownChecksumMismatchAbortsWritingNothing`), conflist write-if-absent
  (`TestEnsureConflistWritesIfAbsentLeavesExistingUntouched`), install --print
  (`TestOneServiceInstallPrint` + `TestPlanListsEveryAssetAndTouchesNothing`), plus idempotent-skip,
  url-construction, and the renderer/ip_forward facets.
- **Helper naming note (not a defect).** The impl named the helpers `ensureConflist`/`setIPForward` vs
  the ADR's illustrative `ensureCNIConf`/`enableIPForward`; the contract names were illustrative, the
  behavior matches exactly.
- **Tracking.** ADR-0056 at `Reviewing` with the `Accepted → Reviewing` bump only; F19 carries the
  ADR-0056 link and remains `implemented` (F19 was already implemented by ADR-0026 et al. before this
  work — ADR-0056 *advances* it, doesn't move the row's status word). No dev-machine reference in any
  changed file.

### Definition of Done

ADR Review-checklist (7 items) + applicable generic DoD items — **all hold**:
- [x] install downloads + SHA-256-verifies before chmod/exec + lays down crun/containerd(+shim+ctr)+CNI, idempotent; mismatch → `fault.Invalid`, writes nothing.
- [x] pinned versions + digests are constants; no new Go module.
- [x] Manager resolves from `/var/lib/funcd/bin` first then `PATH`; child has that dir on `PATH`.
- [x] driver writes the conflist only if absent + enables ip_forward before `cni.Load`.
- [x] `install --print` prints unit + plan, touches nothing.
- [x] execution model (crun, runc-v2 shim, bridge+firewall netns, fixed-port) unchanged.
- [x] temporary workaround documented with the bundled-release exit; non-gated tests pass; real-box install deferred to IT lane; no leak.
- [x] full suite green (`go build`/lint/test/mod verify); real behavior, no shipped stubs; tree matches the ADR surface; conventions hold.

DoD: 8/8 hold.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0056 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 8/8.
See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. Stamp ADR-0056 `Reviewing → Implemented`; F19 already `implemented`. The two
root+network+containerd scenarios remain owed to the `FUNCD_IT=1` Linux lane (sequencing, tracked by
the ADR), not by this gate.
