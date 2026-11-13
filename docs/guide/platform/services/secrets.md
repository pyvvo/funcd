---
title: Secrets
description: Values that a Function or a Lake reads and that a Function may write, each read and write decided by Cedar.
---

![A Function's ctx.secret get or list call goes to its local API, which asks Cedar whether the Function may secret::read each Secret that it binds. On allow, funcd resolves the bound Secrets from the metastore, and on deny the read is refused with 403](../../assets/secrets.drawio.svg)

A Secret holds named values, its `spec.data`, in a namespace. A Function names the Secrets that it reads in
`spec.bindings.secret`, and a Workflow step and a Lake bind Secrets the same way. A Function reads the values with
`ctx.secret.get` and `ctx.secret.list` through its local API, so they never reach the worker's environment. Each read
asks funcd, with no cache: a changed Secret shows at the next read, without a restart. A read resolves every Secret
that the Function binds, and when two of them hold the same key, the later Secret's value wins. A Function can also
write a key of a Secret that it binds, with `ctx.secret.put`, or remove one, when it is the Secret's owner or a role
allows it to write the Secret. The next read of each Function that binds the Secret shows the change, unless a
Secret that comes later in that Function's `spec.bindings.secret` holds the same key.

Each read asks Cedar whether the Function may `secret::read` each Secret that it binds. The built-in policy
`secret-read-binding` allows a bound Secret. The built-in roles `Secret Data Reader` and `Secret Data Writer` also
hold `secret::read`, but a read names only the Secrets that the Function binds, so neither role lets it read more.
Each write asks Cedar whether the Function may `secret::write` the Secret: the built-in policy allows only the
Secret's owner and the principals that a role such as `Secret Data Writer` names. A namespace Policy can forbid a read
or a write of a Secret. After a forbid of reads, each of the Function's next reads fails with 403, even for a key that
another Secret holds, and its workers stop being ready within `runtime.supervisionPeriod` (10 s by default). With no
ready worker, the Function is `Degraded` and its calls wait. Once the forbid goes, the next check makes the workers
ready again. A forbid of writes refuses each write with 403, and leaves the workers ready.

funcd stores Secrets in the metastore. It encrypts them at rest when `secrets.encryptionKeyFile` names a key file.
Without one, it stores them unencrypted and logs a warning at startup. People read and write Secrets through the API,
where RBAC decides, as for every other kind.

## A Lake's Secrets

![The Lake reconciler asks Cedar, as the Lake, whether it may secret::read each Secret that the Lake binds. On allow, the Lake's engine starts with the values, and on deny the Lake is held](../../assets/secrets-lake.drawio.svg)

A Lake's engine reads its Secrets, such as its `QUACK_TOKEN`, from its environment when it starts. The Lake
reconciler therefore asks Cedar, as the Lake, whether it may `secret::read` each Secret that the Lake binds, and it
asks again on each of its passes. A forbid holds the Lake in `Pending` with the reason `BindingResolveFailed`, and it
stops an engine that runs. funcd asks again every `controller.referentPollInterval` (2 s by default), and once the
forbid goes, the engine starts with the values.
