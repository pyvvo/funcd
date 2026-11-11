---
title: What is funcd?
description: A lightweight, modular, single-binary serverless platform for Linux, designed for the agent era.
icon: Funcd
---

## Why funcd

There is no lightweight way to run agents and functions on one Linux machine without adopting Kubernetes or Docker,
and existing FaaS platforms are cloud-locked, cluster-shaped or capped. funcd deploys, runs, exposes, monitors and
administers functions and agents as one binary, with low resource use, strong observability and a path to multi-node
later.

Agents are its defining workload. They are event-driven and bursty, so a Function scales to zero when it is idle and
wakes on demand. They keep their state in platform services, so a Function binds a KV store, a Bucket, a SQL database
or a Lake.

## What runs on it

| You declare | What it is |
| --- | --- |
| Function | A function or an agent in TypeScript or Python, called over HTTP, by an event or on a timer |
| App | A whole app in one resource: its parts are applied, reported, pruned and deleted as one, and rolled back through its revisions |
| Site | A static web app: an immutable bundle served from its own Bucket and Route |
| Workflow | A run of several steps, each one a Function; each execution is a WorkflowRun |

## The parts

- **funcd**, the server, runs on Linux. One binary holds the API, the controller, the gateway and the runtime that
  starts the workers.
- **funcdctl**, the CLI, runs on macOS and Linux. It applies, lists and describes resources and prints a Function's
  logs. Its dev build also runs Functions on your machine with `funcdctl dev`.
- **The shims** give a Function its context: the stores it binds and calls to other Functions. They are
  `@funcd-dev/shim` on npm and `funcd-shim` on PyPI.
- **The examples** in [pyvvo/funcd-functions](https://github.com/pyvvo/funcd-functions) are Functions and Apps that
  funcd's end-to-end tests run on a real funcd.

## Get started

- [Install](get-started/install.md) funcdctl, and the funcd server on Linux.
- [Run a first Function](get-started/quickstart.md) on your machine with `funcdctl dev`.
