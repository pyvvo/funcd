## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0330 rollout to Blob and Secret, implementation, model: claude-opus-5-5)

The work rolls ADR-0330 out to the eight Blob and Secret endpoints of the local API, as Decision 9 plans after the KV
pilot. It adds 24 known causes, raises them at their sites, declares them on the endpoints, maps the blob and secret
methods of both shims in `kindref`, and proves each declared cause with one row of `TestBlobCauseMatrix` or
`TestSecretCauseMatrix`. Every check below ran on branch `feat/rollout-0330-blob-secret` at `1cf4fe65`, one commit on
`94f1821f`. The bar is the ADR and the shims that `go.mod` pins, funcd-typescript v0.29.0 and funcd-python v0.24.0.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **`quota.exceeded` names only `spec.quota`, but a Blob write can also pass a Bucket prefix's quota** · attribution:
  model.
  - `PUT /blob/{binding}/{key...}` now declares `quota.ErrExceeded`. The KV pilot wrote its text for a KVStore: "The
    write would take what its store holds past spec.quota." and "Delete data to make room, or raise the store's
    spec.quota." (`internal/quota/causes.go`).
  - `blob.Quoted` also asks the quota of the prefix that the key lies in, when `spec.prefixes[].quota` sets one
    (ADR-0292; `internal/blob/quoted.go`, `subjects`). `internal/quota/quota.go:207` refuses that write with the same
    cause. A scratch test wrote 9 bytes into a prefix whose quota is 8 bytes. The answer was 507, "quota.Admit: prefix
    p of bucket default/pq holds 0 of its 8 bytes (spec.quota): a write of 9 bytes would pass it", and the recorder
    saw `quota.exceeded`.
  - So the Bucket page's `put` row for this cause tells such a caller to raise the store's spec.quota, which does not
    lift a prefix's quota. The cause's when and fix can name the prefix's quota too, and `just generate` then rewrites
    the KV and Bucket pages. The text was right for KV, which has no prefix quotas.
- **`blob.key-unstorable` says that a segment is too long, but the whole key can be too long** · attribution: model.
  - `mapErr` raises `blob.ErrKeyUnstorable` on `ENAMETOOLONG` (`internal/blob/gocloud/gocloud.go`). The OS returns that
    errno for a path past its limit as well as for a segment past its limit.
  - A scratch test wrote a key of 5,399 bytes, in segments of 8 bytes, to the file store. The answer was 400, "…
    cannot be stored by this backend: file name too long", and the recorder saw `blob.key-unstorable`. The cause's
    when reads "a segment is longer than the file system takes".
  - The change is one clause in `internal/blob/causes.go`, such as "the key or one of its segments is longer than
    the file system takes", and `just generate`.

### Routed (not this work's)

