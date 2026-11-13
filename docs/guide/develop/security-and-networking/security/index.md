---
title: Security
description: Create an Identity for a client outside funcd, give roles to it and to Functions at a scope, and allow or forbid requests with a Cedar Policy.
---

## Usage

funcd checks each request for data that a Function or a client outside funcd sends, and refuses it unless something
allows it. The [Platform page](../../../platform/security-and-networking/security.md) explains where funcd checks a
request; this page shows how to give access and how to remove it.

The first example gives a client outside funcd read access to the objects that a Function writes into a Bucket. The
client is curl, which sends S3 requests with a key pair that funcd issues. The steps below start the Bucket example
s3-roundtrip on the local computer, let its Function write an object, then create an **Identity**, the object that
stands for the client in funcd, and give it a role: the client reads the object, whose content is `roundtrip-001`, and
its write into the same prefix fails with the S3 error `AccessDenied`.

The example needs five programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the repositories of the example and of funcd.
- curl 8.0 or later, which calls the Function and signs the S3 requests with its `--aws-sigv4` option.
- jq, which reads one value of the JSON that funcdctl prints.
- Node.js on the `PATH`, which runs the example's Function.

`funcdctl dev` starts a local platform and runs the resource files of one folder on it. It also runs funcd's **S3
gateway**, a server that answers S3 requests for the Buckets, and prints the address of its control plane, which a
`funcdctl` command reaches with `--server` and the token `funcd-dev-token`. The `--gport`, `--cport` and `--s3port`
flags set the ports of the Functions' URLs, of the control plane and of the S3 gateway, so the commands below use the
ports 8080, 8081 and 8082.

The example's folder, `examples/s3-roundtrip` of the funcd-typescript repository, holds the Bucket `lakehouse` and the
Function `s3-roundtrip`, which the [Bucket](../../services/bucket/index.md) page explains. The Function is the
**owner** of the Bucket's prefix `gold`, a separate set of objects inside the Bucket: the owner of a prefix writes it
with no role. At each call, the Function writes into `gold` an object named after the call's tag. The folder also
holds resources that this page does not use.

