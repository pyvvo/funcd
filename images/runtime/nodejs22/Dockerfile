# Curated Node.js 22 runtime image (ADR-0054, supersedes ADR-0032/0039 on the *base* only).
#
# Carries the Node 22 runtime + the funcd runtime shim (ADR-0030). The function artifact is
# NOT baked in — the platform delivers it read-only at /var/funcd/artifact via a bind mount
# (ADR-0032 §2) and points FUNCD_ARTIFACT at it. In the sandbox the shim binds FUNCD_PORT on
# 0.0.0.0 inside the per-function netns (ADR-0032 §3); the reconciler addresses it on the
# netns IP. Execution mechanics are unchanged from ADR-0011/0032.
#
# Base: gcr.io/distroless/nodejs22-debian12 (ADR-0054) — still Node 22 (ADR-0039's version
# pin kept), now a distroless base (~50–60 MB compressed, embeddable). Distroless ships node
# as the image ENTRYPOINT (["/nodejs/bin/node"]) and has NO shell or package manager, so funcd
# execs node directly; the shim path is the CMD (i.e. node's first arg) and so becomes the
# effective entrypoint target. Distroless already runs as the non-root "nonroot" user.
#
# This image is EMBEDDED into the funcd binary (go:embed; see internal/runtime/embedimg) and
# imported into funcd's managed containerd at startup — NO registry pull. Build + export it
# with: just build-runtime-images   (docker build -> docker save, gzip -9 OCI tars per arch).
FROM gcr.io/distroless/nodejs22-debian12

# The funcd runtime shim, copied to a fixed path and run as node's entrypoint target.
COPY shim/nodejs/shim.mjs /opt/funcd/shim.mjs

# Where the platform bind-mounts the function artifact (read-only).
ENV FUNCD_ARTIFACT=/var/funcd/artifact/handler.mjs

# Distroless' ENTRYPOINT is ["/nodejs/bin/node"]; CMD supplies node's first arg (the shim).
CMD ["/opt/funcd/shim.mjs"]
