# funcd justfile — the single task runner for all operations.
# Recipes: help, fmt, lint, test, build, tidy, ci.
# e2e and integration are reserved for FEAT-0000/F20 (testing strategy).

# ---- helpers ----
_has-packages := `go list ./... 2>/dev/null`
# the packages with files built only under `-tags dev` (ADR-0125); lint and test them again with the tag
_dev-packages := "./cmd/funcdctl ./internal/catalog/devengine ./internal/catalog/embedengine"

# ---- recipes ----

# default recipe: list all available recipes
[group('meta')]
default:
    @just --list

# show this help
[group('meta')]
help:
    @just --list

# install the git hooks (lefthook — gofmt gate; see lefthook.yml). Auto-run by `nix develop`.
[group('meta')]
install-hooks:
    lefthook install
    @echo "lefthook hooks installed (.git/hooks) — gofmt gate on pre-commit"

# format Go source files
[group('go')]
fmt:
    go fmt ./...

# run the linter (golangci-lint via go tool); no-op when no Go packages exist
[group('go')]
lint:
    @if [ -n "{{_has-packages}}" ]; then go tool golangci-lint run ./...; fi
    go tool golangci-lint run --build-tags dev {{_dev-packages}}

# run the fast test lane; no-op when no Go packages exist. The heavy pkg/funcd e2e
# scenario suite is build-tagged (`//go:build e2e`) and excluded here — see `test-e2e`.
[group('go')]
test:
    @if [ -n "{{_has-packages}}" ]; then go test ./...; fi
    go test -tags dev {{_dev-packages}}

# run the heavy pkg/funcd e2e scenario suite (build-tagged `e2e`; excluded from the fast
# `test`/`ci` lane). CI runs this in a path-gated job (+ always on main); locally,
# `just ci && just test-e2e` (or `just ci-full`) is the full-confidence run. Extra flags go to
# go test (CI passes -v so the job log lists each test's result).
[group('test')]
test-e2e *flags:
    go test -tags e2e {{flags}} ./pkg/funcd/...

# the Linux integration lane (ADR-0025 L4): real sandbox + the full exit-criterion walk.
# Linux only — needs a container runtime; excluded from the pure-Go `just ci` gate.
[group('test')]
test-integration:
    go test -tags integration ./...

# compile all packages
[group('go')]
build:
    go build ./...

# run the local funcdctl build workflow via nektos/act (.github/workflows/release.yml) and drop the
# cross-compiled CLI binaries (linux/darwin x amd64/arm64) into dist/. Needs a docker daemon (colima
# on macOS: `colima start`). act flags come from .actrc. Usage: `just act-build` or `just act-build v1.2.3`.
[group('go')]
act-build version="dev":
    #!/usr/bin/env bash
    set -euo pipefail
    docker pull catthehacker/ubuntu:act-latest   # idempotent; .actrc then runs act with --pull=false
    act workflow_dispatch -W .github/workflows/release.yml --input version={{version}}
    rm -rf dist && mkdir -p dist
    unzip -o ".act-artifacts/1/funcdctl-{{version}}/funcdctl-{{version}}.zip" -d dist
    echo "funcdctl binaries → dist/:"
    ls -lh dist

