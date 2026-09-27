# ADR-0139 Implementation Review — `Site`, the declarative static web app (F103)

**Verdict**: **pass** — the `Site` kind, its reconciler, the site artifact, the prefix admission, and the CLI/API
threading conform to the Contracts; all 19 Scenarios have named, un-skipped, passing tests; the four sub-checks
are green; the in-process e2e and the `s3` containerd lane both pass, including the real-S3-frontend denial.

**Producing model**: claude-opus-5
**Reviewed against**: ADR-0139 Contracts / Scenarios / Review checklist / DoD · blueprint (ingress: static
serving + `Site`, crash-only) · ADR-0120 (the static arm reused) · ADR-0110 (Route) · ADR-0080/0136 (single
writer) · ADR-0094 (`Workflow.spec.kv` ownership) · ADR-0002 conventions.

## Verification run (evidence)

- `go build ./...` → exit 0. `just lint` → exit 0, **0 issues**; `golangci-lint --build-tags e2e ./pkg/funcd/...`
  → exit 0, 0 issues. `go test ./...` → exit 0 (79 packages ok). `go mod verify` → all modules verified;
  `go mod tidy` leaves `go.mod`/`go.sum` unchanged. `gofmt -l` → empty.
- `just ci` → exit 1 **only** on its git-diff gate (17 tracked Go files uncommitted — the documented CLAUDE.md
  pitfall; every sub-check it runs is green above). It passes once the work is committed.
- OpenAPI golden regenerated (`just generate`); `TestSpecGeneratedFromGo` green; `SiteSpec.required` carries
  `image, bucket, prefix, ingress`.
