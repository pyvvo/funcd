# funcd

Single-node, self-hosted Functions-as-a-Service for a home server.

A small Go daemon that runs container-image functions on **one Linux host** via
[containerd](https://containerd.io) + CNI, and uses [Apache APISIX](https://apisix.apache.org/)
(standalone mode) as the gateway/data plane. No usage caps, no namespace limits — the only
constraints are your hardware's.

- **Control plane:** `funcd` — container lifecycle, networking, secrets, and it rewrites
  APISIX's route file on every deploy.
- **Data plane:** APISIX (`data_plane` / `config_provider: yaml`) — auth, routing, metrics.
  Hot-reloads routes in ~1s. No etcd.

All dependencies are Apache-2.0 / MIT. This is a clean-room project; see [SPEC.md](SPEC.md)
for the full design, milestones, and rationale (including the Go-vs-Rust-vs-JS decision).

## Status

Pre-M0 — design phase. See [SPEC.md](SPEC.md).

## License

TBD (your choice — Apache-2.0 or MIT recommended). Copyright © you.
