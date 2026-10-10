# ADR-0218 implementation review: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 4 minors (ADR-0218 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0218`, head `ea76036f`, based on `1b5dd03e`. Four work commits (`5f6591e9`, `f56a81a9`,
`af687106`, `281b6cfb`) and the tracking commit `ea76036f`.

The implementation matches the Contracts, the Implementation plan and every Review-checklist item. `just ci` exits 0,
and the three scenarios pass. The two e2e scenarios each take less than 5 s and run in parallel. Two Minors are the
model's: the file mode that `app lock` writes, and one test that is outside this ADR. One Minor is the ADR's: `app
lock` cannot replace a malformed lock. One Minor is sequencing: the branch has a small, additive merge conflict with
the current `main`. None of them blocks sign-off, so ADR-0218 moves to `Implemented` and F121 moves to `implemented`.

### Verification run

All commands ran through `scripts/agent/d` in the review worktree, except the rows marked "trial merge".

| Check | Result |
|---|---|
| `just ci` | `exit=0`: tidy, specgen, `hygiene: clean`, fmt, lint `0 issues.` twice, tests, `dev`-tag tests, `go build`, `all modules verified` |
| `go test -tags e2e -count=1 -v -run 'TestScenarioAppTemplatePinned\|TestScenarioAppRegistryValue\|TestScenarioAppDeployWaits\|TestScenarioAppDeleteReports' ./pkg/funcd/` | `TestScenarioAppTemplatePinned` (2.91 s) and `TestScenarioAppRegistryValue` (4.20 s) pass. ADR-0217's `TestScenarioAppDeployWaits` (`/current` 8.02 s, `/failed` 24.36 s) and `TestScenarioAppDeleteReports` (4.56 s), which now lock before they deploy, also pass. `exit=0` |
| `go test -race -count=1 -v -run '…' ./cmd/funcdctl/` (the template, lock, push and deploy tests) | 16/16 `--- PASS`, among them `TestScenarioAppTemplateRange`, `TestCLIAppLock`, `TestCLIAppRenderWithoutLock`, `TestCLIAppRenderMovedTag`, `TestCLIAppTemplateRefSource`, `TestCLIPushTemplate` and ADR-0217's `TestScenarioAppRenderMatches` and `TestScenarioAppRenderRefuses`. `exit=0` |
| `go test -race -count=1 ./internal/app/template/ ./internal/artifact/` | both `ok`, uncached |
| `go test -tags e2e -count=1 -run 'Site\|Digest\|TestScenarioRevision\|TestScenarioApp' ./pkg/funcd/` (the e2e tests that use the refactored site helpers and `OrasMaterializer.Resolve`) | `ok … 219.841s`, `exit=0` |
| `git diff 1b5dd03e..HEAD -- go.mod go.sum` | empty |
| `git diff 1b5dd03e..HEAD -- docs/adr/` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10; accepted 2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | only the F121 status cell changed, `accepted` → `reviewing` |
| `git diff 1b5dd03e..HEAD -- internal/artifact/artifact_test.go` | empty: the ADR-0035 tests are unchanged and pass |
| `grep -rn PullTemplate --include='*.go'` | one non-test caller, `cmd/funcdctl/app.go:311`, plus the definition |
| `panic(`, `fmt.Print*`, a logging import, `not implemented`, `t.Skip` added in Go | none; the only `any` is the sanctioned printf variadic of `fprintf` (`cmd/funcdctl/cli.go:537`) |
| `git merge-tree --write-tree HEAD origin/main` (`main` three commits ahead) | conflicts in `cmd/funcdctl/app.go` and `cmd/funcdctl/app_test.go` only (Minor 4) |
| Trial merge with `origin/main` in a scratch worktree, verb lists unioned, since removed | `go build ./...` and `go vet` of the touched packages exit 0; `cmd/funcdctl`, `internal/app/template` and `internal/artifact` tests `ok`; `TestScenarioAppTemplatePinned`, `TestScenarioAppRegistryValue` and `TestScenarioAppDeleteReports` `ok` |

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1: `app lock` makes `app.lock` readable only by its owner (model).** `WriteLock` creates its temporary file
  with `os.CreateTemp` (`internal/app/template/lock.go:161`), which uses mode 0600, and renames it without a chmod.
  Probe: in a scratch copy of the to-do fixture, `app.lock` was `-rw-r--r--`; after `funcdctl app lock` it was
  `-rw-------`. The pushed digest is not affected, because `packDir` writes every file as 0644
  (`internal/artifact/bundle.go:144`). However, a committed file that another user or a CI container reads becomes
  unreadable to them. Fix (builder): set the temporary file's mode to 0644, or to the existing file's mode, before
  the rename.
