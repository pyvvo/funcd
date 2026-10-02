# Issue #237 Fix Review — identical pushes get different manifest digests

**Verdict**: **pass**. The regression test fails on the pre-fix code for the reported reason and passes with the fix
under `-race`. Four mutants each fail the matching subtest. The change removes the cause the issue names, at every
place the package packs a manifest, and conforms to ADR-0089, ADR-0035, ADR-0031, ADR-0139 and ADR-0145. There are
two Minors, and neither blocks sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #237 · ADR-0089 (same tree, same digest) · ADR-0035 (tag → digest at stamp time) ·
ADR-0031 · ADR-0139 · ADR-0145 · ADR-0002 · `CLAUDE.md` style rules

The change is one commit (`cb59b11`) on `fix/237-reproducible-manifests` on top of `origin/main` (`0f86f3e`). It
touches `internal/artifact/artifact.go` (the `reproducible` helper and the `Push` call), `internal/artifact/bundle.go`
(the `PushBundle` call), `internal/artifact/site.go` (the `PushSite` call) and the new
`internal/artifact/reproducible_test.go`.

## Verdict: pass — 0 blockers, 0 majors  (issue #237 fix, model: claude-opus-5-5)

### Minor
- **Minor · model — `packedAt` restates an epoch the package already defines.** `bundle.go` declares `zeroTime`
  (`time.Unix(0, 0).UTC()`), documented as the canonical time that keeps packed bytes reproducible. The new
  `packedAt = "1970-01-01T00:00:00Z"` is the same instant for the same ADR-0089 purpose, written a second time as a
  string. **Fix**: set the annotation to `zeroTime.Format(time.RFC3339)` in `reproducible` and drop the constant and
  its comment, so the tar headers and the manifest share one determinism epoch.
- **Minor · model — the `site` push closure uses the parent test's `t`.** In `TestIssue237_…`, the `pushes` map is
  built in the parent function, so `siteDir(t)` inside the `site` closure receives the parent `*testing.T` while it
  runs in a parallel subtest. If that fixture fails, `require.NoError` calls `FailNow` on the parent from the
  subtest's goroutine. A probe of that pattern (a parent-`t` `require.NoError` inside a parallel subtest) failed with
  `panic: test executed panic(nil) or runtime.Goexit`, which aborts the whole package run instead of failing one
  subtest. The happy path is unaffected. **Fix**: build the site directory before the map, as the `bundle` and `file`
  fixtures already are, or pass the subtest's `t` into the closure.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** I ran `git revert --no-commit HEAD`, restored
  `reproducible_test.go` from `HEAD` (the revert removes it), and ran `go test -count=1 -run TestIssue237_`. All three
  subtests (`file`, `site`, `bundle`) fail at the digest assertion with "an identical push a second later keeps its
  digest", for example `expected: "sha256:22c1…"`, `actual: "sha256:b47d…"`. I then ran `git reset --hard cb59b11`.
- **It fails for the reported reason.** A probe through `-overlay` pushed the same site twice, one second apart, and
  printed each manifest. On the pre-fix code the two digests differ, and the only differing field is
  `"org.opencontainers.image.created"` (`…T18:04:56Z` against `…T18:04:57Z`). With the fix both pushes give the same
  digest, and both manifests carry `"1970-01-01T00:00:00Z"`. oras-go v2.6.1 `ensureAnnotationCreated` (`pack.go`)
  stamps `time.Now().UTC()` when the key is absent and accepts a caller's value when it is valid RFC 3339, which matches
  the issue's account.
- **It passes with the fix**: `go test -count=1 -race -run TestIssue237_` gives 3/3 subtests. Nothing is skipped. The
  test sleeps to the next second boundary (under 1 s, with the subtests in parallel), so it fails deterministically on
  the pre-fix code and has no time dependence with the fix.
- **The issue's own flaky test is now stable**: `TestScenarioSiteArtifactRoundtrip` passes with `-race -count=10`.
- **The cause is fixed, not the symptom.** The wall-clock stamp is replaced by a fixed value at all three
  `oras.PackManifest` calls in production code (`Push`, `PushBundle`, `PushSite`). `PushIndex` (`platform.go`) writes
  its index as plain JSON with no created annotation, so it was already deterministic and now stays stable over
  re-pushed sources. The two direct `PackManifest` calls in `site_test.go` and `bundle_test.go` build hostile fixtures
  and compare no digests, so they rightly stay as they are. The fix adds no retry, no timeout and no skipped test.
