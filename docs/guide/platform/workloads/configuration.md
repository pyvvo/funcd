---
title: Configuration
description: How workloads get their settings from ConfigMaps and their secrets from Secrets.
---

![An App declares the ConfigMaps and Secrets of its parts. A Function and its Workflow steps, a Lake's engine and a Site read them, a Site only ConfigMaps, and Cedar checks each Secret read](../../assets/configuration.drawio.svg)

Configuration comes in two kinds of resource. A ConfigMap holds plain settings, and a Secret holds values that must
stay private. A Function, a Workflow and an App define ConfigMaps of their own or name existing ones, and they declare
the Secrets that they read, with the keys that they need. A Secret belongs to its namespace: no workload holds, writes
or owns one.

Each workload uses its configuration in its own way:

- A Function receives the keys of its ConfigMaps as environment variables. It reads its Secrets through its local
  API, so their values never reach its environment. A Workflow's steps run as Functions and receive their
  configuration the same way.
- A Lake's engine receives both as environment variables when it starts, such as its tuning settings and its token.
- A Site writes the keys of a ConfigMap into a public configuration file beside its static files. A Site never reads
  a Secret.
- An App declares the ConfigMaps and the Secrets that its parts use, and it changes no part while a Secret that it
  declares is missing.

Each Secret read asks Cedar first. A Secret that the reader binds is allowed, and a namespace Policy can forbid it.
A Function's reads then fail and its workers stop being ready, and a Lake is held with its engine stopped, until the
forbid goes, as the [Secrets](../services/secrets.md) page describes. funcd encrypts Secrets at rest when it has a key
file. Without one, it stores them unencrypted and logs a warning at startup.