- **Minor 2: a test outside this ADR, with a citation that the ADR does not support (model).**
  `TestRenderRefusesAliasMergeAndBinaryKeys` (`internal/app/template/lock_test.go:363`) tests ADR-0217's fragment
  walker. Its "alias value" and "binary key" cases repeat cases that `TestRenderRefuses` already has
  (`internal/app/template/template_test.go:383` and `:398`). Its comment says that "ADR-0218 records these
  refusals", but ADR-0218 does not mention aliases, merge keys or `!!binary`. Fix (builder): move the two new cases
  ("alias key" and "merge key") into `TestRenderRefuses`, delete the duplicates, and delete the ADR-0218 claim.
- **Minor 3: `funcdctl app lock` cannot replace a malformed `app.lock` (adr).** Decision 2 says that `lock` "loads
  the template", and Decision 3 says that the lock is decoded strictly. `appLockCmd` therefore calls `template.Load`
  (`cmd/funcdctl/app.go:336`), which calls `ReadLock` (`internal/app/template/load.go:89`), so a lock that holds git
  merge-conflict markers stops the command that would regenerate it. Probe: `funcdctl app lock` on such a copy
  printed `funcdctl: app.lock: app.lock: error converting YAML to JSON: yaml: line 2: mapping values are not allowed
  in this context` and exited 1. The user must delete `app.lock` first. The model followed the ADR. Follow-up: a
  later ADR or fix can let `lock` ignore the existing lock. The repeated `app.lock: app.lock:` prefix
  (`internal/app/template/lock.go:141`) is cosmetic.
- **Minor 4: the branch conflicts with the current `main` (env, sequencing).** `main` gained ADR-0214's `retry` verb
  (`290222c8`) after this branch was created. Both changes edit `appCmd`'s `Short` and `AddCommand`, the doc comment
  above them, and `TestCLIAppGroupVerbs`. The changes are additive. With the union (`…|lock|…|retry`), the trial
  merge builds and passes (see the table). Fix (integrator): rebase onto `main` and keep both verbs.

### ✅ Verified correct (keep it)

- **Contracts.** Every signature in the Contracts block exists as written: `Template.Parsed`/`Lock`, `Image`,
  `LockedImage` with its JSON tags, `ImageResolver`, `ParseImage`, `EvalRegistry`, `Lock`, `CheckLock`, `ReadLock`,
  `WriteLock`, `ImageRef`, `Moved`, `CheckPush`, `AppTemplateArtifactType`, `PushTemplate`, `ResolveTemplate`,
  `PullTemplate`, `ListTags`, `ResolveDigest` and `TagResolver`. No config key, API field or server reason was added.
- **Decision 1.** `ParseImage` (`internal/app/template/images.go:32`) splits at the first `:` and checks the repo
  with `ValidateRepository`. It refuses a digest, a `+` and a `v` before a version, in an exact entry or a range
  (`vBeforeVersion`, `:27`). The `v` check lets a `v` inside a pre-release through. The spec is exact before it is a
  range. The table test covers every case that the plan lists, and adds `^v` and `<v` in a compound range.
- **Decisions 2 and 3.** `Lock` (`lock.go:49`) works in name order and resolves an exact entry without listing the
  tags (the test checks the list calls). `highest` keeps only `StrictNewVersion` tags. No match is `fault.NotFound`
  and names the image, the range and `<registry>/<repo>`. `WriteLock` writes through a temporary file and a rename,
  with sorted keys, and two writes give the same bytes. `ReadLock` is strict (`UnmarshalStrict`, a `sha256:` and
  64-hex digest). `CheckLock` handles changed, missing and extra names, and a registry change is not stale.
