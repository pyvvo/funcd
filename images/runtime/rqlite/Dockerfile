# funcd's rqlite engine image (ADR-0232): rqlited built statically from the pinned rqlite source, and the pure-Go
# entrypoint funcd-rqlite, on distroless static, as uid 65532. The version and commit come from rqlite.pin, passed by
# `just build-runtime-images`; the entrypoint is built on the host and passed as the build context `entry`. Every
# base is pinned by the digest of its multi-arch index; rqlite's Go modules are checked against its go.sum.
FROM golang:1.26.7-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468 AS rqlite
ARG RQLITE_VERSION
ARG RQLITE_COMMIT
ARG SOURCE_DATE_EPOCH
RUN apk add --no-cache gcc musl-dev git
RUN set -eux; \
    test -n "$RQLITE_VERSION" && test -n "$RQLITE_COMMIT"; \
    git clone --depth 1 --branch "$RQLITE_VERSION" https://github.com/rqlite/rqlite.git /src; \
    test "$(git -C /src rev-parse HEAD)" = "$RQLITE_COMMIT"
WORKDIR /src
RUN set -eux; \
    CGO_ENABLED=1 go build -trimpath -tags 'netgo osusergo' \
      -ldflags "-s -w -extldflags '-static' -X github.com/rqlite/rqlite/v10/cmd.Version=$RQLITE_VERSION -X github.com/rqlite/rqlite/v10/cmd.Commit=$RQLITE_COMMIT" \
      -o /out/rqlited ./cmd/rqlited; \
    ! readelf -l /out/rqlited | grep -q 'Requesting program interpreter'

# The engine alone, for funcdctl dev's embedded linux engine (scripts/build-sql-engine.sh, ADR-0237).
FROM scratch AS engine
COPY --from=rqlite /out/rqlited /rqlited

FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
COPY --from=rqlite /out/rqlited /usr/local/bin/rqlited
COPY --from=entry funcd-rqlite /usr/local/bin/funcd-rqlite
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/funcd-rqlite"]
