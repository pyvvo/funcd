# funcd — SPEC

> Single-node, self-hosted Functions-as-a-Service for a homebox.
> Clean-room design. No usage caps, no namespace limits, no scale-to-zero lock-in.
> All dependencies Apache-2.0 / MIT. This is your code, with your copyright.
>
> `funcd` is a working name — rename freely (`homefaas`, `boxfn`, …).

---

## 1. Goals / Non-goals

### Goals
- Run arbitrary container-image functions on **one Linux host** via containerd.
- **No artificial limits**: unlimited functions, unlimited namespaces.
- HTTP-invokable functions, sync first, async later.
- Deploy / delete / update / list / logs / secrets through a small REST control API + CLI.
- Auth, routing, and metrics handled by **Apache APISIX (standalone)** — not hand-rolled.
- Cleanly packaged for systemd on a Debian/RHEL homebox.

### Non-goals (v1)
- No clustering / HA / multi-node (single host by design — same as faasd).
- No horizontal autoscaling (one container = one replica).
- No scale-to-zero in v1 (functions stay resident; revisit in a later milestone).
- No Kubernetes.
- No rolling updates (update = redeploy with a brief gap).

### Explicitly NOT derived from faasd
This project takes **architectural inspiration only** from publicly-documented patterns
(containerd + CNI + an API gateway + a function contract). It contains **no faasd/OpenFaaS
source**, links no OpenFaaS-licensed images, and reuses only Apache-2.0 / MIT components.
The function "watchdog" convention we adopt (HTTP server on a fixed port) is a generic
pattern; where we optionally support STDIO functions we use the **MIT-licensed
of-watchdog** convention, not any CE-licensed code.

---

## 2. Substrate & language

**Language: Go (1.23+).** Rationale: containerd's high-level client, `go-cni`, the OCI
runtime-spec builders, and the compose loader are all Go-native and are the only mature
interface to this stack. Rust would mean thin hand-rolled containerd gRPC + reimplementing
CNI orchestration (weeks of yak-shaving); Node is the heaviest runtime for a resident daemon
and fights the container plumbing throughout. Go is the pragmatic and correct choice here.

**Core dependencies (all Apache-2.0 / MIT):**

| Concern | Choice | License |
|---|---|---|
| Container runtime | containerd (`github.com/containerd/containerd`) | Apache-2.0 |
| Networking | `github.com/containerd/go-cni` + reference CNI plugins | Apache-2.0 |
| OCI spec | `github.com/opencontainers/runtime-spec` | Apache-2.0 |
| Gateway / data plane | **Apache APISIX** (container `apache/apisix`) | Apache-2.0 |
| Async bus (M4) | NATS (`nats-io/nats.go`) | Apache-2.0 |
| Metrics | Prometheus (scrapes APISIX) | Apache-2.0 |
| CLI | `spf13/cobra` | Apache-2.0 |
| Function STDIO shim (optional) | of-watchdog convention | MIT |

---

## 3. Architecture

```
   faas client / curl / funcd-cli
              │
              ▼  HTTP (:9080 data plane, key-auth + prometheus)
   ┌────────────────────────────┐        regenerates        ┌───────────────────────────────┐
   │  APISIX (standalone)        │◄───────────────────────── │  funcd (control plane daemon) │
   │  deployment.role: data_plane│   conf/apisix.yaml (#END)  │  • control REST API (:8081)   │
   │  config_provider: yaml      │   hot-reload ≤ ~1s         │  • containerd client          │
   │  plugins: key-auth,         │                            │  • go-cni bridge mgmt         │
   │           prometheus,       │                            │  • secrets / logs             │
   │           proxy-rewrite     │                            │  • (M4) NATS queue-worker     │
   └─────────────┬──────────────┘                            └───────────────┬───────────────┘
                 │ proxy to upstream node                                     │ manages
                 │ <fn-ip>:8080                                               │ (containerd + CNI)
                 ▼                                                            ▼
        ┌────────────────┐  ┌────────────────┐  ┌────────────────┐   one container per function,
        │ function A     │  │ function B      │  │ function C     │   each with a CNI IP on the
        │ HTTP :8080     │  │ HTTP :8080      │  │ HTTP :8080     │   funcd0 bridge (10.63.0.0/16)
        └────────────────┘  └────────────────┘  └────────────────┘
```

