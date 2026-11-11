---
title: Configuration
description: How a Function gets its settings and its secrets.
---

![A Function binds ConfigMaps and Secrets. ConfigMap keys reach the worker's environment, and Secret values are read through the local API](configuration.drawio.svg)

A Function binds ConfigMaps and Secrets. The keys of a bound ConfigMap reach its workers as environment variables. A
Secret's values never reach a worker's environment: the Function reads them with `ctx.secret`, through its local API.
funcd encrypts Secrets at rest when it has a key file (`secrets.encryptionKeyFile`). Without a key file, it stores
them unencrypted and logs a warning at startup.
