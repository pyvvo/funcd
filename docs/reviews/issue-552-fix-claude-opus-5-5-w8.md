# Fix review — issue #552 (move the funcdctl dev hot-reload watcher into its own file)

- **Issue**: #552, kind/task — move the hot-reload watcher out of `cmd/funcdctl/dev.go` and fold its two size+mtime checks into one helper.
- **Change**: branch `fix/w8-i552`, commit 9448e06 `refactor(funcdctl): move the dev hot-reload watcher into dev_reload.go`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 0 Minor (0 model-attributed)
- **Checklist**: 8 of 8 applicable items hold (items 1–3, the regression test items, do not apply to a task that adds no `TestIssue552` test)

## Verification run

| Check | Result |
|---|---|
| Pure move | The sorted, non-blank lines of `origin/main:cmd/funcdctl/dev.go` were compared with `dev.go` + `dev_reload.go` on the branch. The only differences are the new file's header (build tag, package, imports), `infoStamp`, and the call sites that now use it. No other line changed. |
| Fingerprint bytes unchanged | Before: `"%s\x00%d\x00%d\n"` (path, size, mtime). After: `"%s\x00%s\n"` with stamp `"%d\x00%d"` (size, mtime). The hashed bytes are identical, so `spec.imageDigest` does not change across the refactor. The manifest and entry still go through `os.Stat` (now inside `fileStamp`). |
| Tests | `go test -race -tags dev ./cmd/funcdctl/...` → ok. No test file is in the diff. |
| Vet | `go vet -tags dev ./cmd/funcdctl/...` → clean; `go build` + `go vet` without the tag → clean |
| Lint | `golangci-lint run --build-tags dev ./cmd/funcdctl/...` → 0 issues |
| Mutant A: `infoStamp` returns `""` | Killed: `TestIssue135_DevHotReloadsEditedHandler` and `TestIssue428_DevReloadsEditedWorkflow` fail |
| Mutant B: walked files are not added to the fingerprint | Killed: `TestIssue320_DevHotReloadsImportsAndManifest` fails |
| Mutant C: `reapply` never sees a changed stamp | Killed: `TestIssue428_…` and `TestIssue501_DevReloadDeletesRenamedWorkflowAndRemovedStep` fail |
| Worktree | Clean after the review; the mutants ran as `-overlay` files outside the tree, and a stray build binary was removed. |

## "Done when"

1. **The hot-reload functions live in their own file**: `applyDesired`, `stageResources`, `keepDropped`, `devHandler` and `fingerprint`, `watchHandlers`, `reloadChanged`, `devWorkflow`, `fileStamp`, `reapply` and `pruneRemoved` are all in `cmd/funcdctl/dev_reload.go`, which has the same `//go:build dev` constraint. `dev.go` dropped from about 2000 lines to 1658. It still holds the log styler and the shim wiring. The issue did not list those for the move, so this is not a finding. ✅
2. **One helper produces the size+mtime stamp**: `infoStamp(fs.FileInfo)`. `fileStamp(path)` stats the file and returns `infoStamp`; `fingerprint` uses `fileStamp` for the manifest and entry and `infoStamp` for every walked file. ✅
3. **The dev tests pass unchanged**: yes, with `-race`, and no test file changed. ✅

## Findings

### Blocker
None.

### Major
None.

### Minor
None.

## Verified correct

- **Scope**: the change follows the person's decision. It is a move plus one folded helper; the bodies of `bootDev` and `reloadChanged` are untouched (they are left for #551), and no other hunk was added.
- **Reuse**: `infoStamp` replaces two duplicated formatters and does not reinvent anything. A search found no existing size+mtime helper in the package or its neighbours.
- **Conventions**: imports are at the top level, the comments on the new helper and the moved code are not bloated, ADR-0002 holds, and the new file mirrors `dev.go`'s build constraint.
- **ADRs**: the hot-reload behaviour of ADR-0125 is unchanged, and no ADR file was edited.
- **Commit shape**: one commit with a `refactor(funcdctl):` subject (the right type for a task with no behaviour change), `Fixes #552`, and the attribution trailer.
- **New defects**: none found on the way.

## Recommendation

Pass. The change can go to the group integrator.
