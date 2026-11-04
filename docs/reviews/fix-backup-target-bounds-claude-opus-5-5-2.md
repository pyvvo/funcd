## Verdict: pass — 0 blockers, 0 majors, 2 minors (the backup target's bounds, re-review, model: claude-opus-5-5)

This report reviews `f5482719..79620e4c` on `fix/backup-target-bounds`: six commits that answer the first review
(`docs/reviews/fix-backup-target-bounds-claude-opus-5-5.md`). `git range-diff` shows that `574eaba1`, `65e883aa` and `f5482719` equal the
three commits the first review read.

Both Majors and the first review's three model Minors are closed, and nothing it marked correct has regressed. The new limits leave a real
store's normal use alone, a spent budget ended in an error in every form I could build, and a scope of exactly 1024 members works from its
Backup to its Restore and its sync. The two Minors below are one sentence and one test's guard. The pass holds if the gate and the lanes are
green on the PR's tip: they run there, and did not run here.

### ✅ The first review's findings

| Finding | Now |
|---|---|
| Major: one GET's answer read uncut | `cappedAnswers` goes by the SDK's operation name. A 2xx `GetObject` stays uncut, a 2xx `ListObjectsV2` spends the listing's budget, and every other answer stops at 64 KiB. My first probe now reads about 64 KiB of a session's answer of 256 MiB and allocates 0.3 MiB on the client. |
| Major: one listing's bytes | All the store pages of one `ListAfter` share 16 MiB, and each page spends at least 4 KiB. One listing also stops at 256 MiB of key bytes. My first probe of one long field per store page now fails on its first page, with a peak of 98 MiB. At 1 MiB a page it fails after 16 pages, with a peak of 43 MiB. Pages with no key end after 3,935 requests. |
| Minor: the time check's test | It holds a later object. `first-object-only` now fails it. |
| Minor: three comments | The ADR-0311 comment is back above `TestListCopy_SkipsAKeyNoKindParses`. `OpenOptions.Static` says the client bounds its answers. The sentence in `answers.go` is gone. |
| Minor: `ListFailed` text | `examples/workload-backup.md` and `api/types/v1alpha1/backup.go` say the listing fails. The runbook names the limits and the way out. |
| Minor, `adr`: the key limit | Decided: a recorded exception, not an ADR. It names the three limits a real target could meet: 1 Mi keys, 256 MiB of key bytes and 1024 members. |

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **A number in a commit message** · attribution: model · `9a804564`. It says a listing at the key limit holds "about 640 MiB, in the shape
  that holds the most". A probe of `SlotCopies` with each key in its own copy directory measured a larger shape: 1 Mi keys of 256 bytes, at
  both limits, peaked at 903 MiB. Short keys at the key limit peaked at 678 MiB, and 16 KiB keys at the byte limit at 310 MiB. The limits
  stand, and only the sentence is off. Fix: leave it out of the PR body, or give about 900 MiB.
- **A test's guard sits close to its run time** · attribution: model · `internal/blob/gocloud/answers_internal_test.go:244`.
  `TestStaticKeyAnswers_EndAListingOfEmptyPages` gives its `ListAfter` 5 s. It takes 0.29 s here, and 1.4 s under `-race` with two CPUs. On
  a loaded runner the guard would fail it with the context's error, not the budget's. Fix: a guard of 60 s. A green run never waits for it,
  and the test's own assertion on the requests still decides.

### ✅ Verified correct (keep)

- **Checks on `79620e4c`, run by me at low priority, one `go test` at a time,** on `internal/blob/gocloud`, `internal/backup/workload`,
  `internal/services/backup`, `api/types/v1alpha1` and `pkg/funcd`:
  - `go build ./...`, on the host and with `GOOS=linux`: exit 0.
  - `go vet` and `golangci-lint` with no tag and with `dev`, `e2e` and `integration`, and with `GOOS=linux`: exit 0, 0 issues.
  - `go test -race -p 1 -count=1`: ok in all five packages. The new tests under `-race`, `-count=5`, and `-count=3` in `pkg/funcd`: ok.
  - `just check-hygiene`: clean.
- **Not run, as agreed:** the gate, the e2e suite, `go test ./...` and any lane.
- **A real S3 store.** I reran the first review's flow on versitygw v1.9.0, the lanes' S3 server, behind the proxy that counts each answer's
  bytes.
  - **All 17 results** are identical, line by line, to the code before the first commit: a missing copy (404, `fault.NotFound`), a missing
    bucket, a wrong secret and an unknown key id (403, with their codes), a conditional PUT on an existing key (412, `fault.Conflict`), and
    a range past the end (416). An 8 MiB part and a 40 MiB multipart upload read back whole.
  - **The largest answers:** an error of 3,899 bytes, 415 bytes to complete an upload, and a listing page of 3.3 MiB (1000 keys of 627 bytes,
    each all `&`), or 357,547 bytes in funcd's key shape.
  - **Through the rig, on the same store:** a slot's Backup and a Backup by hand, with a 20 MiB object, a Restore that reads it back, and a sync
    that lists a copy of 2,500 keys and records it.
- **`TestStaticKeyAnswers_ReadARealListingHoweverItsStoreCutsIt` pins the budget from below.** It fails under budgets of 1, 4 and 9 MiB and under
  a 64 KiB page charge. The comment's "about 5.2 MiB at worst" is my first report's number, for keys of `&`. A store that writes `"` as
  `&quot;` takes about 6.1 MiB, by arithmetic, or 10.1 MiB with a page per key, which still leaves a third of the budget.
