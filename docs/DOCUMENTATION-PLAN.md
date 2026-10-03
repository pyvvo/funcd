# funcd — user documentation plan

> The proposed structure of the **user documentation** — the tree of topics, one comment per page,
> and what each page is grounded in. This is a plan to annotate, not the documentation itself.
> **Status: Draft** (2026-09-18).

## Structural decisions

1. **Two audiences, two trees.** `docs/adr`, `docs/feat`, `docs/roadmap`, `docs/reviews` are the
   *design system* ([ADR-0000](adr/0000-adr-process.md)): why, and in what order. They stay as they
   are. The user documentation is a separate tree that answers *how do I use it* and links into the
   design tree for rationale — a user page never restates a decision.
2. **Diátaxis split.** Start → Concepts → Guides → Reference → Operations → Contributing. Concepts are
   further split by the platform's own capability model ([ADR-0082](adr/0082-provider-model-catalog.md)):
   **core** (the trusted daemon kernel, never function-bindable) · **providers** (what a `spec.*` binding
   links to — built-in = in-daemon pure-Go, add-on = out-of-daemon managed engine) · **external**
   (third-party systems funcd integrates with). The dividing question, from the blueprint: *can it be
   embedded pure-Go in the trusted daemon with direct port access?* Yes → built-in; no → add-on. `bus`
   is core, not a provider: internal plane, no `spec.*` binding.
3. **Reference is generated, not written.** 27 kinds in `api/types/v1alpha1`, the OpenAPI at
   `api/openapi/funcd.v1alpha1.yaml`, ~30 cobra verbs, the DTO validation reference (ADR-0048).
   Hand-written copies drift.

Every page describes the platform **as built**. Where the blueprint states a target that is not
shipped, the page says so in an as-built / target split, the way the security deep-dive does.

## The tree

