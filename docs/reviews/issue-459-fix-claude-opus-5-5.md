## Verdict: changes-requested — 0 blockers, 1 major, 0 minors  (issue #459 fix, model: claude-opus-5-5)

Change: branch `fix/i459`, commit 89da040 `fix(blob): list every key under a prefix with "//", "../" or a trailing "/" on the file backend`.
Files: `internal/blob/gocloud/gocloud.go` (+23/-1), `internal/blob/gocloud/gocloud_test.go` (+44).

The fix is correct and removes the cause. The regression test does not guard it: every prefix in the
test ends in "/" (or is ""), so only the trailing-slash cut is exercised. Three of the four cut
conditions and the re-filter can be deleted and the package's tests still pass.

### 🔴 Blockers

None.

### 🟡 Majors

- **Major 1: the regression test exercises only one of the fix's five key lines** · attribution: `model`.
  Evidence: overlay mutants on `internal/blob/gocloud/gocloud.go`, run against the package's tests:

  | Mutant | Edit | Package tests |
  |---|---|---|
  | M1 | drop `r < ' '` (control-rune cut) | pass (survives) |
  | M2 | drop `i == len(prefix)-1` (trailing-slash cut) | fail, `List("f/")` |
  | M3 | drop `prefix[i-1] == '.'` (cut after `..`) | pass (survives) |
  | M4 | drop the `strings.HasPrefix(obj.Key, prefix)` re-filter in `List` | pass (survives) |
  | M5 | drop `prefix[i-1] == '/'` (cut after `/`) | pass (survives) |

  Every test prefix ends in "/", so the trailing-slash cut alone moves the walk up to a directory that
  holds the escaped key; the other cut conditions never decide the result. The test keys also have no
  sibling that the wider walk would return but the requested prefix excludes, so the re-filter is never
  needed. A scratch probe test (overlay only, not committed) with prefixes that do not end in "/"
  kills each survivor: `List("h\x01/i")` kills M1, `List("x/../y/z")` kills M3, `List("a//b/c")` kills
  M5, and `List("p/")` with a sibling key `"pq"` kills M4. The fix code passes the probe. A partial fix
  that cut only a trailing "/" would pass the committed test while still missing the issue's general
  case (a prefix that contains "//" or "../" mid-path).
  Fix: add prefixes without a trailing "/" (`"a//b/c"`, `"x/../y/z"`, `"h\x01/i"`) and a sibling key
  such as `"pq"` to `TestIssue459_ListFindsKeysUnderEscapedPrefixes`.

### Minors

None.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With 89da040 reverted in the worktree and the
  new test kept, `TestIssue459_ListFindsKeysUnderEscapedPrefixes/file` fails on `List("a//b/")`,
  `List("x/../y/")`, `List("../")`, `List("d../")`, `List("f/")` and `List("h\x01/")`; the memory
  subtest passes. The worktree was then reset to 89da040 and is clean.
- **Passes with the fix** under `-race`, both subtests, un-skipped.
- **Cause, not symptom.** The cause named in the issue is confirmed in gocloud.dev v0.46.0
  `blob/fileblob/fileblob.go`: `ListPaged` roots its walk at `filepath.Join(dir, prefix[:lastSlash])`
  (cleaned, unescaped) while `escapeKey` stores a key with a control rune, a "/" after "/" or "..",
  and a trailing "/" hex-escaped. `fileWalkPrefix` cuts the walk prefix before exactly those runes
  (plus "/" after a single ".", which only widens the walk and is harmless), and `List` filters the
  result back to the requested prefix. fileblob's own `SkipDir` on non-matching directories keeps the
  wider walk bounded to the parent directory's matching entries. The cut also stops `List("../")`
  from failing as "escapes bucket root".
- **Scope.** Two hunks in `gocloud.go` and one new test; the memory and S3 paths are unchanged apart
  from a no-op `HasPrefix` re-check. No test was weakened or removed.
- **Reuse.** fileblob's `escapeKey` is unexported, so mirroring its rule locally is the only option;
  no repo helper does this (searched the package, `internal/blob`, `internal/platform`,
  `internal/testkit`). The test reuses the package's existing `mem://`/`file://` pattern.
- **Conventions.** ctx-first, no `any`, no new dependency, imports at top level, a short doc comment
  that states the why and cites the issue.
- **ADRs.** Restores the ADR-0007 §1 contract (List returns every key with the prefix on every
  driver); no ADR file touched.
- **Checks.** `go test -race ./internal/blob/...` ok (gocloud, s3gateway); `go vet` clean;
  `golangci-lint` 0 issues.
- **Commit shape.** `fix(blob):` subject, body states cause and fix, `Fixes #459`, attribution trailer,
  one issue in one commit.

### Definition of Done

10 / 11 items hold.

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue459_…` reproduces the issue | yes |
| 2 | fails on pre-fix code for the reported reason | yes |
| 3 | passes with the fix under `-race` | yes |
| 4 | reverting or mutating the key lines fails a test | **no** — revert fails it, but 4 of 5 mutants survive (Major 1) |
| 5 | root cause fixed | yes |
| 6 | scope only; no weakened test | yes |
| 7 | no ADR contradicted or edited | yes |
| 8 | build, vet, lint, tests green | yes for the touched packages; Linux lint, e2e and the repo-wide set run at the group gate |
| 9 | conventions | yes |
| 10 | reuse, no duplication | yes |
| 11 | commit shape | yes |

### Model scorecard

claude-opus-5-5 · fix · changes-requested · 0 B / 1 M / 0 m · model-attributed 1 · DoD 10/11.

### Recommendation

Back to `/fix`: extend the regression test as described in Major 1 (no code change needed). Every
mutant listed above must then fail it.