- **Behavior that the work documents as the code does it; not checked yet** · home: elsewhere · destination: the
  decider, after the two checks of CLAUDE.md, if a change is wanted · attribution: none.
  - A missing caller answers 404 on the Secret endpoints (`secret.function` wraps the store's NotFound with its kind),
    where KV and Blob answer 403. The Secrets page shows the 404 under `list`, `put` and `del`; `get` returns no value
    for it in both shims.
  - On the file store, a GET of `a/b` while `a` is an object answers 400 `blob.key-unstorable` (ENOTDIR), where the
    memory store answers 404. The Bucket page shows the 400 under `get`.
  - Both shims' `blob.get` return null for any 404, so a missing Bucket reads as a missing object. The method table
    excepts `blob.bucket-missing` under `get`, as Decision 8 says for a cause that a shim returns as a value.
  - A write, a delete, and a PUT or DELETE sign for a missing Bucket answer 403 `blob.write-denied`: `s3Resource` gives
    a dangling prefix no `writers`, so the `s3-single-writer` forbid fires (`internal/auth/cedar/capabilities.go`,
    `builtin_s3.cedar`). The cause's when says so.
  - A blob store's 4xx detail carries the store's whole key, `s3/<namespace>/<bucket>/<prefix>/<key>`, since a 4xx
    detail is the error's whole text (`api/fault/problem.go`, `detailOf`).
  - A file-store write that meets a full disk answers 500. `mapErr` maps four errnos to a cause, and no blob path
    raises `diskfull.ErrDiskFull`.
  - The blob facade wraps a PDP error with the PDP's kind (`internal/services/blob/blob.go`, `authorize`). The secrets
    resolver wraps it as internal (`internal/secrets/secrets.go`). The Cedar driver forwards the kinds of its entity
    and policy sources.
  - The wire comparison under *Verified correct* shows the same answers on `94f1821f` for the first, second, fourth
    and fifth items.
- **Decision 9 plans one client per pull request** · home: elsewhere · destination: the decider · attribution: none.
  This item covers two clients in one commit, as the batch assigned it, and the batch ships as one pull request at the
  decider's request (CLAUDE.md, one-PR batch mode). Nothing changes here.

### ✅ Verified correct (keep it)

- **The causes and their raise sites.**
  - The work adds 24 causes: 12 in `internal/blob`, 8 in `internal/secrets` and 4 in `local`. It reuses 5:
    `local.body-too-large`, `local.body-unreadable` and the three quota causes. Each is defined once.
  - Each raise site's former constructor had its cause's kind, with the same op and format string. A site that wrapped
    a lower error before still wraps it (`Wrapf`). The when and fix of each cause match its sites, but for the two
    Minors.
  - A deny picks the read or the write cause by action, at run time, in both `authorize` functions.
  - `gocloud.SignedURL` names `blob.sign-unsupported` for gocloud's Unimplemented, where that is its one meaning.
    `mapErr`'s own Unimplemented, which other operations share, stays without a cause.
- **No wire change.**
  - A scratch test, given to `go test -overlay`, ran on the branch and on the base commit, whose Go files the overlay
    put back over the 18 that the commit adds or changes. It sent 83 requests one at a time, in a fixed order: the 60
    of the two matrices, their 3 setups, and 20 more. The extra requests cover the second raise site of each merged
    cause, a write, a delete and both signs for a missing Bucket, a sign on the file store, a long key, a `FUNCD_` key
    and a delete of an invalid key.
  - The test recorded the status, `Content-Type`, `Content-Length` and body of each answer. The two outputs are
    identical.
  - The tests of the shared packages pass under `-race`: `internal/blob`, `internal/blob/gocloud`,
    `internal/blob/s3gateway`, `internal/secrets/...`, `internal/services/blob` and `internal/services/secret`.
- **The choices that the ADR does not state hold.**
  - *The secret causes in `internal/secrets`.* The raisers are `internal/secrets` and `internal/services/secret`,
    which imports it. `local` now imports it too, and `go list -deps` shows that this import adds no other funcd
    package to `local`.
  - *One `blob.object-not-found`.* GET raises it in `local` when the facade finds no object, and DELETE in the store.
    Both mean that the object does not exist, and both shims turn GET's into null.
  - *The three key causes.* They have three remedies, and the split lets the method table except
    `blob.key-not-utf8` alone. No shim can send such a key: `encodeURIComponent` throws on a lone surrogate, and
    Python's `quote` encodes strict UTF-8.
  - *The merged causes.* `blob.sign-put-capped` comes from `Capped` (maxObjectBytes) and `Quoted` (a size quota), and
    its when names both. `local.sign-expiry-invalid` comes from a string that does not parse and from a duration out
    of range, and its when covers both. Each pair has one fix.
  - *`blob.bucket-missing` not declared on PUT and DELETE.* `resolveAuth` asks the PDP before it looks up the Bucket,
    and the PDP denies every write to a missing Bucket (see Routed). PUT, DELETE and the PUT sign answered 403
    `blob.write-denied`, and a GET sign answered 404. Only a Bucket deleted between the decision and the lookup could
    give a write the 404. No test can provoke that race, and ADR-0330's Consequences accept such an untested path.
  - *Update's errors on a Secret write.* `store.Update` validates the object first. The facade has just read that
    object, so only the new key can fail `Secret.Validate`, and an invalid kind means `secrets.key-invalid`. A not
    found kind means the Secret was deleted under the write. A conflict starts the attempt over. A DELETE of an
    invalid key answers 204, so DELETE rightly does not declare the key cause.
  - *The lazily built 64 MiB body.* Only `TestBlobCauseMatrix` builds it. `causeMatrices`, which
    `TestEveryDeclaredCauseHasARow` calls five times, passes an empty body.
- **The declarations and the guard.**
  - `local.Endpoints` lists 17 endpoints. 13 are described: KV's four, health, and the four Blob and four Secret
    endpoints. The rollout list holds the other 4, and `TestEveryEndpointIsDescribedOrListed` passes.
  - The declared causes are 13, 12, 7 and 4 for the blob key GET, PUT, DELETE and list, and 4, 5, 10 and 5 for the
    secret list, key GET, PUT and DELETE.
  - The full `pkg/funcd` run under `-race`, with no filter, passed both halves of `compareCauses`.
- **The matrices.**
  - `TestBlobCauseMatrix` has 36 rows and `TestSecretCauseMatrix` has 24, one for each pair of endpoint and declared
    cause. The file store, a Bucket that cannot be measured, the free-space floor and a store that loses every Secret
    write each get a platform of their own. No row writes anything that another row reads.
  - In three runs of the three matrices together under `-race`, the blob matrix took 0.49 to 0.52 s and the secret
    matrix 0.30 to 0.33 s. Run alone, their peak resident memory is 1.95 GB and 0.69 GB, against 1.2 GB for
    `TestKVCauseMatrix` and 0.13 GB for the binary running `TestEveryDeclaredCauseHasARow`.
- **The shared test change.**
  - `TestEveryDeclaredCauseHasARow` starts from the full matrices and swaps only the KV matrix in its four negative
    cases. Each case still asserts one exact error, so it also proves that the blob and secret matrices add none.
  - The positive case holds all three matrices in both directions. Mutants A and C below show both directions on
    the blob matrix.
- **`kindref`.**
  - The table matches the pinned shims. Each blob and secret `get` returns null, undefined or None for any 404. A blob
    `get` never sends `?sign=`, and a sign never opens an object or meets the file store's key rules.
  - `signedUrl` always sends `?sign=1&method=`, and both shims send any method or expiry string. Neither shim sends a
    broken body or a key that is not UTF-8.
  - The secret `put` sends `{"value": …}` with any JSON value, so `local.secret-body-invalid` shows under it.
  - The Bucket page has 34 rows in each language, and the Secrets page 20. No `get` has a 404 row, and the 500
    sentence appears once per language.
  - `TestScenarioEveryMethodMapped` holds every exception to its endpoint's declarations.
- **Tracking.** The commit changes no ADR, feat row or roadmap file. ADR-0330 stays Implemented, and FEAT-0000/F02 is
  an infra row with no feature issue.

### Checks run

| Check | Result |
| --- | --- |
| `go build ./...`, on the host and with `GOOS=linux` | exit 0 |
| `go vet` on the 8 touched packages and the rest of `internal/blob/...` and `internal/secrets/...`, on the host and with `GOOS=linux`; `go vet -tags e2e ./pkg/funcd` | exit 0 |
| `golangci-lint run` on the same packages, on the host | 0 issues |
| `go test -race -count=1` on `internal/blob/...`, `internal/controlplane/kindref`, `internal/secrets/...`, `internal/services/blob`, `internal/services/secret` and `internal/workernode/local` | 9 packages ok, 1 with no test files |
| `TestBlobCauseMatrix`, `TestSecretCauseMatrix`, `TestKVCauseMatrix` and `TestEveryDeclaredCauseHasARow`, `-race -count=3` | PASS each; 108 blob and 72 secret row passes, none failed |
| `go test -race -count=1 ./pkg/funcd`, with no filter, so `TestMain` checks both halves | ok, 103.9 s |
| `just generate` | exit 0; the tree was clean after it |
| `scripts/check-list-only-shrinks.sh 94f1821f internal/workernode/local/testdata/undescribed-endpoints.txt`; `just check-hygiene`; `gofmt -l` on the changed Go files | exit 0; clean; none |
| `just ci`, the e2e suite, `go test ./...` | not run: the batch gate runs them once |

Each mutant ran through `go test -overlay`, so the tree did not change:

| Mutant | What failed |
| --- | --- |
| A: `GET /blob/{binding}` no longer declares `blob.bucket-missing` | `TestEveryDeclaredCauseHasARow`: "the blob matrix has a row for GET /blob/{binding} and blob.bucket-missing, which the endpoint does not declare"; `TestMain`: "GET /blob/{binding} answered blob.bucket-missing, which it does not declare"; funcd logged one warning |
| B: Update's invalid kind raises `secrets.value-not-text`, with the same status and type | `TestSecretCauseMatrix/PUT_/secret/{binding}/{key}_secrets.key-invalid` |
| C: the matrix loses its `blob.sign-unsupported` row | `TestEveryDeclaredCauseHasARow`: "GET /blob/{binding}/{key...} declares blob.sign-unsupported, which no row of the blob matrix proves" |
| D: `gocloud.SignedURL` loses its Unimplemented branch | `TestBlobCauseMatrix/GET_/blob/{binding}/{key...}_blob.sign-unsupported`; `TestMain`: "answered an error of kind unavailable with no known cause"; funcd logged one warning |
| E: `blob.get` no longer excepts `blob.bucket-missing` | no test of `kindref`; the regenerated Bucket page gains a 404 row under `get` in each language, which `scripts/check-generated.sh` reports until the page is committed. The ADR's Consequences accept that the table is checked for its names, not for what a shim returns |

### Definition of Done

12 of the 13 items hold: the ADR's 6 Definition-of-done items and its 7 checklist items, read for the Blob and Secret
endpoints.

- "`just ci` passes": every part that this review ran passes. The recipe as a whole runs in the batch gate.
- The two Minors concern the text of two causes, which no item names.

### Model scorecard

The batch's last commit records the row, in the one-PR batch mode of CLAUDE.md: claude-opus-5-5 on ADR-0330
(implementation) → pass, 0/0/2, 2 model-attributed, DoD 12/13. See `docs/reviews/model-scorecard.md` once it is
recorded.

### Recommendation

Pass. The two Minors are two cause texts and a `just generate`. They can land in this item's commit before the batch
gate, or follow it; neither changes an answer.

ADR-0330 is already Implemented, so this gate stamps nothing. FEAT-0000/F02 has no feature issue, so the pull request
closes none for this item.
