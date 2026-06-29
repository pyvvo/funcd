# funcd justfile — the single task runner for all operations.
# Recipes: help, fmt, lint, test, build, tidy, ci.
# e2e and integration are reserved for FEAT-0000/F20 (testing strategy).

# ---- helpers ----
_has-packages := `go list ./... 2>/dev/null`

# ---- recipes ----

# default recipe: list all available recipes
[group('meta')]
default:
    @just --list

# show this help
[group('meta')]
help:
    @just --list

# format Go source files
[group('go')]
fmt:
    go fmt ./...

# run the linter (golangci-lint via go tool); no-op when no Go packages exist
[group('go')]
lint:
    @if [ -n "{{_has-packages}}" ]; then go tool golangci-lint run ./...; fi

# run all tests; no-op when no Go packages exist
[group('go')]
test:
    @if [ -n "{{_has-packages}}" ]; then go test ./...; fi

# the Linux integration lane (ADR-0025 L4): real sandbox + the full exit-criterion walk.
# Linux only — needs a container runtime; excluded from the pure-Go `just ci` gate.
[group('test')]
test-integration:
    go test -tags integration ./...

# compile all packages
[group('go')]
build:
    go build ./...

# build + EMBED the curated runtime images (ADR-0054): each is a distroless base carrying its
# language runtime + the funcd shim as entrypoint (node on distroless/nodejs22; python on the
# custom distroless 3.14). The artifact is still bind-mounted at deploy (ADR-0032 unchanged).
#
# Unlike the old registry-publish flow, this EXPORTS each image to an OCI tar at gzip MAX (-9)
# and writes it into internal/runtime/embedimg/, OVERWRITING the committed <1 KB placeholders
# with the real per-arch image. funcd then go:embed's the tar and imports it into its managed
# containerd at startup — NO registry pull. Release builds are per-arch (ARCH defaults to the
# host; set ARCH=arm64/amd64 for the matching-arch embed). This recipe needs docker and is NOT
# run by `just ci` (ci stays green on the tiny placeholders). Compression note: gzip -9 trades
# build time for the smallest embed; docker save reuses shared base layers (e.g. the cc base
# across cc-derived images) so a multi-image embed does not pay for the base twice on disk.
ARCH := `go env GOARCH`
[group('runtime')]
build-runtime-images:
    docker build --provenance=false --sbom=false --platform linux/{{ARCH}} -f images/runtime/nodejs22/Dockerfile -t funcd/runtime-nodejs22:latest .
    docker build --provenance=false --sbom=false --platform linux/{{ARCH}} -f images/runtime/python314/Dockerfile -t funcd/runtime-python314:latest .
    docker save funcd/runtime-nodejs22:latest | gzip -9 > internal/runtime/embedimg/nodejs22.tar
    docker save funcd/runtime-python314:latest | gzip -9 > internal/runtime/embedimg/python314.tar
    @echo "embedded OCI tars written to internal/runtime/embedimg/ for {{ARCH}} (replaces the placeholders)"

# regenerate the Node runtime shims from TypeScript (ADR-0037/0044): typecheck + self-test +
# esbuild bundle → shim/nodejs/shim.mjs (single-tenant, go:embed'd) + pool.mjs (pooled
# worker_threads). Needs npm on PATH. Both are generated artifacts; rerun after editing src/*.ts.
[group('runtime')]
build-shim:
    cd shim/nodejs && npm ci && npm run typecheck && npm test && npm run build

# check the Python runtime shim (ADR-0049/0050): strict typecheck + lint + tests, for the shim and
# the example. Runs on Python 3.14 so the subinterpreter pool-host tests (ADR-0050,
# concurrent.interpreters) run rather than skip. Stdlib-only, go:embed'd — no bundle step. Needs uv.
[group('runtime')]
check-shim-python:
    cd shim/python && uv run --python 3.14 ruff check . && uv run --python 3.14 mypy && uv run --python 3.14 pytest -q
    cd examples/python/hello-world && uv run --python 3.14 ruff check . && uv run --python 3.14 mypy && uv run --python 3.14 pytest -q

