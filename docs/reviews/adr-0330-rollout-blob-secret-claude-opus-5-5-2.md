## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0330 rollout to Blob and Secret, implementation, round 2, model: claude-opus-5-5)

Round 2 reviews `2c7edbb6`, one commit on `8ed42148` on branch `feat/rollout-0330-blob-secret`. The commit answers the
two Minors of [round 1](adr-0330-rollout-blob-secret-claude-opus-5-5.md). It changes the when and fix of
`quota.exceeded` (`internal/quota/causes.go`) and the when of `blob.key-unstorable` (`internal/blob/causes.go`), and
it regenerates the KV and Bucket Spec tabs. No code path, status or detail changes.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None. Both Minors of round 1 are resolved.

- **`quota.exceeded` now covers a Bucket prefix's quota.**
  - The when reads "The write would take what a store or a prefix of a Bucket holds past its quota; the detail names
    which one." The fix reads "Delete data to make room, or raise that quota: the store's spec.quota, or the quota of
    the prefix in its Bucket's spec.prefixes."
  - The cause has two raise sites, `internal/quota/quota.go:205` and `:207`. Each message starts with the subject's
    `String()` (`quota.go:42`): `kvstore <namespace>/<name>`, `bucket <namespace>/<name>`, or `prefix <p> of bucket
    <namespace>/<name>`. The detail is `quota.Admit: ` and that message, so the detail names which quota refused the
    write.
  - Round 1's probes give the evidence for each case. A 9-byte write into a prefix whose quota is 8 bytes answered
    "quota.Admit: prefix p of bucket default/pq holds 0 of its 8 bytes (spec.quota): …". A 65-byte write into a Bucket
    whose quota is 64 bytes answered "quota.Admit: bucket default/full holds 0 of its 64 bytes (spec.quota): …".
  - The fix names fields that exist: `spec.quota` on a KVStore and on a Bucket, and `quota` on each entry of a
    Bucket's `spec.prefixes` (`api/types/v1alpha1/bucket.go`). Only `PUT /kv/{binding}/{key...}` and
    `PUT /blob/{binding}/{key...}` declare the cause, and the text stays true for both.
- **`blob.key-unstorable` now covers a key past the path limit.**
  - The when reads "funcd's file store cannot hold the key: the key or one of its segments is longer than the file
    system takes, or the key needs the path of another object, as a/b does while a is an object."
  - `mapErr` raises the cause on `ENAMETOOLONG`, which covers both a path and a segment past their limits, and on
    `ENOTDIR`, `EISDIR` and `EEXIST`, where a key needs another object's path. Round 1's 5,399-byte key of 8-byte
    segments answered "… cannot be stored by this backend: file name too long" with this cause.

### Routed (not this work's)

Unchanged from round 1: the seven pre-existing behaviors, not checked yet, and Decision 9's one client per pull
request, all for the decider.

### ✅ Verified correct (keep it)

- The commit changes 4 files: the two cause files, whose strings are the only change, and the KV and Bucket Spec tabs.
- The tabs change 14 rows: `quota.exceeded` under the three KV write methods and Bucket `put`, and
  `blob.key-unstorable` under Bucket `get`, `put` and `del`, in both languages. No other row changes.
- No file outside `docs/reviews` still holds either old text.
- `just generate` on `2c7edbb6` leaves the tree clean, so the committed tabs are what the code renders.
- Everything that round 1 verified still holds. This commit changes only text that the problem's detail never prints.

### Checks run

| Check | Result |
| --- | --- |
| `TestBlobCauseMatrix`, `TestSecretCauseMatrix`, `TestKVCauseMatrix` and `TestEveryDeclaredCauseHasARow`, `-race -count=3` | PASS each; 108 blob, 72 secret and 138 KV row passes, none failed |
| `go test -race -count=1` on `internal/controlplane/kindref`, `internal/quota` and `internal/blob` | 3 packages ok |
| `golangci-lint run` on `internal/quota` and `internal/blob`; `gofmt -l` on the two cause files | 0 issues; none |
| `just generate` | exit 0; the tree was clean after it |
| the full `pkg/funcd` suite, `just ci`, the e2e suite | not run in this round: the change is text only, and the batch gate runs them once |

### Definition of Done

12 of the 13 items hold, as in round 1. "`just ci` passes" runs in the batch gate.

### Model scorecard

The batch's last commit records the row, in the one-PR batch mode of CLAUDE.md: claude-opus-5-5 on ADR-0330
(implementation, round 2) → pass, 0/0/0, 0 model-attributed, DoD 12/13.

### Recommendation

Pass. The item is ready for the batch gate. ADR-0330 is already Implemented, so this gate stamps nothing.
