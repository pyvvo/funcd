## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0326 implementation, model: claude-opus-5-5)

The work is uncommitted, in one worktree on `0924e504`:

- the code: `cmd/funcd/credentials.go`, `cmd/funcd/main.go` and `cmd/funcd/credentials_test.go`;
- the six `doc` tags in `internal/platform/config/config.go` and the generated `docs/guide/develop/running-funcd/spec.md`;
- the three rows in `docs/behavior/security.md`, and a new chapter `docs/behavior/egress.md` (with `docs/behavior/README.md`
  and `scripts/behavior/main.go`) that takes the egress rows out of the security chapter;
- the ADR file and its feat row F238, both new in the same tree.

The pass depends on the repository-wide gate (`scripts/agent/gate.sh`), which runs after this review. This review did not run
`go test ./...`, `just generate` or the e2e suite. If that gate fails because of this change, the verdict becomes
changes-requested.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The warn test does not tie each warning to its own key's path** · `model` · `cmd/funcd/credentials_test.go:807-825`,
  `TestScenarioExposedSecretFileWarns`. The test sets the six keys at once. It then checks, over the whole log, that each
  key and each path appear, and it takes the pairs from `secretFiles(cfg)` itself (line 820). The Scenario sets "in turn
  each other key", and Implementation plan step 3 "sets each key in turn". As written, an entry of `secretFiles` that reads
  another key's field passes all three tests: a `go test -overlay` mutant of `cmd/funcd/credentials.go:213` that reads
  `cfg.Backup.CredentialsFile` for `blob.credentialsFile` gave `ok github.com/pyvvo/funcd/cmd/funcd`. Such a warning would
  send the operator to the wrong file. The same whole-log check passes when only one of the six warnings carries the rule's
  text (`require.Contains(t, out, "chmod 0600")`). Today the six entries read the right fields (`credentials.go:211-216`).
  Fix: write the expected key and path pairs in the test instead of taking them from `secretFiles`, set one key at a time,
  and assert that its one warning names that key, its path and the rule's text.
  Round 2: closed. One subtest per key from the test's own table; this mutant now fails (2 warnings for backup, 0 for blob).

### Routed (not this work's)

