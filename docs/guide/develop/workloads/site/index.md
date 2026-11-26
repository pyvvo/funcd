---
title: Site
description: Serve a static web app with settings for each deployment.
---

## Usage

A Site serves a static web app from a Bucket. This example runs a built app locally and gives it a `config.json` for the staging environment. See [how Sites work](../../../platform/workloads/site.md) for the concept.

Install the [dev build of funcdctl](../../../get-started/install.md), Git, curl, and either Node.js or Python 3.12 or later. Put Node.js or `python3` on the `PATH`, or, with Docker or nerdctl, run `funcdctl dev --containers`, which needs
neither. The example needs no build step.

Clone the example and start it:

```console
$ git clone https://github.com/pyvvo/funcd-functions
$ cd funcd-functions
$ funcdctl dev apps/site-config/app -f apps/site-config/values/staging.yaml --build web=apps/site-config/web --gport 8080 --cport 8081
```

The first argument, `apps/site-config/app`, is an [App template](../app/index.md#usage): a folder that holds `app.yaml` beside a `resources` folder. funcdctl renders the template into an App with the values of the `-f` files. [`app.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/site-config/app/app.yaml) names the image `web` under `images`, and each file of `resources` holds sections of the App's spec: [`site.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/site-config/app/resources/site.yaml) declares the Site and its Bucket, and sets the Site's `image` to `${{ images.web }}`. `--build web=apps/site-config/web` packs that folder as the image `web`. For another frontend, use the same three parts: a key under `images`, the Site's `image` set to `${{ images.<key> }}`, and `--build <key>=<folder>` naming the folder that its build writes.

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

[`runtimeConfig`](spec.md#sitespecruntimeconfig) generates `config.json` from a ConfigMap and explicit values. Explicit values win when both define the same key. Every client that can load the Site can read this file. Do not put secrets in it.

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

Set `ingress.path` to the app's path. Without `ingress.path`, a Site that sets `ingress.host` answers at `/`, as in the first example, and any other Site answers at `/site/<name>`. A Site's host follows the rules in [Choose the host of a Route](../../security-and-networking/networking/index.md#choose-the-host-of-a-route). With `baseHref: true`, funcd adds the path to the index so relative URLs and deep links resolve correctly.

This [complete manifest](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/site/site.yaml) serves an app at `/handbook`. Replace `image` with the reference of a bundle published with [`funcdctl push --site`](../../tools/funcdctl/spec.md#funcdctl-push).

::include[../../../../../api/types/v1alpha1/examples/site/site.yaml#site]

At `/handbook`, the Site serves its index: the file that [`index`](spec.md#sitespec) names, else the server's [`site.defaultIndex`](../../running-funcd/spec.md#funcdconfigyaml), `index.html` by default. With `spa: true`, a path under `/handbook` that matches no file also gets the index, so that the app's own router handles a deep link such as `/handbook/chapters/3`.

The Site creates its declared Bucket. Use `ref` instead of `name` to use an existing Bucket. Its bundle prefix must have no owner. See the [Site fields](spec.md#sitespec) for other settings.

To serve files that a Function writes, add [`ingress.rules`](spec.md#sitespecingressrules) for their Bucket prefix. The [reports example](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/site/rules.yaml) serves exported files at `/reports/data` beside its app. These paths serve the stored files directly and do not use the app's SPA fallback.

## Serve the app and a Function on one host

A Site serves its own address through a Route of the Site's name, which the Site creates and owns. To serve the app and a Function on one host, apply a Route of another name, as the site-config example's [`sites/app-route.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/site-config/sites/app-route.yaml) does: its rule at `/` names the Site in [`backend.site`](../../security-and-networking/networking/spec.md#routespecrulesbackend), and its rule at `/api` names `todo-api` in `backend.function`. The example does not create `todo-api`, the Function of the [todo App](https://github.com/pyvvo/funcd-functions/tree/main/apps/todo): name a Function that exists in the Route's namespace. A `backend.site` rule serves the Site's current bundle and its `config.json`. The rule with the longest matching path serves each request. While the Site does not exist or serves no bundle, or the Function does not exist (`BackendNotFound`), that Route serves none of its rules.

Set the Site's [`ingress.enabled`](spec.md#sitespecingress) to `false` so that only that Route serves the bundle: the Site's own address is then off, and the Site can still be `Ready`. With `baseHref: true`, also set the Site's `ingress.path` to the rule's path: the index's `<base href>` is the Site's own path, even while its address is off.

## Access

Set [`ingress.auth.mode`](spec.md#sitespecingressauth) to `open` for public access or `authenticated` to require a credential. If the mode is unset, funcd uses the Namespace's default. If the Namespace sets no default, access is open. This setting covers the Site's own address only: a Route of another name that serves the Site, as in [Serve the app and a Function on one host](#serve-the-app-and-a-function-on-one-host), applies the [`auth`](../../security-and-networking/networking/spec.md#routespecrules) of its own rules, and `sites/app-route.yaml` keeps both of its rules `open`.

Authenticated access requires `server.auth.edge: true` and a credential allowed to get Functions in the Site's namespace. `funcdctl dev` has no edge authenticator. See [authentication and roles](../../security-and-networking/networking/index.md#ask-callers-to-authenticate).

## Limits and quotas

- `config.json` must fit within 32 KiB and the Bucket's `maxObjectBytes`.
- Bundle files and previous bundles count against the [Bucket and prefix quotas](../../services/bucket/spec.md#bucketspecquota). A deployment also needs enough free space for `storage.minFreeSize`.

After freeing space, update the Site or its Bucket, or restart funcd, to retry a failed deployment.

## Data storage

The Bucket keeps every deployed bundle. Deleting a Site keeps its Bucket and data. If the Site belongs to an App, the App's `deletion` setting decides whether to keep or delete its Bucket. The first example uses `delete`.

See [Bucket storage](../../services/bucket/index.md#data-storage) for storage settings and persistence.

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

A disabled address can still have the phase `Ready`. Check [`ingress.enabled`](spec.md#sitespecingress) if the Site is ready but its address is off. The [status reference](spec.md#sitestatus) describes the remaining fields.

## Changes and deletion

- Deploy a new bundle with a new tag or digest in `image`. Moving an existing tag alone does not deploy it. The previous bundle keeps serving until the new one is ready, and keeps serving if deployment fails.
- Change `runtimeConfig` or its ConfigMap to update only `config.json`.
- Change `ingress.path` to move the app without copying its bundle again.
- The bundle prefix in [`bindings.blob`](spec.md#sitespecbindingsblob) cannot change while the Site exists: a new prefix needs a new Site.
- To restore an earlier bundle, set `image` to that bundle's reference.
- To recreate a deleted Site using its retained Bucket, declare the Bucket by `ref`.

A browser checks the index and `config.json` again at each load, as it does `robots.txt`, `sw.js`, the files under `.well-known/` and the files whose names start with `favicon` or `manifest` or contain `service-worker`. It may keep a file for a year when the file's name holds 8 or more lowercase hexadecimal characters after a `.` or `-` and before the extension, such as `app.3f9a2c1b.js`. It may keep any other file, such as the example's [`app.js`](https://github.com/pyvvo/funcd-functions/blob/main/apps/site-config/web/app.js), for up to 5 minutes, so after a new bundle, such a file can look stale for up to 5 minutes. A build that gives each changed file a new hashed name avoids this.

## CLI errors

`funcdctl dev` reports these local setup errors:

| Error | Action |
| --- | --- |
| `no runtime found on PATH` | Add Node.js or Python 3.12 or later as `python3` to the `PATH`, or set `FUNCD_NODE` or `FUNCD_PYTHON`. With Docker or nerdctl, `funcdctl dev --containers` needs neither. |
| The folder `holds no Function or Workflow image step to run`. | Use an App template with `--build` for a Site-only example. |

## Run it locally

For an App template, `funcdctl dev` packs the folder named by `--build` and redeploys when its files change. Set `dev.liveReload: true` in `funcdctl.config.yaml` to reload open pages after each build.

Outside an App template, the folder must also contain a Function or Workflow image step. The Site's image must already be available; `funcdctl dev` does not pack it. Local development runs only the `default` namespace.

Without `--persist`, the Bucket's data is lost when the command stops. With `--persist`, it stays in `.funcd-dev`, or the folder named by `--persist-to`. Delete that folder after stopping the command to clear all local platform data.

## Examples

- [site-config](https://github.com/pyvvo/funcd-functions/tree/main/apps/site-config) runs a static app with configuration for each environment.
- [s3-roundtrip](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip) serves files written by a Function.