# run the local multi-arch bundle workflow via nektos/act (.github/workflows/bundle-multiarch.yml, ADR-0145): the
# catalog-quack bundle for linux/amd64 + linux/arm64, pushed per platform and combined into one OCI image index, left
# under .act-artifacts/. Needs a docker daemon (colima on macOS). The job drives the VM's Docker socket (QEMU, the
# hermetic installs), so this recipe overrides .actrc's `--container-daemon-socket -` for this workflow only.
[group('go')]
act-bundle:
    #!/usr/bin/env bash
    set -euo pipefail
    docker pull catthehacker/ubuntu:act-latest   # idempotent; .actrc then runs act with --pull=false
    act workflow_dispatch -W .github/workflows/bundle-multiarch.yml --container-daemon-socket unix:///var/run/docker.sock
    echo "multi-arch layout → .act-artifacts/"

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
build-runtime-images: embedimg-pin
    docker build --provenance=false --sbom=false --platform linux/{{ARCH}} --build-context shim="$(scripts/moddir.sh github.com/pyvvo/funcd-typescript)/shim" -f images/runtime/nodejs22/Dockerfile -t funcd/runtime-nodejs22:latest .
    docker build --provenance=false --sbom=false --platform linux/{{ARCH}} --build-context shim="$(scripts/moddir.sh github.com/pyvvo/funcd-python)/shim" -f images/runtime/python314/Dockerfile -t funcd/runtime-python314:latest .
    docker build --provenance=false --sbom=false --platform linux/{{ARCH}} -f images/runtime/duckdb/Dockerfile -t funcd/runtime-duckdb:latest .
    docker save funcd/runtime-nodejs22:latest | gzip -9 > internal/runtime/embedimg/nodejs22.tar
    docker save funcd/runtime-python314:latest | gzip -9 > internal/runtime/embedimg/python314.tar
    docker save funcd/runtime-duckdb:latest | gzip -9 > internal/runtime/embedimg/duckdb.tar
    @echo "embedded OCI tars written to internal/runtime/embedimg/ for {{ARCH}} (replaces the placeholders)"

# Fetch + bundle the DuckDB+Quack CATALOG ENGINE for ONE target os/arch into
# internal/catalog/embedengine/engine.tar.gz — the process-mode engine `funcdctl dev` embeds
# (ADR-0125 Decision 5, -tags dev). Overwrites the tiny committed placeholder (skip-worktree'd via
# the dep). Delegates to scripts/fetch-catalog-engine.sh (shared with the CI build step so they
# never drift). Fetches per-arch from duckdb.org (no docker) — works for darwin too. NOT run by ci.
[group('runtime')]
build-catalog-engine os arch: catalog-engine-pin
    scripts/fetch-catalog-engine.sh {{os}} {{arch}}

# Pin/unpin the embedengine placeholder's skip-worktree bit (same rationale as embedimg-pin: the real
# ~130 MB engine overwrites the <1 KB placeholder and must never dirty the tree or get committed).
[group('runtime')]
catalog-engine-pin:
    git update-index --skip-worktree internal/catalog/embedengine/engine.tar.gz
    @echo "embedengine placeholder pinned (skip-worktree) — local engine builds won't dirty the tree"

[group('runtime')]
catalog-engine-unpin:
    git update-index --no-skip-worktree internal/catalog/embedengine/engine.tar.gz

