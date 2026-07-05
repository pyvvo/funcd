# workflow — a five-step funcd Workflow (ADR-0094)

A minimal but feature-complete Workflow: the engine **materializes** each `image:` step into an
owned Function that pulls + serves on containerd, then drives a run's DAG.

```
ingest ── score ──┬── hi  (when score > 30) ──┐
                  └── lo  (when score <= 30) ──┴── report (join: any)
```

- **ingest → score** — sequential chaining; a parent's output is the next step's input verbatim.
- **hi / lo** — fan-out with exclusive `when` conditions (ADR-0095 native-JS over `step.score.output`);
  exactly one runs, the other is Skipped.
- **report** — `join: any` merges whichever branch survived; its output is the run output.

Each step is an ordinary contract-bearing function (`src/<step>.ts`, `FuncInput`/`FuncOutput`): the
dispatcher delivers the flowing input as a CloudEvent `data`, the shim validates it against the
baked contract, and the return value flows on. The step images are pushed with `--runtime nodejs22`
(the `dev.funcd.runtime.v1` annotation) so the materializer resolves each step's runtime from the
manifest alone.

## Build

```bash
npm install            # or: ln -sfn ../../../shim/nodejs/node_modules node_modules
npm run build          # → <step>.mjs (baked contract validators) + <step>.schema.json
```

## Run the e2e (real containerd, Lima)

```bash
just lima-example workflow
```

This builds + pushes the five step images, applies `workflow.yaml` (the engine materializes
`orders-<step>` Functions), waits for them to be Ready, and runs `e2e/workflow.venom.yml` — which
drives the run/chaining/fan-out/when/join happy paths (both branches) and the pause → resume →
cancel lifecycle over `funcdctl workflow …`.

## Deploy manually

```bash
funcdctl push ingest.mjs oci-layout://<registry>:ingest --schema ingest.schema.json --runtime nodejs22
# … repeat for score, hi, lo, report …
funcdctl apply -f workflow.yaml
funcdctl workflow run orders my-run --input '{"n":4}'   # score 40 > 30 → the hi branch
funcdctl workflow describe my-run                       # watch it reach Succeeded
funcdctl workflow runs                                  # list the workflow's runs
```