- **Main has moved on** · home: elsewhere · destination: the integration step. `origin/main` is at `872e6d11` (#1451). That
  commit changes the `doc` tag of `runtime.containerd.imagePrefix` and its row in the same `spec.md`, not one of the six
  tags here, so a rebase should apply. After the rebase, run `just generate` and the gate.
- **The Accepted text is in no commit** · home: elsewhere · destination: the integration step. The ADR and F238 are in no
  commit of any ref (`git log --all -- 'docs/adr/0326-*'` is empty), so this review could not diff the ADR against its
  Accepted text. It read the ADR against the code instead. Plan step 3 still says "in turn", which the test does not do,
  so the text was not changed to fit the work. Commit the ADR on its own ahead of the implementation, as one-PR mode does,
  and the Egress chapter split as its own `docs(behavior)` commit.
- **No feature issue to close**: the ADR's References say that F238 takes no feature issue, like F228, so the PR carries no
  `Fixes #N`.

### ✅ Verified correct (keep it)

- **Build, vet, lint, tests.** `scripts/agent/d go build ./...`: exit 0. `go vet` on `cmd/funcd`,
  `internal/platform/config` and `scripts/behavior`: exit 0. `go tool golangci-lint run` on the same packages: `0 issues.`,
  exit 0. `gofmt -l` on the five Go files: none listed. `go test -count=1` on the three packages: `ok` for each (`cmd/funcd`
  in 54.2 s), exit 0. Under `-race`, the three new tests and `TestScenarioExposedTokenFileRefusesStart` pass; the serve test
  takes 0.33 s.
- **The ADR's three mutants fail as required** (`go test -overlay`; nothing in the tree was edited). With
  `checkTokenFile`'s verdict ignored, `TestScenarioExposedSecretFileWarns` fails (expected 6, actual 0). With every set path
  warning, `TestScenarioPrivateSecretFileIsQuiet` fails. With `serve`'s call removed, `TestServeWarnsOnAnOpenSecretFile`
  fails (expected 1, actual 0).
- **The Contracts, exactly.** `secretFile`, `secretFiles(cfg config.Config) []secretFile` and
  `warnExposedSecretFiles(cfg config.Config, euid int, log *slog.Logger)` match the ADR's signatures. The message
  `funcd: secret file open to other users`, with `key`, `path` and `problem`, matches the ADR's example line.
- **Decisions 1 to 3.** The list holds the six keys in Decision 1's order and drops the unset ones
  (`credentials.go:209-225`); a key whose feature is off is not filtered out. `os.Stat` follows symlinks, a path that does
  not stat is skipped, and `checkTokenFile` is reused unchanged: the diff only adds lines after it. Its text has no
  "token file" prefix, since that prefix belongs to `readTokenFile`. The function returns nothing, so no file stops the
  start, and it never opens a file, so no content can reach the log.
- **Placement.** `serve` calls it once (`main.go:184`), right after `root := logger.Root()` and its deferred error
  wrapper, and before `startSpace`, `safeModeStart` and every store.
- **The scenarios.** Each has a named test with its `// scenario:` marker, un-skipped and passing. The another-owner case
  runs as non-root with euid + 1 and has a root branch (a chown to uid 1). The quiet test covers 0600, 0400, an unset key and
  a missing path. The serve test runs the real `serve` on a free port with a short data dir, through the daemon's own
  logger, and proves a served API call after the warning.
- **Decision 4 and the generated page.** The six `doc` tags carry the same sentence, and `spec.md` changes in exactly those
  six rows. `TestScenarioConfigSpecGenerated` passes without `-update`, so the page is current.
- **The behavior rows.** The three rows in `security.md` match the ADR's table, with `ADR-0326` and `ADR-0171` written before
  the sources. `TestScenarioExposedTokenFileRefusesStart` exists (`credentials_test.go:640`). `scripts/agent/d just
  check-behavior`: exit 0.
- **The Egress split is needed, and the move is verbatim.** At `0924e504` the written part of `security.md` was 16,360
  bytes against `maxChapter` 16,384 (`scripts/behavior/main.go:34`), and the three rows add about 850 bytes. The check's own
  remedy is "cut a row or split the chapter" (`scripts/behavior/main.go:1029`). All 34 removed rows and values, the six
  removed lines of the state machine and the removed prose reappear unchanged in `egress.md`. The new lines are the
  chapter's frame (its title, Kinds line, table headers and generated-block markers) and one link each way. `chapters()`
  gains `{"Egress", []string{"internal/network"}, nil}`, which matches the new index row in `README.md`, and no other page
  linked the security chapter for egress.
- **Scope and conventions.** No new key, flag, state or dependency (`go.mod` and `go.sum` are untouched); `log/slog` only;
  the comments cite the ADR and say why. `scripts/agent/d just check-hygiene`: `hygiene: clean`.
- **Tracking.** The ADR was at `Reviewing` and F238 at `reviewing`. Neither the number 0326 nor F238 is used in another ref.

### Definition of Done

16 / 16 items hold: the ADR's 6 Review-checklist items, its 4 Done-when items (the three mutants, and the packages' tests,
vet, lint, generated page and `just check-behavior`), and 6 generic items (named scenario tests, the Contracts, the
conventions, no new dependency, no scope creep, tracking). The `just ci` item is left to the repository-wide gate, as stated
above.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0326 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 16/16. See
docs/reviews/model-scorecard.md.

### Recommendation

The ADR is stamped `Implemented` and F238 `implemented`. The Minor is a change to the test only, which the builder can make
before the PR; then rebase onto `origin/main` and run the gate.
