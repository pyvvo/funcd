# ADR-0198: Blob presign takes a strict `expiry` duration and `method`, and refuses a bad value with 400

- **Status**: Accepted (2026-10-07)
- **Date**: 2026-10-07
- **Deciders**: green-0-rabbit
- **Tags**: blob, presign, local-api, shim, python, duration, validation, breaking-change
- **Realizes**: [FEAT-0001/F92](../feat/0001-feat-v1.1.md) (context.blob; joined as a further entry)
- **Depends on**: [ADR-0194](0194-api-duration-strings.md) (Accepted): its grammar and `v1alpha1.ParseDuration`.
  ADR-0198 is accepted together with or after ADR-0194; a change to `ParseDuration`'s contract there carries over.
- **Supersedes (in part)** (back-link `Superseded in part by: ADR-0198` added at acceptance; lines at ec274537):
  [ADR-0127](0127-context-blob-data-plane.md) (Implemented): Decision 1, "honoring optional `?method=GET|PUT|DELETE`
  (default GET) + `?expiry=<dur>` (Go duration; default the driver's)" (130–131); Contracts, the Python
  "`expiry: float | None = None`" (239). Its TypeScript `expiry?: string` (249) stays.
- **Relates to**: [ADR-0007](0007-blob-storage-layer-port.md) (`blob.SignOptions` unchanged, line 180),
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (a shim change is a release, then `go get`),
  [ADR-0148](0148-size-caps-answer-413.md) (malformed input is 400). **Blueprint**: no change.

## Context & Need

The worker-node local API mints a presigned blob URL on `GET /blob/{binding}/{key...}?sign=1`, the call behind
`context.blob.signedUrl` (TypeScript) and `signed_url` (Python). `signOptsFromQuery`
(`internal/workernode/local/blob.go:109-127`) parses `expiry` with `time.ParseDuration` and drops the error:
`expiry=10`, `abc` or `1e-05s` signs for the 15-minute default with 200, `-5m` reaches the driver, `1.5s` and `500us`
pass, and any `method` but `PUT` or `DELETE` (`post`, `POST`, `put`) signs a GET (#823 probe at ec274537). The Python
shim sends seconds as `f"{expiry}s"` (funcd-python v0.5.1 `shim/src/funcd_shim/blob.py:108-113`); the TypeScript
shim passes a string through (funcd-typescript v0.8.1 `shim/src/blob.ts:102-108`).

Purpose: a function gets a URL with exactly the method and lifetime it asked for, or a 400 that says why. The only
driver that signs is `s3blob` (`internal/blob/gocloud/gocloud.go:34`); the daemon's `mem://` and `file://`
substrates (`cmd/funcd/main.go:751`, `:768`) answer 503 (ADR-0007).

## Scenarios

- **scenario: valid-expiry-honoured** — Given a function bound to blob `b`, When it requests
  `?sign=1&method=PUT&expiry=10m` (or `1h30m`), Then it gets 200 and a PUT URL signed for exactly 10 (90) minutes.
- **scenario: bad-expiry-refused** — When `expiry` is `10`, `abc`, `1e-05s`, `1.5s`, `500us`, `-5m`, `""`, `0s`,
  `500ms`, `1s500ms` or `168h1s`, Then it gets 400 `urn:funcd:problem:invalid` naming the value; nothing is signed.
- **scenario: unknown-method-refused** — When `method` is `post`, `POST`, `put`, `HEAD` or `""`, Then it gets 400
  naming the method; nothing is signed.
- **scenario: absent-expiry-defaults** — When the request has neither `expiry` nor `method`, Then it gets 200 and a
  GET URL with the 15-minute driver default.
- **scenario: python-shim-takes-duration-string** — When a Python handler calls `signed_url("b", "k", method="PUT",
  expiry="10m")`, Then the shim sends `…?sign=1&method=PUT&expiry=10m` and returns the URL; `expiry=900` raises
  `TypeError` before any request; `expiry=""` sends `expiry=` and, like any 400, raises `RuntimeError`.
- **scenario: typescript-shim-takes-duration-string** — When a TypeScript handler calls
  `signedUrl("b", "k", { expiry: "10m" })`, Then it gets the URL; with `"1.5s"` or `""` it throws an `Error` with the
  400; `null` or no `expiry` gets the default.

## Scope

**In**: the presign route's `method` and `expiry`, their bounds and 400 answers; the Python shim's `expiry` type, its
release and the funcd pin; the TypeScript shim's `expiry` presence test and doc comment. **Out**: the
`blob.SignOptions` port and the drivers; a URL that dies early because the substrate signs with temporary credentials;
signing on `mem://` or `file://`.

## Constraints & Decision drivers

- Decided by the decider (#823, 2026-10-07): ADR-0194's grammar; a 400 problem and no URL for a bad `expiry` or an
  unknown `method`; only an absent `expiry` gives the default; both shims take a duration string. Confirmed at
  acceptance: whole seconds from 1s to 168h; TypeScript sends `""` too; Python raises `TypeError` for a non-string.
- The signer: gocloud refuses a negative expiry (gocloud.dev v0.46.0 `blob/blob.go:1309-1316`); funcd maps zero to
  15 minutes (`gocloud.go:38`, `:521-524`); the AWS SDK writes `X-Amz-Expires` in whole seconds, truncating, with no
  maximum (aws-sdk-go-v2/service/s3 v1.104.1 `internal/customizations/presigned_expires.go:45`); AWS documents seven
  days as the SDK maximum for a presigned URL. HTTP methods are case-sensitive (RFC 9110 §9.1).

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| `time.ParseDuration` plus bounds | stdlib | accepts `1.5s`, `500us`; a second grammar beside ADR-0194 | rejected |
| A grammar check in each shim | fails before a request | three copies of one rule that can drift; the server checks anyway | rejected |
| Python keeps seconds as a number | no API break | two input types for one parameter | rejected by the decider |
| Truncate or round a sub-second remainder | every grammar value signs | the URL lives shorter or longer than asked, silently | rejected |
| `0s` means the default | matches the port (ADR-0007) | a second spelling of "absent" | rejected; follows from the decider's rule |
| No upper bound; S3 refuses at use | no funcd constant | the URL is minted with 200 and fails later | rejected |
| Case-insensitive `method` | forgiving | RFC 9110 §9.1; the TypeScript type is uppercase | rejected |
| 422 for a bad value | huma's schema code | the local API is not huma; ADR-0148: malformed input is 400 | rejected |

## Decision

1. **Where.** `signOptsFromQuery` returns `fault.Invalid`; the sign branch writes it with `fault.WriteProblem` (400
   `urn:funcd:problem:invalid`, `api/fault/problem.go:25`) and no URL. It runs before `Blob.SignedURL`, so a refused
   request reaches neither the PDP nor the driver; `method` is checked first.
2. **`method`.** Absent: GET. Present: exactly `GET`, `PUT` or `DELETE`; any other value, empty included, is refused.
3. **`expiry`.** Absent: a zero `Expiry`, the 15-minute driver default. Present: `v1alpha1.ParseDuration` (ADR-0194:
   h, m, s, ms in that order, digits only); a value outside the grammar, empty included, is refused. The value must
   be a whole number of seconds from `1s` to `168h`, what the signer honours, refused rather than rounded (decided by
   the decider, 2026-10-07); `2000ms` is 2 s and is accepted. These bounds are SigV4/S3 facts: a future signer with
   other limits needs a new decision.
4. **Shims send the string as given and do not check the grammar**; a 400 surfaces as the TypeScript
   `context.blob.signedUrl failed: 400 <problem>` (`blob.ts:64-65`) or a Python `RuntimeError` (`blob.py:119-120`).
   **Python**: `expiry: str | None = None`, sent as `quote(expiry, safe='')` whenever it is not `None`, so `""` sends
   `expiry=` and the server refuses it; a non-`str` raises `TypeError` before any request (decided by the decider,
   2026-10-07).
   **TypeScript**: the doc comment at `blob.ts:44-45` names the grammar, not "a Go duration string".
   `if (opts?.expiry)` (`blob.ts:104`) becomes `opts?.expiry != null`, so `""` is sent and refused as in Python, and
   `null` stays absent like Python's `None` and `method`'s `??` (decided by the decider, 2026-10-07). That turns `""`
   from the default into a 400 on the public `expiry?: string`, so it is a `feat(shim)!:` commit with
   `BREAKING CHANGE:`; with `bump-minor-pre-major: true` it cuts a minor release.
5. **Release and pin.** funcd-python lands one `feat(shim)!:` commit with `BREAKING CHANGE:`; release-please
   (`bump-minor-pre-major: true`) cuts the next minor, v0.6.0 unless another release comes first. The funcd PR changes
   the handler, runs `go get github.com/pyvvo/funcd-python@<that tag>` (`go.mod:39`) and carries `Fixes #823`, `!`
   and `BREAKING CHANGE:`. The order is safe because funcd runs only the shim it pins (`cmd/funcd/main.go:915`,
   `shimpython.Extract`), the funcd PR bumps that pin together with the handler, and no funcd test or lane runs presign
   through a real shim; the old shim's float values (`60.0s`, `0.5s`) are refused by the new handler.

## Temporary workarounds

None.

## Contracts

```go
package local // internal/workernode/local/blob.go

// minSignExpiry and maxSignExpiry bound a presign lifetime (ADR-0198): S3 signs whole seconds; SigV4 caps at 7 days.
const minSignExpiry, maxSignExpiry = time.Second, 168 * time.Hour

// signOptsFromQuery reads ?method= and ?expiry= (ADR-0198): an absent method is blob.SignGet, an absent expiry is
// zero (the driver default), and a present value outside the rules is fault.Invalid.
func signOptsFromQuery(q url.Values) (blob.SignOptions, error)
```

| Refused request | `detail` (400, `type` `urn:funcd:problem:invalid`) |
|---|---|
| `method` present, not exactly `GET`, `PUT` or `DELETE` | `workernode.local.blob.sign: method "post" is not GET, PUT or DELETE` |
| `expiry` present, outside the grammar | `workernode.local.blob.sign: expiry "10" is not a duration string such as 10m or 1h30m` |
| `expiry` parsed, not whole seconds in 1s–168h | `workernode.local.blob.sign: expiry "500ms" must be a whole number of seconds from 1s to 168h` |

| funcd ↔ shim contract and I/O | Rule |
|---|---|
| Consumes | `v1alpha1.ParseDuration` (ADR-0194), `fault.Invalidf`, `url.Values.Has`; no new dependency |
| `?method=` | absent: GET; `GET`, `PUT`, `DELETE` exactly; else 400 |
| `?expiry=` | absent: 15 min; ADR-0194 grammar, whole seconds, 1s–168h; else 400 (empty included) |
| Python `signed_url(…, expiry=)` | `str \| None` (was `float \| None` seconds), such as `"10m"`; pinned at `go.mod:39` |
| TypeScript `signedUrl(…, { expiry })` | `string` (type unchanged); sent when `!= null`, so `""` gets the 400 |

## Implementation plan

1. funcd-python: `shim/src/funcd_shim/blob.py:108-113` and its docstring per Decision 4; `shim/tests/test_blob.py:88-94`
   uses `expiry="15m"` and the route `…&expiry=15m`; the new scenario test; the release (Decision 5).
   funcd-typescript: the doc comment at `shim/src/blob.ts:44-45`, the `!= null` test at `:104`, `blob.test.ts` cases
   for `""` and `null`, and the `!` commit; funcd takes it at its next pin (`go.mod:40`).
2. funcd, after ADR-0194's `v1alpha1.ParseDuration` lands (an earlier PR or the same one):
   `internal/workernode/local/blob.go` calls `signOptsFromQuery(r.URL.Query())`, which tells absent from empty with
   `Has`; the sign branch (`:45-55`) writes its error; the comments at `:33-36` and `:109-110` state the rules. Then
   `go get github.com/pyvvo/funcd-python@<tag>` and the tests below.

**Test plan** — one test per scenario, named after it:

| Scenario | Where |
|---|---|
| valid-expiry-honoured, absent-expiry-defaults, bad-expiry-refused, unknown-method-refused | `internal/workernode/local/blob_test.go`: `blobHandler` over a bucket that records the `SignOptions` it gets; a refusal asserts 400, the problem type and detail, and no call |
| valid-expiry-honoured, absent-expiry-defaults (signer) | `internal/blob/gocloud/gocloud_test.go`: a hermetic s3blob presign (dummy credentials via `t.Setenv`; `AWS_PROFILE` cleared and `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE` pointed into `t.TempDir()`, because a set profile is resolved before environment credentials, aws-sdk-go-v2/config v1.32.26 `resolve_credentials.go:115-117`; `endpoint=http://127.0.0.1:9`, no network) gives `X-Amz-Expires` 600 for `10m`, 5400 for `1h30m`, 900 for a zero `Expiry` |
| check order (Decision 1) | `blob_test.go`: `/blob/nope/k?sign=1&expiry=10m` ⇒ 403; `…&expiry=10` ⇒ 400 |
| typescript-shim-takes-duration-string | same file: the queries `blob.ts:103-104` builds (`?sign=1&method=GET`, `…&expiry=10m`, `…&expiry=1.5s`); funcd-typescript `shim/test/blob.test.ts:86-96` green, plus the new `""` and `null` cases |
| python-shim-takes-duration-string | funcd-python `shim/tests/test_blob.py` |
| contract | a table test of `signOptsFromQuery`: Decisions 2–3 and the bounds (`1s`, `168h` accepted; `999ms`, `168h1s` refused) |

**Definition of done**: `just ci` green; the funcd-python shim suite green at the new tag, which `go.mod` pins;
`grep -n 'time.ParseDuration' internal/workernode/local/blob.go` finds nothing.

## Review checklist

- [ ] `signOptsFromQuery` uses `v1alpha1.ParseDuration`, with no `time.ParseDuration` and no copy of the pattern.
- [ ] Absent and empty are told apart with `url.Values.Has`; only an absent parameter takes a default.
- [ ] A refused request never calls `Blob.SignedURL`; status, problem type and details match the Contracts table.
- [ ] Python `signed_url` takes `str | None`, quotes it and raises `TypeError` for a non-`str`; no shim checks the grammar.
- [ ] TypeScript sends `expiry` whenever it is `!= null`; its release is a `feat(shim)!:` commit.
- [ ] `go.mod` pins the funcd-python release that carries the change; every scenario has a same-name test.

## Consequences

- (+) A function gets the method and lifetime it asked for, or a 400 naming the bad value; one grammar everywhere.
- (−) Python handlers that pass seconds (`expiry=900`) fail with `TypeError` at the new pin; values that signed
  before are refused (`1.5s`, `500ms`, `0s`, `method=put`, over 168h, and TypeScript's `expiry: ""`).
- (−) A URL can still expire early when the substrate signs with temporary credentials; funcd does not check that.
- (−) `method=DELETE` passes the check but answers 503 `unavailable` on every driver linked today: s3blob does not sign
  DELETE (gocloud.dev v0.46.0 `blob/s3blob/s3blob.go:920-922`), which `gocloud.go:569-570` maps to 503.
- (−) 168h is the substrate maximum, not a security cap: a URL outlives an unbinding by up to 7 days (already so
  under ADR-0127).

## Open questions

None.

## References

- Issue #823 and the decider's comment of 2026-10-07; #816 (ADR-0194); tracker #817.
- gocloud.dev v0.46.0 `blob/blob.go:1309-1316`, `blob/memblob/memblob.go:426-428`, `blob/fileblob/fileblob.go:967-970`;
  aws-sdk-go-v2/service/s3 v1.104.1 `internal/customizations/presigned_expires.go:33-45`; RFC 9110 §9.1.
- AWS S3 User Guide, presigned URLs, "Expiration time for presigned URLs" (checked 2026-10-07).
