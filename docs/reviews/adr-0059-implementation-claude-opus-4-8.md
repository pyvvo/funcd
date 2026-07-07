# ADR-0059 Implementation Review — Contract as OCI manifest metadata

**Verdict**: **pass** — the implementation conforms to the ADR's Contracts, all five Scenarios are named/un-skipped/passing tests, the Review checklist + Definition of Done hold, and the four sub-checks are green. No Blockers, no Majors.
**Reviewed**: ADR-0059 (status `Reviewing`) · FEAT-0001/F30 · blueprint (artifact distribution) · ADR-0002 conventions · ADR-0031 (refined seam)
**Producing model**: claude-opus-4-8

## Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | **exit 0** |
| mod | `go mod verify` | **exit 0** — all modules verified |
| lint | `go tool golangci-lint run ./internal/artifact/… ./cmd/funcdcli/… ./internal/bench/…` | **0 issues** |
| test | `go test ./...` | **exit 0** — 43 packages ok, 0 failures |
| deps | `git diff HEAD -- go.mod go.sum` | **unchanged** — no new dependency (reuses oras-go) |

(`just ci`'s git-diff gate fails on the uncommitted tracked files, as expected during implementation; the four sub-checks are the green signal, satisfied by committing.)

## Scenario → test traceability (all passing, none skipped)

| ADR Scenario | Test | Asserts |
|---|---|---|
| contract-embedded-on-push | `TestScenarioContractEmbeddedOnPush` | Inspect reads back `{input, output, dialect}` from the manifest + blob |
| inspect-without-pull | `TestInspectWithoutPullNeverFetchesBundle` (white-box, counting target) | `fetched[bundleMediaType] == 0`; contract blob fetched once |
| no-contract-no-metadata | `TestScenarioNoContractNoMetadata` | nil contract → Inspect = `fault.NotFound`; manifest annotations stay nil |
| bundle-selected-by-mediatype | `TestScenarioBundleSelectedByMediaType` | Pull returns bundle bytes with a contract layer present |
| contract-digest-pinned | `TestScenarioContractDigestPinned` | Inspect by the original digest returns the original contract after the tag moves |
| (CLI surface) | `TestScenarioCLIInspectReadsContract` | `push --contract-input` + `inspect <ref>@<digest>` end-to-end; contract-less → error |

## Review checklist (ADR-0059) — all hold

- ✅ Dedicated `application/vnd.funcd.contract.v1+json` blob layer + `dev.funcd.contract.v1` annotation (not inlined) — `Push` (artifact.go:94), asserted by the white-box test (annotation = blob digest).
- ✅ `Inspect` fetches manifest + contract blob only; the bundle blob is **never** fetched — counting-target test, `fetched[bundleMediaType] == 0`.
- ✅ `Pull` selects the bundle by `bundleMediaType` (`layerByMediaType`, artifact.go:80), robust to the added layer.
- ✅ No-contract push is byte-compatible with the ADR-0031 manifest — `opts.ManifestAnnotations` stays nil; Inspect → NotFound.
- ✅ Contract is content-addressed (`content.NewDescriptorFromBytes`) + read by manifest digest — digest-pinned test.
- ✅ `funcdcli inspect` renders the schemas; no new dependency; no `any` in the new surface; no identity/path leak.
- ✅ `push` takes labeled `--contract-input`/`--contract-output`, gates each via `contract.Check`, assembles `{input?, output?, dialect}`; a bare-tag inspect resolves the tag.

## ✅ Verified correct — keep it

- **The static-inspection invariant is *proven*, not asserted by eye.** The counting-target white-box test (`inspect_internal_test.go`) drives the extracted `inspectFrom` seam and asserts the bundle media type is fetched zero times — exactly the ADR's load-bearing guarantee. Keep this seam.
- **Backward-compat is structural, not incidental.** `ManifestAnnotations` is only set when a contract exists, so a contract-less push is byte-identical to the ADR-0031 path by construction (not a special-case branch). Keep.
- **`ContractBlob` single-sources the payload + dialect** (the judge's nit) — the media type, annotation key, and `2020-12` dialect live as constants in one package; the CLI only gates + assembles. Keep.
- **The Major fold landed correctly**: labeled `--contract-input`/`--contract-output` replace the unlabeled StringSlice, so the keyed blob is unambiguous; the live binary smoke renders a discriminated-union output through `inspect` (ties in the F29 union-codegen fix).
- **Conventions**: ctx-first on Push/Pull/Inspect; `api/fault` throughout (Invalid/NotFound/Internal mapped sensibly); no panic; the lone `...any` is the pre-existing `writef` print helper (the idiomatic variadic exception), not new API.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None blocking. (Observation, not a finding: `Inspect` locates the contract layer by media type rather than by reading the annotation digest — both are explicitly sanctioned by Decision §2, and media-type selection is the more robust of the two.)

## Recommendation

**pass** → stamp ADR-0059 `Reviewing → Implemented`, advance FEAT-0001/F30 → `implemented`. F30 (the last v1.1 feature) is complete and verified. No board item to move (F30 is a feat-doc roadmap item; the registry/AI-matching consumer is the separate V2 board item).
