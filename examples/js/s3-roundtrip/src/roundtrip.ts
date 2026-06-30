// s3-roundtrip — a function that exercises the F47 S3-protocol frontend (ADR-0080/0085) end to end
// using ONLY the funcd-INJECTED keypair. For a function that declares spec.blob, the daemon injects
// AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (a per-function SigV4 keypair derived from the node master
// secret — the function never chooses it), AWS_REGION, and AWS_ENDPOINT_URL_S3 (the sandbox-facing
// gateway address: under containerd the CNI bridge gateway IP). On invoke the handler:
//   (a) PutObject  lakehouse/gold/roundtrip-<inv>.txt   — the fn OWNS the gold prefix → allowed
//   (b) GetObject  it back                              — body must match
//   (c) ListObjectsV2 prefix lakehouse/gold/            — the key must be present
//   (d) PutObject  lakehouse/other/x.txt               — NOT bound / not owner → 403 AccessDenied
// and returns {"put","get","list","denied"} (denied=true means step (d) was correctly rejected by the
// Cedar PEP on the live daemon). This proves: keypair-injection → SigV4 over TCP → the Cedar PEP →
// blob.Bucket, on real containerd.
//
// AWS SDK v3 config note: forcePathStyle:true + region/endpoint/credentials from env, AND
// requestChecksumCalculation/responseChecksumValidation = "WHEN_REQUIRED" — the SDK's default
// streaming-CRC trailer breaks against the gateway (exactly as the Go scenario tests set
// RequestChecksumCalculation=WhenRequired / ResponseChecksumValidation=WhenRequired).
import type { CloudEvent, FunctionContext } from '@funcd/shim-nodejs';

import {
  S3Client,
  PutObjectCommand,
  GetObjectCommand,
  ListObjectsV2Command,
} from '@aws-sdk/client-s3';

export interface FuncInput {
  /** Optional invocation tag woven into the object key (defaults to a timestamp). */
  inv?: string;
}
export interface FuncOutput {
  put: boolean;
  get: boolean;
  list: number;
  denied: boolean;
}

const BUCKET = 'lakehouse';
const GOLD = 'gold'; // the prefix this function OWNS
const OTHER = 'other'; // a prefix it is NOT bound to → must be denied

function newClient(): S3Client {
  return new S3Client({
    region: process.env.AWS_REGION,
    endpoint: process.env.AWS_ENDPOINT_URL_S3,
    forcePathStyle: true,
    credentials: {
      accessKeyId: process.env.AWS_ACCESS_KEY_ID ?? '',
      secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY ?? '',
    },
    // CRITICAL: the gateway rejects the SDK's default streaming-CRC trailer; require checksums only
    // when the operation explicitly needs them (parity with the Go scenario tests).
    requestChecksumCalculation: 'WHEN_REQUIRED',
    responseChecksumValidation: 'WHEN_REQUIRED',
  });
}

async function streamToString(body: unknown): Promise<string> {
  // In Node the GetObject Body is a Readable stream; transformToString is the SDK helper.
  const b = body as { transformToString?: () => Promise<string> } | undefined;
  if (b?.transformToString) {
    return b.transformToString();
  }
  return '';
}

export async function handle(ctx: FunctionContext, event: CloudEvent<FuncInput>): Promise<FuncOutput> {
  const inv = event.data?.inv ?? String(Date.now());
  const key = `${GOLD}/roundtrip-${inv}.txt`;
  const want = `roundtrip-${inv}`;
  const s3 = newClient();

  // (a) PutObject into the OWNED gold prefix → allowed.
  await s3.send(new PutObjectCommand({ Bucket: BUCKET, Key: key, Body: want }));
  ctx.log(`s3-roundtrip: PUT ${BUCKET}/${key} ok`);
  const put = true;

  // (b) GetObject it back → body must match.
  const got = await s3.send(new GetObjectCommand({ Bucket: BUCKET, Key: key }));
  const body = await streamToString(got.Body);
  const get = body === want;
  ctx.log(`s3-roundtrip: GET ${BUCKET}/${key} → match=${get}`);

  // (c) ListObjectsV2 the gold prefix → the key must be present.
  const listed = await s3.send(
    new ListObjectsV2Command({ Bucket: BUCKET, Prefix: `${GOLD}/` }),
  );
  const keys = (listed.Contents ?? []).map((o) => o.Key);
  const list = keys.includes(key) ? keys.length : 0;
  ctx.log(`s3-roundtrip: LIST ${GOLD}/ → ${keys.length} object(s), present=${keys.includes(key)}`);

  // (d) PutObject into a prefix it is NOT bound to / does not own → must be DENIED (403) by the PEP.
  let denied = false;
  try {
    await s3.send(
      new PutObjectCommand({ Bucket: BUCKET, Key: `${OTHER}/x.txt`, Body: 'nope' }),
    );
    ctx.log(`s3-roundtrip: PUT ${OTHER}/x.txt UNEXPECTEDLY allowed — PEP did not deny`);
  } catch (err) {
    const e = err as { $metadata?: { httpStatusCode?: number }; name?: string };
    const code = e?.$metadata?.httpStatusCode;
    denied = code === 403 || e?.name === 'AccessDenied';
    ctx.log(`s3-roundtrip: PUT ${OTHER}/x.txt denied=${denied} (status=${code}, name=${e?.name})`);
  }

  return { put, get, list, denied };
}
