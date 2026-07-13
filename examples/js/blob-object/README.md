# blob-object — native blob via `context.blob` (ADR-0127)

The in-function analogue of [`s3-roundtrip`](../s3-roundtrip): the same put → get → list → denied
round-trip over the **same blob substrate**, but reached **natively** through `context.blob` instead of
the S3-protocol frontend. No `@aws-sdk`, no injected SigV4 keypair — just the binding.

```ts
await ctx.blob.put('gold', key, bytes);        // the fn owns the gold prefix → allowed
const back = await ctx.blob.get('gold', key);  // reads it back
const keys = await ctx.blob.list('gold', 'roundtrip-');
await ctx.blob.put('other', 'x.txt', bytes);   // UNBOUND alias → 403 (bind-as-grant default-deny)
```

## How it works

`context.blob.{get,put,delete,list,signedUrl}` dials the per-sandbox **worker-node local API**
(HTTP-over-UDS, `FUNCD_INVOKE_SOCKET`) — the same channel as `context.kv`/`context.invoke`. The platform
routes `/blob/…` to a **binding-gated Facade** that:

1. resolves the alias → `(bucket, prefix)` from the function's `spec.blob` (**bind-as-grant**, default-deny);
2. authorizes the **`S3Capability`** `s3::read`/`s3::write` on the bound `BlobPrefix` — the *same* PDP the
   S3 frontend (ADR-0080) uses, with the function as the principal (its `spec.blob` are its `blobBindings`);
3. acts on the *same* substrate view under the *same* `blobKey` keyspace — so an object written here is the
   object `aws s3 ls s3://lakehouse/gold/` sees.

The binding grants **read**; owning the prefix (`bucket.yaml` `owner: blob-object`) grants **write**. The
function does **not** declare the `other` alias, so a write there is `Forbidden` — proving default-deny.

## The resources

| File | What |
|---|---|
| [`src/object.ts`](src/object.ts) | the handler (typed against `@funcd/shim-nodejs`) |
| [`funcdctl.yaml`](funcdctl.yaml) | the client push/dev config (runtime · handler · blob binding · contract) |
| [`function.yaml`](function.yaml) | the deploy `Function` CRD (`spec.blob` = the capability) |
| [`bucket.yaml`](bucket.yaml) | the `lakehouse` Bucket — `gold` (owned) + `other` (unowned) |

## Build

```bash
npm install && npm run build   # → object.mjs (+ baked contract validators + object.schema.json)
```

The in-process end-to-end lane lives at `pkg/funcd/blob_e2e_test.go` (`just test-e2e`): it builds this
handler, applies the Bucket + Function, invokes, and asserts `{put, get, list, denied}` — the native
`context.blob` path end to end, with no keypair.