# Pin/unpin the embedimg placeholders' skip-worktree bit. `build-runtime-images` OVERWRITES the tracked
# <1 KB placeholders with real 30–100 MB images, which would otherwise leave the working tree permanently
# dirty (and risk a `git commit -a` staging a build artifact). The skip-worktree bit makes git ignore the
# local overwrite: `git status`/`git commit` never see it, so you never have to `git checkout` the tars
# after a lane run. It is LOCAL to this clone (lives in .git/index, not shared) — hence a recipe so a fresh
# clone can re-apply it in one step. `embedimg-unpin` clears it (needed before a legitimate placeholder
# update, e.g. a git pull that changes them). The check-hygiene guard is the backstop that still blocks
# committing a >4 KB blob even if the bit is off.
[group('runtime')]
embedimg-pin:
    git update-index --skip-worktree internal/runtime/embedimg/*.tar
    @echo "embedimg placeholders pinned (skip-worktree) — local image builds won't dirty the tree"

[group('runtime')]
embedimg-unpin:
    -git update-index --no-skip-worktree internal/runtime/embedimg/*.tar 2>/dev/null || true

# run the benchmark & sustainability harness (ADR-0040): drive the data plane + sample memory
# across the memory and file substrate → the RAW report docs/reports/report.{md,json}, plus the
# separate worker-pool comparison docs/reports/pool-report.{md,json} (ADR-0044: density + throughput,
# pooled vs per-function). Both feed the authored docs/reports/bench-overview.md (regenerate via the
# bench-overview skill). Needs node (the shims are embedded). Numbers are dev-machine + process-RSS (see caveats).
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
lima-up:
    #!/usr/bin/env bash
    set -euo pipefail
    scripts/lane-lock.sh $$
    export FUNCD_LANE_LOCK_HOLDER="${FUNCD_LANE_LOCK_HOLDER:-$$}"
    just ARCH={{ARCH}} build-runtime-images
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


# GENERIC data-driven example lane (ADR-0077). `just lima-example <name>` reads the lane's section from
# scripts/lanes.yaml (the REGISTRY), runs scripts/lane.py to execute its `build` + stage its files + the
# registry into lane.tgz, boots the ONE generic VM (scripts/lima-lane.yaml — which reads the same section
# in-guest to push + apply + probe Ready), and runs the lane's Venom suite. Add a lane by adding a section
# to scripts/lanes.yaml — NO per-lane recipe or VM YAML. Needs docker (+ uv for the duckdb lane's `build`).
# Examples: `just lima-example env-echo` · `just lima-example duckdb`.
[group('example')]
lima-example name:
    #!/usr/bin/env bash
    set -euo pipefail
    scripts/lane-lock.sh $$
    export FUNCD_LANE_LOCK_HOLDER="${FUNCD_LANE_LOCK_HOLDER:-$$}"
    just ARCH={{ARCH}} build-runtime-images
    name='{{name}}'; deps='{{lima_deps}}'; vm='{{lima_name}}-{{name}}'
    mkdir -p "$deps"
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o "$deps/funcd"    ./cmd/funcd
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o "$deps/funcdctl" ./cmd/funcdctl
    CGO_ENABLED=0 GOOS=linux GOARCH={{ARCH}} go build -o "$deps/cniadd"   ./e2e/cniadd
    # host driver: run the lane's `build`, stage lane.tgz (files + registry + LANE marker), print its suite.
    suite="$(python3 scripts/lane.py "$name" "$deps")"
    trap "limactl stop -f '$vm' >/dev/null 2>&1 || true; limactl delete -f '$vm' >/dev/null 2>&1 || true" EXIT
    limactl delete -f "$vm" >/dev/null 2>&1 || true
    limactl start --name "$vm" --tty=false scripts/lima-lane.yaml
    # Declarative e2e via OVH Venom (ADR-0077) — the generic VM already gated `limactl start` on the lane's
    # `ready` target, so we invoke immediately. Run from the scratch dir with the absolute suite path.
    # VENOM_VERBOSE=2 keeps each step's output in venom.<pid>.log even when the suite PASSES (venom
    # prints a step's `info:` line to the console only on failure/retry) — so a green run leaves a
    # transcript to cite, not just a pass/fail XML.
    ( cd "$deps" && VENOM_VERBOSE=2 venom run --output-dir "$deps" --var "vm=$vm" "$suite" )
    echo "venom results: $deps/test_results_$(basename "$suite" .yml).xml"

# Prime the local Lima cache with the pinned Debian VM image so lanes boot with NO upstream dependency (the
# digest pin already skips the freshness HEAD on repeat boots; this seeds a COLD cache). Downloads the
# host-arch image from the pinned mirror in scripts/lima.yaml, verifies the sha512 digest, and places it in
# Lima's download cache. Idempotent. Use `from=<url>` to pull from an alternate reachable source (still
# digest-verified) if the pinned host is unreachable. `just lima-cache-image` · `just lima-cache-image from=https://…`
[group('example')]
lima-cache-image from="":
    python3 scripts/lima-cache.py {{ if from == "" { "" } else { "--from " + from } }}

# Run EVERY Venom e2e lane back-to-back: the data-driven lanes from scripts/lanes.yaml (each has its own
# `venom:` suite, ADR-0077) PLUS the metastore lane. Lanes are enumerated from the registry, so a new lane
# is covered automatically; an `optional` lane is left out (`just lima-example <name>` runs it). Continues
# past a failing lane and prints a PASS/FAIL summary, exiting non-zero if any lane failed. Needs docker
# (colima) up. `just lima-example-all`.
[group('example')]
lima-example-all:
    #!/usr/bin/env bash
    set -uo pipefail
    scripts/lane-lock.sh $$ || exit 1
    export FUNCD_LANE_LOCK_HOLDER="${FUNCD_LANE_LOCK_HOLDER:-$$}"
    lanes=$(python3 scripts/lane.py --venom-lanes) || { echo "lima-example-all: cannot list the lanes of scripts/lanes.yaml" >&2; exit 1; }
    echo "venom lanes: $lanes metastore"
    passed=""; failed=""
    for lane in $lanes; do
        echo "═════════════ venom lane: $lane ═════════════"
        if just lima-example "$lane"; then passed="$passed $lane"; else failed="$failed $lane"; fi
    done
    echo "═════════════ venom lane: metastore ═════════════"
    if just lima-example-metastore; then passed="$passed metastore"; else failed="$failed metastore"; fi
    echo "═════════════════════════════════════════════════"
    echo "PASSED:$passed"
    [ -n "$failed" ] && { echo "FAILED:$failed"; exit 1; }
    echo "ALL VENOM E2E LANES PASSED ✅"

# the containerd-lane METASTORE e2e (ADR-0065): boot funcd with the REAL production config (runtime
# containerd + storage file = the pure-Go Badger metastore), apply a ConfigMap, RESTART the daemon, and read
# it back — proving the new engine persists control-plane state across a real daemon restart under
# containerd. Reuses the bench VM (scripts/lima.yaml, which provisions containerd via `funcd install`);
# the smoke runs inside it (scripts/lima-metastore-smoke.sh). Needs docker (embedded-image build).
lima_meta_vm := lima_name + "-meta"
[group('example')]
lima-example-metastore:
    #!/usr/bin/env bash
    set -euo pipefail
    scripts/lane-lock.sh $$
    export FUNCD_LANE_LOCK_HOLDER="${FUNCD_LANE_LOCK_HOLDER:-$$}"
    just ARCH={{ARCH}} build-runtime-images
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
# Inputs: docs/demo/demo.yaml · CRD: docs/demo/function.yaml · function: hello-world from the pinned
# funcd-typescript module (ADR-0141). Needs node + yq on PATH.
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
# another shell `funcdctl push` + `apply` an example (see hello-world in pyvvo/funcd-typescript or pyvvo/funcd-python).
[group('example')]
funcd-example:
    go run ./cmd/funcd --config examples/funcdconfig.yaml

# run the fn-to-fn link example end-to-end (ADR-0064/0058): takes the committed TS example (bundles +
# generated JSON Schema with baked validators) from the pinned funcd-typescript module (ADR-0141), pushes
# it to an OCI layout, applies the manifests, and drives the real broker round-trip + contract-422 +
# invoke-propagation + default-deny. Needs node on PATH (the tests skip without it).
[group('example')]
example-fn-to-fn:
    go test -tags e2e ./pkg/funcd/ -run 'TestScenario(HandlerInvokesLinkedFunction|ContractRejectsBadInput|InvokePropagatesContract422|UnlinkedAliasDeniedE2E)' -v

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

# guard against accidentally committing build artifacts: the ADR-0054 embedimg curated-image tars must
# stay <1 KB placeholders in git (the real per-arch images are `just build-runtime-images` output, NEVER
# committed — see internal/runtime/embedimg/README.md), and no compiled bench binary is tracked. Checks
# the STAGED/committed blob (not the working tree, which may hold a locally-built real image).
[group('go')]
check-hygiene:
    #!/usr/bin/env bash
    set -euo pipefail
    fail=0
    for f in internal/runtime/embedimg/*.tar; do
        sz=$(git cat-file -s ":$f" 2>/dev/null || echo 0)
        if [ "$sz" -gt 4096 ]; then
            echo "hygiene: $f is ${sz}B staged — commit the <1KB placeholder, not the built image (just build-runtime-images output)"
            fail=1
        fi
    done
    if git ls-files --error-unmatch bench/expr-engine/expr-engine >/dev/null 2>&1; then
        echo "hygiene: bench/expr-engine/expr-engine is tracked — it is a compiled build output; keep it gitignored"
        fail=1
    fi
    # ADR-0141: only go.mod/go.sum pin a language module, so a bump reaches every consumer; nothing
    # hard-codes a version or a module-cache path (frozen docs keep history).
    if git grep -n -E 'pyvvo/funcd-(typescript|python|functions)@v[0-9]|pkg/mod/github\.com/pyvvo' -- ':!go.mod' ':!go.sum' ':!docs/adr/' ':!docs/reviews/' ':!docs/legacy/'; then
        echo "hygiene: a language-module version or module-cache path is hard-coded above — resolve it through go.mod (scripts/moddir.sh)"
        fail=1
    fi
    if [ "$fail" -eq 0 ]; then echo "hygiene: clean"; fi
    exit "$fail"

# CI pipeline — the FAST lane (generate staleness + fmt check + lint + fast test + build +
# tidy-diff check). The heavy pkg/funcd e2e suite is build-tagged out; run it via `test-e2e`
# (CI does, in a path-gated job). `ci-full` runs both for full local confidence.
[group('go')]
ci-full: ci test-e2e

[group('go')]
ci: tidy generate check-hygiene
    go fmt ./...
    @if [ -n "$(git diff --name-only -- '*.go')" ]; then echo "Run just fmt and commit the result" && exit 1; fi
    @if [ -n "{{_has-packages}}" ]; then go tool golangci-lint run ./...; fi
    go tool golangci-lint run --build-tags dev {{_dev-packages}}
    @if [ -n "{{_has-packages}}" ]; then go test ./...; fi
    go test -tags dev {{_dev-packages}}
    go build ./...
    go mod verify
    @if [ -n "$(git diff --name-only -- go.mod go.sum)" ]; then echo "go.mod or go.sum is not tidy — run just tidy and commit the result" && exit 1; fi
# run the KV example end-to-end (ADR-0069): take the committed kv-counter example from the pinned
# funcd-typescript module (ADR-0141), push it, apply it, and POST twice — the handler increments a per-name counter via context.kv (→ worker-node local API →
# PDP Facade → durable driver), so the count goes 1 then 2. Needs node on PATH (the test skips without it).
[group('example')]
example-kv:
    go test -tags e2e ./pkg/funcd/ -run TestScenarioE2EKVCounterViaContextKV -v

# Run a bundled example under `funcdctl dev` (ADR-0125) in ONE command, so you can reproduce it easily:
# builds the fat -tags dev funcdctl (embedding the host-arch DuckDB+Quack catalog engine ONLY when the
# example binds a catalog — fetched once, ~63 MB) and runs the example FROM SOURCE — a localhost gateway
# + S3 (+ catalog), zero hand-written CRDs, real contract enforcement. The embedded shims are already
# built into funcdctl, so no push. <project> is `js/<name>` / `python/<name>` or a bare unique example name
# from the pinned language modules (run from a writable copy under .modcopy/, ADR-0141), or a path (used
# as-is, e.g. a sibling clone); a workflow example (has workflow.yaml) runs its DAG.
# Extra flags pass through. Examples:
#   just dev-example catalog-quack            # the DuckLake/Quack catalog example (python)
#   just dev-example js/kv-counter --persist  # persist KV/blob across runs (qualify the ambiguous name)
#   just dev-example workflow                 # runs funcd-typescript's examples/workflow/workflow.yaml as a DAG
# Reproduce any bundled example locally with `funcdctl dev` — build, boot, serve from source.
# Ports are args (default gateway 3005 / S3 3006 / control-plane 3007) so the URLs are reproducible and the
# `seed-releves`/`run-releve` recipes reach the control plane out of the box: `just dev-example catalog-quack`
# (or pass different ports: `just dev-example <p> 3005 3006 3007`). Extra flags still pass through after them.
[group('example')]
dev-example project gport="3005" s3port="3006" cport="3007" *args:
    #!/usr/bin/env bash
    set -euo pipefail
    root="$PWD"
    dir="$(scripts/example-copy.sh "{{project}}")"
    target="$dir"; [ -f "$dir/workflow.yaml" ] && target="$dir/workflow.yaml"
    echo "▶ example: $target"
    # A catalog example needs the real engine embedded — fetch once (skip-worktree'd so it never dirties the tree).
    if grep -rql 'catalogs:' "$dir" --include='*.yaml' 2>/dev/null && [ "$(wc -c < internal/catalog/embedengine/engine.tar.gz)" -lt 4096 ]; then
      echo "▶ fetching the DuckDB+Quack catalog engine for $(go env GOOS)/$(go env GOARCH) (once) …"
      git update-index --skip-worktree internal/catalog/embedengine/engine.tar.gz 2>/dev/null || true
      scripts/fetch-catalog-engine.sh "$(go env GOOS)" "$(go env GOARCH)"
    fi
    mkdir -p dist
    echo "▶ building funcdctl (-tags dev) …"
    go build -tags dev -o dist/funcdctl-dev ./cmd/funcdctl
    # Fixed ports (args, default 3005/3006/3007) so the URLs are reproducible run-to-run and the seed/run
    # recipes reach the control plane without extra flags.
    # Launch FROM the example dir: the manifest's dev.backends are CWD-relative (file://.funcd-dev/blob),
    # so running here lands .funcd-dev BESIDE the example copy (co-located with the manifests it belongs
    # to) instead of at the repo root. Build/fetch above stay repo-root-relative.
    if [ "$target" = "$dir" ]; then rel="."; else rel="$(basename "$target")"; fi
    echo "▶ (cd $dir) funcdctl dev $rel --gport {{gport}} --s3port {{s3port}} --cport {{cport}} {{args}}"
    cd "$dir"
    exec "$root/dist/funcdctl-dev" dev "$rel" --gport {{gport}} --s3port {{s3port}} --cport {{cport}} {{args}}

# Flags pass through to landing/generate_synthetic.py; output lands in the example copy's landing/
# (.modcopy/funcd-python/examples/releve-lakehouse/landing/) as synthetic-releve-*.pdf. Examples:
#   just gen-releve                                    # defaults (Jan–Nov 2025, monthly)
#   just gen-releve --start 2025-01 --end 2026-01      # a 13-month series (→ a 13-row gold mart)
#   just gen-releve --period-months 2 --seed 7         # bi-monthly, seeded
#   just gen-releve --min-tx 40 --max-tx 60            # force multi-page statements
# Generate FAKE bank statement PDFs for the releve-lakehouse example (fake data, Faker, reproducible).
[group('example')]
gen-releve *args:
    cd "$(scripts/example-copy.sh python/releve-lakehouse)" && uv run --group build python landing/generate_synthetic.py {{args}}

# Seed the FULL synthetic releve series into the dev S3 landing prefix, then run the pipeline ONCE — extract
# processes EVERY PDF in landing/ (bronze is 1:1 with a source file; build-silver aggregates), so a single
# run yields a gold mart spanning every month. Depends on gen-releve. ONE prereq: `just dev-example
# releve-lakehouse` running (its default ports are 3005/3006/3007). S3 creds are auto-loaded via `funcdctl dev
# … --print-env` — no copy-paste. Pass the daemon's S3 port if it isn't 3006; control plane defaults to /dev
# token (override via FUNCD_SERVER / FUNCD_TOKEN).
[group('example')]
seed-releves s3port="3006": (gen-releve "--start" "2025-01" "--end" "2026-01")
    #!/usr/bin/env bash
    set -euo pipefail
    dir="$(scripts/example-copy.sh python/releve-lakehouse)"
    bin=dist/funcdctl-dev
    [ -x "$bin" ] || { echo "build the dev binary first: just dev-example releve-lakehouse" >&2; exit 1; }
    eval "$("$bin" dev "$dir/workflow.yaml" --s3port {{s3port}} --print-env)"   # load the dev S3 creds (no boot)
    export FUNCD_SERVER="${FUNCD_SERVER:-http://127.0.0.1:3007}" FUNCD_TOKEN="${FUNCD_TOKEN:-funcd-dev-token}"
    echo "▶ pushing releves → s3://releves/landing/"
    aws s3 sync "$dir/landing/" s3://releves/landing/ --exclude '*' --include 'synthetic-releve-*.pdf'
    echo "▶ funcdctl workflow run releve-pipeline   (server-generated name; ONE run, no input — extract processes ALL of landing/)"
    run=$("$bin" workflow run releve-pipeline | sed -n 's|.*WorkflowRun/||p')   # server assigns releve-pipeline-<id>
    echo "  → $run"
    sleep 12
    "$bin" workflow describe "$run"
    echo "▶ done — one run processed every statement; the gold DuckLake mart spans every month."
    echo "  (S3 reads are binding-gated in dev; query the gold mart through the lakehouse catalog.)"

# Run the releve-lakehouse medallion workflow ONCE via `funcdctl workflow run` against the running dev
# daemon — extract processes EVERY PDF in landing/ (NO input), then verify → build-silver → to-gold, then
# describe the per-step result. Self-seeds: if landing/ is empty it generates + pushes the canonical
# statement first (so it just works on a fresh daemon); already-present statements are all processed. The
# run name is server-generated (releve-pipeline-<id>), so it re-runs freely. Prereq: `just dev-example
# releve-lakehouse` running. Arg: the daemon's S3 port if it isn't 3006.
[group('example')]
run-releve s3port="3006":
    #!/usr/bin/env bash
    set -euo pipefail
    dir="$(scripts/example-copy.sh python/releve-lakehouse)"
    bin=dist/funcdctl-dev
    [ -x "$bin" ] || { echo "build the dev binary first: just dev-example releve-lakehouse" >&2; exit 1; }
    eval "$("$bin" dev "$dir/workflow.yaml" --s3port {{s3port}} --print-env)"   # load the dev S3 creds (no boot)
    export FUNCD_SERVER="${FUNCD_SERVER:-http://127.0.0.1:3007}" FUNCD_TOKEN="${FUNCD_TOKEN:-funcd-dev-token}"
    echo "▶ funcdctl workflow run releve-pipeline   (server-generated name; no input — extract processes all of landing/)"
    run=$("$bin" workflow run releve-pipeline | sed -n 's|.*WorkflowRun/||p')   # server assigns releve-pipeline-<id>
    echo "  → $run"
    sleep 10
    "$bin" workflow describe "$run"

# View funcd dev OTLP logs as ONE merged table, sorted by time. The path is a DIR (scans all
# **/*.jsonl under it), a single file, or a glob. Flattens the nested resourceLogs envelope with DuckDB
# into time/function/severity/body — generic log viewers can't read that nesting.
#   just logs .modcopy/funcd-python/examples/releve-lakehouse/.funcd-dev/blob/logs/default
#   just logs 1783897216394510000.jsonl
[group('example')]
logs path:
    uv run --no-project --with duckdb --python 3.12 python scripts/otlp-logs.py "{{path}}"

# Export funcd dev OTLP logs (dir/file/glob) to a flat, TYPED parquet you can open in a DuckDB viewer
# (Parquet Explorer). Defaults the output to funcd-logs.parquet.
#   just logs-parquet .modcopy/funcd-python/examples/releve-lakehouse/.funcd-dev/blob/logs/default
[group('example')]
logs-parquet path out="funcd-logs.parquet":
    #!/usr/bin/env bash
    set -euo pipefail
    uv run --no-project --with duckdb --python 3.12 python scripts/otlp-logs.py "{{path}}" --parquet "{{out}}"
    echo "▶ open in Parquet Explorer: {{out}}"
