# The backup target's bounds, fix review — claude-opus-5-5

## Verdict: changes-requested — 0 blockers, 2 majors, 4 minors (the backup target's bounds, model: claude-opus-5-5)

Reviewed `bfa7f73d`, `bbe98376` and `82171224` on `ship/backup-members-2`, on base `2f5fe853`, built by another session. The fix has
no public issue, so its tests are named for their behavior and its commits carry no `Fixes #N`.

The bounds do what they say for every error answer and for everything a real S3 store answers: a Backup, a Restore and a sync on a real
store came nowhere near a limit, and every answer kept its meaning. Two gaps remain in what the commits state. One GET's answer is read
uncut, and one listing's bytes are not bounded by its two limits. Both are small to close.

### 🔴 Blockers

None.

### 🟡 Majors

- **One GET's answer is read uncut** · attribution: model · `internal/blob/gocloud/answers.go:34-39`. `cappedAnswers` decides what an
  object's content is by exclusion: any 2xx GET without `list-type`. This client can send a third GET, the S3 client's `CreateSession`, so
  it reads that answer whole, against `bfa7f73d`'s "at most 64 KiB of any answer but an object's content". A probe of the client shows
  the whole answer read. Fix: name the two kinds instead. `awsmiddleware.GetOperationName(r.Context())` gives `GetObject`, which stays
  uncut, and `ListObjectsV2`, which takes `maxListPageBytes`; every other answer takes `maxAnswerBytes`. With that change as an overlay,
  every new test and the real-store flow below pass as they are. Add a test for that answer.
- **What one listing reads is not bounded by its two limits** · attribution: model · `internal/blob/gocloud/gocloud.go:512`,
  `internal/backup/workload/copies.go:167`. `maxListPageBytes` bounds one store page, and `MaxListedKeys` counts keys after `ListAfter`
  returns. Inside `ListAfter`, gocloud's `ListPage` goes on asking until it holds 1000 keys and keeps each object's strings until then,
  so a store that cuts its pages short is read for many pages of up to 16 MiB. A listed key also has no bound on its length below the
  page's, where S3 stops at 1,024 bytes. A probe of the client shows the bytes held growing with each store page. Fix: one bound across
  the store pages of a `ListAfter`, and a listed key over 1,024 bytes failing its page, in the same middleware as the time check.

### Minors

- **The time check's test holds one object** · attribution: model · `internal/blob/gocloud/answers_internal_test.go:212`. A mutant that
  looks only at a page's first object survives (`first-object-only`). The code is right: a probe with a second object with no time fails
  its page. Fix: put an object with a time first in that test's page.
- **Three comments say less than the code** · attribution: model.
  - `internal/backup/workload/copies_test.go:112`: the new code went in below this comment, so it now sits above `pagedKeys`, and
    `TestListCopy_SkipsAKeyNoKindParses` (`:150`) lost it.
  - `internal/blob/gocloud/gocloud.go:76-78` gives `maxAnswerBytes` for every answer but an object's content, while `bbe98376` gave a
    listing page its own limit.
  - `internal/blob/gocloud/answers.go:21`: "A page of 1000 keys takes under 2 MiB". A real store's page of 1000 keys of 627 bytes, each
    all `&`, took 3.3 MiB. The limit is right, and only the sentence is not.
- **`ListFailed` is documented as a refusal** · attribution: model · `examples/workload-backup.md:148`,
  `api/types/v1alpha1/backup.go:184`. Both say "while the target refuses the listing". A listing over the key limit now gives the same
  reason from a target that answers, and "point `spec.target.url` at a target that answers" does not describe its way out. Fix: name the
  limit in the runbook, with that way out.
- **The key limit is a new policy** · attribution: adr, not scored · `internal/backup/workload/copies.go:21`. `CLAUDE.md` asks for an ADR
  for a fix that adds a limit. ADR-0287 Decision 3 sets none, and this contradicts nothing in it. Of the three limits, this is the one a
  real target could meet, so it should be recorded as a decision. The decider owns the call: an ADR, or a recorded exception.

