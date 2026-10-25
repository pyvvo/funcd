# Vector store design: a fixed `context.vector` over a swappable engine

- **Status**: Draft (2026-10-22): the design the decider refined for [FEAT-0013](../feat/0013-feat-vector.md); the
  ADRs decide, and an Accepted ADR wins over this report.
- **Deciders**: green-0-rabbit

## Summary

funcd gets vector stores: a namespace declares a `VectorStore`, functions search and write it through
`context.vector` from TypeScript and Python, and the engine underneath can change without any change to a function.

- **One engine per store**, shared by the functions that bind the store, as one rqlite engine serves each SQL
  database ([ADR-0232](../adr/0232-sql-database-resource-and-rqlite-engine.md)). The engine keeps its tables in the
  store's own durable directory ([ADR-0231](../adr/0231-durable-storage-for-add-on-engines.md)).
- **A fixed API, `context.vector`**, reached over the function's own socket, as `context.sql` is
  ([ADR-0233](../adr/0233-sql-access-from-functions.md)): a portable core of search, count, upsert and delete, a
  portable filter instead of SQL, and capabilities an engine may add, such as full-text and hybrid search.
- **A swappable engine**: `VectorStore.spec.engine` names one of funcd's own engines. LanceDB is the default and the
  only engine of this epoch; zvec and Cloudflare Vectorize are references for later. An engine is funcd's code, picked
  by name: there is no plugin registry and no `RuntimeClass`.
- **A wrapper in two layers**: a Go engine port in the daemon, which the facade calls whatever the engine, and
  `vector/v1`, funcd's own protocol between the daemon and an engine container. An in-memory engine implements the
  port for unit tests, `funcdctl dev` runs the real LanceDB engine, and one conformance suite runs against every
  engine, as `blobcontract` does for the blob port.

## The question

