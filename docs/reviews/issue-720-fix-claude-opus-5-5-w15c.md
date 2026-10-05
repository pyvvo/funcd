## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #720 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i720`, one commit `9ede6b2b fix(activator): keep edge CORS and X-Request-Id headers single when the Function sets them`.
Files: `internal/activator/activator.go` (+10/-3), `internal/dataplane/upstream_test.go` (+70).

### Proof first (the decided rule)

`TestIssue720_EdgeHeadersNotDuplicated` was run on current `origin/main` code with no fix: an overlay of the
`origin/main` version of `internal/activator/activator.go`. `origin/main` has moved past the branch base, but
`internal/activator`, `internal/dataplane`, `internal/edge` and `internal/gateway` are byte-identical between
the base and `origin/main` (`git diff` of those paths is empty), so the overlay run is the current-main proof.

```
--- FAIL: .../function/f/both      actual: ACAO ["https://app.test" "https://app.test"], X-Request-Id ["rid-1" "rid-1"]
--- FAIL: .../function/f/wildcard  actual: ACAO ["https://app.test" "*"]
--- FAIL: .../function/f/upgrade   actual: ACAO ["https://app.test" "*"], X-Request-Id ["rid-1" "rid-1"]
--- FAIL: .../catalog/lake/both    (same as function/f/both)
--- FAIL: .../catalog/lake/wildcard
--- FAIL: .../catalog/lake/upgrade
FAIL  github.com/pyvvo/funcd/internal/dataplane
```

Each case in the issue has its own proof: both headers set, the wildcard origin, and the control case where
the Function sets neither header (this case passes, as the issue reports). The test also proves the
upgrade (101) path and the Upstream route, which the issue lists as untested. Every failure has the issue's
cause: the upstream value is appended to the edge value.

### Verification run

- With the fix, `-race`: `go test -race -count=1 ./internal/activator/ ./internal/dataplane/` → `ok` for both packages.
- `go vet` on both packages → clean. `golangci-lint run ./internal/activator/... ./internal/dataplane/...` → `0 issues.`
- Mutant 1 (Del loop disabled, `if false`) → the six cases above fail with the duplicated values. Killed.
- Mutant 2 (Vary also deleted, `if true`) → `function/f/both` and `catalog/lake/both` fail on the Vary `ElementsMatch`. Killed.
- The worktree was left clean (no tracked or untracked changes). The overlay and mutant files are in the scratchpad only.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Observation, not scored: when the Function also sends `Vary: Origin`, the response carries `Origin` twice
in Vary. RFC 9110 allows a repeated Vary member and it has no effect on caching, so this is a valid
trade-off of keeping Vary as a merged list.

### ✅ Verified correct (keep it)
- **Cause, not symptom.** `KeepEdgeHeaders`' `ModifyResponse` runs before ReverseProxy's `copyHeader`
  (and before `handleUpgradeResponse` on a 101). Deleting the upstream's copy of each edge-snapshot key
  there removes the append. This is the cause the issue names, and the fix uses the location the issue
  proposes. There is no retry, no timeout and no swallowed error.
- **One site covers every affected path.** The activator (`activator.go:433`) and the Upstream route
  (`dataplane.go:298`) share `KeepEdgeHeaders`, and the 101 path goes through the same `ModifyResponse`.
- **Siblings.** The other ReverseProxies (`internal/catalog/gateway/proxy.go`, `internal/gateway/embedded`)
  are not behind the edge `RequestID`/`shape` chain (`pkg/funcd/funcd.go:1108`/`1113` wrap only
  `dataplane.Handler`), so they cannot duplicate an edge header. No sibling was left unfixed.
- **Edge-wins semantics** match the `headersMW` set rules that the issue cites. The ADR-0114 CORS
  scenario still holds, and no ADR decision is changed. No ADR file was edited.
- **Scope.** Both hunks serve the issue. No test was weakened. The #417 test still passes in the same package run.
- **Reuse.** The test reuses `seedFunction`, `fakeEndpoints`, `noScaler`, the production `gateway.Chain`,
  `gateway.RequestID` and `shape.Chain`, beside the existing #417 test. The fix is a loop around the
  existing `maps.Copy`, with no new helper.
- **Conventions.** No new import, no YAML, and one doc comment that states the why with the issue number.
  The commit subject is `fix(activator): …`, the body has `Fixes #720` and the attribution trailer, and
  the commit covers one issue.

### Definition of Done
12 / 12 items hold. Item 8 was verified for the touched packages: tests with -race, vet and host lint.
The Linux lint, the repo-wide tests and e2e are run once by the group gate, as this wave requires.

### Model scorecard
Ledger fields (not recorded by this review): issue 720, phase fix, model claude-opus-5-5 → pass, 0/0/0,
0 model-attributed, DoD 12/12.

### Recommendation
Pass. Hand back to `/fix` Step 8 for integration into the group PR.