### ✅ Verified correct (keep)

- **Checks on `82171224`, run by me on the touched packages** (`internal/blob/gocloud`, `internal/backup/workload`,
  `internal/services/backup`, `pkg/funcd`):
  - `go build ./...`: exit 0.
  - `go vet` and `golangci-lint` with no tag and with `dev`, `e2e` and `integration`, and with `GOOS=linux`: exit 0, 0 issues.
  - `go test -race -count=1`: ok in all four packages (29.8 s, 3.7 s, 2.1 s and 48.0 s).
  - The new tests under `-race`, `-count=5`, and `-count=3` in `pkg/funcd`: ok. The slowest new test takes 0.24 s.
- **Not run, as agreed:** the gate, `just ci-full`, the e2e suite, `go test ./...` and any lane. They run once on the PR's tip.
- **Each commit's tests against its own change.**
  - `bfa7f73d` with the base's `gocloud.go`: `TestStaticKeyAnswers_ReadNoMoreThanTheLimit` fails on `GetRange`, `Put` and `ListAfter`.
    `TestStaticKeyAnswers_KeepTheirStatusAndCodeUpToTheLimit` passes either way, as it holds what must not change.
  - `bbe98376` with each change taken out: `TestStaticKeyAnswers_BoundAListingPage` fails without the page limit, and
    `TestScenarioUnreachableTargetRetried` fails without the target in the error. `TestSlotCopies_RefusesAListingOverItsLimit` and
    `TestListCopy_RefusesAListingOverItsLimit` fail without the key limit, on `82171224`.
  - `82171224` with `bbe98376`'s `gocloud.go`: `TestStaticKeyAnswers_FailAListedObjectWithNoTime` and
    `TestBackup_GoesOnWhenAListedObjectHasNoTime` fail.
  - Each commit's own tests pass on its own tree under `-race`.
- **A real S3 store.** I built versitygw v1.9.0, the S3 server of the lanes, and put a proxy that counts each answer's bytes in front of
  it. On the static-key client:

  | Answer | Largest |
  |---|---|
  | An error, of any call (a 403 `SignatureDoesNotMatch` on a PUT) | 3,899 bytes |
  | `CreateMultipartUpload`, `CompleteMultipartUpload` | 262 and 415 bytes |
  | A listing page of 1000 keys, in funcd's key shape | 357,547 bytes |
  | A listing page of 1000 keys of 627 bytes, each all `&` | 3,301,365 bytes |
  | `GetObject`, a 206 by range, read whole | 41,943,040 bytes |

  - **Each call:** a HEAD, a GET and a ranged GET of an 8 MiB part, a 40 MiB multipart upload read back, and a conditional PUT on an
    existing key (412, `fault.Conflict`).
  - **Each error:** a missing copy (404, `fault.NotFound`, on HEAD, GET and ranged GET), a missing bucket (404), a wrong secret and an
    unknown key id (403, with their codes), and a range past the end (416).
  - Every result is identical, line by line, to a run with the code before the three commits.
  - **Through the rig, on the same store:** a Backup by hand and a slot's Backup of a KV store and a Bucket with a 20 MiB object, a
    Restore `as` that reads both back, and a sync that lists a copy of 2,500 keys and records it. These made nine ranged GETs, the largest
    8 MiB, and five listing pages.
- **The limits against a legitimate target.**
  - **64 KiB:** the largest real error is about 17 times smaller. A longer one only loses precision (below).
  - **16 MiB a page:** S3 keys stop at 1,024 bytes, so 1000 keys of all `&` take about 5.2 MiB by arithmetic, a third of the limit.
  - **1 Mi keys:** a small copy takes about five keys, so one listing holds about 200,000 copies: 23 years of an hourly schedule, or 145
    days of a minutely one. A sync lists only the copies after its restored point. Over the limit, the message names the limit.