The client gets its access from two objects of the funcd repository's file
`api/types/v1alpha1/examples/security/identity.yaml`. The Identity `analyst` stands for curl, and its
[`type`](spec.md#identityspec), `external`, means a caller outside funcd:

::include[../../../../../api/types/v1alpha1/examples/security/identity.yaml#identity]

funcd issues the Identity a credential and writes it into a Secret of the Identity's namespace, named after the
Identity unless [`credentialSecretName`](spec.md#identityspec) names another one: an S3 key pair, under the keys
`accessKeyId` and `secretAccessKey`, and a token for the gateway of a Lake, which the
[Lake](../../services/lake/index.md#access) page explains, under `lakeToken`. The credential only proves who the
client is: it grants nothing.

A **RolesAssignment** gives roles. Each of its [entries](spec.md#rolesassignmentspecassignments) gives one role to one
**principal**, a Function or an Identity, on one **scope**, the resources where the role applies. The RolesAssignment
`analyst-reads-gold` gives `analyst` the built-in role `Blob Data Reader`, which allows reading objects, on the prefix
`gold` of the Bucket `lakehouse`:

::include[../../../../../api/types/v1alpha1/examples/security/identity.yaml#grant]

The [Spec](spec.md) tab describes every field of an Identity, a Role, a RolesAssignment and a Policy.

Clone both repositories into one folder, and start the example from the root folder of funcd-typescript:

```console
$ git clone https://github.com/pyvvo/funcd-typescript
$ git clone https://github.com/pyvvo/funcd
$ cd funcd-typescript
$ funcdctl dev examples/s3-roundtrip --gport 8080 --cport 8081 --s3port 8082
```

In another terminal, in the folder that holds both clones, call the Function once with the tag `001`. It writes the
object `gold/roundtrip-001.txt`, and its response reports each step, as the Bucket page explains:

```console
$ curl -s -X POST -H 'content-type: application/json' -d '{"inv":"001"}' http://127.0.0.1:8080/function/s3-roundtrip
{"put":true,"get":true,"list":1,"denied":false,"granted":true}
```

Apply the Identity and its RolesAssignment:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/security/identity.yaml
applied Identity/analyst
applied RolesAssignment/analyst-reads-gold
```

funcd issues the credential after the apply has returned. Read the Identity's status, and repeat the command until
the phase is `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get identity analyst -n default -o json
```

The command prints the Identity as JSON. Under `status`, `"phase"` is `"Ready"`, the condition `Ready` has the status
`"True"`, and `"accessKeyId"` is `"FUNCIDMRSWMYLVNR2AAYLOMFWHS43U"`. funcd derives the access key ID from the
Identity's namespace and name, so it is the same at each run.

Read the key pair from the Secret `analyst`. funcdctl prints each value of a Secret in base64, so jq takes the value
out of the JSON and decodes it with `@base64d`:

```console
$ KEY_ID=$(funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get secret analyst -n default -o json | jq -r '.spec.data.accessKeyId | @base64d')
$ SECRET_KEY=$(funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get secret analyst -n default -o json | jq -r '.spec.data.secretAccessKey | @base64d')
```

Read the object as `analyst`, then try to write an object into the same prefix:

```console
$ curl -s -w '\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$KEY_ID:$SECRET_KEY" http://127.0.0.1:8082/lakehouse/gold/roundtrip-001.txt
roundtrip-001
$ curl -s -w '\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$KEY_ID:$SECRET_KEY" -X PUT --data-binary draft http://127.0.0.1:8082/lakehouse/gold/draft.txt
<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>AccessDenied</Code><Message>Access Denied.</Message><RequestId>JZVQER1LLBCXW5PG</RequestId><HostId>+StDZ2GlZ/+qPaz5nqjiKzTpySRQDBJfLPzShaDl1h6aoK21GI3btH9kXuFim3eUdaW1VlxlM1SEou+xvEvXW5o=</HostId></Error>
```

Each URL names the Bucket, then the object's key, whose first segment is the prefix. The option
`--aws-sigv4 aws:amz:us-east-1:s3` signs each request as an S3 client does, for `us-east-1`, the one region of the S3
gateway, and `-w '\n'` ends the answer with a line break. The read succeeds, because `Blob Data Reader` allows
reading the prefix `gold`. The write fails with the S3 error `AccessDenied` and the HTTP status 403, because the role
allows no writing and `analyst` is not the prefix's owner. The `RequestId` and the `HostId` differ at each run.

Keep the example running: [Choose a role](#choose-a-role), [Choose the scope of a grant](#choose-the-scope-of-a-grant)
and [Write a Policy](#write-a-policy) apply more objects to it, and use `KEY_ID` and `SECRET_KEY` in the second
terminal. Pressing Ctrl-C in the terminal that runs `funcdctl dev` stops the example, and `funcdctl dev` then loses the
Identity, its credential and the objects of the Bucket. To start again, repeat the steps above from `funcdctl dev`
on: the Identity then gets a new secret.

## Choose a role

An **action** is an operation that funcd checks, such as `s3::read`, which reads objects, or `kv::write`, which writes
KV keys. A role allows a set of actions. funcd has built-in roles, and a **Role** is a set of actions defined as an
object of a namespace. An entry of a RolesAssignment names one or the other in its
[`roleRef`](spec.md#rolesassignmentspecassignmentsroleref): `kind: BuiltinRole` with the name of a built-in role, or
`kind: Role` with the name of a Role of the RolesAssignment's namespace.

A role is needed only beyond what a principal gets without one, which [Access](#access) lists: an Identity gets
nothing, and a Function reads what it binds and writes what it owns. A role also allows only the requests that its
principal can send. A Function reaches a KV store, a Bucket prefix or a SQL database through a client of its context,
such as `ctx.kv`, which takes the alias of a **binding**, the entry under `spec.bindings` that connects the store to
the Function. A role creates no declaration, no binding and no link, and never makes valid an alias that no binding
declares: through these clients, it lets a Function write a table, a prefix or a database that the Function binds,
never reach one that it does not bind. An Identity's credential works only at the S3 gateway and at the gateway of a
Lake. Each built-in role therefore serves these principals:

- `Blob Data Reader` allows `s3::read`, and `Blob Data Writer` allows `s3::read` and `s3::write`. An Identity uses them
  through the S3 gateway, as the first example does. A Function uses them through the S3 gateway too, with the key
  pair that funcd gives a Function that binds a prefix, and uses `Blob Data Writer` through `ctx.blob` to write a
  prefix that it binds. The Bucket page's [Access](../../services/bucket/index.md#access) section explains both ways.
- `KV Data Writer` allows `kv::read` and `kv::write`, and `SQL Data Writer` allows `sql::read` and `sql::write`. A
  Function uses them to write a table or a database that it binds and does not own, as the
  [KV](../../services/kv/index.md#access) and [SQL](../../services/sql/index.md#access) pages show.
- `Lake Query Reader` and `Lake Contributor` both allow `lake::query`. An Identity uses them to query a Lake through
  the Lake's gateway, with the `lakeToken` of its Secret, as the [Lake](../../services/lake/index.md#access) page
  shows.
- `Reader` allows `s3::read` and `kv::read`, and `Contributor` and `Owner` allow `s3::read`, `s3::write`, `kv::read`
  and `kv::write`. They cover Bucket and KV data as the roles above do, and nothing else.
- `KV Data Reader` allows `kv::read`, `SQL Data Reader` allows `sql::read`, and `Function Invoker` allows
  `link::invoke`, the action of a call to another Function. funcd asks these actions only about the tables, the
  databases and the Functions that a Function's own bindings and links name, which already allow them, so these roles
  give no principal more access.

A Role lists the actions that it allows under [`actions`](spec.md#rolespec), which [Write a Policy](#write-a-policy)
lists; an action that funcd does not know allows nothing. The Role `object-writer` allows `s3::write` alone: unlike
`Blob Data Writer`, it lets its principal write objects without reading them, as a client that only uploads needs.
The RolesAssignment `analyst-writes-shared` gives it to `analyst` on the prefix `shared` of the Bucket `lakehouse`, a
prefix with no owner:

::include[../../../../../api/types/v1alpha1/examples/security/role.yaml#role]

::include[../../../../../api/types/v1alpha1/examples/security/role.yaml#grant]

The complete file,
[api/types/v1alpha1/examples/security/role.yaml](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/security/role.yaml),
also holds the Identity `analyst`. Applying an Identity again with the same spec keeps its credential. While the first
example runs, apply the file, write an object into `shared`, and read it back. Each `curl` command prints only the
HTTP status of its answer:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/security/role.yaml
applied Identity/analyst
applied Role/object-writer
applied RolesAssignment/analyst-writes-shared
$ curl -s -o /dev/null -w '%{http_code}\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$KEY_ID:$SECRET_KEY" -X PUT --data-binary report http://127.0.0.1:8082/lakehouse/shared/report.txt
200
$ curl -s -o /dev/null -w '%{http_code}\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$KEY_ID:$SECRET_KEY" http://127.0.0.1:8082/lakehouse/shared/report.txt
403
```

The write succeeds, and the read is refused: the Role allows no reading, and `Blob Data Reader` covers `gold` only.
An entry whose `roleRef` names a role that does not exist gives nothing; with `kind: Role`, it gives the Role's
actions once a Role of that name exists.

## Choose the scope of a grant

The [`scope`](spec.md#rolesassignmentspecscope) of an entry names the resources where its role applies, in the
RolesAssignment's namespace:

- `Namespace` covers every resource of the namespace, whatever the scope's `name`.
- `BucketPrefix`, with the name `<bucket>/<prefix>`, covers one prefix of a Bucket. No scope covers one Bucket as a
  whole.
- `KVStore` covers every table of a store, with the name `<store>`, or one table, with the name `<store>/<table>`.
- `SQLDatabase` and `Lake`, with the name of the database or of the Lake, cover that one resource.

A role's actions apply only to the resources of their kind: `Blob Data Writer` on a `KVStore` scope allows nothing.
The [principal](spec.md#rolesassignmentspecprincipal) is a Function or an Identity of the RolesAssignment's namespace.
funcd checks neither that the principal exists nor that the scope's resource does: a grant applies to the principal
and to the resources that have those names, once they exist.

[`spec.principal`](spec.md#rolesassignmentspec) and `spec.scope` give the principal and the scope of each entry that
sets none, so that one RolesAssignment gives several roles. funcd refuses an entry left with no principal or no scope.
The RolesAssignment `analyst-reads-namespace` gives `analyst` two roles on the whole namespace `default`:
`Blob Data Reader`, to read every prefix of every Bucket, and `Lake Query Reader`, to query every Lake:

::include[../../../../../api/types/v1alpha1/examples/security/rolesassignment.yaml#rolesassignment]

The complete file,
[api/types/v1alpha1/examples/security/rolesassignment.yaml](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/security/rolesassignment.yaml),
also holds the Identity. While the first example runs, apply the file, and read the object that
[Choose a role](#choose-a-role) wrote into `shared`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/security/rolesassignment.yaml
applied Identity/analyst
applied RolesAssignment/analyst-reads-namespace
$ curl -s -w '\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$KEY_ID:$SECRET_KEY" http://127.0.0.1:8082/lakehouse/shared/report.txt
report
```

The read that `object-writer` did not allow now succeeds. The grants of a principal add up: its bindings, its
ownerships, every RolesAssignment that names it and every Policy `permit` that matches its request. A request is
allowed when one of them allows it and no rule forbids it, as [Write a Policy](#write-a-policy) explains.

## Write a Policy

A **Policy** holds rules of its namespace in [Cedar](https://www.cedarpolicy.com/), the policy language in which
funcd decides each request for data. funcd compiles the Policies of a namespace together with the RolesAssignments and
with its built-in rules, which the Access sections of the [KV](../../services/kv/index.md#access),
[Bucket](../../services/bucket/index.md#access), [SQL](../../services/sql/index.md#access),
[Lake](../../services/lake/index.md#access), [Secrets](../../services/secrets/index.md#access) and
[Function](../../workloads/function/index.md#access) pages quote. A request is allowed when a `permit` statement
matches it and no `forbid` statement does, so a `forbid` wins over a binding, an ownership, a role and a `permit`
alike.

The Policy's [`cedar`](spec.md#policyspec) field holds one or more statements. Each statement is a `permit` or a
`forbid` with three parts, and an optional condition:

- The principal: `principal` alone matches every principal, and `principal == <entity>` one principal.
- The action: `action == Action::"<action>"` names exactly one of these actions: `s3::read`, `s3::write`, `kv::read`,
  `kv::write`, `sql::read`, `sql::write`, `lake::query`, `secret::read`, `link::invoke` and `egress::connect`.
- The resource: `resource` alone matches every resource, `resource == <entity>` one resource, and
  `resource in <entity>` also every resource that the entity holds.
- A `when` or `unless` clause adds a condition on the attributes of the principal and of the resource.

Each principal and each resource is an entity, written as its type and its ID. These are the entities, each with an ID
that starts with its namespace, except the last one:

- `Function::"<namespace>/<name>"`, `Identity::"<namespace>/<name>"` and `Lake::"<namespace>/<name>"` are the
  principals. A Lake is the principal of the requests of its engine for the Lake's prefixes and Secrets, and the
  resource of `lake::query`. A Function is also the resource of `link::invoke`.
- `BlobPrefix::"<namespace>/<bucket>/<prefix>"` is the resource of `s3::read` and `s3::write`, and
  `Bucket::"<namespace>/<bucket>"` holds the prefixes of the Bucket.
- `KVTable::"<namespace>/<store>/<table>"` is the resource of `kv::read` and `kv::write`, and
  `KVStore::"<namespace>/<store>"` holds the tables of the store.
- `SQLDatabase::"<namespace>/<database>"` is the resource of `sql::read` and `sql::write`, and
  `Secret::"<namespace>/<name>"` the resource of `secret::read`.
- `NetDestination::"<ip>:<port>"`, the address and the port of an outbound connection, is the resource of
  `egress::connect`. Its attributes are `ip`, `port` and `domains`: the names that the Function's
  [worker](../../../platform/workloads/index.md#runtime), the process that runs its code, looked up through funcd and
  that resolved to that address. The [Networking](../networking/index.md#allow-a-functions-outbound-connections) page
  explains outbound connections.

Each other entity has the attribute `namespace`, except a table, a store, a prefix or a Bucket whose store or Bucket
does not exist: such an entity has no attribute at all. A table, a store, a prefix, a Bucket and a SQL database that
exist also have `resourceGroup`, and so do a Function and a Lake when they are the principal.

The Policy `analyst-writes-nothing` forbids `analyst` to write any prefix of the Bucket `lakehouse`, whatever its roles
allow, for example while its uploads are checked:

::include[../../../../../api/types/v1alpha1/examples/security/policy.yaml#policy]

The complete file,
[api/types/v1alpha1/examples/security/policy.yaml](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/security/policy.yaml),
also holds the Identity. While the first example runs, apply the file and write into `shared` again; then delete the
Policy, and write once more. Each `curl` command prints only the HTTP status of its answer:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token apply -f funcd/api/types/v1alpha1/examples/security/policy.yaml
applied Identity/analyst
applied Policy/analyst-writes-nothing
$ curl -s -o /dev/null -w '%{http_code}\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$KEY_ID:$SECRET_KEY" -X PUT --data-binary report http://127.0.0.1:8082/lakehouse/shared/report.txt
403
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token delete policy analyst-writes-nothing -n default
deleted Policy/analyst-writes-nothing
$ curl -s -o /dev/null -w '%{http_code}\n' --aws-sigv4 aws:amz:us-east-1:s3 --user "$KEY_ID:$SECRET_KEY" -X PUT --data-binary report http://127.0.0.1:8082/lakehouse/shared/report.txt
200
```

The write that `object-writer` allows is refused while the Policy exists, and allowed again from the next request once
the Policy is deleted.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

funcd checks a Policy when it is applied. It refuses text that does not parse or holds no statement, and a statement
that does not name exactly one action that funcd knows. It also refuses an entity that a statement's principal or
resource names with `==` or `in`, when funcd does not know its type or the entity belongs to another namespace;
[Errors](#errors) quotes the messages. funcd checks nothing else in a statement: it accepts an unknown type in a test
such as `principal is User`, and any entity or attribute that a `when` or `unless` condition names. A Policy decides
only the requests whose principal and resource are both in its namespace.

A `permit` cannot allow a write: the built-in rules forbid each write by a principal that neither owns the resource nor
holds a role that allows the write, and that `forbid` wins. A `permit` allows more only where funcd decides a request
on a resource that the principal does not bind: an S3 request through the S3 gateway, a query through the gateway of
a Lake, and an outbound connection. A `permit` of `s3::read` on `resource in Bucket::"default/lakehouse"`, for
example, lets its principal read every prefix of that Bucket, a scope that no RolesAssignment has.

## Access

Without any role, each principal gets the access below. The Access section of each page linked here explains it, and
quotes the built-in policy that funcd applies to it, where funcd has one:

- A Function reads the KV tables, the Bucket prefixes and the SQL databases that it binds, with the actions
  `kv::read`, `s3::read` and `sql::read`, and writes those that it owns, with `kv::write`, `s3::write` and `sql::write`
  ([KV](../../services/kv/index.md#access), [Bucket](../../services/bucket/index.md#access),
  [SQL](../../services/sql/index.md#access)). It queries the Lakes that it binds, with `lake::query`
  ([Lake](../../services/lake/index.md#access)), reads the Secrets that it binds, with `secret::read`
  ([Secrets](../../services/secrets/index.md#access)), and calls the Functions that its links name, with `link::invoke`
  ([Function](../../workloads/function/index.md#access)).
- A Lake's engine reads the prefixes and the Secrets that the Lake binds, and writes the prefix that the Lake owns
  ([Lake](../../services/lake/index.md#access)).
- An Identity gets nothing: each of its requests needs a role or a `permit`.
- No principal gets an outbound connection by itself: on a server that controls outbound connections, an EgressPolicy
  or a Policy's `permit` allows them ([Networking](../networking/index.md#access)).

Any other access needs a role, as [Choose a role](#choose-a-role) and
[Choose the scope of a grant](#choose-the-scope-of-a-grant) explain, or a Policy's `permit`, as
[Write a Policy](#write-a-policy) explains.

funcd's API decides who may create, change and read the objects of this page with role-based access control (RBAC),
not with Cedar: the role of the caller's credential decides, as for every object of the API. funcd's configuration
sets the credentials, in `auth.token` and `auth.credentials`, which the funcd
[Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes:

- An `admin` acts on every object of every namespace.
- A `developer` creates, changes, reads and deletes Identities, Roles, RolesAssignments and Policies in the namespaces
  of its credential, and reads their Secrets, the credentials of the Identities included. `funcdctl dev`'s token,
  `funcd-dev-token`, is a developer credential of the namespace `default`.
- A `viewer` reads these objects in the namespaces of its credential, but never a Secret, so it cannot read an
  Identity's credential.

The roles that a RolesAssignment gives apply to data only: no role, not even `Owner`, lets a principal create or change
a RolesAssignment.

The calls from outside funcd to a Function through funcd's **edge**, the part of funcd that receives them, are checked
by RBAC too, with the API's credentials, when a Route or the Function's address asks its callers to authenticate.
These are not the credentials of the Identities. The Networking page's [Access](../networking/index.md#access) section
explains these calls.

## Data storage

funcd stores the Identities, Roles, RolesAssignments and Policies in its metastore, with the other objects of its API,
and the credential of each Identity in its Secret, as it stores every Secret: unencrypted unless the operator sets
`secrets.encryptionKeyFile`, and kept across a restart with `storage.mode: file`, the default. The Secrets page's
[Data storage](../../services/secrets/index.md#data-storage) section explains both settings.

## Status

`funcdctl get identity <name> -n <namespace> -o json` prints an Identity's status, as the first example shows:

- `phase` is `Ready` once funcd has written the credential into the Secret, and `Pending` when funcd cannot write it.
  The condition `Ready` explains the phase. It is `True` once the credential is issued, and otherwise `False` with a
  reason: `SecretNotOwned` when the Secret that the Identity names exists and no Identity of that name controls it,
  with a message such as `secret not owned: secret "app-secret" is not controlled by identity "analyst"`, or
  `SecretIssueFailed` when funcd could not write the Secret. funcd checks an Identity with `SecretNotOwned` again every
  `runtime.supervisionPeriod`, 10 seconds by default.
- `accessKeyId` holds the access key ID of the key pair, which funcd derives from the Identity's namespace and name.
- `observedRotate` holds the [`rotate`](spec.md#identityspec) value of the credential that funcd issued last, as
  [Changes and deletion](#changes-and-deletion) explains.

A Role, a RolesAssignment and a Policy have no status: a request that they should allow or refuse shows whether they
apply, as the steps above do.

## Changes and deletion

- A change to a Role, a RolesAssignment or a Policy applies from the next request, with no restart, and so does its
  deletion, as [Write a Policy](#write-a-policy) shows. Deleting a Role leaves the RolesAssignments that name it, which
  give nothing until a Role of that name exists again.
- To rotate an Identity's credential, raise its [`rotate`](spec.md#identityspec) above `status.observedRotate`, such
  as from 0 to 1, and apply the Identity. funcd writes a new `secretAccessKey` and a new `lakeToken` into the Secret,
  under the same access key ID, and sets `status.observedRotate` to the new value. The old secret stops working at
  once, with no overlap: the S3 gateway answers a request signed with it with the S3 error `SignatureDoesNotMatch`. A
  client then reads the Secret again.
- Deleting an Identity deletes its Secret, and its credential stops working: the S3 gateway answers its requests with
  the S3 error `InvalidAccessKeyId`. The RolesAssignments that name the Identity stay, and give their roles to a new
  Identity of the same name, which gets a new secret under the same access key ID.
- The gateway of a Lake checks a caller only when a session opens. After a rotation or the deletion of an Identity, its
  old token opens no new session, and after a change that removes a caller's access, the caller opens no new session.
  A session that is already open goes on.
- A Function that shares a worker with other Functions, through
  [`spec.pooling.worker`](../../workloads/function/spec.md#functionspecpooling), shares it only with Functions of the
  same access. A RolesAssignment entry whose principal is that Function, or a Policy statement that names it, changes
  the Function's access, so funcd moves the Function to a shared worker of Functions with the same access: while it
  moves, its calls get the HTTP status 404, then 503 until that worker has loaded it.

## Errors

`funcdctl apply` refuses an object of this page that breaks a rule, with a message that names the field or the
statement at fault, such as:

- `spec.type must be "external" (got "internal")` for an Identity;
- `spec.actions must list at least one action` for a Role;
- `spec.assignments[0] has no principal (and no top-level default)`, the same for the scope, and
  `spec.assignments[0].scope.kind "Bucket" is invalid` for a RolesAssignment;
- for a Policy, a message that names the rule that the text breaks, such as
  `admission.policy-validity: Policy "<name>" has invalid Cedar: cedar.ValidateCedar: cedar policy references unknown action "s3::delete"`.
  The other rules read `cedar.ValidateCedar: cedar policy does not parse`,
  `cedar.ValidateCedar: cedar policy must name a specific action`,
  `cedar.ValidateCedar: cedar policy references unknown entity type "User"` and
  `cedar.ValidateCedarInNamespace: cedar policy statement 0 names Identity::"other/analyst" outside the Policy's namespace "default"`.

A request that funcd denies reads differently at each place that checks it:

- Through the S3 gateway, the answer is the S3 error `AccessDenied`, with the HTTP status 403, and names no rule, as
  the first example shows. A request signed with an access key ID that the gateway does not know, such as that of a
  deleted Identity, gets `InvalidAccessKeyId`, and one signed with a wrong secret, such as one that a rotation
  replaced, gets `SignatureDoesNotMatch`, both with 403.
- Through a Lake's gateway, the session's first request gets 403 with the message `lake query denied`, as the
  [Lake](../../services/lake/index.md#access) page explains.
- Through a client of a Function's context, the call fails with 403, and the error's message names the `forbid` that
  matched: `cedar: forbidden by policy builtin/<rule>` for a built-in rule, such as `builtin/kv-single-writer`, and
  `cedar: forbidden by policy <namespace>/<policy>#<n>` for the statement `n` of a Policy, counted from 0, such as
  `default/forbid-app-secret#0` on the [Secrets](../../services/secrets/index.md#access) page.
- An outbound connection that nothing allows meets the refusals that the Networking page's
  [Errors](../networking/index.md#errors) section lists.

An API request that the caller's role does not allow fails with 403, such as
`get Secret in "default" denied: viewer may not act on kind "Secret"` for a viewer that reads a Secret.

## Run it locally

`funcdctl dev` decides each request as a funcd server does, with the same built-in rules, and applies the Identities,
Roles, RolesAssignments and Policies of its folder and those that `funcdctl apply` sends it. It differs from a server
in these ways:

- It runs the namespace `default` only, and stops on a resource of another namespace.
- It runs the S3 gateway at each run, at the port that `--s3port` sets or at a free port that its banner prints, so an
  Identity's key pair works at once. A funcd server runs it only when the operator sets `s3gateway.enabled: true`, and
  it listens at `s3gateway.listenAddr`, `127.0.0.1:9000` by default, an address that only the server's own machine
  reaches.
- It gives each Function of its folder the Role `dev-blob-writer`, which allows `s3::write`, on the whole namespace,
  through a RolesAssignment named `dev-blob-writer-<function>`, which a server does not give. While the first example
  runs, as [Usage](#usage) starts it,
  `funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get roles -n default` lists `dev-blob-writer`, and
  `get rolesassignments` with the same flags lists `dev-blob-writer-s3-roundtrip`. A write that
  succeeds locally can therefore fail once deployed, as the Bucket page's
  [Run it locally](../../services/bucket/index.md#run-it-locally) section explains. A Policy's `forbid` still applies
  to these Functions.
- It refuses a folder that holds no Function and no Workflow step with an image, with the message
  `holds no Function or Workflow image step to run`. A folder of Identities and grants alone does not start: apply
  them to a running example, as the first example does.
- Its edge has no authenticator, as the Networking page's [Run it locally](../networking/index.md#run-it-locally)
  section explains.

`funcdctl dev` keeps its objects in memory, where they are lost when it stops, so each run issues a new secret for an
Identity, under the same access key ID. With the `--persist` flag, it keeps them between runs in the metastore of its
persist folder: `.funcd-dev` in the current folder, unless `--persist-to` names another folder. The credentials of the
Identities are stored there unencrypted, because `funcdctl dev` sets no encryption key. To remove them, delete the
persist folder after `funcdctl dev` stops; that clears all the state that the local platform stored: its metastore,
KV, blob, SQL, lake and volume data. The [reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev)
lists its flags.

## Examples

- [s3-roundtrip in TypeScript](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip): the Bucket
  and the Function that the steps on this page run, with a RolesAssignment that gives the Function a writer role on
  one prefix.
