---
title: Overview
description: The operations of funcd's control plane API, what each one answers and when, from funcd's OpenAPI spec.
---

This is the API of funcd's control plane. funcdctl, the SDK and any HTTP client call it under
`/apis/funcd.io/v1alpha1`, at the address that `server.listenAddr` sets. Its one operation on the gateway is the call of
a Function, `POST /function/{name}`, at the address that `server.dataPlaneAddr` sets. Each page of this section is one
operation: its method and path, its parameters, its body, its authentication, and each answer with when it happens and
what to do. The sections follow the Develop chapter's: a kind's operations sit where its Spec tab does.

## Authentication

Each operation of the control plane needs a credential of funcd's config: a bearer token in the `Authorization` header,
or an API key in the `X-Api-Key` header. Only the spec, at `/openapi.json` and `/openapi.yaml`, and its interactive view
at `/docs` need none. A missing or unknown credential gets 401 Unauthorized. The call of a Function needs a credential
only when the Function's `spec.ingress.auth.mode`, else its namespace's `spec.edgeDefaults.auth.mode`, is
`authenticated`.

## Errors

A refusal answers with an `application/problem+json` body (RFC 9457), unless its operation names another body, as the
call of a Function does when a value breaks the Function's contract. The body's `type` names the problem, and its
`detail` says what went wrong. Each operation's page lists its answers: for each status, the cases that give it, each
with its problem type, when it happens and what to do. The problem types that the operations list:

| Problem type | Status |
| --- | --- |
| `about:blank` | 400, 415, 422 |
| `urn:funcd:problem:conflict` | 409 |
| `urn:funcd:problem:deadline-exceeded` | 504 |
| `urn:funcd:problem:forbidden` | 403 |
| `urn:funcd:problem:invalid` | 400 |
| `urn:funcd:problem:not-found` | 404 |
| `urn:funcd:problem:payload-too-large` | 413 |
| `urn:funcd:problem:resource-exhausted` | 429 |
| `urn:funcd:problem:unauthorized` | 401 |
| `urn:funcd:problem:unavailable` | 503 |

Any operation can also answer 500 when funcd meets an error that it does not expect. No operation lists that case, and
funcd logs its cause.

## Conditional writes and dry runs

A replace or a delete can name the version of the object that it read: the quoted `resourceVersion` in an `If-Match`
header, or, for a replace, the body's `metadata.resourceVersion`. It applies only while the stored object still has that
version, and answers 409 Conflict otherwise. A create or a replace with `dryRun=true` passes every check and answers with
the object as it would be stored, but stores nothing.

## The spec

This reference is rendered from funcd's OpenAPI spec, `api/openapi/funcd.v1alpha1.yaml`, which funcd generates from its
code. The control plane serves its own spec at `/openapi.json` and `/openapi.yaml`, and an interactive view of it at
`/docs`. That spec holds the control plane's operations, so not the call of a Function. The gateway serves neither.
