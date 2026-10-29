# FEAT-0017: Artifact supply chain, an SBOM for every function artifact and a vulnerability scan of what runs

- **Status**: Active: a living document; the tracking table updates as ADRs progress
- **Date**: 2026-10-29
- **Deciders**: green-0-rabbit
- **Defines**: an **artifact supply chain epoch**: every function artifact carries a software bill of materials (SBOM)
  of exactly what its bundle ships, and an operator lists the known vulnerabilities of the revisions that run.
  Positioned alongside the other additive epochs (FEAT-0003 to FEAT-0016).

## Initial need

No scanner can check what a funcd function ships. Experiment 0015
([pyvvo/experiment#27](https://github.com/pyvvo/experiment/pull/27)) measured it:

- `trivy image`, syft, grype and zot's built-in scanner refuse a function manifest, and `trivy image` and zot report a
  multi-arch function's index clean, with exit 0 and no error;
- a TS bundle inlines every dependency into one `.mjs`, so no scanner finds a package in it; a Python bundle keeps each
  `*.dist-info`, so only a directory scan of the unpacked bundle reads it;
- funcd verifies no signature or provenance before a worker starts: the security summary's row 15 names
  "provenance/SBOM attestation" as the missing mitigation, and the blueprint's artifact-trust bullet plans signature
  verification.

## How this document works

This file captures **what** this capability set must contain and **why**. The **how** lives in ADRs (`docs/adr/`,
process in [ADR-0000](../adr/0000-adr-process.md)). The decisions were refined with the decider on 2026-10-29 before
the first ADR was drafted, on the evidence of experiment 0015.

## Features

| # | Feature | Builds on | ADR(s) | Status |
|---|---|---|---|---|
| F214 | **An SBOM for every function artifact**: a function built with funcd's bundlers and pushed with funcdctl carries, for each platform, an SBOM of exactly the packages its bundle ships, which a scanner reads without the bundle; a CI may sign the same SBOM | — | [ADR-0298](../adr/0298-a-function-artifact-carries-an-sbom.md) | accepted |
| F215 | **A vulnerability scan of what runs**: funcdctl lists the known vulnerabilities of the revisions that run, or of a pushed artifact, from their SBOMs, and names each one it could not scan instead of passing it | F214 | [ADR-0299](../adr/0299-funcdctl-scan-matches-sboms-in-an-extension.md) | accepted |
| F216 | **Trusted artifacts**: a node checks an artifact's signed provenance against its namespace's policy before it starts a worker, and a funcd registry could take only signed artifacts | F214 | — | idea |

## Exit criterion

A function pushed with funcdctl carries an SBOM of exactly the packages its bundle ships, one per platform, and
`funcdctl scan` reports the known vulnerabilities of every running revision from those SBOMs alone, naming each
revision it could not scan.
