# Contributing to funcd

Thank you for your interest in funcd. This guide explains how a change reaches the project.

## Before you start

- Open an issue first, with one of the issue forms: a bug, a feature, a flaky test or a task. A maintainer triages it.
- A bug fix that needs no design decision can go straight to a pull request that references the issue.
- A new feature, or a change to how something works, needs a design decision first. funcd records each decision in
  an architecture decision record (ADR) under [`docs/adr/`](docs/adr/), and the maintainers decide. Describe the
  problem in an issue, and wait for the decision before you write code.
- The language shims and the example functions live in their own repositories:
  [funcd-typescript](https://github.com/pyvvo/funcd-typescript), [funcd-python](https://github.com/pyvvo/funcd-python)
  and [funcd-functions](https://github.com/pyvvo/funcd-functions). A change to a shim starts there.

## Contributor License Agreement

Before we can merge your first pull request, you sign the pyvvo Contributor License Agreement (CLA). The CLA assistant
comments on your pull request with a link, and signing with your GitHub account takes a minute. One signature covers
every pyvvo repository. You keep the copyright in your work; the CLA lets pyvvo distribute it, including under license
terms other than Apache-2.0.

If you contribute on behalf of your employer, your employer signs the
[Corporate CLA](https://github.com/pyvvo/.github/blob/main/CLA-corporate.md) first.

## Development environment

The Nix flake pins the toolchain. `nix develop` opens a shell with the pinned Go, `just` and the other tools; run every
command in it, or prefix a command with `nix develop -c`.

- `just ci` runs the checks that CI runs on every pull request: formatting, generated files, lint, the unit tests and
  the repository checks.
- `just test-e2e` runs the end-to-end suite, and `just ci-full` runs both.
- On macOS, `just test-linux` runs the tests of the Linux-only packages in a Lima VM.

## Fixing a bug

1. Write a regression test that fails on `main` and shows the bug. Name it after the issue:
   `TestIssue<N>_<WhatItChecks>`.
2. Fix the cause, not the symptom, and keep the change to what the issue describes.
3. Check that the test fails when you revert the fix, and passes with it.
4. Run `just ci`.
5. Open the pull request, with `Fixes #<N>` in its description.

## Pull requests

- The title follows [Conventional Commits](https://www.conventionalcommits.org/), for example `fix(store): …` or
  `feat(cli): …`. A check enforces it: pull requests are squash-merged through a merge queue with the title as the
  commit message, and release-please writes the changelog from it.
- Keep a pull request to one change.
- For a first-time contributor, CI runs after a maintainer approves the run.
- The code follows the conventions of
  [ADR-0002](docs/adr/0002-source-code-conventions-and-patterns.md). YAML is block style, imports stay at the top of a
  file, and a comment explains why, not what.
- No file may contain anything specific to your machine, such as an absolute path or your username.

## Security

Report a vulnerability privately, as the [security policy](https://github.com/pyvvo/.github/blob/main/SECURITY.md)
describes. Never report one in a public issue or pull request.

## Code of conduct

Everyone who takes part in the project follows the
[code of conduct](https://github.com/pyvvo/.github/blob/main/CODE_OF_CONDUCT.md).

## License

funcd is licensed under the [Apache License 2.0](LICENSE).