# run the benchmark & sustainability harness (ADR-0040): drive the data plane + sample memory
# across the memory and file substrate → the RAW report docs/reports/report.{md,json}, plus the
# separate worker-pool comparison docs/reports/pool-report.{md,json} (ADR-0044: density + throughput,
# pooled vs per-function). Both feed the authored docs/reports/bench-overview.md (regenerate via the
# bench-overview skill). Needs node + the shims. Numbers are dev-machine + process-RSS (see caveats).
[group('runtime')]
bench:
    go run ./cmd/funcd bench --out docs/reports --density 8

# characterize Badger's RSS + throughput limits for the slatedb → pure-Go engine question (an ADR-0006
# follow-up). Standalone module (bench/badger) — Badger is NOT a funcd dependency. Writes JSON to
# bench/badger/results/. See bench/badger/FINDINGS.md. Override scale/profile: `just bench-badger 5000000 default`.
[group('runtime')]
bench-badger keys="1000000" profile="lowmem":
    cd bench/badger && go run . -keys {{keys}} -profile {{profile}} -json results/badger-{{keys}}-{{profile}}.json

# --- reproducible Lima bench harness (ADR-0052/0054) on macOS ----------------------------------
# The containerd cgroup-footprint lane (`funcd bench --containerd`) needs Linux + root (cgroup +
# netns); on macOS these four recipes run it in a SELF-PROVISIONING Lima VM (scripts/lima.yaml,
# which installs crun + CNI + the conflist + ip_forward declaratively). funcd is self-contained
# (ADR-0054): it brings its OWN privately-managed containerd + embedded curated images in a
# dedicated namespace — no registry/buildah/image-import. Flow: `just lima-up` (needs docker for the
# image build) → `just lima-bench` → `just lima-report` → `just lima-down`.
lima_name := "funcd-bench"
lima_deps := env('HOME') / ".cache/funcd-lima" # mounted read-only at /mnt/funcd-deps (see the yaml)

# build the real curated images + the funcd binary into the mounted deps dir, then boot the
# self-provisioning Lima VM (crun/CNI/conflist/ip_forward come up via the yaml's provision blocks).
[group('runtime')]
lima-up: build-runtime-images
    mkdir -p {{lima_deps}}
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcd ./cmd/funcd
    limactl start --name {{lima_name}} --tty=false scripts/lima.yaml

# run the containerd footprint lane in the VM (density / duration overridable: `just lima-bench 4 8s`)
[group('runtime')]
lima-bench density="8" duration="10s":
    limactl shell {{lima_name}} -- sudo /mnt/funcd-deps/funcd bench --containerd \
        --density {{density}} --duration {{duration}} --out /tmp/funcd-bench-out

# fetch the footprint report from the VM into docs/reports/
[group('runtime')]
lima-report:
    mkdir -p docs/reports
    limactl shell {{lima_name}} -- sudo cat /tmp/funcd-bench-out/footprint-report.md | tee docs/reports/lima-footprint-report.md
    limactl shell {{lima_name}} -- sudo cat /tmp/funcd-bench-out/footprint-report.json > docs/reports/lima-footprint-report.json
    @echo "→ docs/reports/lima-footprint-report.{md,json}"

# stop + delete the Lima VM (frees the disk)
[group('runtime')]
lima-down:
    -limactl stop -f {{lima_name}}
    -limactl delete {{lima_name}}