- **A spent budget always ends in an error.**
  - **Eighteen forms** each gave the budget's error, with nothing listed and no more to come: spaces, text, a comment, an instruction or a
    directive before the root; spaces, text, a comment or CDATA in the root; a long key, attribute, element name, entity, tag or token; and
    many objects. Only `io.EOF` reads as an empty listing, and the budget never returns it.
  - **The edges:** a root that closes on the last byte is read, and one that closes a byte later fails. A whole page with a long tail is read,
    as its document is complete, and if that page says more follow, the next page fails with the budget's error.
- **A scope of exactly 1024 members, on the real store.** A slot's Backup of a ResourceGroup succeeds in 0.85 s, with a record of 50 KiB. A
  Restore of one member reads it back. A sync on a second rig records the copy with all 1024 members. Its manifest of 175,122 bytes comes back
  whole, through a ranged `GetObject`.
- **What a schedule sees over a limit.**
  - **On the real store, a copy of 1025 members:** `TargetSynced` reads `False`, `ListFailed`, with the target and "lists more than 1024
    members" in its message, and no slot is taken once the hold is released. Once the listing fits, here with one member's keys removed, it
    reads `True` and records the copy.
  - **On a stub, over 1 Mi keys or 256 MiB of key bytes:** the same reason, with "it holds more than 1048576 keys or 268435456 bytes of keys".
    The key limit takes 1,049 pages and 3 s. After it, when the store answers again, the copy is recorded and the slot due is taken.
  - **A schedule whose scope has 1025 members:** each slot's Backup fails with the limit in its message and writes nothing, and the schedule
    shows `rpoRisk`.
  - **A Backup by hand** goes on with its own copy after a listing over the key limit or the member cap, and its record holds its own scope's
    members. At the cap, with
    the longest names, a record takes 178 KiB.
- **`Resolve` catches every scope form.** It has one path and one exit, and the check sits after a brought Lake and a Function's volumes join.
  Sixteen rows of a probe agree: `KVStores`, `Buckets`, `Databases` and `Lakes` at 1024 and over, a mix at 1024 and over, an App at 1024, an App
  over by a Function's volume, and a ResourceGroup of each kind over. `BackupReconciler.copy` is its only caller outside the tests, and it runs
  before the target is opened.
- **The budget through the context.**
  - **A retry spends the same budget:** a page of 10 MiB reset at its end and then answered whole fails on the second attempt. A reset in a small
    page is retried and read. A 500 spends nothing, so two 500s and then a page of the whole budget are read. The budget's own error is not
    retried.
  - **A redirect:** the answer that ends the chain spends once. Ten hops before each page with no key still ended with the budget's error, after
    3,935 pages.
  - **The context:** it reaches every attempt, through a URL's `prefix` too. Eight `ListAfter` calls at once on one bucket each have their own
    budget, also under `-race`, and one that is cancelled returns the context's error. A session asked for first spends none of the budget, and
    the listing after it can use it all.
- **A credentials file's client stays as it was.** A 20 MiB page and a 200 KiB error are read whole, and `TestCredentialsFileAnswers_AreNotCut`
  fails with the cap on both clients. `s3ListAfter`'s context value has no reader on that client.
- **Every other operation on a static-key client stays small or goes by its name.** Only `Put`, `Attributes`, ranged reads and `ListAfter` reach a
  target. `Versioning` and `RestoreAt`, whose answers can pass 64 KiB, are only called on stores opened with a credentials file.
- **A listing's names.** `ObjectName.Validate` and the volume rule are the rules these stores and volumes are created under, so `fill` skips no
  real member.
- **Mutants.** 30 of my 31 fail a test: both rules of `cappedAnswers`, its status check, the budget's size and charge, each branch of its read,
  the key bytes, the member cap at either side, both listings ignoring it, both name checks, and `Resolve` off by one or before the volumes. The
  survivor replaces `listingBody`'s budget for a page outside a `ListAfter`. No target path calls `List`, so nothing reaches that branch, and it
  is a safeguard. The test for the time check on a credentials file's client stays out, as decided.
- **Scope, reuse and conventions.** Ten files, each hunk for a stated bound. `go.mod` is unchanged, as `aws/middleware` is already a dependency.
  `TestStaticKeyAnswers_BoundAListingPage` gives way to stricter tests at the budget's byte. No ADR file is edited. ADR-0287 Decisions 2, 3, 5 and
  6 hold, with `ListFailed` and its retry unchanged.
- **Shape.** Five `fix(backup):` commits and one `test(backup):` commit, each with the attribution trailer. The text describes behavior only. The
  queue squashes, so `3b626026` dropping a test that `c2cdf438` restores leaves no gap in the final tree.

### Fix checklist

11 of 12. Item 8 stays open until the gate and the lanes run on the PR's tip. The tests are named for their behavior, and the commits carry no
`Fixes #N`, because the fix has no public issue.

### Model scorecard

Recorded: claude-opus-5-5 on backup-target-bounds (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/12. See docs/reviews/model-scorecard.md.

### Recommendation

Ship it with the branch, once the gate and the lanes pass on the PR's tip. The two Minors are a sentence and one line, and neither needs a new review.
