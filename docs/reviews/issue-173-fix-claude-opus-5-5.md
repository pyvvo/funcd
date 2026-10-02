## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #173 fix, model: claude-opus-5-5)

The fix is commit `b51b196` (`fix(sensor): treat a null action input as absent so the target gets the event data`)
on the eventing group branch. It touches `internal/sensor/sensor.go` and `internal/sensor/sensor_test.go`.
This review covers only that commit. The review worktree was detached at the group head `39e677e`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The `staticCheck` hunk is behavior-neutral, but the commit message presents it as part of the defect** ·
  attribution: `model` · evidence: the overlay mutant that restores `len(a.Input) == 0` in `staticCheck`
  (and keeps `inputAbsent` in `buildInput`) passes the whole `internal/sensor` suite
  (`ok github.com/pyvvo/funcd/internal/sensor 7.690s`). This mutant is equivalent, not a test gap: before
  the fix, `json.Unmarshal("null", &fields)` succeeds and leaves a nil map, so the field loop did nothing
  and `staticCheck` already accepted a null input correctly. The commit message says that both `buildInput`
  and `staticCheck` "only treated an empty value as absent, so null took the object branch and unmarshalled
  to an empty map", but only `buildInput` produced the wrong `{}`. Keep the hunk, because it keeps the
  two absent checks in step through one helper. No code change is needed; a reworded message is optional.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit b51b196`
  applied cleanly on `39e677e` (no conflict with the later group commits). With the test file restored,
  `TestIssue173_NullInputPassesEventData` failed under `-race`: `expected: map[string]interface {}{"key":"drop/a"}`,
  `actual: map[string]interface {}{}`, message "a null input is an absent input: the run gets the event data".
  This is the `{}` that the issue reports.
- **It passes with the fix.** After `git reset --hard 39e677e`:
  `--- PASS: TestIssue173_NullInputPassesEventData`, and `go test -race -count=1 ./internal/sensor/...` → `ok`.
  The test is not skipped. It covers both a `workflow:` action (the run's `spec.input`) and a `function:`
  action (the delivered CloudEvent's `data`), which are the two paths that the issue names.
- **The user-visible behavior is fixed, from a real manifest.** A scratch probe (not committed) decoded a
  YAML Sensor manifest with `sdk.DecodeManifest`, the decoder that `funcdctl apply` uses, in four forms:
  a bare `input:`, `input: null`, `input: ~`, and an omitted key. The three null forms decode to `"null"`,
  and the omitted key decodes to `""`. With the fix, all four start a run whose input is `{"key":"drop/a"}`.
  With the fix reverted, the three null forms give `{}` and the omitted key gives the event data. This
  reproduces the issue's own probe result.
- **The root cause is fixed, not masked.** The cause is the `len(raw) == 0` check in `buildInput`, which sent
  the 4-byte `null` value into the object branch. The new `inputAbsent` helper treats an omitted or null
  input as absent. ADR-0109 Decision 4 says "`input` absent ⇒ the event's `data` verbatim", and the scenario
  `input-absent-passes-event-data` describes the same behavior. The fix adds no retry, timeout or swallowed
  error. The exact string match on `"null"` is sufficient: `json.RawMessage` carries the value token
  without surrounding whitespace, and the manifest path produces exactly `null`.
- **Mutants.** (1) `len(raw) == 0` restored in `buildInput` only → `TestIssue173_NullInputPassesEventData`
  fails. (2) `inputAbsent` always returns `true` → `TestInputProjection`, `TestBadStaticInputNotReady` and
  `TestIssue113_FunctionActionReceivesProjectedInput` fail. (3) The `staticCheck` mutant survives as an
  equivalent mutant (see the Minor finding above).
- **Scope.** Each hunk serves the issue: the two call sites, the helper, and the test. No test was weakened or
  deleted. The commit does not touch the refuted part of the issue (nested and interpolated `${{ }}` values),
  which is correct, because ADR-0109 §1/§4 and ADR-0095 define those values as literals.
- **Reuse.** The repository has no shared helper for a JSON-null raw value. `internal/contract/schema.go`
  writes the same `len(b) == 0 || string(b) == "null"` idiom inline in two unexported `UnmarshalJSON`
  methods, and the sensor package does not depend on that package. A three-line private helper with one
  name, used at both of the package's call sites, is the right size. The test reuses the existing
  `harness`, `createSensor`, `fire`, `runs` and `fakeInvoker` helpers.
- **Conventions.** The helper is unexported, has no `any`, and needs no new imports. Its doc comment states
  the *why* (the YAML forms that produce null) and does not narrate the code. The test follows the
  `TestIssue<N>_…` naming and uses the `require` idiom of its neighbours. `gofmt -l internal/sensor` is empty.
- **ADRs.** No ADR file was edited. The change makes the code conform to ADR-0109 Decision 4 and contradicts
  no Accepted or Implemented ADR. The `Action.Input` field comment in `api/types/v1alpha1/sensor.go`
  ("absent ⇒ the event data verbatim") stays true.
- **Checks** (all through `nix develop -c`):
  - `go build ./...` → ok
  - `go vet ./internal/sensor/` → ok, on the host and with `GOOS=linux`
  - `golangci-lint run ./internal/sensor/...` → `0 issues.`, on the host and with `GOOS=linux`
  - `go test -race -count=1 ./internal/sensor/...` → ok
  - `go test -tags e2e -count=1 ./pkg/funcd/...` → `ok github.com/pyvvo/funcd/pkg/funcd 115.378s`. The
    Sensor, DeadLetter, Blob and Dataplane e2e subset was rerun on the verified-clean tree → ok.
  - `just check-hygiene` → `hygiene: clean`
  - The Lima lanes were not run, as the batch instructions require. A later stage owns them.
- **Shape.** The commit is one issue per commit, with the subject `fix(sensor): …`, `Fixes #173`, the
  regression test named in the body, and the `Co-Authored-By` trailer.

### Definition of Done

11 / 11 items hold (the fix checklist). Misses: none. The Minor finding above does not fail an item: item 4
holds, because reverting or mutating the key line in `buildInput` fails a test.

### Model scorecard

Not recorded by this stage, as the batch instructions require. The ledger fields are: issue 173, phase fix,
model claude-opus-5-5 → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation

Sign off. The fix removes the cause at the one place that produced `{}` and is proven by a test that fails for
the reported reason. Rewording the commit message's `staticCheck` claim is optional.
