---
title: Broken
description: A page that breaks every rule of the develop-page template.
---

## Overview

ADR-0001 is named here, [a tag](https://github.com/pyvvo/funcd-typescript/tree/v0.27.0/examples/kv-counter) is
linked, and so is [a release](https://github.com/pyvvo/funcd-python/releases/tag/v0.22.0).

```yaml
apiVersion: funcd.io/v1alpha1
kind: KVStore
```

```console
$ funcdctl apply -f - <<EOF
{"apiVersion": "funcd.io/v1alpha1", "kind": "KVStore"}
EOF
## Methods
[a fenced link](https://github.com/pyvvo/funcd-typescript/tree/v0.1.0/examples/kv-counter)
```

::include[../../../../_repos/funcd-typescript/examples/kv-counter/src/main.ts#handler]

## Quotas

| Method | What it does |
| --- | --- |
| `get` | Reads a key. |

## Access

## Limits and quotas

## Conditional writes

## Access

## Examples

- [gone](https://github.com/pyvvo/funcd-typescript/tree/main/examples/no-such-example)
- [the shim](https://github.com/pyvvo/funcd-typescript/tree/main/shim)

## Notes
