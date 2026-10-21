## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0257 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0257-last-good-site` at 40d56f18, built in the one-PR mode, with three commits
on main 980a849c:

- c70d10cf docs, ADR-0257 Accepted;
- af0f9db4 fix, a defect found on the way;
- 40d56f18 feat (`Fixes #1137`), ADR-0257 and F168 to Reviewing.

The change is `cmd/funcdctl/dev_app.go` (`prepare(ctx, op, reload)`, `keepLine`), `cmd/funcdctl/devlivereload.go`
and their tests.

### Minor
- **`keepLine`'s refusal branch has no test** · attribution: model · evidence: mutant K2 drops the refusal's message
  from the line (`images.<key>: keeping its last build`). It passes `TestScenarioDevSiteLastBuild`,
  `TestDevSiteKeepsLastImage` and the App tests.
  - Every test keeps a Site on a miss: empty, or holding no index.
  - None keeps one on a refused folder (a `--build web=<file>`) or a failed push (a symlink in the build under live
    reload). The Contracts word those lines as the refusal's message.
- **No test holds a non-Site build to the old rule** · attribution: model · evidence: mutant K3 drops
  `img.part != partSite` from the keep condition, so a function build that breaks in a reload would also keep its
  last image. It passes the same tests.
  - The ADR scopes the keep to Sites ("Out: function and step builds"), and a function's broken build must still fail
    the reload (ADR-0253 Decision 9).
  - A reload test with a function's `key=path` removed would hold it.

### ✅ Verified correct (keep it)
- **The keep (Decisions 1 to 4).**
  - In a reload, a Site is checked with `siteHoldsIndex` after `resolveImage`.
  - A miss, a refusal or a failed push of a Site with an earlier image in `s.images` (digest set) prints
    `keepLine(key, err)`, locks that image's digest and writes the same `*devImage` back. So the last pushed image stays
    the last, the Site's part of the revision is unchanged, and live reload's Site comparison sends no event.
  - Any other error, a Site without an earlier image, and the first deploy (`reload=false`) return the error as
    before.
  - A Site that builds again pushes as before, because its fingerprint differs from the kept image's.
- **The line.** A miss uses `buildMiss.waiting()`, the start's waiting wording (`<dir> is absent`, `is empty`, `holds
  no index.html`); a refusal uses its innermost message.
- **Tests.**
  - `TestScenarioDevSiteLastBuild` runs its three scenarios as named subtests, with no `watch` and live reload on: an
    emptied folder keeps the last build while the function's change serves and no event goes out; an index-less folder
    keeps it; a rebuilt folder pushes and serves.
  - `TestDevSiteKeepsLastImage` holds Decisions 2 and 3 and the unchanged first deploy, including the locked digest in
    the render and `require.Same` on the kept image.
- **Mutants.** The fixer killed three: the first deploy checking the index, the first deploy keeping, and a keep
  overwriting the last image. The review killed K1, which drops the reload's index check and fails both tests. K2 and
  K3 survive (the Minors).
- **The found defect (af0f9db4).**
  - `dev.liveReload: true` without `dev.watch` stopped dev at start, because `startLiveReload` wrote its port file into
    a process dir that only the watch commands' start created.
  - With main's `devlivereload.go` overlaid, `TestDevLiveReloadWithoutWatch` fails with `keep the live reload port:
    open …/process/livereload.port: no such file or directory`; with the fix it passes.
  - The fix, a `MkdirAll` before the port file, addresses the cause.
- **Docs.** ADR-0253, which is Implemented, gains only the permitted "Superseded in part by" back-link. ADR-0257's
  substance is unchanged since c70d10cf; 40d56f18 touches only its Status line.
- **Checks on 40d56f18.** gofmt is clean, `go build ./...` passes, and vet passes with and without `-tags dev`.
  `golangci-lint` reports 0 issues with and without `--build-tags dev`. `go test -tags dev -race ./cmd/funcdctl/`
  passes (172 s), and so does `go test ./cmd/funcdctl/` (4.3 s). The gate runs once on the PR.

### Definition of Done
5 / 5: the 4 checklist items plus Done-when's build, vet, lint and tests. ADR-0257's Done-when has no live check. The
two Minors are test gaps; the code behaves as the ADR says on both paths.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0257 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 5/5.

### Recommendation
Pass: ADR-0257 is stamped `Implemented` and F168 `implemented` in this commit. Adding the two tests (a refused Site
folder in a reload, and a function build broken in a reload failing it) before the PR merges would close the Minors.
