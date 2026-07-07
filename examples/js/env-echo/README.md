# env-echo — config + secret env injection on real containerd

A minimal funcd example proving that a Function receives **both** its bound configuration sources as
environment at runtime:

- **`spec.config: [app-config]`** — a **ConfigMap** (ADR-0093), non-sensitive `Data` (plain string map).
- **`spec.secrets: [app-secret]`** — a **Secret** (ADR-0057), sensitive `Data` (base64-encoded `[]byte`).

The handler ([`src/handler.ts`](src/handler.ts)) takes **no input** (`FuncInput = void` → the ADR-0090
void-input contract `{"type":"null"}`) and echoes three env vars back:

```json
{ "config": "$APP_MODE", "secret": "$API_KEY", "shared": "$SHARED" }
```

`APP_MODE` comes only from the ConfigMap, `API_KEY` only from the Secret, and `SHARED` is present in
**both** — so the response proves the **config-then-secrets merge order** (ADR-0092/0093): the Secret wins
the shared key.

Expected invoke body:

```json
{ "config": "prod", "secret": "s3cr3t", "shared": "from-secret" }
```

## Run it

This is the container-mode counterpart to the in-process scenario
`pkg/funcd/config_secret_e2e_test.go`. Run the full Lima/containerd lane (build → self-deploying VM →
Venom):

```bash
nix develop -c just lima-example-env-echo
```

The recipe builds `env-echo.mjs` + `env-echo.schema.json` (esbuild + the contract toolchain), stages them
with the four manifests into the VM bundle, boots `scripts/lima-env-echo.yaml` (which pushes the artifact
and applies configmap → secret → function, then probes the Function to `Ready` — both bindings resolved),
and runs [`e2e/env-echo.venom.yml`](../../../e2e/env-echo.venom.yml).

## Files

- `src/handler.ts` — the typed void-input handler that echoes the injected env.
- `build.ts`, `package.json`, `tsconfig.json` — the contract-aware build (emits `env-echo.mjs` +
  `env-echo.schema.json`, both gitignored).
- `configmap.yaml` — the `app-config` ConfigMap (`APP_MODE=prod`, `SHARED=from-config`).
- `secret.yaml` — the `app-secret` Secret (`API_KEY=s3cr3t`, `SHARED=from-secret`, base64-encoded).
- `function.yaml` — the `env-echo` Function binding both.
- `funcdconfig.yaml` — the daemon config for the containerd lane.
