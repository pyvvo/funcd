## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #850 fix, model: claude-opus-5-5)

This report reviews branch `fix/850-master-under-data-dir`, commit 15353eff
`fix(funcd): keep the node master secret under storage.dataDir with the S3 gateway off`
(`git diff origin/main...HEAD`: `cmd/funcd/main.go`, `cmd/funcd/main_test.go`, `examples/funcdconfig.yaml`,
`internal/blob/s3gateway/s3gateway.go`, `internal/blob/s3gateway/issue850_test.go`, `pkg/funcd/funcd.go`,
`pkg/funcd/options.go`). Issue: with the S3 gateway off, the node master secret ignores `s3gateway.masterSecretFile`
and `storage.dataDir` and is created under the working directory. Governing ADRs: ADR-0204 Decision 7 (master
location and migration), ADR-0085 (master secret), ADR-0137 (catalog tokens derived from the master), ADR-0002.

### Minor
- **The platform-level migration is not covered by a test** · attribution: model · evidence: mutant M3 deletes the
  `s3gateway.MigrateMaster(...)` call in `buildControlPlane` (`pkg/funcd/funcd.go`); `go test -run
  'TestIssue850|Master|S3' ./cmd/funcd/ ./pkg/funcd/` stays `ok` for both packages. The seven `issue850_test.go`
  tests cover `MigrateMaster` as a function, and the `cmd/funcd` tests cover the location, but nothing starts a node
  with a working-directory key. ADR-0204's scenario `master-key-migrates` is stated at node start ("When it starts on
  this build … its catalog tokens stay"). Without the call, an upgraded gateway-off node would silently generate a
  new master and its catalog tokens would change. The behavior is correct today: I ran the real daemon (below). Fix:
  in `cmd/funcd/main_test.go`, write a key to `<cwd>/s3gateway/master.key` before `addressesFromConfigFile` and
  assert that the data-dir key equals it.
- **The `masterSecretFile`-set case is an interpretation of Decision 7** · attribution: adr · evidence:
  `MigrateMaster` returns early with only a warning when `masterSecretFile != ""`
  (`TestIssue850_MasterSecretFileNeverMigrates`). Decision 7 says "both present and different ⇒ `fault.Invalid`"
  and its rationale row says the node "never picks between two keys". With `masterSecretFile` set on a gateway-off
  node that issued catalog tokens from a working-directory key, the fix uses the operator's file, so those tokens
  change after the upgrade with only a warning. The reading is defensible, because the refusal message names
  `s3gateway.masterSecretFile` as the way to choose, so a set file is the operator's choice. The commit body says
  these defaults were "settled by the ADR-0204 owner"; the review found no record of that in the ADR or the issue.
  Decision 7's migration is itself marked "accepted as default, not confirmed by the decider". Recorded for the
  decider to confirm, not scored. The same applies to the in-memory master when neither `dataDir` nor
  `masterSecretFile` is set, which Decision 7 does not cover; the production default `storage.dataDir` is
  `/var/lib/funcd`, so only the InMemory preset, tests and programmatic callers reach it.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** Overlay of the `origin/main` versions of
  `cmd/funcd/main.go`, `pkg/funcd/funcd.go` and `internal/blob/s3gateway/s3gateway.go` (`go test -overlay`; the new
  `options.go` option is kept so the test compiles, and the pre-fix `main.go` never calls it):
  `TestIssue850_GatewayOffMasterUnderDataDir` fails with "Expected error with "file does not exist" … a gateway-off
  node writes no master into the working directory", and `TestIssue850_InMemoryMasterNotWritten` fails with
  "Should be empty, but was [d s3gateway/]". With only `cmd/funcd/main.go` reverted, the first test fails on "a
  gateway-off node keeps its master under storage.dataDir".
- **It passes with the fix under `-race`**: both `cmd/funcd` tests and all seven `internal/blob/s3gateway`
  `TestIssue850_*` tests pass, none skipped.
- **The user-visible behavior is fixed** (real daemon, `storage.mode: memory`, a set `storage.dataDir`, the gateway
  off): started from directory A holding a legacy key, the daemon logs "copied the working-directory master secret
  under the data dir; remove the old file" with `from` and `to`, and the data-dir key is 0600 and byte-equal to the
  legacy key. Restarted from directory B, it creates nothing in B and the data-dir key is unchanged. With a different
  key placed in B, startup fails with `fault.Invalid`: "master secrets … differ: keep one at … or set
  s3gateway.masterSecretFile".