**Control/data split.** funcd never proxies user traffic. It only (a) drives containerd/CNI
and (b) rewrites `conf/apisix.yaml`. APISIX owns every request hop: auth, routing, retries,
metrics. This is the key simplification over faasd, which bundled a proprietary gateway.

### 3.1 Deploy flow
1. `POST /functions` to funcd control API with `{name, image, namespace, env, secrets, limits}`.
2. funcd validates namespace label, validates referenced secrets exist.
3. Pull image via containerd (configurable snapshotter, default `overlayfs`).
4. Build OCI spec: env, `CAP_NET_RAW`, secret bind-mounts (`/run/secrets/<name>`),
   resolv.conf/hosts mounts, optional memory limit (`LinuxMemory.Limit`).
5. `NewContainer` → `NewTask` → attach CNI (`funcd0` bridge) → read assigned IP.
6. `task.Start`.
7. **Regenerate `conf/apisix.yaml`**: add an `upstream` (node `<fn-ip>:8080`) and a `route`
   (`/function/<name>` and/or host `…`/namespace-scoped). Write atomically, end with `#END`.
8. APISIX picks up the change within ~1s. Function is live.

### 3.2 Invoke flow
`GET/POST :9080/function/<name>` → APISIX matches route → proxies to the function's upstream
node (its CNI IP:8080). One container = one replica; no LB needed. APISIX `prometheus` plugin
records latency/counters; `key-auth` (or `basic-auth`) enforces access.

### 3.3 Delete / update
- **Delete**: stop+remove task/container, remove CNI, regenerate `apisix.yaml` without the
  route/upstream.
- **Update**: delete-then-deploy (brief gap; documented non-goal to do rolling).

### 3.4 apisix.yaml (generated example)
```yaml
upstreams:
  - id: fn-openfaas-fn-echo
    type: roundrobin
    nodes:
      "10.63.0.7:8080": 1
routes:
  - id: rt-openfaas-fn-echo
    uri: /function/echo
    upstream_id: fn-openfaas-fn-echo
    plugins:
      proxy-rewrite:
        regex_uri: ["^/function/echo/?(.*)", "/$1"]
      key-auth: {}
      prometheus: {}
#END
```

### 3.5 config.yaml (APISIX standalone, static)
```yaml
deployment:
  role: data_plane
  role_data_plane:
    config_provider: yaml
apisix:
  enable_admin: false        # file-driven; no Admin API
plugins:
  - key-auth
  - prometheus
  - proxy-rewrite
plugin_attr:
  prometheus:
    export_addr: { ip: 0.0.0.0, port: 9091 }
```

---

## 4. Component breakdown (proposed layout)

```
funcd/
  cmd/funcd/main.go        # cobra root: up | deploy | rm | ls | logs | secret | version
  internal/
    daemon/                # bootstrap: containerd reachability, CNI init, APISIX supervise
    api/                   # control REST API (:8081) — deploy/delete/list/update/info
    runtime/               # containerd: pull, container+task lifecycle, OCI spec builder
    network/               # go-cni bridge (funcd0, 10.63.0.0/16, host-local IPAM + firewall)
    gateway/               # apisix.yaml renderer (templating + atomic write + #END footer)
    secrets/               # /var/lib/funcd/secrets/<ns>/<name> read/write + bind-mount specs
    logs/                  # task IO → journald/file; tail endpoint
    queue/                 # (M4) NATS subscriber → POST to function route
  deploy/
    config.yaml            # APISIX static config (standalone)
    funcd.service          # systemd unit
    apisix.service         # systemd unit (or run APISIX as a containerd container)
  SPEC.md
  README.md
```

---

## 5. Function contract

