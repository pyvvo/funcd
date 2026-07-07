# embedimg — embedded curated runtime images (ADR-0054)

This package `go:embed`s funcd's curated runtime image tars into the binary so funcd is
**self-contained**: at startup the [`ctrmanager`](../ctrmanager) imports the matching-arch
tar into its privately-managed containerd (`client.Import`) and the platform runs functions
with **no registry pull** (the k3s/dockerd model).

## The committed `.tar` files are PLACEHOLDERS

`nodejs22.tar` and `python314.tar` as committed are **tiny (<1 KB) clearly-labeled
stand-ins, not real OCI images**. They exist for one reason: `go:embed` needs a file at
compile time, and `embedimg.Tar(...)` must return a **non-empty** reader for the ADR-0054
non-gated unit test. `go build` and `just ci` are green on these placeholders.

Real multi-MB per-arch images are **never** committed: they are a release/integration
artifact. Building them requires `docker` on a Linux target and importing them requires
`root` + a live containerd — the deferred Linux integration lane (`FUNCD_IT=1`).

## Producing the real images (release / integration time)

The real images replace the placeholders via:

```
just build-runtime-images        # docker build the distroless images, docker save -> gzip -9 OCI tars
```

This recipe (see the repo `justfile`) builds:

- `images/runtime/nodejs22/Dockerfile`  — node on `gcr.io/distroless/nodejs22-debian12`
- `images/runtime/python314/Dockerfile` — custom distroless Python 3.14 on `gcr.io/distroless/cc-debian12`

exports each to an OCI tar at gzip max compression (`-9`, shared base layers where possible),
and writes them into this directory as `nodejs22.tar` / `python314.tar`, **per arch** (a
per-arch release build embeds its own matching-arch image — ADR-0054). The recipe is **not**
run by `just ci`.

## Keeping the working tree clean after a build (`skip-worktree`)

`build-runtime-images` **overwrites** the committed placeholders with real 30–100 MB tars, which
would otherwise leave the working tree permanently dirty and risk a `git commit -a` staging a
build artifact. To prevent that, the recipe first runs `just embedimg-pin`, which sets git's
**`skip-worktree`** bit on the three `*.tar` files: git then ignores the local overwrite, so
`git status`/`git commit` never see it and you never have to `git checkout` the tars after a lane
run. The index keeps the placeholder blob, so commits and the `check-hygiene` guard are unaffected.

The bit is **local to each clone** (it lives in `.git/index`, not shared through the repo) — hence
the recipes, so a fresh clone can opt in with one command:

```
just embedimg-pin      # ignore local image-build overwrites (auto-run by build-runtime-images)
just embedimg-unpin    # clear it — needed before a legitimate placeholder update (e.g. a git pull)
```

`check-hygiene` remains the backstop: even with the bit off, it blocks committing a `>4 KB` blob.

## Override (no registry by default)

A runtime not present in the embed falls through to the Manager's `--image runtime=ref`
override (`ImageOverride`), which pulls from a registry (e.g. `ghcr.io/green-0-rabbit/…`).
A runtime in neither embed nor override is a `fault.NotFound` — never a silent miss.
