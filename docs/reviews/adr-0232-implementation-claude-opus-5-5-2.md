## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0232 implementation re-review, model: claude-opus-5-5)

Re-review of the working tree of `feat/sql-provider` against `43ae961e` after the first review
([adr-0232-implementation-claude-opus-5-5.md](adr-0232-implementation-claude-opus-5-5.md), changes-requested).

**Stamped**: ADR-0232 `Reviewing → Implemented`, FEAT-0011 F128 `reviewing → implemented`. The coordinator moves the
board card.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| gofmt | `gofmt -l cmd internal pkg api` | empty, exit 0 |
| build | `GOOS=linux go build ./...` | exit 0 |
| touched packages, race | `go test -race -count=1` on `internal/services/sql/...`, `internal/controlplane/...`, `api/...`, `cmd/funcd-rqlite`, `cmd/funcdctl`, `internal/platform/...`, `internal/runtime/embedimg`, `pkg/sdk`, `internal/provider`, `pkg/funcd`, `cmd/funcd` (e2e tagged out) | all `ok`, exit 0 |
| scenario tests | the 8 `TestScenario…` tests in `pkg/funcd`, `-v` | 8 × PASS, 0.17 s to 0.49 s each |
| vet | `go vet` on the same packages | exit 0 |
| lint | `go tool golangci-lint run` on the same packages | `0 issues.` |
| ADR diff | `git diff 43ae961e --stat -- docs/adr/` | the status line only |

`just smoke-rqlite-image` was not rerun: the fixes touch no file the image is built from. It printed
`smoke-rqlite-image: ok` in the first review.

### What the first review's findings became

- Major 1 (gofmt in `metadata.go`): fixed; the tree is gofmt-clean.
- Minor 1 (misplaced doc comments): fixed. In `internal/platform/config/config.go` the `Site` comment is on `Site` and
  `SQL` has its own comment after it; the `Evidence` comment in `internal/platform/hold/hold.go` is one reflowed
  paragraph.
- Minor 2 (Contract signature): fixed. `NewSQLDatabaseDeletionProtectionAdmission(r StoreReader, p SQLProber)` matches
  the Contract; `pkg/funcd/funcd.go:1214` and the admission test pass the reader. The admission stores `r` but does
  not read it yet, like a parameter kept for the delete-while-bound rule that ADR-0233 adds.
- Minor 3 (`env`, the containerd shim's memory per engine): still open, as expected. It is measured on the `sql` lane
  under F129. Not scored.

### ✅ Verified correct (keep)

Everything listed in the first review still holds: the kind threaded through every site, the engine spec, one teardown
per generation change, status written only on change, delete and orphan reclaim only when not held, `sqlOrphans` in
hold status, the fail-closed deletion probe, the quota defaults, the entrypoint's auth file and arguments, and the
pinned static image with no shell, running as 65532.

### Definition of Done

14 / 14 items hold (the ADR's 9 Review-checklist items and 5 Definition-of-done items).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0232 (implementation, re-review) → pass, 0/0/1, 0 model-attributed, DoD 14/14.
See `docs/reviews/model-scorecard.md`.

### Recommendation

Done. The open shim-memory measurement goes with the F129 `sql` lane.
