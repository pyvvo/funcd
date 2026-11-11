---
title: Configuration
description: How a Function gets its settings and its secrets.
---

![A Function binds ConfigMaps and Secrets. ConfigMap keys reach the worker's environment, and Secret values are read through the local API](../../assets/configuration.drawio.svg)

A Function binds ConfigMaps and Secrets. The keys of a bound ConfigMap reach its workers as environment variables. A
Secret's values never reach a worker's environment: the Function reads them with `ctx.secret`, through its local API.
Each read asks Cedar whether the Function may `secret::read` each Secret that it binds. A bound Secret is allowed, and
a namespace Policy can forbid it. The Function's reads then fail with 403, and its workers stop being ready until the
forbid goes. funcd encrypts Secrets at rest when it has a key file (`secrets.encryptionKeyFile`). Without a key file, it stores
them unencrypted and logs a warning at startup.