```
docs/
├─ 0-start/
│  ├─ what-is-funcd.md              # the 3 anchors (bottomless, ports+drivers, hexagonal) + design point (~100 agents / 8-core / 18 GB) — PROJECT-SUMMARY §1, FEAT-0000
│  ├─ when-to-use-it.md             # single box, RAM-bound, scale-to-zero agents/MCP; explicit non-goals (multi-node, OIDC, canary, north-south egress)
│  ├─ install.md                    # exists (docs/install.md); add `funcd install` self-provisioning of crun/containerd/CNI (ADR-0054/0056)
│  ├─ quickstart.md                 # push → apply → get → invoke in 5 min; reuse docs/demo (gif + journey.sh)
│  └─ glossary.md                   # Function/Revision/worker/worker node/Route/Sensor/Site…; the repo reuses words precisely (ADR-0045/0079)
│
├─ 1-concepts/
│  ├─ overview/
│  │  ├─ architecture.md            # global diagram, single-binary process model, control vs data plane listeners; core/provider/external as the map
│  │  ├─ resource-model.md          # metadata/spec/status, 7-step deploy lifecycle, phase state machine (ADR-0003)
│  │  └─ provider-model.md          # ADR-0082: binding = link, port + ≥2 drivers = contract; built-in vs add-on and the dividing question (pkg/funcd/providers.go)
│  │
│  ├─ core/                         # the trusted daemon kernel — nothing here is function-bindable
│  │  ├─ api-server.md              # huma/OpenAPI control plane, authn middleware, admission framework, referential-integrity admission (ADR-0005/0018/0063/0121)
│  │  ├─ controller.md              # one reconcile framework, one loop per kind, default-deny on drift, no-op write coalescing (ADR-0015/0047)
│  │  ├─ metastore.md               # store.Store port, Badger engine, RV/generation/watch semantics (ADR-0065)
│  │  ├─ bus.md                     # embedded NATS/JetStream as the internal plane; why it is not a provider (ADR-0008)
│  │  ├─ scheduler-and-activator.md # placement port, scale-to-zero, buffer → wake → forward (ADR-0016/0017)
│  │  ├─ worker-node-and-runtimes.md# runtime.Runtime port: process vs containerd/crun, curated images, per-worker netns, worker-node UDS API (ADR-0032/0054/0064)
│  │  ├─ runtime-shim-and-pooling.md# Node/Python shims, CloudEvents in, invocation context, worker pooling threads/subinterpreters (ADR-0030/0049/0044/0050)
│  │  ├─ functions-and-contracts.md # Function → Revision (digest-pinned); types → JSON Schema → precompiled validator; 422/500/204 (ADR-0020/0035/0058/0060/0090)
│  │  ├─ iam.md                     # authn, Authorizer PDP port, built-in RBAC, Cedar, Identity/Role/RolesAssignment, the PEP map (ADR-0018/0074–0076/0135–0137)
│  │  ├─ network-manager.md         # netns/bridge wiring, L3/L4 lateral default-deny, TLS termination; as-built vs blueprint's four-depth egress (ADR-0111/0115)
│  │  ├─ workflow-engine.md         # Workflow/WorkflowRun state machine, reference engine (goja), typed edges, sub-workflows, replay (FEAT-0005, ADR-0094–0107)
│  │  └─ observability-kernel.md    # slog root, OTLP telemetry, audit channel, one-run-one-trace span model (ADR-0009/0010/0101–0105)
│  │
│  ├─ providers/
│  │  ├─ builtin/                   # in-daemon, pure-Go, always-on — one page per descriptor in pkg/funcd/providers.go
│  │  │  ├─ kv.md                   # KVStore kind, spec.kv, sub-domain tables, Badger engine, DR backup + CDC (ADR-0066–0073)
│  │  │  ├─ blob.md                 # Bucket kind, spec.blob, gocloud drivers mem/file/S3 (ADR-0007/0021)
│  │  │  ├─ s3.md                   # S3/SigV4 frontend over blob, per-function keypair, provider-as-principal (ADR-0080/0085/0088)
│  │  │  ├─ secrets.md              # encryptor + PDP-authorized resolver, last-mile injection (ADR-0022/0057)
│  │  │  ├─ eventing.md             # EventSource v2, Sensor, Invocation record, DLQ, object-store source (ADR-0108/0109/0118/0119)
│  │  │  ├─ invoke.md               # fn-to-fn links: spec.links, context.invoke, UDS-brokered (ADR-0064)
│  │  │  ├─ ingress.md              # gateway.Gateway port, Route v2, static Route backend, limits, edge PEP, shaping (FEAT-0006, ADR-0120)
│  │  │  ├─ egress.md               # egress gateway PEP, NetDestination/EgressPolicy, default-deny + audit (FEAT-0007)
│  │  │  └─ log-ingest.md           # function-telemetry side channel → OTLP-JSONL → Parquet, + the in-daemon read path / funcdctl logs (ADR-0081/0083/0084)
│  │  └─ addon/                     # out-of-daemon managed engines, deployed by the provider runtime
│  │     ├─ provider-runtime.md     # internal/provider over runtime.Runtime: converge, readiness probe, optional ingress route (ADR-0087)
│  │     └─ catalog-query.md        # CatalogService, DuckLake/DuckDB/Quack, spec.catalogs binding, catalog proxy + catalog::query PEP, external Route (ADR-0086/0091/0137/0138)
│  │
│  └─ external/                     # third-party systems funcd integrates with — not platform-offered
│     ├─ oci-registry.md            # any OCI registry (or oci-layout://); push/pull via oras; digest resolution (ADR-0031/0035)
│     ├─ s3-compatible-storage.md   # the blob substrate's S3 backend; the metastore does NOT depend on it (ADR-0007/0065)
│     └─ otlp-collector.md          # telemetry.endpoint → any OTLP gRPC collector (ADR-0010, funcdconfig.yaml)
│
├─ 2-guides/                        # how-to — one task per page, every page runnable against examples/
│  ├─ write-a-function/
│  │  ├─ typescript.md              # lift from funcd-typescript examples/hello-world/README.md (already the right shape)
│  │  ├─ python.md                  # uv project, handler.py, dependency bundling, fastjsonschema (ADR-0049/0071/0089)
│  │  └─ funcdctl-yaml.md           # client config: runtime/handler/bindings/contract; multi-function files (ADR-0122/0124)
│  ├─ local-dev-loop.md             # `funcdctl dev`: from source, zero CRDs, --persist, print-env, interpreter config (ADR-0125–0132)
│  ├─ package-and-push.md           # OCI artifact: push/pull/inspect/login; digest pinning at Revision (ADR-0031/0035/0059)
│  ├─ deploy-and-expose.md          # Function + Route; TLS, limits, edge authn, shaping (FEAT-0006)
│  ├─ config-and-secrets.md         # ConfigMap/Secret binding, injection order (config then secret, secret wins), key file (ADR-0022/0057/0093)
│  ├─ use-kv.md                     # KVStore, spec.kv, tables, typed accessors, binding-as-read-grant, backup + CDC (examples/*/kv-counter)
│  ├─ use-blob-and-s3.md            # Bucket, spec.blob, injected SigV4 keypair, boto3/DuckDB httpfs from the same env (funcd-typescript examples/s3-roundtrip)
│  ├─ call-another-function.md      # fn-to-fn links; 422 propagation (ADR-0064, funcd-typescript examples/fn-to-fn)
│  ├─ triggers.md                   # HTTP, timer, object-store EventSource, Sensor actions (ADR-0108/0109/0119)
│  ├─ build-a-workflow.md           # Workflow/WorkflowRun; run/describe/cancel/replay --from <step>; run logs (funcd-typescript examples/workflow)
│  ├─ query-the-catalog.md          # CatalogService, consumer binding, per-caller catalog::query RBAC (ADR-0086/0091/0137, funcd-python examples/catalog-quack)
│  ├─ logs-and-traces.md            # `funcdctl logs <function>` / `<run>`, OTLP endpoint, cross-sub-workflow trace linking (ADR-0084/0104/0106)
│  ├─ serve-a-static-site.md        # static Route backend (ADR-0120); `Site` = ADR-0139 Proposed → marked unbuilt until Implemented
│  ├─ lock-down-egress.md           # NetDestination / EgressPolicy, default-deny (FEAT-0007, funcd-typescript examples/egress-probe)
│  ├─ identity-and-roles.md         # Identity / Role / RolesAssignment (FEAT-0008)
│  ├─ embed-as-a-library.md         # pkg/funcd: New(opts...), presets, providers; docs/demo/server/main.go is the example
│  └─ examples.md                   # index of examples/*: what each proves + which ADR; releve-lakehouse is the capstone
│
├─ 3-reference/                     # look-up — generated where a generator exists
│  ├─ kinds/                        # one page per kind (27), generated from api/types/v1alpha1 + DTO validation reference (ADR-0048)
│  ├─ api/                          # rendered api/openapi/funcd.v1alpha1.yaml; RFC 9457 problem kinds from api/fault
│  ├─ cli/
│  │  ├─ funcdctl.md                # cobra tree: apply/get/describe/delete/push/pull/inspect/login/logout/logs/dev/types/manifest/workflow/eventing/bench
│  │  └─ funcd.md                   # daemon: install/uninstall/bench/version, flags
│  ├─ funcdconfig-yaml.md           # every key, default, FUNCD_* override; precedence flag > env > file > default (examples/funcdconfig.yaml, ADR-0061/0062)
│  ├─ funcdctl-yaml.md              # full key reference for the client config (ADR-0122/0124)
│  ├─ function-context.md           # Node + Python: context.kv / blob / log / links / span; the CloudEvent envelope (funcd-typescript shim/src/types.ts, funcd-python shim/src/funcd_shim)
│  ├─ contract-profile.md           # supported type subset (closed records, enums, unions, Json…) and what fails a push (ADR-0058/0090)
│  ├─ expressions.md                # `${{ … }}` Select + Condition on goja; where they apply (ADR-0095)
│  ├─ cedar.md                      # entities, actions, built-in policies, Policy resource (ADR-0074–0076)
│  ├─ status-and-errors.md          # phases + conditions per kind, fault taxonomy → HTTP mapping (api/types/v1alpha1/status.go, api/fault)
│  ├─ go-library.md                 # pkg/funcd options/presets and pkg/sdk client → pkg.go.dev
│  └─ ports-and-drivers.md          # matrix: port × drivers × dev/prod — the flat view of the core/provider split
│
├─ 4-operations/
│  ├─ deployment-modes.md           # dev (memory + process) vs prod (file + containerd); what changes and why (ADR-0043/0054)
│  ├─ filesystem-and-systemd.md     # /var/lib/funcd/{store,kv,workflow,deadletter,containerd,cni}, hardened unit, CAP_NET_ADMIN (configs/systemd, ADR-0026/0065)
│  ├─ networking.md                 # ports, CNI bridge + subnet, lateral default-deny, TLS termination (ADR-0111/0115)
│  ├─ backup-and-recovery.md        # KV DR backup + CDC, metastore restart semantics (ADR-0067/0068; the metastore Venom lane proves the restart)
│  ├─ sizing.md                     # baseline RSS, pooling density (~2.8× Node / ~3.7× Python), cold start (docs/reports/bench-overview.md)
│  ├─ hardening-checklist.md        # Gate 0–4 from funcd-security-deep-dive §6; token hashing, key file, loopback-only defaults
│  ├─ observability-in-prod.md      # OTLP collector, funclog compaction, audit stream (ADR-0010/0083)
│  ├─ troubleshooting.md            # readiness gate, Failed/Degraded, DLQ inspection, 422 vs 500, containerd/CNI failures
│  └─ upgrade-and-uninstall.md      # binary swap, state left in place, `funcd uninstall`
│
├─ 5-contributing/                  # links into the design tree, duplicates none of it
│  ├─ dev-environment.md            # Nix flake, `just ci` vs `just ci-full`, Lima lanes, colima prerequisite
│  ├─ repository-layout.md          # api / pkg / cmd / internal / shim / e2e / tests / bench (ADR-0001)
│  ├─ conventions.md                # exists (docs/conventions.md → ADR-0002)
│  ├─ testing.md                    # contract suites per port, pkg/funcd e2e, Venom lanes, chaos tier (ADR-0025/0047/0077)
│  ├─ design-process.md             # exists (docs/method/README.md: the whole method, from ADR to chaos campaign and audit)
│  ├─ adr-index.md                  # generated from docs/adr headers (139 today: status + Realizes)
│  ├─ feature-versions.md           # → docs/feat
│  ├─ roadmap.md                    # → docs/roadmap
│  ├─ benchmarks.md                 # → docs/reports/bench-overview.md
│  └─ security-assessment.md        # → funcd-security-summary-table.md + funcd-security-deep-dive.md
│
└─ appendix/
   ├─ comparison.md                 # vs faasd / OpenFaaS / K3s+Knative / Lambda; the "why build funcd" decision (FEAT-0000)
   ├─ limits-and-non-goals.md       # V1 fence in one table: multi-node, OIDC, canary, north-south egress, workload-token enforcement, MCP/Terraform
   └─ faq.md                        # "where is the config component", "casbin or cedar", "is CNI configured", "why Badger not slatedb"
```