- `go test -tags e2e ./pkg/funcd/ -run 'TestScenarioE2ESite$|TestScenarioE2EStaticServing'` → PASS (6.6 s /
  0.15 s): materialize + serve, asset content-type + weak ETag, `/data` mount, data-mount miss is `404` while
  the SPA path serves the shell, a **hammered redeploy with zero failed requests**, and **`PutObject` into the
  serving prefix is 403 through the real S3 frontend for both a bound Function and an external `Identity`**
  (the Function's read still works — binding is a read grant).
- **`just lima-example s3` on real containerd → final status PASS, exit 0, 7/7 testcases**, including the three
  new Site cases: `funcdctl push --site` in the guest, the `Site` **adopting** the lakehouse Bucket, index +
  asset at the edge, `/data/roundtrip-site.txt` served from the gold layer, the data-mount miss `404`,
  and the `bi → bi-v2` redeploy observed with `SWAP bad=0`.

## Scenario → test traceability (all passing, none skipped)

| Scenario | Test |
|---|---|
| site-materializes-and-serves · data-mount-serves-sibling-prefix · data-mount-does-not-spa-fallback · redeploy-swaps-atomically · bundle-prefix-has-no-writer | `TestScenarioE2ESite` (pkg/funcd, `e2e`) + `s3` Venom lane testcases 5–7 |
| owned-bucket-and-route-materialized | `internal/site` `TestScenarioOwnedBucketAndRouteMaterialized` |
| adopted-bucket-prefixes-preserved · adopted-prefix-with-owner-rejected | `…AdoptedBucketPrefixesPreserved` · `…AdoptedPrefixWithOwnerRejected` |
| redeploy-swaps-atomically · partial-unpack-recovers | `…RedeploySwapsAtomically` (asserts the index is the last `Put`) · `…PartialUnpackRecovers` |
| not-ready-until-index-present · tag-resolved-once-per-generation · rollback-to-previous-digest | `…NotReadyUntilIndexPresent` (redeploy via a status-wiping update) · `…TagResolvedOncePerGeneration` · `…RollbackToPreviousDigest` (zero `Put`s) |
| retain-stamps-no-owner-reference · foreign-route-not-adopted · site-not-ready-when-route-not-ready | `…RetainStampsNoOwnerReference` · `…ForeignRouteNotAdopted` · `…SiteNotReadyWhenRouteNotReady` (pending ⇒ `RequeueAfter` 2 s; current NotReady ⇒ no requeue) |
| delete-cascade-rejected · public-and-authenticated-rejected · prefix/index/rule rules | `TestSiteValidate` matrix (api/types); `ingress` required → `TestScenarioSiteSchemaRequiresIngress` (wire 422) |
| prefix-is-immutable | `TestScenarioSitePrefixIsImmutable` (unit, with and without a prior digest) + `TestScenarioCLIApplySite` (through the real API) |
| traversal-safe-unpack (+ artifact round-trip, function artifact rejected) | `TestScenarioSiteTraversalSafeUnpack` · `…SiteArtifactRoundtrip` · `…ResolveSiteRejectsFunctionArtifact` |

## Minor

- **`Deps.DefaultIndex` is one field beyond the ADR's `Deps{Store, Buckets, Logger}` contract** · attribution:
  `adr` · `internal/site/reconcile.go:40-47`. It carries the platform config's `site.defaultIndex`
  (`FUNCD_SITE_DEFAULT_INDEX`, default `index.html`) — a decider-directed addition during implementation so the
  default follows the convention every other feature uses (`internal/platform/config/config.go`, `WithSiteDefaultIndex`).
  Covered by `TestScenarioSiteDefaultIndexConfig` and `TestScenarioConfiguredDefaultIndex`. Not a model error;
  recorded so the next ADR touching `Site` carries the knob in its Contracts.
- **The checklist says `Validate` "requires `ingress`", but the Contracts type it as a value field**, which
  `Validate()` cannot observe as absent · attribution: `adr` (a wording/contract inconsistency inside the ADR).
  The requirement holds at the enforcement layer huma tags provide (required on the wire, proven by the 422
  test); no code change needed.

## ✅ Verified correct — keep

- **The owned `Route` is the durable record and `status` is derived** (`materialize.go` `servingDigest` is the
  exact inverse of `compileRoute`; `publish` re-derives digest/servingPrefix/objects/bytes every reconcile). The
  status-wiping-apply path is exercised by the unit tests and by the real `funcdctl apply` in the lane. Do not
  "optimize" this into reading `status.digest` back.
- **Index-last write order + presence-as-completeness** (`unpack`): the recorder-based tests pin the last `Put`
  to the index and prove a crashed partial prefix is re-uploaded before the swap. An index-less bundle is not
  uploaded at all (`IndexMissing`) and the previous digest keeps serving.
- **Tag resolution once per generation**, with `observedGeneration` advanced **only** when the generation's digest
  is programmed on the Route — a failed deploy stays unobserved and retries; a moved registry tag never redeploys
  on a self-triggered reconcile.
- **Ownership semantics exactly as decided**: the Bucket is adopted-or-created, only ever *added to*, never stamped
  in V1; `PrefixOwned` on the one collision; the Route is always stamped and checked by `{Kind, Name, UID}`;
  a deleted `Site` reclaims nothing (no collector — the recorded platform gap).
- **ADR-0120 untouched**: no edits under `internal/edge/static` or to `StaticBackend`; `SiteRule` has no
  `spa`/`index`, so a data-mount miss cannot become the shell — proven on the live edge.
- **Security**: the serving prefix is ownerless and the denial is proven through the real S3 PEP for both principal
  kinds; `ResolveSite`/`PullSite` refuse a function artifact; the traversal-safe untar is reused (`../` and absolute
  entries refused, nothing written outside the scratch dir).
- **Conventions**: no `any` in exported signatures; `api/fault` kinds match the Contracts (`NotFound` / `Invalid`);
  ctx-first; `log/slog` only; no `panic`/`fmt.Print`; the entry-less packer refactor is behaviour-preserving
  (`PackBundle` still gates its entry — tested). `api/**` imports nothing from `internal`.
- **A real defect caught by the lane, fixed before hand-off**: `stampTypeMeta` (`internal/controlplane/handlers.go`)
  is a per-type switch and lacked `*v1.Site`, so an API create arrived with an empty `apiVersion`. Fixed and now
  covered by `TestScenarioCLIApplySite` — the one path the in-process e2e (which seeds the store) does not walk.
- **Tracking**: ADR-0139 at `Reviewing` with its substance unchanged since acceptance (the only post-acceptance
  edit is the status line); FEAT-0003/F103 at `reviewing`; module path correct.

## Definition of Done

**28 / 28** hold (18 ADR Review-checklist items + 10 generic). Notes: `just ci` is green on every sub-check and
exits 0 on commit (its git-diff gate is the only failing line today); the "`Validate` requires `ingress`" item is
satisfied at the wire schema (see Minor 2).

## Model scorecard

Recorded: claude-opus-5 on ADR-0139 (implementation) → pass, 0 blockers / 0 majors / 2 minors, 0
model-attributed, DoD 28/28. See docs/reviews/model-scorecard.md.

## Recommendation

Sign off. Advance ADR-0139 `Reviewing → Implemented` and F103 `→ implemented`; move the board card to Done.
Carry the two `adr`-attributed minors (the `DefaultIndex` config knob; the `ingress`-required wording) into the
next ADR that touches `Site` rather than editing the frozen decision.