A function image MUST serve **HTTP on `:8080`**. Two supported authoring styles:
1. **Native HTTP**: image runs its own server on 8080 (any language/framework).
2. **STDIO (optional)**: image bundles the MIT of-watchdog; funcd sets `fprocess=<cmd>` and
   the watchdog turns each request into a process invocation over stdio. Lets simple
   scripts become functions without writing an HTTP server.

Env injected by funcd: user `env`, mounted `secrets` under `/run/secrets/`, plus
`FUNCD_NAME`, `FUNCD_NAMESPACE`.

---

## 6. Control REST API (v1 surface)

| Method | Path | Purpose |
|---|---|---|
| POST | `/functions` | deploy |
| PUT  | `/functions/{name}` | update (delete+deploy) |
| DELETE | `/functions/{name}` | remove |
| GET  | `/functions` | list (across all namespaces) |
| GET  | `/functions/{name}` | status (image, IP, created, replicas=1) |
| GET  | `/functions/{name}/logs` | tail logs |
| POST/GET/DELETE | `/secrets` | manage secrets |
| GET  | `/system/info` | version, counts (no caps) |
| GET  | `/namespaces` | list/create funcd-labelled containerd namespaces |

(API shape is ours. Optionally add a faas-provider-compatible adapter later if you want to
reuse `faas-cli` — tracked as a stretch goal, not a v1 requirement.)

---

## 7. Milestones

- **M0 — Scaffold.** Repo, `go.mod`, cobra skeleton, containerd connectivity check,
  `funcd0` CNI bridge init, APISIX running in standalone mode with a hand-written route.
  _Exit: `curl :9080/healthz` hits a manually-placed dummy upstream._
- **M1 — One function, end to end.** Pull image → container+task → CNI IP → render
  `apisix.yaml` → invoke through APISIX. _Exit: deploy an `echo` image and curl it via :9080._
- **M2 — Control API.** REST deploy/delete/update/list/info with automatic `apisix.yaml`
  regeneration + atomic reload. CLI wraps it. _Exit: full lifecycle with no manual file edits._
- **M3 — Secrets + logs.** Secret store + bind-mounts; log tailing endpoint.
- **M4 — Async.** NATS + a queue-worker subscriber that POSTs to function routes; `/async`
  route in APISIX enqueues. _Exit: fire-and-forget invocation returns 202 and runs._
- **M5 — Hardening + packaging.** key-auth, Prometheus scrape of APISIX, systemd units,
  install script (Debian + RHEL), docs. _Exit: `systemctl start funcd` on a fresh box._
- **M6 (optional) — Scale-to-zero.** Idle-watcher pauses/stops idle tasks; an APISIX
  pre-route hook (or funcd-side activator) cold-starts on first request.

---

## 8. Risks & open questions

- **containerd access**: run as root or configure socket perms; decide rootless support (defer).
- **CNI prerequisites**: reference plugins in `/opt/cni/bin`, bridge/firewall kernel modules.
- **APISIX reload latency (~1s)**: acceptable for deploy; document it. For zero-gap updates
  later, consider APISIX's API-driven standalone (in-memory POST) instead of file mode.
- **IP discovery**: read CNI result files (like the reference pattern) vs. parse `cni.Setup`
  return directly — prefer the latter (`gocni.Setup` returns the result; avoid scraping
  `/var/run/cni`).
- **Function port convention**: fixed `:8080` vs. per-function configurable target port
  (add `targetPort` to the deploy payload early to avoid a breaking change later).
- **APISIX lifecycle**: run it as a systemd service vs. as a containerd-managed container
  that funcd boots in `up` (the latter keeps everything under funcd, mirrors the supervisor
  pattern). Decision pending — leaning systemd for v1 simplicity.

---

## 9. Why this is clean and unlimited by construction

There is nothing to "remove": the caps in the inspiration project existed because its gateway
and provider were a vendor's proprietary build. Here the gateway is APISIX (Apache-2.0), the
provider is your own Go daemon, and the only limits are the ones the hardware imposes. No
attribution to strip, no license to circumvent — you can use, modify, and even sell this.
