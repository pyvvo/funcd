---
title: What is funcd?
description: A lightweight, modular, single-binary serverless platform for Linux, designed for the agent era.
icon: Funcd
---

## Why funcd

There is no lightweight way to run agents and functions on one Linux machine without adopting Kubernetes or Docker.
Existing FaaS platforms are tied to one cloud, built for clusters, or limited. funcd is one binary that deploys, runs,
exposes, monitors and administers functions and agents. It uses few resources, gives strong observability, and leaves
a path to several nodes later.

Agents are its defining workload. They are event-driven and their load comes in bursts, so a Function scales to zero
when it is idle and starts again when a call arrives. Agents keep their state in platform services, so a Function
binds a KV store, a Bucket, a SQL database or a Lake.

## What runs on it

| Kind | What it is |
| --- | --- |
| Function | A function or an agent in TypeScript or Python, called over HTTP, by an event or on a timer |
| App | A whole app in one resource. funcd applies, reports and deletes its parts as one, removes the parts it no longer declares, and rolls it back through its revisions |
| Site | A static web app: an immutable bundle served from its own Bucket and Route |
| Workflow | Several steps, each of them a Function. Each execution of a Workflow is a WorkflowRun |

## The parts

- **funcd**, the server, runs on Linux. One binary holds the API, the controller, the gateway and the runtime that
  starts the workers.
- **funcdctl**, the CLI, runs on macOS and Linux. It applies, lists and describes resources and prints a Function's
  logs. Its dev build also runs Functions on the local machine with `funcdctl dev`.
- **The shims** give a Function its context: the stores it binds, and the calls it makes to other Functions. They are
  `@funcd-dev/shim` on npm and `funcd-shim` on PyPI.
- **The examples** in [pyvvo/funcd-functions](https://github.com/pyvvo/funcd-functions) are Functions and Apps that
  funcd's end-to-end tests run on a real funcd.

The [platform overview](platform/overview.mdx) shows how a call, an event and a deploy move through funcd.

## Get started

- [Install](get-started/install.md) funcdctl, and the funcd server on Linux.
- [Run a first Function](get-started/quickstart.md) on the local machine with `funcdctl dev`.
