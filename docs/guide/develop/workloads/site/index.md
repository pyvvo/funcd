---
title: Site
description: Serve a static web app with settings for each deployment.
---

## Usage

A Site serves a static web app from a Bucket. This example runs a built app locally and gives it a `config.json` for the staging environment. See [how Sites work](/funcd/docs/platform/workloads/site.md) for the concept.

Install the [dev build of funcdctl](/funcd/docs/get-started/install.md), Git, curl, and either Node.js or Python 3.12 or later. Put Node.js or `python3` on the `PATH`. The example needs no build step.

Clone the example and start it:

```console
$ git clone https://github.com/pyvvo/funcd-functions
$ cd funcd-functions
$ funcdctl dev apps/site-config/app -f apps/site-config/values/staging.yaml --build web=apps/site-config/web --gport 8080 --cport 8081
```

In another terminal, repeat this command until `status.phase` is `Ready`. The Site may not exist yet while the App starts.

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get site site-config-web -n default -o json
```

Fetch its configuration and index. `--resolve` directs the example's host name to the local server.

```console
$ curl -s --resolve site-config.staging.test:8080:127.0.0.1 http://site-config.staging.test:8080/config.json
{"API_URL":"https://api.staging.example","NEW_DASHBOARD":"false"}
$ curl -s --resolve site-config.staging.test:8080:127.0.0.1 http://site-config.staging.test:8080/ | head -4
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8"><base href="/">
```

Press Ctrl-C in the first terminal to stop the example.

## Set runtime configuration

[`runtimeConfig`](/funcd/docs/develop/workloads/site/spec.md#sitespecruntimeconfig) generates `config.json` from a ConfigMap and explicit values. Explicit values win when both define the same key. Every client that can load the Site can read this file. Do not put secrets in it.

Start the example again with the additional values file:

```console
$ funcdctl dev apps/site-config/app -f apps/site-config/values/staging.yaml -f apps/site-config/values/new-dashboard.yaml --build web=apps/site-config/web --gport 8080 --cport 8081
```

Once the Site is `Ready`, fetch its configuration:

```console
$ curl -s --resolve site-config.staging.test:8080:127.0.0.1 http://site-config.staging.test:8080/config.json
{"API_URL":"https://api.staging.example","NEW_DASHBOARD":"true"}
```

The bundle is unchanged. Only `config.json` changes. Press Ctrl-C to stop the example.

## Serve an app at a path

Set `ingress.path` to the app's path. With `baseHref: true`, funcd adds the path to the index so relative URLs and deep links resolve correctly.

This [complete manifest](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/site/site.yaml) serves an app at `/handbook`. Replace `image` with the reference of a bundle published with [`funcdctl push --site`](/funcd/docs/develop/tools/funcdctl/spec.md#funcdctl-push).

::include[../../../../../api/types/v1alpha1/examples/site/site.yaml#site]

The Site creates its declared Bucket. Use `ref` instead of `name` to use an existing Bucket. Its bundle prefix must have no owner. See the [Site fields](/funcd/docs/develop/workloads/site/spec.md#sitespec) for other settings.

To serve files that a Function writes, add [`ingress.rules`](/funcd/docs/develop/workloads/site/spec.md#sitespecingressrules) for their Bucket prefix. The [reports example](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/site/rules.yaml) serves exported files at `/reports/data` beside its app. These paths serve the stored files directly and do not use the app's SPA fallback.

## Access

Set [`ingress.auth.mode`](/funcd/docs/develop/workloads/site/spec.md#sitespecingressauth) to `open` for public access or `authenticated` to require a credential. When unset, the Namespace's defaults apply, otherwise access is open.

Authenticated access requires `server.auth.edge: true` and a credential allowed to get Functions in the Site's namespace. `funcdctl dev` has no edge authenticator. See [authentication and roles](/funcd/docs/develop/security-and-networking/networking.md#ask-callers-to-authenticate).

## Limits and quotas

- `config.json` must fit within 32 KiB and the Bucket's `maxObjectBytes`.
- Bundle files and previous bundles count against the [Bucket and prefix quotas](/funcd/docs/develop/services/bucket/spec.md#bucketspecquota). A deployment also needs enough free space for `storage.minFreeSize`.

After freeing space, update the Site or its Bucket, or restart funcd, to retry a failed deployment.

## Data storage

The Bucket keeps every deployed bundle. Deleting a Site keeps its Bucket and data. If the Site belongs to an App, the App's `deletion` setting decides whether to keep or delete its Bucket. The first example uses `delete`.

See [Bucket storage](/funcd/docs/develop/services/bucket.md#data-storage) for storage settings and persistence.

## Status

Read `status.phase` and the `Ready` condition with the command in the first example.

| Phase | Reason | Meaning |
| --- | --- | --- |
| `Ready` | `Materialized` | The bundle is stored and the Route is configured. |
| `Pending` | `ArtifactNotFound` | funcd cannot fetch the bundle. |
| `Pending` | `DigestUnresolved` | The image lacks a tag or digest, or is not a Site bundle. |
| `Pending` | `IndexMissing` | The bundle does not contain its index file. |
| `Pending` | `MaterializeFailed` | funcd could not store the bundle or a generated file. Read the condition message for the cause. |
| `Pending` | `QuotaExceeded` | The deployment exceeds a quota or leaves too little free space. |
| `Pending` | `BucketNotFound` | The referenced Bucket or prefix does not exist. |
| `Pending` | `StoreNotOwned` | The named Bucket exists and was not created by this Site. Use `ref` to reuse it. |
| `Pending` | `PrefixOwned` | The bundle's prefix has an owner. |
| `Pending` | `RouteNotOwned` | A Route with the same name exists and does not belong to this Site. |
| `Pending` | `RouteNotReady` | The Route cannot serve. Its condition gives the cause. |
| `Pending` | `ConfigMapNotFound` | The runtime ConfigMap does not exist. The bundle can still deploy. |
| `Pending` | `RuntimeConfigTooLarge` | `config.json` exceeds 32 KiB or the Bucket's object size limit. |

A disabled address can still have the phase `Ready`. Check [`ingress.enabled`](/funcd/docs/develop/workloads/site/spec.md#sitespecingress) if the Site is ready but its address is off. The [status reference](/funcd/docs/develop/workloads/site/spec.md#sitestatus) describes the remaining fields.

## Changes and deletion

- Deploy a new bundle with a new tag or digest in `image`. Moving an existing tag alone does not deploy it. The previous bundle keeps serving until the new one is ready, and keeps serving if deployment fails.
- Change `runtimeConfig` or its ConfigMap to update only `config.json`.
- Change `ingress.path` to move the app without copying its bundle again.
- To restore an earlier bundle, set `image` to that bundle's reference.
- To recreate a deleted Site using its retained Bucket, declare the Bucket by `ref`.

## CLI errors

`funcdctl dev` reports these local setup errors:

| Error | Action |
| --- | --- |
| `no runtime found on PATH` | Add Node.js or Python 3.12 or later as `python3` to the `PATH`, or set `FUNCD_NODE` or `FUNCD_PYTHON`. |
| The folder `holds no Function or Workflow image step to run`. | Use an App template with `--build` for a Site-only example. |

## Run it locally

For an App template, `funcdctl dev` packs the folder named by `--build` and redeploys when its files change. Set `dev.liveReload: true` in `funcdctl.config.yaml` to reload open pages after each build.

Outside an App template, the folder must also contain a Function or Workflow image step. The Site's image must already be available; `funcdctl dev` does not pack it. Local development runs only the `default` namespace.

Without `--persist`, the Bucket's data is lost when the command stops. With `--persist`, it stays in `.funcd-dev`, or the folder named by `--persist-to`. Delete that folder after stopping the command to clear all local platform data.

## Examples

- [site-config](https://github.com/pyvvo/funcd-functions/tree/main/apps/site-config) runs a static app with configuration for each environment.
- [s3-roundtrip](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip) serves files written by a Function.
