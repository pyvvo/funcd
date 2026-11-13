# Secrets

Status words and how to read this: [the index](README.md).

Kinds: Secret.

```mermaid
stateDiagram-v2
    [*] --> Bound : a read, a readiness check or a write names a Secret its workload binds
    [*] --> Refused : a write names a Secret its Function does not bind
    Bound --> cedar : secret::read, or secret::write after the write's read
    cedar --> Refused : no permit, or a forbid
    cedar --> Served : a permit and no forbid
    Served --> Bound : a write's update meets a Conflict, 5 attempts in all
    Served --> [*]
    Refused --> [*]
```

A workload reads the Secrets it binds: the binding is the read grant, and a namespace Policy forbid revokes it (ADR-0321). It
writes a bound Secret's keys when it is the Secret's owner or a writer role names it (ADR-0327); `Secret Data Reader` adds no read
beyond the binding. People's reads stay RBAC's: [Security](security.md). A Lake's forbidden Secret: [Services](services.md); a
failed readiness check: [Workers](workers.md). New words: Bound, Served, Refused.

| Where | What happens | Time | You see | Status | Source |
|---|---|---|---|---|---|
| `ctx.secret.get` or `list` | Asks funcd on each read, with no cache, and Cedar decides each bound Secret as the Function: a changed Secret shows at the next read, at a round trip per read | none | the new value, no restart | decided | ADR-0249 Decision 2 and Consequences, ADR-0321 Decision 3, `TestScenarioSecretReadLive`, `TestScenarioSecretBoundReads` |
| A Policy forbids a Function's read of a bound Secret | Each next `ctx.secret` read fails, of any key, and readiness fails ([Workers](workers.md)): with no ready replica it is `Degraded`, its calls held | the next read; `runtime.supervisionPeriod` 10 s | 403 `cedar: forbidden by policy <id>`; `DependencyNotReady` | decided | ADR-0321 Decisions 3 and 4, `TestScenarioSecretForbidReadiness` |
| A bound Secret is missing | The Function waits; a worker already serving goes on, and `ctx.secret.get` reads nothing; a Secret applied later is found at the re-check | `controller.referentPollInterval` 2 s | `Pending`, `SecretNotFound`, then `SecretKeyMissing` while a declared key is absent | decided | ADR-0248 Decision 6, ADR-0249 Decisions 2 to 4, `TestScenarioSecretMissingUnchanged` |
| A bound Secret value that is not UTF-8 or holds a NUL | No worker starts; a corrected Secret does not clear it until the Function changes or funcd restarts | none | `Failed`, `SecretResolveFailed`, `is not valid UTF-8; env delivery cannot carry binary data` | **uncovered** | ADR-0306 Contracts requeue this gate on NotFound only; ADR-0057 Scope leaves rotation out; a probe |
| A Secret a Lake's engine binds changes | The engine holds it in its environment, so it is replaced with the new value | within `runtime.supervisionPeriod` 10 s | the engine restarts | accepted | ADR-0249 Scope, #1195, `TestIssue1195_ConvergeReplacesAnEngineOfAnotherSpec`; no lane rotates it |
| A `secrets.encryptionKeyFile` that does not match the stored Secrets | funcd does not start | none | `stored Secrets do not decrypt with secrets.encryptionKeyFile: they were written with a different key or with none` | accepted | #93, `TestIssue93_KeyMismatchFailsAtStartup` |
| `ctx.secret.put`, `del` or `delete` | Sets or removes one key of a bound Secret for its owner or a writer grant; others get 403, a missing Secret 404, a bad key or value 400, a body over `maxSecretBytes` 1 MiB 413; readers see it next read | `writeAttempts` 5 | 204; 409 after that many conflicts | decided | ADR-0327 Decisions 1, 2, 4 and 7, ADR-0328, `TestScenarioSecretOwnerWrites` |
| A RolesAssignment grants `Secret Data Reader` or `Secret Data Writer` | The writer joins the Secret's writers; neither role lets a workload read a Secret it does not bind | none | the next write's answer | decided | ADR-0327 Decision 3, `TestScenarioSecretWriterRole` |
| A person applies a Secret a Function wrote | The applied data replaces the Secret's, keys the Function wrote included | none | the next read shows the applied data | accepted | ADR-0327 Decision 7 and Consequences, `TestScenarioSecretApplyReplacesWrittenKeys` |

<!-- generated values, do not edit: just behavior-values -->
<details>
<summary>Secrets: values</summary>

| Value | Default | From |
|---|---|---|
| `secrets.encryptionKeyFile` | none | config |
| `writeAttempts` | 5 | `internal/services/secret/secret.go` |

</details>
<!-- end of generated values -->
