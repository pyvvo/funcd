# s3-roundtrip — the S3-protocol frontend, in-platform (ADR-0080/0085, F47)

A **TypeScript** function that, on invoke, uses **only its funcd-INJECTED keypair** to round-trip S3
against the gateway — proving the whole live path: **keypair-injection → SigV4 over TCP → the Cedar
PEP → `blob.Bucket`**, on real containerd.

For a function that declares `spec.blob`, the daemon injects (ADR-0085 `addS3Env`):

| env var | value |
|---|---|
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | a per-function SigV4 keypair derived from the node master secret (the function never chooses it) |
| `AWS_REGION` | `us-east-1` |
| `AWS_ENDPOINT_URL_S3` | the sandbox-facing gateway URL — under containerd the CNI **bridge gateway IP** (`http://10.63.0.1:9000`) |

## What the handler does

```
(a) PutObject  lakehouse/gold/roundtrip-<inv>.txt   (s3-roundtrip OWNS gold)  → ok        put=true
(b) GetObject  it back                              body must match          → ok        get=true
(c) ListObjectsV2 prefix lakehouse/gold/            the key must be present  → n>=1      list=n
(d) PutObject  lakehouse/other/x.txt                NOT bound / not owner    → 403 deny  denied=true
```

It returns `{"put":true,"get":true,"list":<n>,"denied":true}`. `denied=true` means step (d) was
correctly rejected by the Cedar PEP on the live daemon (the single-writer `forbid` — `s3-roundtrip`
is not `other`'s owner and binds no read for it).

## The AWS SDK v3 (JS) config that works against the gateway

```ts
new S3Client({
  region: process.env.AWS_REGION,
  endpoint: process.env.AWS_ENDPOINT_URL_S3,
  forcePathStyle: true,
  credentials: {
    accessKeyId: process.env.AWS_ACCESS_KEY_ID,
    secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY,
  },
  requestChecksumCalculation: 'WHEN_REQUIRED',  // the default streaming-CRC trailer breaks the gateway
  responseChecksumValidation: 'WHEN_REQUIRED',  // — require checksums only when explicitly needed
});
```

The last two knobs are load-bearing: they mirror the Go scenario tests
(`RequestChecksumCalculation=WhenRequired` / `ResponseChecksumValidation=WhenRequired`). Without them
the SDK adds a streaming-CRC trailer the gateway rejects.

## Fixtures and the apply order — any order (ADR-0121)

The Bucket's `gold.owner` references the Function and the Function's `spec.blob` references the Bucket:
a create-time cycle that used to need a two-phase workaround (a binding-less `function-base.yaml`
first, then the Bucket, then the Function with its binding). **ADR-0121 removed that** — owner/binding
existence is reconcile-time, so both apply in **any order**:

1. `function.yaml` — Function `s3-roundtrip` **with** `spec.blob[gold]` and `minReplicas: 1`;
2. `bucket.yaml` — Bucket `lakehouse`, prefixes `gold` (owner `s3-roundtrip`) + `other` (no owner).

The lane applies the **Function first** (bound to the not-yet-applied Bucket) — it is admitted and held
`Ready=False/BucketNotFound`, then converges once the Bucket lands. That is the live apply-any-order
proof of the Bucket↔Function-owner cycle on real containerd. (Because the function carries `spec.blob`
from the start, `addS3Env` injects its `AWS_*` keypair on the first Ready replica — ADR-0085.)

## Build it

```bash
npm install                                 # @aws-sdk/client-s3 + esbuild
node --experimental-strip-types build.ts    # → roundtrip.mjs (the SDK is bundled in)
```

## Run the e2e

```bash
nix develop -c just lima-example-s3
```

Boots a fresh containerd VM, deploys the function, and runs `e2e/s3.venom.yml` — one invoke asserts
`put/get/list/denied`, proving the round-trip and the PEP deny on the live daemon.