- **Decision 4.** `EvalRegistry` validates only the keys that `registry` reads when `lock` is true, through
  `readKeys` and subschema pointers. So `app lock` needs no `-f` with the fixture's required `host`. `badRegistry`
  follows the ADR rule exactly: a tag after the last `/` of a layout or after the first `/` of a host, and a port is
  allowed. `Render` refuses a missing or stale lock first, and every image is `ImageRef`'s
  `<registry>/<repo>:<version>@<digest>`. `TestScenarioAppRenderMatches` checks that every Function image carries a
  digest from `app.lock` and an empty `imageDigest`.
- **Decision 5.** `Moved` warns only, on stderr, from a directory source (`cmd/funcdctl/app.go:265`).
  `TestCLIAppRenderMovedTag` proves that stdout is byte-identical before and after the tag moves, that exactly one
  warning names the image and both digests, and that a ref source prints only `template <ref>@<digest>`.
- **Decision 6.** `PushTemplate` (`internal/artifact/template.go:24`) refuses the tag cases before any read. It packs
  into an in-memory store to learn the digest, reads the version tag at the target (`tagDigest`), and only then opens
  the write target. The tests prove that a refused push creates no layout, that a conflict leaves the layout tree
  unchanged, and that the same digest writes nothing. The site path now shares `pushTarArtifact`, `resolveTyped`,
  `pullTyped` and `assertArtifactType`, which reuses that code instead of copying it, and the Site e2e tests still
  pass.
- **Decision 7.** `templateFlags.load` treats the source as a directory only when one exists at that path. A ref is
  resolved with its type checked, pulled by digest into `os.MkdirTemp`, removed on return, and refused without
  `app.lock`. `TestCLIAppTemplateRefSource` covers a template without a lock and a site artifact, and checks that
  nothing is printed.
- **Scenarios.** `app-template-pinned` runs the full sequence. It locks, pushes, and compares the pulled files with
  the source byte for byte. It then moves the tag and deploys from the ref: the Function image is pinned and the
  Revision's `imageDigest` is the locked digest. It also deploys from the directory, which warns and names both
  digests. Finally it checks both refused pushes and that the registry holds only `1.2.0` and no `todo-copy`.
  `app-registry-value` locks at one layout and deploys from another, with no warning, and the App reaches `Ready`.
  `app-template-range` covers the highest match, `^3.0.0`, which leaves the lock unchanged, the stale-lock refusal
  with empty stdout, and the re-lock.
- **ADR-0217 tests.** The tests were updated as the plan says and none was weakened. The fixture gains `app.lock`,
  and the golden output carries its digests. The removed `NotContains "@sha256:"` assertion is replaced by stronger
  digest checks. The range case becomes `todo-stats:latest`.
- **Tracking.** The only edit to the ADR is the status line. The feat row is at `reviewing`. `go.mod` is unchanged.

### Definition of Done

17 of 18 items hold. The items are ADR-0218's 9 Review-checklist items, its 3 Done items, and 6 generic items that
those lists do not already cover: real behavior, Contracts, tree, conventions, scope and tracking. The generic
contract-suite item does not apply, because `ImageResolver` has no shared contract suite. The miss is "no scope
creep" (Minor 2, model). The Done item "`just ci` and `just ci-full` green" is counted as held on this evidence:
`just ci` exits 0, and the template e2e scenarios plus the site, digest, revision and App e2e tests pass. The whole
e2e suite runs once at the PR gate, as the repository's rule asks.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0218 (implementation) → pass, 0/0/4, 2 model-attributed, DoD 17/18. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0218 moves `Reviewing → Implemented` and F121 moves to `implemented`. Before the merge, the integrator
rebases onto `main` and keeps both verbs (Minor 4). The builder can fix Minors 1 and 2 in a small follow-up. Minor 3
needs a later decision on whether `app lock` reads the old lock at all.