## Grounding notes

**Already written, lift as-is:** `docs/install.md`, `docs/conventions.md`, `docs/demo/README.md`,
`examples/funcdconfig.yaml`, `funcd-typescript examples/hello-world/README.md`, `e2e/README.md`,
`docs/reports/bench-overview.md`, the two security docs.

**New writing:** everything in `1-concepts` and most of `2-guides`, sourced from
[PROJECT-SUMMARY](PROJECT-SUMMARY.md) plus each ADR's Context and Decision sections.

**Must be stated as-built, not as the blueprint says:**

- Provisioning is CLI + Go SDK. PROJECT-SUMMARY also names MCP and Terraform; neither exists in code.
- Egress: as-built is L3/L4 lateral default-deny plus the ADR-0115–0117 substrate. The blueprint's
  four-depth enforcement (shim intercept, SNI peek, DNS forwarder, seccomp) is target.
- Workload identity: UDS connection-trust only; the blueprint's minted signed tokens are not built
  (security deep-dive §5.4).
- `Site` ([ADR-0139](adr/0139-site-declarative-static-web-app.md)) is Proposed; the static-site guide
  marks it unbuilt until the ADR reads Implemented.
- The provider catalog no longer lists `observability-serving` (dropped per ADR-0084); the F54 read
  path is documented under `log-ingest`.

## Open questions

- [ ] `egress` sits under `providers/builtin` (it is in the ADR-0082 catalog) while the netns wiring sits
      under `core/network-manager` — one page or two?
- [ ] Generator for `3-reference/kinds/` and `adr-index.md`: a script under `scripts/`, or a docs-site
      build step?
- [ ] Docs-site tooling (mkdocs / docusaurus / plain markdown in-repo) — undecided; the tree is tool-agnostic.