# reproduce the fn-to-fn link example (ADR-0064/0058) on REAL containerd sandboxes in Lima: a
# self-contained DEMO. The deploy is DECLARATIVE — the example is tarred + mounted, and the demo VM
# (scripts/lima-fn-to-fn.yaml) extracts → pushes → runs funcd → applies it in its own provision blocks,
# coming up already-deployed (both functions Ready). This recipe only builds the bundle, then INVOKES the
# round-trip + contract-422 + invoke-propagation (prints outputs; asserts nothing), then tears down.
# The containerd analogue of the process-lane `example-fn-to-fn`. Needs docker (embedded-image build) +
# node (the example build); takes minutes (full VM boot).
lima_fn_vm := lima_name + "-fn"
[group('example')]
lima-example-fn-to-fn: build-runtime-images build-shim
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{lima_deps}}
    # the linux binaries + the example bundle the demo VM mounts at /mnt/funcd-deps
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcd    ./cmd/funcd
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcdctl ./cmd/funcdctl
    ( cd examples/js/fn-to-fn && node --experimental-strip-types build.ts )
    tar czf {{lima_deps}}/fn-to-fn.tgz -C examples/js/fn-to-fn \
      greeter.mjs front.mjs greeter-input.schema.json greeter-output.schema.json \
      front-input.schema.json front-output.schema.json greeter.yaml front.yaml funcdconfig.yaml
    # the demo VM deploys itself on boot; tear it down on exit
    trap 'limactl stop -f {{lima_fn_vm}} >/dev/null 2>&1 || true; limactl delete -f {{lima_fn_vm}} >/dev/null 2>&1 || true' EXIT
    limactl delete -f {{lima_fn_vm}} >/dev/null 2>&1 || true
    limactl start --name {{lima_fn_vm}} --tty=false scripts/lima-fn-to-fn.yaml
    # the VM is up ALREADY DEPLOYED (the Ready probe gated start) — drive the demo via the declarative
    # Venom suite (host-side against the forwarded ports; see e2e/fn-to-fn.venom.yml + the venom-e2e skill).
    suite="$(pwd)/e2e/fn-to-fn.venom.yml"
    ( cd {{lima_deps}} && venom run --output-dir {{lima_deps}} --var "vm={{lima_fn_vm}}" "$suite" )
    echo "venom results: {{lima_deps}}/test_results_fn-to-fn.venom.xml"

# the containerd-lane KV example (ADR-0069): a SELF-DEPLOYING VM (scripts/lima-kv.yaml) boots funcd in
# containerd mode with a DURABLE Badger KV (kvstore.engine: badger), pushes + applies BOTH kv-counter
# functions — the JS one (nodejs22) and the Python one (python314), each contract-validated — and comes up
# Ready; this recipe then POSTs each twice: the handler increments a per-name counter via context.kv (→
# worker-node local API → PDP Facade → durable KV), so the count goes 1 then 2 on a real containerd sandbox.
# The containerd analogue of the process-lane `example-kv`. Needs docker + node + uv.
lima_kv_vm := lima_name + "-kv"
[group('example')]
lima-example-kv: build-runtime-images build-shim
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{lima_deps}}
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcd    ./cmd/funcd
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcdctl ./cmd/funcdctl
    # JS kv-counter → counter.mjs + I/O schemas (esbuild + contract toolchain via the shim node_modules)
    ln -sfn ../../../shim/nodejs/node_modules examples/js/kv-counter/node_modules
    ( cd examples/js/kv-counter && node --experimental-strip-types build.ts )
    # Python kv-counter → counter.py (baked validators) + I/O schemas (funcd_shim.build via uv)
    ( cd examples/python/kv-counter && uv run --group build python build.py )
    # Stage both functions into one bundle — py/ subdir keeps the shared schema-file basenames from colliding.
    stage="$(mktemp -d)"
    # ADR-0073: each kv-counter declares its KV binding on the Function (spec.kv) + an owned KVStore with
    # a per-table owner (default-deny — no grant.yaml). ADR-0076: a declared spec.kv binding GRANTS kv::read
    # on its table (built-in permit), so own-table reads need NO read Policy; the owner-write is a built-in
    # forbid (no write Policy). No policy.yaml is staged — the lane proves reads work with no read Policy.
    cp examples/js/kv-counter/counter.mjs examples/js/kv-counter/counter-input.schema.json \
       examples/js/kv-counter/counter-output.schema.json examples/js/kv-counter/counter.yaml \
       examples/js/kv-counter/counter-unbound.yaml examples/js/kv-counter/store.yaml \
       examples/js/kv-counter/funcdconfig.yaml "$stage/"
    mkdir -p "$stage/py"
    cp examples/python/kv-counter/counter.py examples/python/kv-counter/counter-input.schema.json \
       examples/python/kv-counter/counter-output.schema.json examples/python/kv-counter/counter.yaml \
       examples/python/kv-counter/store.yaml "$stage/py/"
    tar czf {{lima_deps}}/kv-counter.tgz -C "$stage" .
    rm -rf "$stage"
    trap 'limactl stop -f {{lima_kv_vm}} >/dev/null 2>&1 || true; limactl delete -f {{lima_kv_vm}} >/dev/null 2>&1 || true' EXIT
    limactl delete -f {{lima_kv_vm}} >/dev/null 2>&1 || true
    limactl start --name {{lima_kv_vm}} --tty=false scripts/lima-kv.yaml
    # Declarative e2e via OVH Venom (Apache-2.0, flake-pinned — ADR-0077): the full positive (each counter
    # reads 1→2 with no read Policy) + fail-closed negative (apply counter-unbound.yaml in-VM, then assert the
    # read is Forbidden) suite. Venom writes venom.log in its CWD, so run it from the scratch dir (outside the
    # repo) with an absolute suite path; --output-dir keeps the JUnit results there too.
    suite="$(pwd)/e2e/kv-counter.venom.yml"
    ( cd {{lima_deps}} && venom run --output-dir {{lima_deps}} --var "vm={{lima_kv_vm}}" "$suite" )
    echo "venom results: {{lima_deps}}/test_results_kv-counter.venom.xml"

