# Spec / Prompt de conception — Plateforme funcd, faasd-like, modulaire, single-binary, and inspired by kubernetes internal architecture.


## Context

Design a lightweight serverless platform, inspired by faasd, provisionally named funcd.

The platform must allow deploying, running, exposing, monitoring, and administering serverless functions, or small services on a Linux machine, without mandatory dependency on Kubernetes or Docker.

The platform will also provide services that augment the basic serverless capabilities, such as KV storage, blob storage, eventing sourcing, and other services that can be used by the functions.

- The design must be:
    - modular;
    - single-binary on the platform side;
    - API-first;
    - compatible with SDK/CLI generation via OpenAPI;
    - extensible to multi-node later;
    - compatible with a PaaS vision for wasm / functions / agents;
    - testable in phases with progressive e2e validation;
    - designed to minimize code duplication as much as possible.


## Purpose of the platform

In an AI era where agents are becoming more and more common, the platform will allow to deploy and run agents as serverless functions, with a focus on:
- simplicity of deployment and operation;
- low resource consumption;
- high performance;
- high observability and monitoring capabilities;
- high security and isolation of the functions;
- high extensibility and modularity of the platform;

The platform should be built on existing tools and libraries to avoid reinventing the wheel, and to leverage the existing ecosystem of serverless functions and agents.

## Components

In order to make this platform self-contained, we will need to implement the following components:

- **Function** :
    - **Runtime**: A lightweight runtime that can execute serverless functions written in various languages (e.g., Go, Python, Node.js, etc.) and WebAssembly (WASM). This runtime should be able to handle function invocation, scaling, and lifecycle management.
        - We should follow the OpenFunction spec to capture events and invoke the functions. Aws lambda runtime API is also a good source of inspiration.
        - The function should be of kind "serverless", event based (input) and produce response via output or side effects (e.g., storage, event etc.).  Stateful functions will rely on the services provided by the platform (e.g., KV storage, blob storage, graph database, etc.) to store and retrieve state.
    - **Containerization**: A containerization system that allows functions to be packaged and deployed
    - **Security and Isolation**: A security and isolation system that ensures that functions are executed in a secure and isolated environment, preventing unauthorized access to the host system and other functions. Baseline (V1–V2): runc with conservative OCI defaults — no added capabilities, `no_new_privileges`, default seccomp. Strong isolation is scheduled for V3 (researched 2026-06, to be formalized in its own ADR): **Kata Containers** as a standard containerd runtime-v2 shim — one KVM microVM per function with Dragonball as the default VMM and Cloud Hypervisor as fallback; firecracker-containerd rejected (forked containerd, devmapper requirement, maintenance-mode cadence). The gVisor middle tier was dropped (2026-06-13) — for untrusted code the WASM runtime provides isolation by construction instead. Runtime classes (runc / wasm / microvm) stay selectable per function behind one sandbox interface.

