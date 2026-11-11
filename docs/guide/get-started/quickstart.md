---
title: Quickstart
description: Run a first Function on the local machine with funcdctl dev, call it, and read its logs.
icon: Rocket
---

This page runs the hello-world example of [funcd-typescript](https://github.com/pyvvo/funcd-typescript) on the local
machine. It needs no funcd server. It needs only two things:

- `funcdctl` in its dev build (see [Install](install.md));
- Node.js on the `PATH`, because funcd starts the Function with `node`.

## 1. Get the example

```sh
git clone https://github.com/pyvvo/funcd-typescript
cd funcd-typescript/examples/hello-world
```

The folder holds the handler and what funcd needs to run it:

| File | What it is |
| --- | --- |
| `function.yaml` | The Function's manifest: its name, runtime and handler |
| `hello-world.mjs` | The handler, built from `src/main.ts` |
| `hello-world.contract.yaml` | The JSON Schema of the Function's input and output |

The handler greets the name it receives:

```ts
import type { Handler } from '@funcd-dev/shim';

export interface FuncInput {
  name: string;
  excited?: boolean;
}

export interface FuncOutput {
  greeting: string;
}

export const handle: Handler<FuncInput, FuncOutput> = (context, event) => {
  const { name, excited } = event.data!;
  context.log('greeting', name);
  return { greeting: excited ? `Hello, ${name}!` : `Hello, ${name}.` };
};
```

## 2. Run it

```sh
funcdctl dev --gport 8081 --cport 8080
```

`funcdctl dev` starts an embedded platform, runs the folder's Functions and prints where they answer:

```text
gateway   http://127.0.0.1:8081     invoke functions
control   http://127.0.0.1:8080     apply · workflow run — funcdctl --server <url> --token funcd-dev-token

FUNCTIONS
POST  http://127.0.0.1:8081/function/hello-world
```

Without `--gport` and `--cport`, it picks free ports. It also prints each Function's log lines, and Ctrl-C stops it.

## 3. Call it

Call it from another terminal:

```sh
curl -X POST http://127.0.0.1:8081/function/hello-world \
  -H 'content-type: application/json' \
  -d '{"name": "funcd"}'
```

```json
{"greeting":"Hello, funcd."}
```

funcd checks the data against the contract before the handler runs. A name that is not a string gets a 422:

```sh
curl -X POST http://127.0.0.1:8081/function/hello-world \
  -H 'content-type: application/json' \
  -d '{"name": 42}'
```

```json
{"error":"event data does not match the input contract","details":[{"path":"/name","message":"got number, want string"}]}
```

## 4. Inspect it

funcdctl manages the embedded platform as it manages a funcd server:

```sh
funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token get functions -n default
funcdctl --server http://127.0.0.1:8080 --token funcd-dev-token logs hello-world -n default
```

The first command lists `hello-world`, and the second prints its log lines.