- **Mutants**: M2 (delete the `sameFile` check) → `TestIssue850_WorkingDirEqualToDataDirIsOneKey` and
  `TestIssue850_MasterSecretFileNeverMigrates` fail. M5 (drop `gatewayOn` from the keep-and-warn branch) →
  `TestIssue850_GatewayOnKeepsMaster` fails. M3 survived (see the first Minor).
- **The fix addresses the cause.** The issue's cause is that `cmd/funcd` passes the master file and data dir only
  through `WithS3Gateway`, while `buildControlPlane` loads the master whatever `s3gwEnabled` says. The new
  `WithMasterLocation` is set unconditionally in `buildOptions`, and `LoadOrCreateMaster` no longer joins an empty
  data dir into a relative path. No timeout, retry or swallowed error is involved.
- **ADR-0204 Decision 7 conformance.** The location is `masterSecretFile`, else `<dataDir>/s3gateway/master.key`,
  gateway on or off. The gateway-off migration copies with 0600 and warns naming both paths, leaves the old file,
  refuses two different keys with `fault.Invalid` naming both paths and `s3gateway.masterSecretFile`, and compares
  with `subtle.ConstantTimeCompare`. A gateway-on node keeps its key and warns. Paths compare after `filepath.Abs`
  and `EvalSymlinks`, and the symlink case is tested. No ADR file was edited. ADR-0085's "a supplied file that does
  not exist is an error" is unchanged.
- **Scope.** Every hunk serves the issue or Decision 7's migration. The `examples/funcdconfig.yaml` comment and the
  `WithS3Gateway` doc now state the gateway-on-or-off location. The `addressesFromConfigFile` signature change only
  returns the data dir; its two existing callers discard it. No test was weakened or deleted.
- **Reuse.** `writeMaster` is extracted from `LoadOrCreateMaster` and shared with the migration rather than copied.
  No path-identity helper exists in the module (the only other `EvalSymlinks` use is `internal/artifact/bundle.go`,
  which serves another purpose), and the ADR names `Abs` + `EvalSymlinks`. The tests reuse testify and `t.Chdir`.
- **Siblings.** The other `storage.dataDir`-derived paths in `buildOptions` (artifacts, invoke, tls, process, pool,
  shims) are set unconditionally or default to a temp dir. No sibling with the same cause was found.
  `cmd/funcdctl/dev.go` always passes a master file to `WithS3Gateway`, so it is unaffected.
- **Conventions.** `api/fault` kinds (`Invalid`, `Internal`) with `op`, slog only, ctx-free helpers, imports at the
  top, `//nolint:gosec` lines carry a reason, and the comments name the ADR they implement rather than narrate.
- **Checks.** `go build ./...` ok; `gofmt -l` on the three packages is empty; `go vet` darwin and `GOOS=linux` ok;
  `golangci-lint run` darwin and linux → 0 issues; `go test -race -count=1 ./cmd/funcd/ ./pkg/funcd/
  ./internal/blob/s3gateway/` → all ok; the linux test binaries compile. Per the review scope, the e2e suite, the
  repo-wide tests and the Lima lanes were not run; the PR gate runs them.
- **Shape.** One commit, `fix(funcd):` subject, `Fixes #850`, `Refs ADR-0204 Decision 7`, the attribution trailer,
  author green-0-rabbit.

### Definition of Done
11 / 12 items hold. Miss: item 4 (mutant M3, deleting the platform's `MigrateMaster` call, survives; model, Minor).
Item 8 holds for the touched packages; the gate covers e2e.

### Model scorecard
Recorded: claude-opus-5-5 on #850 (fix) → pass, 0/0/2, 1 model-attributed, DoD 11/12.

### Recommendation
Ready for the PR. Optionally add a node-start test with a working-directory key, which kills mutant M3. Ask the
decider to confirm the two defaults outside Decision 7's text: a set `masterSecretFile` wins over a different
working-directory key with a warning, and no data dir means an in-memory master.
