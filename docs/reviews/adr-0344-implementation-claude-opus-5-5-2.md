## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0344 implementation, round 2, model: claude-opus-5-5)

Round 2 reviews the fix commit `9322d4dc` on `feat/addon-runtimes`, on top of `41f280a7` (ADR-0345's tip), against
the round-1 report (`docs/reviews/adr-0344-implementation-claude-opus-5-5.md`). The commit touches docs, doc strings,
the generated reference and two new tests, and no code of the shipped path. So round 1's checks of that code and its
lane evidence stand. Every check below was run at `9322d4dc`, not read.

### Round 1's findings

- **Major 1, the guide's first command: closed.** `docs/guide/develop/services/addons/index.md:19` now pipes
  funcd-addons v0.3.0's `rauthy.yaml`, which is the release's `rauthy/app/addon.yaml` in the runtimes shape. Round 1
  checked that a funcdctl built from this change decodes it.
- **Minor 1, doc text left stale or broken: closed.**
  - `AddonObjectSpec.Config` now names "the config schema of its Addon's kind entry".
  - `AddonObjectBindings.Secret` names "its Addon's kind entry" and its `secretKeys`.
  - The add-on object's 422 case names the kind entry's config schema.
  - The spec generator leaves `api/openapi` and the Spec pages unchanged, so they are regenerated, and no stale path
    remains in them.
  - The `Reconcile` doc comment is one sentence again (`internal/services/addon/reconcile.go:27-29`).
- **Minor 2, two rules that no test reached: closed.**
  - `TestAddonRuntimeFillWaitsForEveryWorker` replaces one of two replicas while the other runs. It asserts that the
    Revision records no image and that the replacement runs the operator's image. Mutant M2b (record the image while
    a worker runs) now fails it: `Should be empty, but was registry.example.com/deno@sha256:…`.
  - `TestAddonRefusesAnImageOverrideKey` assembles the platform from a config through `buildOptions`, with a short
    data dir. It gets 400 for an Addon that ships `ruby3`, an override key, and stores one that ships `deno2`.
    Mutant M7 (no override keys reach the admission) now fails it.

### Routed (not this work's)

- **pyvvo/funcd-addons' README** still pipes v0.1.0's `rauthy.yaml`, as round 1 routed. Home: elsewhere, that
  repository's next PR.

### ✅ Verified correct (keep it)

- **The checks** at `9322d4dc`, through `scripts/agent/d`, on the packages the commit touches (`api/types/v1alpha1`,
  `cmd/funcd`, `internal/controlplane`, `internal/function`, `internal/services/addon`):
  - `go build ./...` passes.
  - `go vet` is clean, `go tool golangci-lint run` reports 0 issues, and gofmt is clean.
  - The two new tests pass under `-race`, in 0.01 s and 0.45 s.
  - `go test` passes on the five packages (`cmd/funcd` 66 s).
  - The two round-1 mutants are caught.
  - `just check-hygiene` is clean, and the spec generator leaves no drift.
- **Unchanged since round 1**: the shipped code of ADR-0344, its fourteen scenario tests, seven caught mutants, the four
  lane runs on containerd and the funcd-addons v0.3.0 pin.

### Definition of Done

17 of 17 items hold: the 8 Review-checklist items, the guide and the API reference among them now, and 9 generic
items.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0344 (implementation, round 2) → pass, 0 blockers, 0 majors and 0 minors, DoD
17/17. See `docs/reviews/model-scorecard.md`.

### Recommendation

ADR-0344 is stamped `Implemented`, and F139's runtimes part reads `implemented`. Its dev-workers part stays `accepted`
until ADR-0346 is built, and its shim-protocol part stays `idea`. The PR that ships it references #1017 and does not
close it: the last of F139's ADRs closes it.
