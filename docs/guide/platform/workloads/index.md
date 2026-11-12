---
title: Workloads
description: The Functions, Apps and Sites that run on funcd, and what a Function needs to run.
---

![An App, a Function and a Site take their code and files from bundles in an OCI registry, pinned by digest, and rely on their configuration: ConfigMaps and Secrets, or only ConfigMaps for a Site](../../assets/workloads.drawio.svg)

funcd runs three kinds of workload. A Function is code, an App is a whole app in one resource, and a Site is a static
web app. Their code and files come from bundles in an OCI registry, pinned by digest: a Function's code, a Site's static
files and the bundles of an App's parts. All three rely on configuration: ConfigMaps for their settings and, except for
a Site, Secrets. The pages of this section cover each kind, then what a Function needs to run: its artifact, its runtime
and its configuration.

## Function

A [Function](function.md) is the code that funcd runs. An HTTP client calls it through the edge, and another
Function, a Sensor, a Workflow step or an App's hook calls it from inside funcd. Every call goes through the invoke
chain and the activator. A Function binds the stores, the Secrets and the other Functions that it uses. Each change
to it creates an immutable Revision, and workers run that Revision, either workers of its own or a pool that it
shares with other Functions.

## App

An [App](app.md) declares a whole app in one resource: its Functions, Workflows, Sites, Routes, stores,
EventSources, Sensors and ConfigMaps. funcd rolls the App out as one unit, through an AppRevision for each changed
spec. It calls the App's hooks around the rollout, deletes the parts that the App no longer declares, and rolls the
App back through its revisions.

## Site

A [Site](site.md) is a static web app. funcd copies its bundle into a prefix of a Bucket, and the edge serves the
files from there through the Site's Route, with no worker. When a deploy fails, the previous version keeps serving.

## Artifacts

A Function's code reaches funcd as an OCI [artifact](artifacts.md), together with its contract and its runtime. The
Revision pins the artifact's digest, so the code that was validated is the code that runs, and funcd pulls it by that
digest.

## Runtime

A Function's code runs in a worker, where a shim in TypeScript or in Python loads it and gives it its context. The
[runtime](runtime.md) driver runs each worker as a host process by default, or, on Linux, in a container of its own.

## Configuration

A Function takes its settings from ConfigMaps and its secrets from Secrets. A ConfigMap's keys reach its workers as
environment variables, while a Secret's values never do: the Function reads them through its local API, as the
[Configuration](configuration.md) page describes.