# the containerd-lane FUNCLOG e2e (ADR-0081): deploy the JS + Python log-burst examples (each emits >=100
# console./logging logs per invoke) on REAL containerd; the curated-image shim writes Path B over the UDS
# channel and funcd captures the logs as OTLP-JSONL under <dataDir>/blob/logs/. Proves the producer +
# transport + pipeline end to end, for both languages. The containerd analogue of the in-process funclog
# e2e. Needs docker + node.
lima_funclog_vm := lima_name + "-funclog"
[group('example')]
lima-example-funclog: build-runtime-images build-shim
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{lima_deps}}
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcd    ./cmd/funcd
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcdctl ./cmd/funcdctl
    # JS log-burst → burst.mjs (plain esbuild, no I/O contract — the burst is the point)
    ln -sfn ../../../shim/nodejs/node_modules examples/js/log-burst/node_modules
    ( cd examples/js/log-burst && node --experimental-strip-types build.ts )
    # Stage the JS bundle + both manifests + the Python handler (shipped as-is) into one tarball.
    stage="$(mktemp -d)"
    cp examples/js/log-burst/burst.mjs examples/js/log-burst/burst.yaml \
       examples/js/log-burst/funcdconfig.yaml "$stage/"
    mkdir -p "$stage/py"
    cp examples/python/log-burst/src/handler.py examples/python/log-burst/handler.yaml "$stage/py/"
    tar czf {{lima_deps}}/log-burst.tgz -C "$stage" .
    rm -rf "$stage"
    trap 'limactl stop -f {{lima_funclog_vm}} >/dev/null 2>&1 || true; limactl delete -f {{lima_funclog_vm}} >/dev/null 2>&1 || true' EXIT
    limactl delete -f {{lima_funclog_vm}} >/dev/null 2>&1 || true
    limactl start --name {{lima_funclog_vm}} --tty=false scripts/lima-funclog.yaml
    # Declarative e2e via OVH Venom (ADR-0077/0081): each invoke emits >=100 logs, and funcd captured them
    # as OTLP-JSONL under <dataDir>/blob/logs/ (asserted by an in-VM read), for BOTH JS + Python.
    suite="$(pwd)/e2e/funclog.venom.yml"
    ( cd {{lima_deps}} && venom run --output-dir {{lima_deps}} --var "vm={{lima_funclog_vm}}" "$suite" )
    echo "venom results: {{lima_deps}}/test_results_funclog.venom.xml"

# the containerd-lane METASTORE e2e (ADR-0065): boot funcd with the REAL production config (runtime
# containerd + storage file = the pure-Go Badger metastore), apply a ConfigMap, RESTART the daemon, and read
# it back — proving the new engine persists control-plane state across a real daemon restart under
# containerd. Reuses the bench VM (scripts/lima.yaml, which provisions containerd via `funcd install`);
# the smoke runs inside it (scripts/lima-metastore-smoke.sh). Needs docker (embedded-image build).
lima_meta_vm := lima_name + "-meta"
[group('example')]
lima-example-metastore: build-runtime-images
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{lima_deps}}
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcd    ./cmd/funcd
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o {{lima_deps}}/funcdctl ./cmd/funcdctl
    # bundle the static fixtures (daemon config + the ConfigMap resource) into the mounted deps dir (→ /mnt/funcd-deps)
    cp e2e/fixtures/metastore-daemon.yaml e2e/fixtures/metastore-configmap.yaml {{lima_deps}}/
    trap 'limactl stop -f {{lima_meta_vm}} >/dev/null 2>&1 || true; limactl delete -f {{lima_meta_vm}} >/dev/null 2>&1 || true' EXIT
    limactl delete -f {{lima_meta_vm}} >/dev/null 2>&1 || true
    limactl start --name {{lima_meta_vm}} --tty=false scripts/lima.yaml
    # the declarative metastore smoke (ADR-0077): an in-VM daemon-lifecycle suite (start → apply → restart →
    # recover), run via the flake-pinned venom; see e2e/metastore.venom.yml + the venom-e2e skill.
    suite="$(pwd)/e2e/metastore.venom.yml"
    ( cd {{lima_deps}} && venom run --output-dir {{lima_deps}} --var "vm={{lima_meta_vm}}" "$suite" )
    echo "venom results: {{lima_deps}}/test_results_metastore.venom.xml"