- **A failed listing, through the rig.**
  - **A schedule's sync:** I fed it a page with no time, a listing over the key limit (1,049 pages, 3 s), a page over 16 MiB and a 403. Each
    time, `TargetSynced` read `False`, `ListFailed`, with the target and the cause in its message, and a Warn was logged. No slot was taken
    once the hold was released. When the store answered again, `TargetSynced` read `True`, the copy was recorded and the slot due was
    taken.
  - **A Backup by hand** went on with its own copy and reached `Succeeded` on a page with no time (its schedule's sync pending or not), a
    listing over the key limit and a page over 16 MiB. By reading, `run` and `settle` treat a failed listing as ADR-0287 Decision 5 has it.
- **A cut answer keeps its meaning.** A 403 of 64 KiB plus one byte keeps its status and maps to `PermissionDenied`, a 404 stays
  `fault.NotFound`, and a 500 or 503 is still tried three times. At 64 KiB exactly, the code and message are kept. A 403 with a 256 MiB body
  allocates 0.51 MiB on `GetRange`, 0.57 MiB on `Put` and 0.51 MiB on `ListAfter`, as `bfa7f73d` states.
- **Every GET of a copy goes through the content path.** `ReadManifest` and `newPartReader` read with `readAtMost`, which makes a ranged
  `GetObject`. Its `Get` is only for a bucket with no `RangeReader`, and `openTarget` always gives one. No other call on a target reads an
  object.
- **A credentials file's client stays as it was.** `openTarget` is the only caller that sets `Static`; every other `OpenWith` passes a credentials
  file, and `Open` does not reach `openS3With`. `TestCredentialsFileAnswers_AreNotCut` pins the right thing for the answers: with the cap on
  both clients it fails. On that client a 200 KiB error kept its code and a 20 MiB page was read. The time check is not pinned there: its
  mutant `times-on-both-clients` survives. I ask for no test, as such a mutant changes only how a listing with no time fails on that
  client.
- **The time check.** It runs on every `ListObjectsV2` answer of the static-key client, and fails as a failed listing does, with no retry.
  An empty or a bad time already fails in the S3 client.
- **Mutants.** 21 of my 23 fail a test. They cover both caps, each limit by one, each status and method test, the close, the middleware's
  place, the key count and the error's target. A range cut at 64 KiB is caught by `TestWriteSQLDatabase`, whose stub answers 206. The
  survivors are `first-object-only` (a Minor above) and `times-on-both-clients` (above).
- **Scope and reuse.** Eight files, and each hunk serves a stated bound. No test was weakened: `TestScenarioUnreachableTargetRetried` only
  gains an assertion. `go.mod` is unchanged, and the fix uses `io.LimitReader`, `slices` and `api/fault`.
- **ADRs.** No ADR file is edited. ADR-0287 Decisions 2, 3, 5 and 6 hold, with `ListFailed` and its retry unchanged. ADR-0184's `ListAfter`,
  ADR-0203's credentials file and ADR-0222 Decision 6's static key keep their contracts, and nothing here touches ADR-0311's or ADR-0312's
  member kinds.
- **Shape.** Three `fix(backup):` commits, one concern each, with the attribution trailer. The text describes behavior only.

### Fix checklist

8 of 12. Items 1, 2, 3, 6, 8 (for what ran here), 9, 10 and 11 hold. Item 4 misses `first-object-only`, item 5 and item 12 miss the two
Majors, and item 7 misses the `ListFailed` text.

### Model scorecard

Recorded: claude-opus-5-5 on backup-target-bounds (fix) → changes-requested, 0/2/4, 5 model-attributed, DoD 8/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Back to `/fix` for the two Majors, with the Minors in the same round. Everything verified above should stay as it is. The gate and the
lanes run once on the PR's tip, after that round.