Where should LanceDB run, and what does a function see? Two shapes were measured
([experiments](#sources) 0002, 0003 and 0004 in the private experiment repo):

| | A: LanceDB inside each function | C: one engine per store, a thin client in the function |
|---|---|---|
| What a function loads | LanceDB, Arrow and NumPy, in every worker | the shim's client only |
| Memory | every warm worker pays for LanceDB | the engine pays once; a worker stays a plain one |
| Cold start | LanceDB's import on every start | none added |
| Pooling ([ADR-0050](../adr/0050-python-worker-pooling-subinterpreters.md)) | impossible: LanceDB's native module refuses subinterpreters | unchanged |
| TypeScript | blocked: a bundle cannot carry a Node native addon ([ADR-0144](../adr/0144-bundling-in-the-language-toolchains.md)) | works: the client is plain code |
| Concurrent writers | unsafe on a Bucket today (below) | safe with the engine's own directory |
| Platform work | none | an epoch, as SQL's |

The decider chose C on 2026-10-22, with the tables in the engine's own directory: funcd targets many agents on a box
short of memory, its apps are TypeScript-first, and experiment 0004 showed that LanceDB's features survive behind a
thin client.

Why not on a Bucket: LanceDB commits each version with a create-only PUT (`If-None-Match: *`), and the S3 gateway
answers that header with a check followed by a separate Put ([backend.go:684](../../internal/blob/s3gateway/backend.go)),
which ADR-0159 left to a follow-up ADR. Two concurrent commits can both pass the check, and one is lost. An engine
with its own directory commits through the local filesystem and does not meet the race.

## LanceDB facts

- An embedded library, Apache-2.0, with official SDKs for Python, TypeScript and Rust; a Go SDK exists only as a
  community binding over the Rust library, so LanceDB can never run inside the pure-Go daemon.
- No open-source server: LanceDB Cloud and Enterprise are the only servers. funcd writes its own engine around the
  Python SDK, as it wrote the duckdb engine's shim ([ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md)).
- Vector search (L2, cosine, dot, Hamming on binary vectors), multivector search, scalar and vector indexes
  (IVF, HNSW and RaBitQ variants, BTREE, BITMAP, LABEL_LIST), BM25 full-text search, hybrid search with rerankers,
  versions with tags and restore, and schema changes.
- Its Linux x86_64 packages, LanceDB, PyArrow and NumPy, are large; how the engine image ships is an open question
  ([below](#open-questions)).

## What a thin client keeps

Experiment 0004 drove LanceDB through a function that holds no LanceDB: the function sends a request to the engine,
and the engine runs it. Every feature that is data and queries gave the same answer as LanceDB in-process, with the
tables in a directory and on a Bucket: every search mode, filters before and after the search, columns, pages,
distance ranges, several query vectors at once, query plans, every index type, full-text, phrase and boolean queries,
hybrid search with the RRF, linear and MRR rerankers, writes, versions and maintenance.

What does not cross is code the function owns:

- a table's own embedding function: the engine does not hold its code; the function embeds and sends vectors;
- a reranker written by the function: it reranks the rows the engine returns, which reproduced the engine's own RRF.

Two facts for the client: funcd hands a function's JSON answer back with its keys sorted and 0.0 written as 0, and a
JSON answer with vectors is several times its Arrow size.

## Decisions

Refined with the decider on 2026-10-22; the last five rows on 2026-10-25.

| Topic | Decision |
|---|---|
| Shape | C: one engine per store, shared by the functions that bind it; no LanceDB in a function |
| API | a fixed `context.vector`, not a mirror of LanceDB's calls |
| Engines | funcd's own, picked by `spec.engine`; `lancedb` is the default (platform config) and the only one now |
| Later engines | zvec and Cloudflare Vectorize, as references; each engine has its own settings |
| Registry | none: no `RuntimeClass` and no plugin images (the Jobs `engine` field is the model, not their registry) |
| Storage | the engine's own durable directory ([ADR-0231](../adr/0231-durable-storage-for-add-on-engines.md)), not a Bucket |
| Core | search, count, upsert and delete; full-text, hybrid and the rest are capabilities an engine declares |
| Filter | a portable JSON filter; no SQL in the core |
| Tuning | engine `params`, checked against a schema funcd ships for each engine |
| Tables | declared in the spec: fields, the key, which fields are filterable, full-text fields, vector fields |
| Writes | `upsert` and `delete` only; one owner per store plus writer roles, as SQL |
| Admin | only from the spec: functions cannot create indexes, optimize or restore |
| Wire | JSON; 8 MiB per request and per answer, 1,000 rows per call, 30 s per call |
| Engine language | Python first, as the duckdb engine (to confirm in the ADR) |
| Hybrid fusion | RRF only; the other rerankers are left out |
| Scalar indexes | the engine indexes every filterable field; no choice per field |
| Left out | the LanceDB features that [FEAT-0013](../feat/0013-feat-vector.md#lancedb-features-not-implemented-in-this-epoch) lists as not implemented, each with its reason |
| Dev | `funcdctl dev` runs the real LanceDB engine, as it runs SQL's and the lake's; the in-memory engine serves unit tests only |
| Embedding | the function embeds its text; LanceDB's embedding functions are not used, and the platform embedding through an AI gateway is the idea F201 |

## Illustrative shapes

Illustrative and non-normative: every name here is new, and the ADRs decide.

### The kind

```yaml
apiVersion: funcd.io/v1alpha1
kind: VectorStore
metadata:
  name: memory
  namespace: team-a
spec:
  engine: lancedb
  owner: agent
  resources:
    memory: 512Mi
  params:
    index: ivf_hnsw_sq
  tables:
    - name: notes
      key: id
      fields:
        - name: id
          type: string
        - name: kind
          type: string
          filterable: true
        - name: created
          type: int
          filterable: true
        - name: text
          type: string
          fullText: true
        - name: embedding
          type: vector
          dimensions: 1536
          distance: cosine
```

- Field types in the core: `string`, `int`, `float`, `bool`, `vector` (with `dimensions` and a `distance` of `cosine`,
  `l2` or `dot`). A table needs its key and at least one vector field.
- The engine, a field's type, and a vector field's dimensions and distance never change after creation; a new field
  may be added.
- A delete is refused while a table holds rows or a Function binds the store, as for SQL; a namespace quota as SQL's.

### A function binds it and uses it

```yaml
spec:
  vector:
    - ref: memory
  bindings:
    vector:
      - alias: mem
        store: memory
```

```ts
const rows = await ctx.vector.search("mem", "notes", {
  vector: embedding,
  filter: { kind: "note", created: { gte: 1700000000 } },
  select: ["id", "text"],
  limit: 5,
});
await ctx.vector.upsert("mem", "notes", [{ id: "n1", kind: "note", created: 1700000001, text: "…", embedding }]);
```

A query carries `vector` or `text` (both make a hybrid search), the vector `field` when a table has several,
`filter`, `select`, `limit` (default 10) and the engine's own `params`. A row carries `_distance` for a vector search,
smaller being closer on every engine, or `_score` for a text or hybrid search. Python has the same calls in snake case.

### The filter

```json
{ "kind": "note", "created": { "gte": 1700000000 } }
```

Fields are joined with AND, and a bare value means `eq`; the operators are `eq`, `ne`, `in`, `nin`, `lt`, `lte`, `gt`
and `gte`. A filter names only the key and `filterable` fields. The lancedb engine translates it into Lance's filter
language; Vectorize's filter has the same operators, so the later engines translate it too.

### Grants

As SQL ([ADR-0233](../adr/0233-sql-access-from-functions.md)): `search` and `count` need `vector::read`, `upsert` and
`delete` need `vector::read` and `vector::write`. A binding grants reads; a write needs the store's owner or a
`Vector Data Writer` role. The engine's address and credential never reach the sandbox.

### The engine port and `vector/v1`

```go
type Engine interface {
	Capabilities() Capabilities
	Schemas() (store, query []byte)
	Apply(ctx context.Context, s Store) error
	Search(ctx context.Context, s Ref, table string, q Query) ([]Row, error)
	Count(ctx context.Context, s Ref, table string, f Filter) (int64, error)
	Upsert(ctx context.Context, s Ref, table string, rows []Row) (int64, error)
	Delete(ctx context.Context, s Ref, table string, f Filter) (int64, error)
}
```

The `lancedb` engine implements the port by speaking `vector/v1` to its container: HTTP and JSON, a credential
derived from the node master as SQL's, and calls to apply the tables, search, count, upsert, delete and optimize. The
`memory` engine implements it in Go with exact search.

## Gaps in today's code

- No vector vocabulary: no kind, binding, Cedar action, role or shim helper names `vector` (grep of `api`,
  `internal`, `pkg` and `cmd`, 2026-10-22).
- The add-on runtime runs funcd's own images, embedded in the binary
  ([ADR-0054](../adr/0054-self-contained-runtime-embedded-images-managed-containerd.md)); LanceDB's image is much larger
  than rqlite's.
- No engine port: the SQL facade talks to rqlite directly. The vector facade calls a port instead.

## Open questions

| Question | Decided in |
|---|---|
| How the lancedb image ships: embedded in the binary, as rqlite's, or pulled from a registry | the VectorStore ADR, with the decider |
| The memory engine's algorithm and size limits | the engine port ADR |
| How `funcdctl dev` gets the LanceDB engine: embedded in funcdctl, as the lake's engine, or installed on first use | the dev ADR |
| The capability names, and how a query refuses one its engine lacks | the engine port ADR |
| How an engine moves to a new LanceDB version | the VectorStore ADR |
| The workload backup format of a store | the backup ADR |

## Sources

- Experiments in the private repo `pyvvo/experiment`, at commit `a861748`: 0002 (LanceDB inside a function), 0003 (one
  shared engine) and 0004 (LanceDB's features through a thin client).
- LanceDB: [documentation](https://docs.lancedb.com), [Lance's commit handlers](https://github.com/lance-format/lance/blob/main/rust/lance-table/src/io/commit.rs).
- zvec: [zvec.org](https://zvec.org/en/), [alibaba/zvec](https://github.com/alibaba/zvec).
- Cloudflare Vectorize: [limits](https://developers.cloudflare.com/vectorize/platform/limits/),
  [metadata filtering](https://developers.cloudflare.com/vectorize/reference/metadata-filtering/).