# run the CLI demo end to end (build → boot → push/apply/get/invoke → teardown).
# Inputs: docs/demo/demo.yaml · CRD: docs/demo/function.yaml · function: examples/js/hello-world.
# Needs node + npm + yq on PATH.
[group('demo')]
demo:
    bash scripts/demo/setup.sh
    bash scripts/demo/journey.sh
    bash scripts/demo/teardown.sh

# (re-)record the demo GIF + WebM from the tape. Needs vhs + ffmpeg + ttyd (+ node, yq).
[group('demo')]
demo-record:
    vhs docs/demo/cli-demo.tape

# launch funcd locally with the example config (examples/funcdconfig.yaml, ADR-0061): a zero-infra
# dev daemon — in-memory substrate + process runtime, control plane on 127.0.0.1:8080, data plane on
# :8081. Runs until Ctrl-C. With node / python3 on PATH, pushed functions actually execute — then in
# another shell `funcdctl push` + `apply` an example (see examples/*/hello-world/README.md).
[group('example')]
funcd-example:
    go run ./cmd/funcd --config examples/funcdconfig.yaml

# run the fn-to-fn link example end-to-end (ADR-0064/0058): builds the shim + the TS example with its
# CONTRACT build (generated JSON Schema + baked validators), pushes to an OCI layout, applies the
# manifests, and drives the real broker round-trip + contract-422 + invoke-propagation + default-deny.
# Needs node on PATH (the tests skip without it).
[group('example')]
example-fn-to-fn: build-shim
    go test ./pkg/funcd/ -run 'TestScenario(HandlerInvokesLinkedFunction|ContractRejectsBadInput|InvokePropagatesContract422|UnlinkedAliasDeniedE2E)' -v

# build the version-stamped single binary (ADR-0026) → dist/funcd.
# Pure-Go static (CGO_ENABLED=0) — ADR-0065 made the metastore engine pure-Go Badger (no cgo lane).
[group('release')]
release:
    ./scripts/build.sh

# regenerate the OpenAPI spec from Go types (via huma reflection)
[group('go')]
generate:
    go run ./internal/controlplane/cmd/specgen/ -out api/openapi/funcd.v1alpha1.yaml

# tidy go.mod and go.sum
[group('go')]
tidy:
    go mod tidy

# CI pipeline (generate staleness + fmt check + lint + test + build + tidy-diff check)
[group('go')]
ci: tidy generate
    go fmt ./...
    @if [ -n "$(git diff --name-only -- '*.go')" ]; then echo "Run just fmt and commit the result" && exit 1; fi
    @if [ -n "{{_has-packages}}" ]; then go tool golangci-lint run ./...; fi
    @if [ -n "{{_has-packages}}" ]; then go test ./...; fi
    go build ./...
    go mod verify
    @if [ -n "$(git diff --name-only -- go.mod go.sum)" ]; then echo "go.mod or go.sum is not tidy — run just tidy and commit the result" && exit 1; fi
# run the KV example end-to-end (ADR-0069): build the shim + the kv-counter example, push it, apply it,
# and POST twice — the handler increments a per-name counter via context.kv (→ worker-node local API →
# PDP Facade → durable driver), so the count goes 1 then 2. Needs node on PATH (the test skips without it).
[group('example')]
example-kv: build-shim
    go test ./pkg/funcd/ -run TestScenarioE2EKVCounterViaContextKV -v
