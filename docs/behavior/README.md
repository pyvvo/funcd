# Platform behavior

What the platform does, when, and who decided it. If a chapter misstates an ADR, the chapter is corrected. Where the code differs
from an ADR, the row says what the code does, with `differs`. Numbers come from the code and the config: each chapter's folded block is
generated, and `just check-behavior` fails when a row's citation or named number no longer holds.

## Before you call something a bug

Grep what you saw across the chapters, and read every row it hits: the status tells them apart.

```bash
grep -rl "did not become ready" docs/behavior
sed '/generated values/q' docs/behavior/functions.md
```

The second command reads a chapter down to its generated block, which is about 40% of the largest chapters. Grep the block for a name.
A row whose cited clause reads as the row says ends the dive, with no issue and no fix. About to file or fix? Do the rest of step 2 of
the bug rule in `.claude/CLAUDE.md`: a row points at what to read and proves nothing alone.

## Status words

| Status | It means |
|---|---|
| `decided` | an ADR or the decider chose it |
| `accepted` | an ADR or the decider named it as a cost or a risk, left it out on purpose or rejected its fix; or no clause speaks of it and a test pins it |
| `deferred` | a board card, an ADR's open question or an open issue holds it |
| `uncovered` | nothing speaks of it: a question for the decider, no fix until answered |
| `differs` | an ADR or the blueprint says otherwise and the code does this: a surprise for the bug rule |

## Chapters

| Chapter | Holds |
|---|---|
| [Platform lifecycle](platform-lifecycle.md) | start order, hold, safe mode, restart, stop, upgrade |
| [Controller](controller.md) | how a write becomes a pass, retries, owners and collection |
| [Admission](admission.md) | what the API does with a write, Namespaces, ResourceGroups |
| [Security](security.md) | who may act, Secrets, egress |
| [Workers](workers.md) | a worker's boot, crash, liveness and stop, volumes, logs |
| [Functions](functions.md) | phases, gates, wake and idle, revisions, pools |
| [Calls](calls.md) | a call from the listener to a worker, routes, Sites, deadlines |
| [Services](services.md) | KVStore, Bucket, SQLDatabase, Lake, the S3 gateway |
| [Eventing](eventing.md) | sources, Sensors, dead letters, Workflow runs |
| [Apps](apps.md) | rollouts, hooks, rollback, delete |
| [Disaster recovery](disaster-recovery.md) | backups, Restores, namespace holds |
| [Health](health.md) | storage probes, what funcd says about itself |
| [Limits](limits.md) | quotas, the disk floor, caps a user hits |
| [funcdctl dev](funcdctl-dev.md) | the dev loop, funcdctl's waits and errors |