- **Services** :
    - **KV Storage**: A key-value storage system that allows functions to store and retrieve data in a fast and efficient manner. We will use existing technologies like etcd, redis, or rocksdb to provide a simple and efficient KV storage for the functions.
    - **Blob Storage**: A blob storage system that allows functions to store and retrieve large binary objects (blobs) in a fast and efficient manner. We will use existing technologies like minio, ceph, or s3 to provide a simple and efficient blob storage for the functions.
    - **Graph database**: A graph database that allows functions to store and query graph data, which can be used for tasks like social network analysis, recommendation, and knowledge graphs. We will use existing technologies like neo4j, arangodb, or dgraph to provide a simple and efficient graph database for the functions. (https://github.com/petgraph/petgraph, https://kuzudb.github.io/ ...)
    - **Cryptography services**: A cryptography service that allows functions to perform cryptographic operations, such as encryption, decryption, signing, and verification. https://github.com/tink-crypto/tink-go
    - **Monitoring and Logging**: A monitoring and logging system that collects metrics, logs, and traces from the functions and the platform itself. We will only be OpenTelemetry compliant, and we will use existing tools like stdout, stderr, and log files, and/or external monitoring systems like victoria-metrics, victoria-logs, victoria-trace, and grafana for visualization and analysis.
    - **Workflow engine**: A workflow engine that allows functions to be composed into complex workflows, with support for conditional branching, parallel execution, and error handling. we could take inspiration from existing workflow engines like temporal, but try to keep it simple and lightweight and rely on message systems like nats.
    - **Vector database**: A vector database that allows functions to store and retrieve high-dimensional vectors, which can be used for tasks like similarity search, recommendation, and machine learning. We will use existing technologies like pinecone, weaviate, or milvus to provide a simple and efficient vector database for the functions.
    - **Secrets management**: A secrets management system that allows functions to securely store and retrieve sensitive information, such as API keys, database credentials, and other secrets. We will use existing technologies like [openbao](https://openbao.org/)
    - **Eventing system**: An eventing system that allows functions to be triggered by various events, such as HTTP requests, timers, events, or external events from other systems. We will use existing technologies like nats and inspiration from aws eventbridge.
        - Argo events example :
        ```mermaid
        flowchart LR
            subgraph External["External Systems"]
                GitHub
                S3["S3/MinIO"]
                MQTT["MQTT/Kafka/etc."]
            end

            subgraph K8s["Kubernetes Cluster / Argo Events Namespace"]
                Webhook["Webhook svc"]
                EventSource["EventSource Pod(s)"]
                EventBus["EventBus (JetStream/Kafka)"]
                Sensor["Sensor Pod(s)"]
                K8sObjs["Create K8s objs<br/>(CRDs, Deployments, ...)"]
                Workflows["Argo Workflows"]
                Calls["HTTP/NATS/Kafka calls"]
            end

            GitHub --> Webhook --> EventSource
            S3 --> EventSource
            MQTT --> EventSource
            EventSource -- publish --> EventBus
            EventBus -- "subscribe / filter / conditions" --> Sensor
            Sensor -- triggers --> K8sObjs
            Sensor -- triggers --> Workflows
            Sensor -- triggers --> Calls
        ```
        - A more generic eventing system example :
        ```mermaid
        flowchart LR
            subgraph External["EXTERNAL PRODUCERS"]
                GitHub
                S3["S3 / SNS / SQS"]
                Eventing["Eventing systems<br/>(NATS jetstream)"]
                MQTT
                Timers
                Other["…"]
            end

            subgraph Sources["EVENT SOURCES"]
                direction TB
                Adapters["adapters: Webhook, Kafka, NATS, Files, …"]
                Normalize["→ normalize to CloudEvents"]
                Publish["→ publish to subject / topic"]
                Adapters --> Normalize --> Publish
            end

            subgraph Bus["EVENT BUS (messaging layer)"]
                direction TB
                subgraph JetStream["NATS JetStream"]
                    Stream["stream: \"default\""]
                    Subjects["subjects: default.&lt;src&gt;.&lt;ev&gt;"]
                end
                subgraph Kafka["Kafka"]
                    TopicsBus["topics per bus"]
                    TopicsSensor["topics per sensor (event / trigger / action)"]
                end
            end

            subgraph Sensors["SENSORS"]
                direction TB
                Subscribe["→ subscribes to subjects / topics"]
                Filter["→ deps logic, filters, transforms, parameters"]
                Resolve["→ resolves expressions e.g. (A || B) && C"]
                Subscribe --> Filter --> Resolve
            end

            subgraph Triggers["TRIGGERS"]
                direction TB
                Execute["→ executes side effects when deps satisfied"]
                Actions["HTTP calls, serverless, msg publish, notify, …"]
                Execute --> Actions
            end

            External --> Sources --> Bus --> Sensors --> Triggers
        ```

        LEGEND (concise):
        - Event Sources: capture external events → convert to CloudEvents → publish to bus (subject/topic).
        - Event Bus: transports events (JetStream subjects or Kafka topics; optional retention/persistence).
        - Sensors: subscribe; filter/transform; evaluate dependency graph; parameterize trigger payloads.
        - Triggers: invoke HTTP APIs, serverless functions, publish messages, send notifications, call workflow engines, etc.

- **External Dependencies** :
    - **Registry**: This could any OCI compliant registry (e.g., Docker Hub, GitHub Container Registry, etc.) and even local registries like [Zot Registry](https://zotregistry.dev/)
    - **Messaging engine**: A messaging engine that allows functions to communicate with each other and with external systems in a decoupled manner. ([Nats/jetstream](https://github.com/nats-io/nats-server))
    - **Monitoring and Logging**: External monitoring and logging systems that can be integrated with the platform to collect metrics, logs, and traces from the functions and the platform itself. ([Victoria-metrics](https://docs.victoriametrics.com/victoriametrics/index.html), [victoria-logs](https://docs.victoriametrics.com/victorialogs/index.html), [victoria-trace](https://docs.victoriametrics.com/victoriatraces/index.html)) + vmauth for authentication and authorization and HTTP proxy of the monitoring systems.... https://docs.victoriametrics.com/victoriametrics/data-ingestion/opentelemetry-collector/ could be used to collect and export metrics, logs, and traces from the platform and the functions to the monitoring systems by bathing them in the OpenTelemetry Collector.
    - **S3-compatible storage**: An S3-compatible storage system that allows functions to store and retrieve large binary objects (blobs) in a fast and efficient manner. That will as well serve as the storage of the metastore of the platform ([slatedb](https://github.com/slatedb/slatedb))


- **Internal components** :

    - **API Server**: An API server that exposes the platform's functionality via a RESTful API, with support for authentication, authorization, and other API management features. 
    - **Controller**: A controller that manages the lifecycle of the functions and the services, including deployment, scaling, and monitoring. The controller will be responsible for ensuring that the desired state of the functions and the services is maintained, and for taking corrective actions when necessary. It will reconcile the desired state of the functions and the services with the actual state.
    - **Scheduler**: A scheduler that schedules the execution of the functions based on various factors, such as resource availability, function priority, and other scheduling policies.
    - **Messaging layer**: A messaging layer that allows the internal components of the platform to communicate with each other in a decoupled manner. We will use existing technologies like nats to provide a simple and efficient messaging layer for the internal components of the platform. NATS is embedded in-process (nats-server is a plain Go library): JetStream runs with memory storage for tests and file storage for production; pointing funcd at an external NATS cluster stays a drop-in option for multi-node.
    - **Metastore**: A metastore that stores the metadata of the functions and the services, such as their configuration (CRD like kubernetes), state, and other information. We will use existing technologies like slatedb to provide a simple and efficient metastore for the platform.
    - **Control plane**: The control plane that manages the overall operation of the platform, including the API server, the controller, the scheduler, and the messaging layer. The control plane will be responsible for ensuring that the platform is running smoothly and efficiently, and for taking corrective actions when necessary.
    - **Worker**: The worker that executes the functions and the services, and that provides the necessary resources and environment for their execution.
    - **External providers**: The external providers that provide the necessary resources and services for the execution of the functions and the services, such as the registry, the API gateway, the messaging engine, the monitoring and logging systems, and the S3-compatible storage.
    - **Ingress controller**: An ingress controller that manages the ingress traffic to the functions and the services, and that provides the necessary routing and load balancing capabilities. 
        - **API Gateway**: An API gateway that exposes the functions as HTTP endpoints, MCP, gRPC ... via [Apache APISIX](https://github.com/apache/apisix) or [Pingap](https://github.com/vicanso/pingap), with support for authentication, authorization, rate limiting, and other API management features.
    - **Network manager (egress control)**: wires each function sandbox's network namespace (veth/bridge — done directly by the runtime driver on a single node; no Kubernetes CNI machinery needed) and enforces egress policy in two layers: **L3/L4** — nftables default-deny for lateral traffic (function → function only through the gateway, platform services only through their facades) and no direct internet route; **L4–L7** — all remaining outbound TCP/UDP (HTTP, HTTPS, database connections, any custom protocol) is **transparently redirected** at the netns boundary (nftables `REDIRECT`/`TPROXY` on the sandbox veth — no env vars, no app cooperation, nothing to bypass) into the **egress gateway**: a transparent proxy embedded in the funcd binary (a goroutine server, not a child process) that recovers the original destination (`SO_ORIGINAL_DST`/TPROXY), identifies the calling workload by sandbox source IP, captures every connection to the audit channel, and allows/blocks via the in-process PDP against the namespace's `EgressPolicy`. Fail-closed by construction: if the gateway is down, the redirect has nowhere to deliver and default-deny holds. `HTTP_PROXY` env vars are still injected as a courtesy so well-behaved HTTP clients receive a descriptive 403 instead of a reset connection. Note: the messaging layer (NATS) is the platform's *internal communication plane* — it does not replace packet networking; sandboxes still need network wiring.
        - **One policy, tiered enforcement.** `EgressPolicy` compiles to Cedar and is evaluated by the same `auth.Authorizer` PDP as every other decision (action namespace `egress:*`), at four depths with decreasing request context: (1) **wasm host functions** — the guest cannot do I/O except through host-implemented functions (`wasi:http` pattern), so every outbound request is captured *in-process, pre-encryption, with full URL* — capability-based, zero bypass surface; (2) **SDK layer (cooperative, containers)** — the funcd SDK ships a policy-aware `http.RoundTripper` evaluating the same Cedar policies in the function's own process for instant, descriptive denials and traces — DX, not a security boundary (arbitrary binaries can bypass it, and `LD_PRELOAD`-style shims don't work on static Go binaries); (3) **egress gateway (enforced, all protocols)** — the node-level transparent proxy, i.e. the "shared sidecar" (ambient-mesh style: one embedded proxy per node; a sidecar co-process *per function* was rejected — N proxies of RAM for zero policy gain on one box). Context per protocol: full URL for plain HTTP; domain via SNI peek for TLS, no MITM (full-path policy would require a per-namespace MITM CA — invasive, breaks pinning, decide in the egress ADR); `host:port` for raw TCP such as databases, where domain-level rules are enabled by the embedded **DNS forwarder** — sandbox DNS is redirected too, so the gateway correlates resolved IPs with domains (Cilium-style DNS-aware policy); (4) **kernel (enforced, candidate)** — seccomp user-space notification on `connect()`, decided by the worker-side PDP with L4 context only.



## Architecture

The platform will be designed as a modular, single-binary application that can be deployed on a Linux machine. The architecture will be inspired by the internal architecture of Kubernetes, with a clear separation of concerns between the different components of the platform.

> This blueprint defines the target architecture. Concrete decisions — repository setup, gateway rendering mechanics, sandbox lifecycle, scale-to-zero ordering, … — are made per topic in ADRs under `docs/adr/`: each ADR captures the need, constraints, alternatives, and the final contract, and is the source of truth for its topic. The blueprint is kept in sync with accepted ADRs; if they ever disagree, the newest accepted ADR wins and the blueprint gets updated.

 ### Resources definition (CRD-like)

 #### Concept

 We will define the resources of the platform in a CRD-like manner, similar to Kubernetes or [OAM](https://github.com/oam-dev/spec).

So the control plane could have a contract like this:

- Namespace: A logical grouping of functions and services, similar to Kubernetes namespaces.
- Function: A serverless function that can be deployed and executed on the platform.
- Service: A service that provides additional capabilities to the functions, such as KV storage, blob storage, eventing, and other services.
- Event: An event that can trigger the execution of a function, such as an HTTP request, a timer, or a message from a eventing system
- ...

Example of a CRD-like definition for a function:

This below CRD is just an example, and it can be extended to include more fields and capabilities as needed.
```yaml
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: my-function
  namespace: my-namespace
spec:
  runtime: go
  handler: main
  image: my-function:latest
  env:
    - name: MY_ENV_VAR
      value: my-value
  resources:
    limits:
      cpu: "500m"
      memory: "128Mi"
  scaling:
    minReplicas: 0      # 0 = scale-to-zero enabled
    maxReplicas: 5
    concurrency: 10     # target in-flight requests per replica
    idleTimeout: 5m     # reclaim instances after 5 min without traffic
  triggers:
    - name: httpTrigger
      type: http
      route: /my-function
      method: POST
      auth: none
    - name: timerTrigger
      type: timer
      schedule: "*/5 * * * *"
  services:
    - name: my-service
      type: kv
      config:
        bucket: my-bucket
status:                  # owned by the controller, never written by the user
  phase: Ready           # Pending | Deploying | Ready | Idle | Degraded | Failed | Terminating
  observedGeneration: 3
  replicas: 1
  conditions:
    - type: Scheduled
      status: "True"
    - type: RouteConfigured
      status: "True"
#...
```

Every resource follows the same split as in Kubernetes: `spec` is the desired state (owned by the user), `status` is the observed state (owned by the controller). Reconciliation is exactly the process of making `status` converge to `spec`.

This specification can be used to define the desired state of the function, and the controller will be responsible for ensuring that the actual state of the function matches the desired state.

So the Resources definition like function is an high-level Resource definition of :
- Event
- Service
- Config
- Secret

So we could use the same approach for the other resources, and define their desired state in a CRD-like manner, and the controller will be responsible for ensuring that the actual state of the resources matches the desired state.

#### Lifecycle 

| Step | Phase                  | Result                                                                                          |
|------|------------------------|-------------------------------------------------------------------------------------------------|
| 1    | Validate               | YAML/JSON validated, quotas checked                                                             |
| 2    | Store spec             | Raw manifest stored in the metadata store                                                       |
| 3    | Deploy request         | User calls `/deploy`                                                                             |
| 4    | Translate to Vendor RD | The Resource Definition is translated to a vendor-specific format (Apache APISIX, NATS, OpenBAO, etc.) |
| 5    | Slice into tasks       | The OAM is broken down into logical tasks                                                       |
| 6    | Schedule & provision   | The scheduler places the tasks; the worker provisions sandboxes and vendor resources            |
| 7    | Expose                 | Routes and triggers are programmed in the gateway and the eventing system                       |
| n    | Monitor & reconcile    | The controller monitors the function/service and takes corrective actions to maintain desired state |

> Additional steps may occur between 7 and n (e.g., warm-up, canary rollout).

The same lifecycle as a sequence diagram:

```mermaid
sequenceDiagram
    actor Dev as User (CLI / SDK)
    participant API as API Server
    participant MS as Metastore
    participant Bus as Messaging layer (NATS)
    participant Ctrl as Controller
    participant Sched as Scheduler
    participant W as Worker (runtime)
    participant GW as Ingress / API Gateway

    Dev->>API: apply Function manifest
    API->>API: authn/authz · validate schema & quotas
    API->>MS: store spec (desired state)
    API-->>Dev: 202 Accepted (id, generation)
    API->>Bus: publish "resource changed"
    Bus->>Ctrl: notify (watch)
    Ctrl->>MS: read desired state
    Ctrl->>Ctrl: diff desired vs actual → slice into tasks
    Ctrl->>Sched: request placement
    Sched->>W: place instance(s)
    W->>W: pull image · create sandbox (microVM / wasm)
    W-->>Ctrl: actual state (running, healthy)
    Ctrl->>GW: translate & program vendor RD (routes, triggers)
    Ctrl->>MS: write back status (phase: Ready)
```

### Features

#### Namespace

Namespaces are a logical grouping of functions and services, similar to Kubernetes namespaces. Each namespace will have its own set of resources, such as functions, services, events, secrets, and configurations. The namespace will provide isolation between different groups of functions and services, and will allow for multi-tenancy on the platform.

#### Function

Functions are the core building blocks of the platform. Each function will have its own runtime, handler, and configuration, and will be able to interact with other functions and services within the same namespace. Functions will be triggered by events, such as HTTP requests, timers, or messages from an eventing system, and will be able to access secrets and configurations as needed.

#### Service

Services provide additional capabilities to the functions, such as KV storage, blob storage, eventing, and other services. Each service will have its own configuration and will be able to interact with functions within the same namespace. Services will be managed by the controller, and will be able to scale independently of the functions.

#### Event

Events are the triggers that cause functions to be executed. Events can come from various sources, such as HTTP requests, timers, or messages from an eventing system. Each event will have its own configuration and will be able to trigger one or more functions within the same namespace. Events will be managed by the controller, and will be able to scale independently of the functions.

#### Controller

The controller is responsible for managing the lifecycle of the functions and services, including deployment, scaling, and monitoring. The controller will reconcile the desired state of the functions and services with the actual state, and will take corrective actions as needed. The controller will also be responsible for managing events and triggers, and for ensuring that functions are executed in response to events.

Each resource kind gets its own control loop, following the Kubernetes controller pattern:

```mermaid
flowchart LR
    MS[("Metastore<br/>desired state + status")]
    subgraph Loop["Control loop (one per resource kind)"]
        Watch["watch / list"]
        Diff["diff<br/>desired vs observed"]
        Act["act<br/>create / update / delete"]
        Status["write back status"]
        Watch --> Diff --> Act --> Status
    end
    MS -- "change events (via NATS)" --> Watch
    Status --> MS
    Act -. "provision / converge" .-> Providers["Vendor providers<br/>runtime · APISIX · NATS · OpenBAO · …"]
    Providers -. "observed state" .-> Diff
```

#### Config

Configurations provide a way to store configuration information for functions and services, such as environment variables, command-line arguments, and other configuration parameters. Configurations will be managed by the controller, and will be able to be updated dynamically without requiring a redeployment of the functions or services.

#### Secret

Secrets provide a way to store sensitive information for functions and services, such as API keys, database credentials, and other secrets. Secrets will be managed by the controller, and will be able to be updated dynamically without requiring a redeployment of the functions or services.

#### Scaling & scale-to-zero

Functions scale horizontally between `minReplicas` and `maxReplicas`, driven by in-flight request concurrency and/or event-queue depth. With `minReplicas: 0`, idle functions are reclaimed after `idleTimeout` and consume zero resources. The first request or event addressed to a scaled-to-zero function is buffered by an **activator** while the controller scales the function back up (cold start). Cold-start latency can be mitigated with pre-warmed sandbox pools and microVM snapshot/restore (e.g., Kata VM templating).

#### Etc..

### Single-binary process model

Like k3s or faasd, funcd ships as a single binary that runs several cooperating parts:

- **Library-first**: the entire platform is an embeddable Go library (`pkg/funcd`); `cmd/funcd` is a thin shell that parses configuration, selects the drivers (store, bus, gateway, runtime), and calls `funcd.New(...).Run(ctx)`. E2e tests embed the very same library with in-memory drivers — no daemon, no root, no network. See [Repository structure](#repository-structure).
- **In-process (goroutines)**: API server, controllers, scheduler, embedded NATS/JetStream, metastore (pluggable: in-memory / sqlite / slatedb), and the built-in service facades. They communicate through the messaging layer and well-defined interfaces, so any of them can later be extracted into a standalone process (multi-node) without changing APIs.
- **Supervised child processes**: components reused as-is — API gateway (APISIX/Pingap), containerd, OpenBAO, … — are launched, configured, and supervised by funcd itself (config rendering, health checks, restarts), the same way faasd supervises containerd.
- **Crash-only design**: on restart, funcd rebuilds its world view from the metastore plus the actual state of sandboxes and routes, then lets the reconciliation loops converge. No state lives only in memory.
- **Embed-first rule**: a dependency is embedded as a Go library whenever a credible one exists (NATS server, metastore drivers, policy engine, wasm runtime, OTel pipeline); a supervised child process is the fallback only for components with no embeddable form (containerd, APISIX, OpenBAO).

### Platform logging

How the funcd codebase itself logs (info / warn / error) — distinct from function logs, which are tenant telemetry.

- **One API: `log/slog`** (stdlib). No third-party logging API anywhere in the codebase (depguard-enforced); handlers decide rendering: human-readable text in dev, JSON in production.
- **Built once, injected everywhere**: `internal/observability` constructs the root logger at bootstrap from daemon config (`log.level`, `log.format`, `log.otlp`); the app container hands every component a named child logger — `root.With("component", "controller")`. No package-level globals, so tests can assert on log output with an in-memory handler.
- **Canonical fields**: `component`, `namespace`, `kind`, `name`, `generation`, `request_id`, `trace_id`, `span_id`, `error` — dashboards and alerts key on these.
- **Trace correlation**: middleware and control loops carry request-id + OTel span context in `context.Context`; a thin `slog.Handler` decorator lifts `trace_id`/`span_id` from the context into every record, so a log line in victoria-logs links to its trace in victoria-traces. Components use the `*Context` variants (`InfoContext`, …) everywhere.
- **Level conventions**:

| Level | Meaning | Examples |
|-------|---------|----------|
| `Debug` | high-volume internals, off in prod | reconcile diffs, bus message handling, driver chatter |
| `Info` | state transitions worth a timeline | component started, function deployed, route programmed, scaled 0→1 |
| `Warn` | degraded but self-healing | retry with backoff, slow driver, stale heartbeat, fallback used |
| `Error` | an operation failed for good | reconcile gave up after retries, dropped event, recovered panic |

- **Log once at the boundary**: inner layers wrap and return (`fmt.Errorf("render route: %w", err)`); only the outermost owner of the operation (control loop, HTTP middleware) logs it — one failure, one log line.
- **Runtime level switching**: the root level lives in a `slog.LevelVar`; an admin endpoint (`PUT /v1/admin/log-level`) adjusts global or per-component levels without restart.
- **Export**: stdout JSON by default (12-factor — journald or any collector picks it up); optionally the `otelslog` bridge ships the same records through OTLP into victoria-logs — same OTel pipeline as function logs, separate stream labels (`source=platform` vs `source=function`).
- **Child processes**: the supervisor captures stdout/stderr of APISIX / containerd / OpenBAO and re-emits each line through the same slog pipeline (`component=apisix`), so the single-binary deployment has exactly one log stream.
- **Audit is not ops logging**: security-relevant events (who deployed what, policy decisions) go to the dedicated audit channel (`internal/observability/audit.go`) with its own retention; never interleaved with operational logs.

```go
// bootstrap (internal/app)
root := observability.NewLogger(cfg.Log)          // level, format, optional OTLP bridge
ctrl := controller.New(deps, root.With("component", "controller"))

// inside a reconciler
log.InfoContext(ctx, "function deployed",
    "namespace", fn.Namespace, "name", fn.Name, "generation", fn.Generation)
log.WarnContext(ctx, "route render failed, retrying", "error", err, "attempt", n)
```

### Security model

- **API access**: every API-server request is authenticated (static tokens first, OIDC later) and authorized through namespace-scoped RBAC (e.g., admin / developer / viewer roles).
- **Multi-tenancy boundary**: the namespace — quotas, secrets, routes, and service instances are namespace-scoped and never shared across namespaces.
- **Function isolation**: each function runs in its own sandbox — runc container with conservative OCI defaults (V1–V2), WASM sandbox, or microVM from V3 (Kata); no shared filesystem, PID, or network namespace between functions by default.
- **Service credentials**: functions never receive long-lived platform credentials; the worker injects short-lived, scoped workload tokens only for the services declared in the function spec — see [Internal IAM](#internal-iam).
- **Secrets at rest**: encrypted in the metastore (tink / OpenBAO-backed keys), delivered to sandboxes via env vars or tmpfs mounts.
- **Egress control**: sandbox networking is default-deny for lateral traffic — functions reach other functions only through the gateway and other systems only through the bus or declared services; outbound internet egress is governed by the namespace's `EgressPolicy` (domain/CIDR/port allowlists), enforced by the network manager: nftables at L3/L4 plus the egress proxy for L7 capture and audit of HTTP(S). A compromised function cannot scan the host or sibling sandboxes, and every outbound call it makes is observable and blockable.
- **Artifact trust**: images and wasm modules are pinned by digest when a `Revision` is created, and optionally verified against signatures (sigstore/cosign) before a sandbox starts; a pull-through registry cache keeps deploys working when the upstream registry is down.
- **Internal traffic**: mTLS between control plane and workers once deployed multi-node.

### Internal IAM

Functions call functions (sync through the gateway, async through events) and call platform services (kv, blob, …). All of these hops need authentication and authorization **without API keys** — everything is internal, so the platform can do better than shared secrets.

**Identity — minted at sandbox creation.** The control plane is the trust root: it creates every sandbox, so identity is injected at birth (the Kubernetes ServiceAccount token-projection idea):

- every workload gets a SPIFFE-style identity: `spiffe://funcd/ns/<namespace>/fn/<function>/rev/<revision>`;
- the worker mounts a short-lived signed token (minutes, not days) into the sandbox (tmpfs + env var) and rotates it before expiry — nothing to create, store, or revoke manually, which is exactly what kills API keys;
- tokens are audience-bound (a token minted for the kv service is useless against blob or another function) and carry namespace / function / revision claims;
- signing keys come from the platform crypto service (tink, OpenBAO-backed later); verification is local in every enforcement point (public key, no network call on the hot path);
- SPIFFE-compatible naming keeps a clean upgrade path to SPIRE federation in multi-node — without running SPIRE today (embed-first).

**Authorization — one PDP behind a port, PEPs at every hop.**

- the `auth.Authorizer` port is the single decision point (PDP), called in-process by every enforcement point (PEP): API-server middleware (user → platform), service facades (fn → kv/blob/…), gateway & activator (fn → fn sync), bus facade (fn → events), and the egress path (fn → outside world: wasm host functions, egress proxy, network manager — see [Network manager](#components));
- **default deny across namespaces, explicit grants within**: binding a service in `Function.spec.services` *is* the grant for that instance; everything else — fn→fn invoke, cross-namespace event flows, shared services — requires a declarative `Grant` resource, reviewable and reconciled like every other resource.

**Policy engine — OPA, challenged.** OPA embeds fine in Go (the `rego` package is explicitly intended for eval-only embedding), so it fits the embed-first rule. But it is a heavyweight dependency (large dep tree, real binary-size impact) and Rego is a general-purpose datalog — a lot of language for decisions that are 95% "may *principal* do *action* on *resource*?". Layered decision:

1. **Built-in engine (default, zero deps)**: namespace-scoped RBAC for humans + `Grant` evaluation for workloads, default deny. Covers the platform's own needs entirely.
2. **Embedded policy-language driver (optional)** behind the same `Authorizer` port for conditional, fine-grained policies (attribute matches, time windows, …): [cedar-go](https://github.com/cedar-policy/cedar-go) is the default choice — official Go implementation of a purpose-built, analyzable authz language (RBAC + ABAC), dramatically lighter than OPA. An OPA/Rego driver stays a drop-in alternative when the Rego ecosystem matters to an operator; the port makes the choice reversible.
3. **Never OPA-as-sidecar**: policy decisions stay in-process — no HTTP hop on the invoke path.

**Bus-level enforcement — accounts, scoped to what they are good at.** Namespaces map to NATS **accounts** — one per namespace (tens to hundreds), never per-function or per-entity. This is the officially supported JetStream multi-tenancy model and it buys three things cheaply: natively isolated subject spaces, per-account JetStream quotas (`max_mem`, `max_file`, `max_streams`, `max_consumers`) that implement namespace quotas for free, and cross-namespace event flows rendered declaratively as account exports/imports from `Grant` resources.

Known limitation, designed around: raw JetStream **API** permissions do not compose. Bucket/stream names are not tokenizable in ACL subjects (no mid-token wildcards, no dots in bucket names — [nats-server#4225](https://github.com/nats-io/nats-server/issues/4225)), so per-entity buckets force a choice between god-like `$JS.API.>` grants and unmaintainable permission lists; and many buckets are themselves costly, since each KV/Object bucket is a stream and streams are the most resource-consuming JetStream asset. funcd therefore **never exposes the JetStream API to functions**:

- functions hold account-scoped credentials for **plain core-NATS subjects only** (`<ns>.<source>.<event>` — fully tokenizable, trivially wildcardable, cheap to permission);
- KV, blob, and durable consumption go through the platform **service facades**, which hold the privileged JetStream access, own a few pooled buckets per service (maintainer guidance: few buckets, metadata in keys), multiplex tenants via key prefixes (`<namespace>/<binding>/<key>`), create buckets on the fly, and enforce per-request authorization through the in-process PDP. The "privileged facade wrapping JetStream behind a narrow exported API" that raw-NATS users end up hand-building is a first-class platform component here — in-process, audited, with workload identity instead of trial-and-error ACLs;
- if account-per-namespace ever becomes a scaling concern, the `Bus` port collapses to a single account with subject-prefix permissions derived from workload identity — a driver change, invisible to functions and to the API.

```mermaid
flowchart LR
    User["User (CLI / SDK)"]
    FN["Function instance<br/>workload token<br/>ns / fn / rev"]

    subgraph PEP["Enforcement points (PEP)"]
        APIp["API server<br/>middleware"]
        GWp["Gateway / activator<br/>fn → fn (sync)"]
        SVCp["Service facades<br/>fn → kv · blob · …"]
        BUSp["Bus facade + NATS accounts<br/>fn → events"]
    end

    subgraph PDP["Authorizer (PDP, in-process)"]
        RBAC["1 · built-in: RBAC + Grants<br/>(default deny)"]
        ENG["2 · optional driver:<br/>cedar-go (default) / OPA"]
    end

    POL[("RBAC roles · Grant resources ·<br/>service bindings · policies")]
    AUD["Audit channel"]

    User --> APIp
    FN --> GWp & SVCp & BUSp
    APIp & GWp & SVCp & BUSp -- "principal · action · resource" --> PDP
    PDP --- POL
    PDP -- "allow / deny (logged)" --> AUD
```

### Diagrams

#### Global architecture

```mermaid
flowchart TB
    subgraph Clients["Clients"]
        CLI["CLI / SDK<br/>(OpenAPI-generated)"]
        Callers["External callers<br/>(HTTP / gRPC / MCP)"]
        Producers["External event producers<br/>(webhooks, S3, MQTT, timers, …)"]
    end

    subgraph Binary["funcd — single binary (Linux host)"]
        direction TB
        subgraph CP["Control plane"]
            API["API Server<br/>authn/authz · validation · OpenAPI"]
            Ctrl["Controller(s)<br/>reconciliation loops"]
            Sched["Scheduler"]
            Bus["Messaging layer<br/>embedded NATS / JetStream"]
            Meta["Metastore<br/>slatedb"]
        end
        subgraph DP["Data plane (worker)"]
            GW["Ingress / API Gateway<br/>APISIX or Pingap"]
            RT["Function runtime<br/>containerd + shims"]
            VM["microVM sandboxes<br/>kata shim (Dragonball / Cloud Hypervisor)"]
            WASM["WASM runtime"]
            SVC["Built-in services<br/>KV · blob · graph · vector · crypto ·<br/>workflow · eventing · secrets"]
        end
    end

    subgraph Ext["External dependencies"]
        REG["OCI registry<br/>zot, ghcr, …"]
        S3["S3-compatible storage"]
        OBS["Observability (OTel)<br/>victoria-metrics / logs / traces"]
        BAO["OpenBAO"]
    end

    CLI -- REST --> API
    Callers --> GW
    Producers --> GW
    GW -- invoke --> RT
    RT --> VM
    RT --> WASM
    API <--> Meta
    API <--> Bus
    Bus <--> Ctrl
    Bus <--> Sched
    Ctrl -- "program routes / triggers" --> GW
    Ctrl -- "desired state" --> RT
    Sched -- placement --> RT
    VM --> SVC
    WASM --> SVC
    SVC --> BAO
    Meta -- persistence --> S3
    RT -- "pull images" --> REG
    Binary -- "metrics · logs · traces" --> OBS
```

#### Function invocation flow (warm vs cold)

```mermaid
sequenceDiagram
    actor C as Caller
    participant GW as Ingress / API Gateway
    participant Act as Activator
    participant FN as Function instance (sandbox)
    participant Ctrl as Controller / Scheduler

    rect rgb(235, 245, 235)
        note over C,FN: Warm path — at least one instance running
        C->>GW: request (HTTP / gRPC / event)
        GW->>FN: proxy / load-balance
        FN-->>C: response
    end

    rect rgb(245, 238, 225)
        note over C,Ctrl: Cold path — function scaled to zero
        C->>GW: request
        GW->>Act: no healthy upstream → buffer request
        Act->>Ctrl: scale 0 → 1
        Ctrl->>FN: create sandbox, start runtime, health check
        FN-->>Act: ready
        Act->>FN: replay buffered request
        FN-->>C: response
    end
```

#### Resource state machine

```mermaid
stateDiagram-v2
    [*] --> Pending : manifest validated & stored
    Pending --> Deploying : controller picks up, scheduler places
    Deploying --> Ready : instances healthy, routes programmed
    Deploying --> Failed : pull / sandbox / route error
    Failed --> Deploying : retry with backoff
    Ready --> Degraded : partial failure detected
    Degraded --> Ready : reconciliation repairs
    Ready --> Idle : no traffic for idleTimeout (scale-to-zero)
    Idle --> Deploying : request / event arrives (scale from zero)
    Ready --> Terminating : delete requested
    Idle --> Terminating : delete requested
    Terminating --> [*] : sandboxes, routes & vendor resources reclaimed
```

### Open design points

A few important things intentionally left open at this stage:

- **Build pipeline**: the blueprint assumes functions arrive as OCI images / WASM modules; how source code is built and pushed (buildpacks, `ko`, nixpacks, in-platform builder?) is not yet specified.
- **Versioning & rollout**: traffic splitting and canary / blue-green strategies. The immutable `Revision` resource (see [Resource model](#resource-model)) gives the foundation; the rollout mechanics on top are not yet specified.
- **Multi-node path**: workers registering to the control plane over NATS, node heartbeats, and scheduler placement across nodes.
- **Quotas & limits**: per-namespace resource quotas and admission-time enforcement (per-account JetStream limits already cover the bus dimension — see [Internal IAM](#internal-iam)).
- **Backup & disaster recovery**: metastore snapshot/restore and JetStream stream backups; declarative resources keep namespaces re-applyable from manifests (GitOps-style) as a coarse-grained fallback.
- **Platform upgrades**: single-binary swap with forward-only store schema migrations; control-plane/worker version-skew rules once multi-node.

## Repository structure

### Platform as a library

The hard design constraint: **funcd is a Go library first, a daemon second.**

- `pkg/funcd` is the embeddable facade: `funcd.New(...Option) (*Platform, error)`, `Run(ctx)`, `Shutdown(ctx)`.
- Every infrastructure dependency hides behind a small interface ("port") with at least two drivers — one real, one in-memory:

| Port | Production driver | Dev / e2e driver |
|------|-------------------|------------------|
| `store.Store` (metastore) | sqlite (file) — slatedb (S3-backed) later | in-memory |
| `bus.Bus` (messaging) | embedded NATS JetStream, file storage | embedded NATS with memory storage, or pure in-memory bus |
| `gateway.Gateway` (ingress) | APISIX (rendered standalone config) | embedded Go reverse proxy |
| `runtime.Runtime` (sandboxes) | containerd + runc (kata microVM shim from V3) | plain process / wasm |

- `cmd/funcd` only holds the configuration of external components and driver selection — zero business logic.
- The e2e harness boots the platform with the `InMemory()` preset: same code paths, no root, no containerd, no network ports beyond an ephemeral listener.
- **Embedded NATS in production: yes.** nats-server is an ordinary Go dependency; JetStream with file storage gives durability inside the single binary, and an external NATS URL remains a config-level swap for multi-node.

```go
// cmd/funcd — production
plat, err := funcd.New(
    funcd.WithConfigFile("/etc/funcd/funcd.yaml"),
    funcd.WithStore(sqlite.Open(dataDir)),
    funcd.WithBus(nats.Embedded(nats.FileStorage(dataDir))),
    funcd.WithGateway(apisix.New(gwCfg)),
    funcd.WithRuntime(containerd.New(rtCfg)),
)

// tests/e2e — the same platform, zero infrastructure
plat, err := funcd.New(funcd.InMemory())
```

### Layout

```text
funcd/
├── api/                                  # public contracts — the only packages SDK/CLI may depend on
│   ├── openapi/
│   │   ├── funcd.v1.yaml                 # REST surface, source of truth (oapi-codegen)
│   │   └── generated/
│   │       ├── server.gen.go             # strict server interface
│   │       ├── types.gen.go
│   │       └── client.gen.go
│   ├── proto/
│   │   └── funcd/v1/
│   │       ├── controlplane.proto        # worker ⇆ control-plane registration & watch (multi-node)
│   │       ├── worker.proto              # placement & sandbox lifecycle (multi-node)
│   │       └── runtime.proto             # out-of-tree runtime shim contract
│   ├── types/
│   │   └── v1alpha1/                     # CRD-like resource model (matches apiVersion funcd.io/v1alpha1)
│   │       ├── metadata.go               # TypeMeta, ObjectMeta, labels, owner refs
│   │       ├── namespace.go
│   │       ├── function.go
│   │       ├── revision.go
│   │       ├── route.go
│   │       ├── service.go
│   │       ├── eventsource.go
│   │       ├── config.go
│   │       ├── secret.go
│   │       ├── invocation.go
│   │       ├── runtimeclass.go
│   │       ├── worker.go
│   │       ├── gateway.go
│   │       └── status.go                 # shared Conditions / Phase types
│   └── errors/
│       └── problem.go                    # RFC 9457 application/problem+json
│
├── cmd/
│   ├── funcd/
│   │   └── main.go                       # thin shell: config → drivers → funcd.Run(ctx)
│   └── funcdcli/
│       └── main.go                       # separate CLI; depends on pkg/sdk only
│
├── pkg/                                  # public Go surface — "the platform as a library"
│   ├── funcd/
│   │   ├── funcd.go                      # New(...Option) (*Platform, error), Run, Shutdown
│   │   ├── options.go                    # WithStore / WithBus / WithGateway / WithRuntime / …
│   │   └── presets.go                    # InMemory() for tests, Production() defaults
│   └── sdk/                              # ergonomic wrapper over the generated client
│
├── internal/
│   ├── app/                              # composition root behind pkg/funcd
│   │   ├── bootstrap.go                  # wire store, bus, features, controllers, servers
│   │   ├── config.go                     # daemon config schema + validation + env overrides
│   │   ├── container.go                  # dependency container
│   │   └── lifecycle.go                  # ordered start/stop, readiness, crash-only restart
│   │
│   ├── features/                         # vertical slices — one package per resource kind
│   │   ├── feature.go                    # Feature = types + validation + handlers + reconciler
│   │   ├── registry.go                   # features self-register: API routes + control loops
│   │   ├── namespaces/
│   │   ├── functions/
│   │   ├── revisions/
│   │   ├── routes/
│   │   ├── services/
│   │   ├── eventsources/
│   │   ├── configs/
│   │   ├── secrets/
│   │   ├── grants/
│   │   └── invocations/
│   │
│   ├── controlplane/                     # API server (implements the generated server interface)
│   │   ├── server.go
│   │   ├── router.go
│   │   ├── middleware/                   # authn, audit, recovery, request-id, otel
│   │   └── admission/                    # validation, defaulting, quota enforcement
│   │
│   ├── controller/                       # generic reconciliation engine
│   │   ├── manager.go                    # runs every feature's control loop
│   │   ├── reconciler.go                 # watch → diff → act → write status
│   │   └── workqueue.go                  # rate-limited retries with backoff
│   │
│   ├── scheduler/
│   │   └── scheduler.go                  # placement (trivial single-node, pluggable multi-node)
│   │
│   ├── gateway/
│   │   ├── gateway.go                    # Gateway port: ProgramRoutes(desired), health
│   │   ├── activator.go                  # scale-from-zero request buffering
│   │   ├── embedded/                     # built-in reverse-proxy driver (dev / e2e)
│   │   └── apisix/
│   │       ├── exporter.go               # renders apisix.yaml (standalone mode)
│   │       └── template.go
│   │
│   ├── worker/                           # node agent
│   │   ├── worker.go
│   │   ├── supervisor.go                 # child processes: apisix, containerd, …
│   │   └── heartbeat.go
│   │
│   ├── runtime/
│   │   ├── runtime.go                    # Runtime port: Create/Start/Stop/Exec/Logs
│   │   ├── manager.go
│   │   └── providers/
│   │       ├── process/                  # plain OS processes (dev / e2e)
│   │       ├── wasm/                     # wazero / wasmtime
│   │       ├── containerd/               # containerd + runc (kata microVM shim from V3)
│   │       └── external/                 # remote runtimes via runtime.proto
│   │
│   ├── store/
│   │   ├── store.go                      # Store port: CRUD + generations + watch
│   │   ├── memory/                       # e2e / ephemeral
│   │   ├── sqlite/                       # single-node production default
│   │   ├── slatedb/                      # S3-backed driver (later)
│   │   └── migrations/
│   │
│   ├── bus/
│   │   ├── bus.go                        # Bus port: pub/sub + streams
│   │   ├── memory/                       # pure in-process bus (unit tests)
│   │   └── nats/                         # embedded or external NATS/JetStream
│   │
│   ├── eventing/                         # runtime machinery behind EventSource
│   │   ├── source.go                     # adapters → CloudEvents normalization
│   │   ├── sensor.go                     # filters, dependency expressions
│   │   └── trigger.go                    # invoke functions, publish, notify
│   │
│   ├── auth/
│   │   ├── authenticator.go              # request authn: static tokens → OIDC later
│   │   ├── authorizer.go                 # Authorizer port (PDP) — every PEP calls this
│   │   ├── rbac.go                       # built-in engine: namespace RBAC + Grants, default deny
│   │   ├── identity.go                   # workload identity: mint / rotate / verify (SPIFFE-style)
│   │   └── policy/                       # optional embedded engines: cedar-go (default), opa/rego
│   │
│   ├── observability/
│   │   ├── logger.go                     # slog, structured
│   │   ├── metrics.go                    # OTel
│   │   ├── tracing.go                    # OTel
│   │   └── audit.go
│   │
│   ├── platform/                         # tiny shared kernel — zero business logic
│   │   ├── errors.go
│   │   ├── validation.go
│   │   ├── pagination.go
│   │   ├── idempotency.go
│   │   ├── retry.go
│   │   └── clock.go
│   │
│   └── version/
│       └── version.go                    # filled by -ldflags at build time
│
├── tests/
│   ├── e2e/                              # black-box: only pkg/funcd + pkg/sdk imports (depguard-enforced)
│   │   ├── harness/                      # boots funcd.InMemory() per scenario
│   │   ├── fixtures/
│   │   └── scenarios/
│   ├── integration/                      # per-driver: sqlite, embedded NATS, containerd, …
│   └── contract/                         # conformance suites run against every driver of a port
│
├── configs/
│   ├── funcd.yaml                        # production example
│   ├── funcd.dev.yaml
│   └── apisix.example.yaml
├── deploy/
│   ├── systemd/funcd.service
│   └── compose.dev.yaml                  # optional local deps: registry, victoria-*, …
├── scripts/
│   ├── generate.sh                       # openapi + proto codegen (wired to go:generate)
│   ├── test-e2e.sh
│   └── build.sh                          # static single binary + version stamping
├── docs/                                 # blueprint, SPEC, ADRs (architecture decision records)
├── .github/workflows/ci.yml              # lint → unit → codegen-drift → integration → e2e
├── .golangci.yml
├── justfile                              # single task runner (just)
├── go.mod                                # codegen tools pinned via the `tool` directive
├── go.sum
├── LICENSE
└── README.md
```

### Challenged & changed (vs the draft layout)

- **`pkg/funcd` added**: `internal/` alone cannot be embedded by another module or by black-box tests; the library-first requirement demands a public facade (same pattern as embedding nats-server).
- **e2e moved from `internal/e2e` to `tests/e2e`**: inside `internal/` the tests could cheat and import internals; at `tests/e2e` they exercise only the public library + SDK, which is exactly the contract we want to validate (enforced via golangci-lint `depguard`).
- **`internal/controller/` added**: the reconciliation engine is the heart of the design (watch → diff → act → status) yet had no home in the draft.
- **Scheduler promoted out of `worker/`**: scheduling is a control-plane concern (placement decisions); the worker is a node agent that executes placements.
- **Gateway slimmed**: `proxy.go`, `rate_limit.go`, `auth.go` dropped — rate limiting and edge auth are APISIX features programmed through its config; platform authn/authz lives in `internal/auth`. Added `activator.go` (scale-from-zero) and an `embedded/` driver for dev/e2e.
- **`controlplane/handlers/` removed**: the feature registry exists precisely so each feature registers its own handlers; a central handlers package would duplicate it.
- **`api/types/v1` → `v1alpha1`**: matches the manifests (`apiVersion: funcd.io/v1alpha1`); graduate to v1 when the contract stabilizes.
- **`gateway.proto` dropped**: the gateway is programmed via rendered config (APISIX standalone) or in-process calls (embedded driver); no RPC contract needed. `controlplane`/`worker`/`runtime` protos stay — they are the future multi-node seams.
- **`store/slatedb/` slot added**: the blueprint names slatedb as the S3-backed metastore; sqlite is the pragmatic single-node default, memory for tests — all behind the same `Store` port.
- **One task runner only**: two task runners drift apart. `just` chosen (clean recipe syntax, arguments, no `.PHONY` ceremony) — decided in ADR-0001, 2026-06-13.
- **`internal/eventing/` added**: EventSource resources need runtime machinery (adapters, CloudEvents normalization, sensors, triggers) distinct from their CRUD feature slice.
- **`internal/version/` + ldflags added**: standard build-info stamping.

### Resource model

Challenged list — kept, renamed, or removed with reasons:

| Kind | Scope | Notes |
|------|-------|-------|
| `Namespace` | cluster | tenancy boundary (quotas, RBAC, isolation) |
| `Function` | namespaced | desired state; every spec change stamps a new immutable `Revision` |
| `Revision` | namespaced | **replaces draft's `Deployment`** — immutable snapshot of a Function (image + config), enabling rollback and canary; "deployment" is an action, not a state |
| `Route` | namespaced | HTTP exposure: domains, paths, traffic split across Revisions; auto-derived from `Function.triggers`, standalone for advanced cases |
| `Service` | namespaced | **was missing from the draft** although central to the blueprint — declares an instance/binding of an augmenting service (kv, blob, vector, …) |
| `EventSource` | namespaced | declarative event source/binding (the blueprint's "Event" renamed: an event is a runtime occurrence, not a declarative resource) |
| `Config` | namespaced | non-sensitive configuration |
| `Secret` | namespaced | sensitive configuration, encrypted at rest |
| `Grant` | namespaced | explicit permission edge: fn→fn invoke, fn→service access, cross-namespace event flow (within-namespace service bindings are auto-granted from `Function.spec.services`) — see [Internal IAM](#internal-iam) |
| `EgressPolicy` | namespaced | outbound allowlist (domains / CIDRs / ports) for function egress; enforced by the network manager + egress proxy — see [Security model](#security-model) |
| `Invocation` | namespaced, read-only | execution record (status, duration, error) with a retention policy — written by the platform, never by users |
| `RuntimeClass` | cluster | **renamed from draft's `Runtime`** (avoids clashing with the language-runtime concept; mirrors Kubernetes RuntimeClass): process, wasm, microVM flavors |
| `Worker` | cluster, status-owned | node inventory, capacity, heartbeat |
| `Gateway` | cluster | listeners, TLS, domains of the ingress layer |

Removed from the draft list:

- **`Deployment`** → folded into `Revision` + `Route` (traffic shifting), as above.

### funcdcli (separate CLI)

- Separate binary `cmd/funcdcli`, releasable on its own; depends only on `pkg/sdk` + `api/*` — never on `internal/`.
- kubectl-style UX: `funcdcli get|describe|apply|delete <kind> [name]`, `funcdcli apply -f fn.yaml`, plus verbs that map to subresources: `funcdcli invoke my-fn --data '…'`, `funcdcli logs my-fn -f`, `funcdcli rollout undo function/my-fn`.
- Client config in `~/.funcd/config.yaml` (contexts: server URL, token, default namespace).
- The SDK and CLI consume the generated OpenAPI client, so CLI, SDK, and server cannot drift from the spec.

### Go best practices baked in

- **Single module**, generated code committed; CI re-runs codegen and fails on diff (`git diff --exit-code`).
- **Codegen tools pinned** in `go.mod` via the `tool` directive (oapi-codegen, buf) — reproducible generation, no "works on my machine".
- **Contract tests over mocks**: one conformance suite per port (`Store`, `Bus`, `Gateway`, `Runtime`) executed against every driver — the in-memory driver is guaranteed to behave like the real one, which is what makes the e2e-on-library strategy trustworthy.
- **Import discipline**: `api/` imports nothing internal; `features/*` never import each other (they communicate via the bus); `platform/` has no business logic; enforced with `depguard`.
- **Errors**: RFC 9457 `application/problem+json` on the wire, wrapped sentinel errors (`errors.Is/As`) internally.
- **Context-first**: every blocking call takes `context.Context`; no package-level singletons; `log/slog` for structured logging, OTel for traces/metrics.
- **Lint & CI**: `.golangci.yml` (govet, staticcheck, depguard, errcheck, …); pipeline = lint → unit → codegen-drift → integration (per-driver) → e2e (in-memory platform) → e2e (full, Linux VM with containerd).



