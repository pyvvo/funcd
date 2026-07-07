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

## Fixtures and the apply order (resolving the admission cycle)

The admission graph has a create-time cycle (ADR-0080): the Bucket's `gold.owner` references the
Function, and the Function's `spec.blob` references the Bucket. Resolved the ADR-0073 KVStore way —
create the Function binding-less, then the Bucket, then re-apply the Function with its binding:

1. `function-base.yaml` — Function `s3-roundtrip`, **no** `spec.blob`, **scaled to 0** (`minReplicas:
   0`) so no warm replica spins up yet;
2. `bucket.yaml` — Bucket `lakehouse`, prefixes `gold` (owner `s3-roundtrip`) + `other` (no owner);
3. `function.yaml` — re-apply (Update) the Function **with** `spec.blob[gold]` and `minReplicas: 1`.

Step 1 stays at zero replicas on purpose: `addS3Env` only injects the `AWS_*` env for a function that
declares `spec.blob` (ADR-0085). If a binding-less replica became Ready in step 1, it would run without
the S3 keypair, and the step-3 binding Update would not recreate an already-Ready replica — so the
first replica must be the one created by step 3 (which carries `spec.blob`).

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