- **Mutants**: each mutant was killed by the matching subtest.
  - `reproducible(...)` dropped from `PushSite` fails `site` only.
  - `reproducible(...)` dropped from `PushBundle` fails `bundle` only.
  - `reproducible(...)` dropped from `Push` fails `file` only.
  - The `annotations[ocispec.AnnotationCreated] = packedAt` line dropped fails all three.
- **Nothing reads the created annotation.** A search of the repository (Go, YAML, shell, Venom suites and docs) and of
  both pinned language modules finds no reader of `org.opencontainers.image.created` or `ocispec.AnnotationCreated`
  other than the new helper. No test pins a golden manifest digest, and the tests that expect a new digest
  (`TestScenarioContractDigestPinned`, the Site tag-move scenario, the `bi` → `bi-v2` lane redeploy) all push different
  content.
- **Scope**: every hunk serves the issue. The issue names only `PushSite`, and the fix also covers `Push` and
  `PushBundle` because they share the cause and the ADR-0089 guarantee. No test was weakened or deleted.
- **Reuse**: the helper uses the standard library (`maps.Copy`) and oras's own documented mechanism (the
  `ManifestAnnotations` key) instead of post-processing the manifest. It copies the map instead of mutating the
  caller's, as oras does. The test reuses the package fixtures `goodBundle`, `writeBundle`, `siteDir` and
  `layoutRef`, and runtime strings the existing tests already use (`nodejs22`, `python314`). Apart from the epoch
  Minor, nothing is duplicated.
- **Conventions**: no new exported surface, no new dependency, imports at the top level, and errors still flow through
  `api/fault`. The comments give the reason (#237, ADR-0089) and do not narrate the code. The test is named
  `TestIssue237_IdenticalPushesKeepTheirDigest`, and its doc comment cites ADR-0089 and ADR-0035.
- **ADRs**: the fix realizes ADR-0089's "same tree ⇒ same digest" at the manifest level, which ADR-0035's tag → digest
  pinning and ADR-0139's reuse of the ADR-0089 transport rely on. A tag re-pushed with unchanged content no longer
  moves, so it no longer stamps a spurious new Revision. ADR-0031's manifest shape and ADR-0145's index rules are
  unchanged: the created key is a standard OCI annotation, not a funcd one. No ADR file was edited.
- **Checks**, all run by me through the cached dev-shell environment:
  - `gofmt -l`: clean.
  - `go build ./...` and `GOOS=linux go build ./...`: ok.
  - `go vet ./...`, on the host and for Linux: ok.
  - `go tool golangci-lint run ./...`, on the host and for Linux: "0 issues." both times.
  - `go test -count=1 -race ./internal/artifact/`: ok.
  - `go test -count=1 ./...`: exit 0, with 79 packages ok.
  - `just check-hygiene`: "hygiene: clean".
  - `api/types` is unchanged. The `pkg/funcd` e2e suite and the s3 Lima lane (which pushes the `bi`, `bi-v2` and `docs`
    sites) were not run here; the CI gate runs them.
- **Shape**: the commit has a `fix(artifact):` subject and a body that states the cause and names the regression
  test. It carries `Fixes #237` and the attribution trailer, and the branch holds one commit for one issue.

### Definition of Done
11 / 11 items hold (the fix checklist). Item 8 holds for every check run here; the e2e suite and the Lima lane are left
to the CI gate. The PR is not open yet, and its description must carry `Fixes #237`.

### Model scorecard
To record: claude-opus-5-5 on issue #237 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/11. See
docs/reviews/model-scorecard.md.

### Recommendation
Sign off and hand back to `/fix` Step 8 to open the PR. Both Minors are optional one-line changes. Worth a line in the
release notes: an artifact pushed before this fix carries a real timestamp, so the first re-push of the same content
moves its tag once, to the stable digest.
