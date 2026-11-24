---
title: Data plane
description: How a call goes through the edge, the invoke chain and the activator to a worker that the Executor runs.
---

![An HTTP client's call reaches the edge, which routes it to a Site's files, a Lake's gateway, an add-on object's engine or a Function. A call to a Function, from the edge or from inside funcd, runs the invoke chain. The activator then hands it to a worker that the Executor runs](../../assets/data-plane.drawio.svg)

The data plane serves the calls. An HTTP client's call reaches the edge, which has a listener of its own and routes the
call to a Site's files, a Lake's gateway, an add-on object's engine or a Function. A call to a Function runs the invoke
chain, whether it comes from the edge or from inside funcd. The calls from inside funcd are a Function's or a Job's call
over a link, a workflow step, a Sensor's action, an App's hook and an add-on kind's objects. The activator then hands
the call to a ready worker, and starts one when the Function has none. The Executor runs the workers.

## Edge

![The edge terminates TLS and refuses a call over its limits with 429, 503 or 413. It matches the call to a Route or a Function's name, else it answers 404. It hands a Function's call to the invoke chain, serves a Site's files from its Bucket, passes a Lake's queries to its gateway, and passes an add-on object's calls to its engine](../../assets/data-plane-edge.drawio.svg)

The edge listens on `server.dataPlaneAddr`, apart from the API. By default, it uses a free port on loopback. It
terminates TLS when `server.tls.enabled` is set, with a self-signed certificate by default, with a certificate that the
operator provides (`provided`), or with one from Let's Encrypt (`acme`). Its limits are off by default. `server.limits`
sets a rate per minute for each client IP, a cap on the calls in flight and a body size. The edge refuses a call over
these limits with 429, 503 and 413 respectively, before any worker starts. It can also log each call, record metrics and
traces (`server.observability`), and set CORS, compression and headers (`server.shaping`).

The edge then matches the call against the rules of the Ready Routes. The longest path wins, and a host breaks a tie.
A rule with no host answers on `<namespace>.<baseDomain>`, or on any host when its namespace is `default`. When no rule
matches, `/function/<name>` calls the Function by name, unless its namespace's `defaultExposure` is `explicit`. Any
other call gets 404. The edge passes a Function's call to the invoke chain, serves a Site's files from its Bucket
prefix, passes a Lake's queries to the Lake's gateway, and passes a call to an add-on object's host to its engine,
under its Addon's `basePath`, with 404 for one of its private paths.

## Invoke chain

![A call to a Function passes Admit, else it gets 401, 403 or 404, and Before, else it gets 503, 429, 413 or 422. It then goes through the activator to its worker. The worker's answer passes After, else it becomes a 500](../../assets/data-plane-chain.drawio.svg)

Every call to a Function's worker runs the invoke chain, which checks the call at three points. Admit runs before
funcd reads anything about the Function. When the call's auth stance is `authenticated`, as set by its Route or by its
namespace's default, the caller needs a token that lets it get the Function under RBAC. Otherwise, the call gets 401
or 403. A path under `/health/` gets 404, so no worker starts.

Before runs before the worker is called or started. A namespace that is held for its data refuses the call with 503,
and a rate per Function refuses it with 429. The body becomes the CloudEvent that the worker receives, and a body over
1 MiB gets 413. The call then gets its deadline: the Function's `spec.timeout`, else `invoke.defaultTimeout`, 60 s. A
call past its deadline gets 504. Last, Before checks the input against the Function's contract, and an input that
breaks it gets 422. After runs once the worker has answered: a result that breaks the output contract becomes a 500.

A call from inside funcd skips the edge. A Function's call over a link meets only the hold and the contract. It keeps
its own timeout, 30 s or the link's `timeout`, and gets 429 when 10 such calls to one Function are already in flight
(`invoke.maxNestedInFlight`).

## Activator

![When a worker is ready, the call goes to it. Otherwise, a Failed Function gets 503 at once. In any other case, the activator holds the call and starts a worker for the Function. The call goes to the worker once it is ready, or gets 503 after 30 s. Idle reclaim stops the worker of an idle Function](../../assets/data-plane-activator.drawio.svg)

The activator hands a call to a ready worker. When the Function has none, the activator holds the call and starts a
worker. One start serves all the calls that arrive in the meantime, and they share its timeout, so a call that arrives
later waits less. When the worker is ready, the activator passes the held calls on. A call gets 503 at once when the
Function is `Failed`, and 503 when no worker is ready within `invoke.activationTimeout`, 30 s.

Every 30 s (`invoke.reclaimInterval`), the activator stops the workers of a Function that has served no call for its
`spec.scaling.idleTimeout`. The Function then reads `Idle`. This does not happen when its `minReplicas` is above 0.
With `replicas` above 1, every call goes to the lowest listening replica, and the other replicas wait as spares.

## Executor

![The controller's pass of a Function, of a JobRun, or of an object that runs an engine, asks the Executor to start or stop its workers. runtime.mode picks the process driver, the default, or containerd with crun on Linux as root. Around each worker, the Executor sets up its local API socket, its egress, its volumes and the capture of its logs](../../assets/data-plane-executor.drawio.svg)

The Executor runs the workers that the controller's pass of a Function or of a JobRun asks for. `runtime.mode` picks its
driver. The default, `process`, runs each worker as a host process, without isolation, enforced limits or volumes.
`containerd`, on Linux as root, runs each worker in its own container with crun, on its own network. Several Functions
can share one pooled worker, but only on `process`.

The passes of Lakes, SQLDatabases and add-on objects ask the Executor for their engines, which it runs on the same
driver. On containerd, an add-on's engine is fenced: it admits connections only from loopback and funcd's bridge
address, so a Function reaches it only through funcd, with `ctx.addon.fetch` on its local API, or with any HTTP client
through the add-on proxy, which checks the Function's token.

Around each worker, the Executor opens its local API socket under `<dataDir>/invoke`. When `server.network.egress` is
on, it applies the worker's egress through the fence, the egress gateway and the DNS forwarder. It mounts the worker's
volumes, `emptyDir` or `local`, and captures its logs to the `funcd-system` bucket in Blob, where `funcdctl logs`
reads them. It captures an engine's output there too, which `funcdctl logs <kind>/<name>` reads.

A worker must listen within `runtime.bootTimeout`, 1 min, and answer its readiness check before it takes calls. A
worker that exits after it listened is replaced after `runtime.supervisionPeriod`, 10 s. A worker that keeps crashing
at boot is started again after a wait that doubles from 10 s to 5 min. A stop sends SIGTERM, then SIGKILL after the
grace period, which is 10 s on containerd and 3 s on process.
