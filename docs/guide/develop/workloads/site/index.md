---
title: Site
description: Serve a static web app from a Bucket, give each deployment its own config.json, serve it at a path, and read a Site's status.
---

## Usage

A Site serves a static web app, such as a single-page app, with no worker: funcd copies the app's built files into a
prefix of a Bucket, and its [edge](../../../platform/security-and-networking/networking.md), the part of funcd that
receives the calls from outside funcd, serves them from there. The [Platform page](../../../platform/workloads/site.md)
explains how a Site deploys; this page shows how to run a Site, check what it serves and read its status.

A Site's [`image`](spec.md#sitespec) names its **bundle**: the app's built files, packed into an OCI artifact by
`funcdctl push --site`. funcd copies each bundle into a folder of its own in that prefix, named after the bundle's
digest, so a Site can deploy a new bundle while the previous one keeps serving.

The first example, site-config, serves the bundle of a small web app, and a `config.json` file that funcd generates
beside the bundle from values that each deployment sets. The app reads `config.json` when it starts. The steps below
start the example on the local computer, read the Site's status until it shows the phase `Ready`, and fetch the Site's
`config.json` and its index: `config.json` contains `"API_URL":"https://api.staging.example"`, and the index holds
`<base href="/">`.

The example needs four programs:

- funcdctl, installed as the [Install](../../../get-started/install.md) page shows. That page installs the dev build
  of funcdctl, which includes the `funcdctl dev` command.
- git, which copies the example's repository.
- curl, which fetches the Site's files.
- Node.js, or Python 3.12 or later as `python3`, on the `PATH`. `funcdctl dev` stops at its start without one of
  them, even though this example has no Function.

The example's app is already built: its folder `web` holds `index.html` and `app.js`, so the example needs no build
tool.

`funcdctl dev` packs a Site's bundle itself only for a Site of an **App template**, a folder that holds an `app.yaml`
file beside a `resources` folder, from which funcdctl renders an [App](../../../platform/workloads/app.md), one
resource that declares a whole app. For a Site's image, `funcdctl dev` packs the folder that `--build <key>=<folder>`
names, where `<key>` is the image's key under `images` in `app.yaml`. It pushes the bundle into an OCI image layout, a
folder that holds images as a registry does, in its own state folder. It then renders the template with the values of
the files that `-f` names, and applies the App, which writes the Site.
[Run it locally](#run-it-locally) says what `funcdctl dev` does with a Site outside an App template.

`funcdctl dev` prints the address of each Site, and the address of its control plane, which a `funcdctl` command
reaches with `--server` and the token `funcd-dev-token`. The `--gport` flag sets the port of each Site's address, and
`--cport` the port of the control plane, so the commands below use the ports 8080 and 8081.

The example's folder, `apps/site-config` in the funcd-functions repository, holds the template in `app`, the app's
built files in `web`, and the values of each deployment in `values`. The template's
[`app/app.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/site-config/app/app.yaml) names its one
image, `web`, and the values that it takes. The values file
[`values/staging.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/site-config/values/staging.yaml) sets
`host` to `site-config.staging.test` and `api.url` to `https://api.staging.example`.

The template's `resources/site.yaml` declares the Site and the Bucket that holds its bundles. Each `${{ … }}`
expression takes a value from the template: `app.name` is the App's name, `site-config`; `images.web` is the image
`web`; and `values.host` and `values.api.url` come from the values file:

::include[../../../../../_repos/funcd-functions/apps/site-config/app/resources/site.yaml]

- [`bindings.blob`](spec.md#sitespecbindingsblob) names the Bucket `site-config-web` and its prefix `web`, which holds
  the Site's bundles. In an App, the App declares the Bucket under its own `buckets`, so the Site declares none. The
  App's `deletion: delete` makes funcd delete the Bucket, with every bundle in it, when the App is deleted; with
  `retain`, the default, the Bucket and its bundles stay.
- [`ingress`](spec.md#sitespecingress) sets where the edge serves the Site, through the **Site's Route**: a
  [Route](../../security-and-networking/networking/index.md) of the Site's name, which the Site writes and owns. Here
  the edge serves the Site on the host `site-config.staging.test`, at the path `/`, since `ingress` sets a host and no
  `path`, and to every caller, since `auth.mode` is `open`. With `baseHref: true`, funcd serves the index with a
  `<base href>` element that holds the Site's path, so the app's relative URLs, such as `app.js` and `config.json`,
  resolve under that path.
- [`spa`](spec.md#sitespec) serves the index for a path that matches no file, so that the app's own router handles a
  link such as `/vehicles/42`.
- [`runtimeConfig`](spec.md#sitespecruntimeconfig) makes funcd generate `config.json` from its `values` and from the
  keys of a [ConfigMap](../configuration/index.md). The template's
  [`resources/flags.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/site-config/app/resources/flags.yaml)
  declares that ConfigMap, which holds `NEW_DASHBOARD`.

The [Spec](spec.md) tab describes every field of a Site and of its status.

Clone the repository, and start the example from the repository's root folder:

```console
$ git clone https://github.com/pyvvo/funcd-functions
$ cd funcd-functions
$ funcdctl dev apps/site-config/app -f apps/site-config/values/staging.yaml --build web=apps/site-config/web --gport 8080 --cport 8081
```

`funcdctl dev` prints its addresses as it starts, before the Site serves its bundle. In another terminal, read the
Site's status, and repeat the command until the status shows the phase `Ready`:

```console
$ funcdctl --server http://127.0.0.1:8081 --token funcd-dev-token get site site-config-web -n default -o json
```

The command prints the Site as JSON. Until the App has written the Site, it fails with a message that contains
`Site "site-config-web" not found`. Under `status`, `"phase"` is `"Ready"` once the Site serves its bundle: the
condition `Ready` then has the status `"True"` and the reason `Materialized`. `"digest"` names the bundle that the Site
serves, and `"servingPrefix"` names the folder of the Bucket that holds it, `web/sha256-<hex>/`. `"objects"` counts the
bundle's files, here `index.html` and `app.js`, and `"bytes"` their total size. [Status](#status) explains the other
reasons.

The terminal that runs `funcdctl dev` prints the Site's address, `http://site-config.staging.test:8080/`. That host
name does not resolve on the local computer, so the commands below use curl's `--resolve` option, which sends the
requests for that host and port to `127.0.0.1`. Once the Site is `Ready`, fetch `config.json`, then the first lines of
the index:

```console
$ curl -s --resolve site-config.staging.test:8080:127.0.0.1 http://site-config.staging.test:8080/config.json
{"API_URL":"https://api.staging.example","NEW_DASHBOARD":"false"}
$ curl -s --resolve site-config.staging.test:8080:127.0.0.1 http://site-config.staging.test:8080/ | head -4
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8"><base href="/">
```

`API_URL` comes from the values file. `NEW_DASHBOARD` comes from the ConfigMap, which holds the default of the
template's value `newDashboard`, `false`. The index is the bundle's `index.html`, with the `<base href="/">` element
that funcd added.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Give each deployment its own configuration

[`runtimeConfig`](spec.md#sitespecruntimeconfig) makes funcd generate one `config.json` file beside a Site's bundle,
so that each deployment of one bundle sets its own values without a new build. funcd writes the file from the keys of
the ConfigMap that `configMap` names and from the keys of `values`; a key of `values` wins over the same key of the
ConfigMap. The file holds one JSON object of strings, in the order of their keys. Each key is an environment variable
name: ASCII letters, digits and `_`, not starting with a digit.

The edge serves the file at the Site's path followed by `config.json`: `/config.json` for a Site at `/`, as in the
first example. It answers with `Cache-Control: no-cache`, so a browser checks the file again at each load of the
page. The app fetches the file relative to its base, which `baseHref` sets, so the same bundle finds its file at any
path. The site-config app's script, `web/app.js`, reads it:

::include[../../../../../_repos/funcd-functions/apps/site-config/web/app.js]

The example's values file
[`values/new-dashboard.yaml`](https://github.com/pyvvo/funcd-functions/blob/main/apps/site-config/values/new-dashboard.yaml)
sets `newDashboard` to `"true"`, which the ConfigMap passes to `NEW_DASHBOARD`. `-f` takes one values file at a time,
and the files merge in order. From the root folder of the funcd-functions repository, cloned as in the first example,
start the example with both files:

```console
$ funcdctl dev apps/site-config/app -f apps/site-config/values/staging.yaml -f apps/site-config/values/new-dashboard.yaml --build web=apps/site-config/web --gport 8080 --cport 8081
```

In another terminal, once the Site's status shows the phase `Ready`, as in the first example, fetch `config.json`:

```console
$ curl -s --resolve site-config.staging.test:8080:127.0.0.1 http://site-config.staging.test:8080/config.json
{"API_URL":"https://api.staging.example","NEW_DASHBOARD":"true"}
```

The Site's `status.digest` names the same bundle as in the first example: only `config.json` changed. In the same
way, a change to `values` or to the ConfigMap rewrites only `config.json`.

The file is public, as every file of the Site: never put a secret in `values` or in the ConfigMap.
[Limits and quotas](#limits-and-quotas) gives the file's size limit.

To stop the example, press Ctrl-C in the terminal that runs `funcdctl dev`.

## Serve a Site at a path

A Site answers at the path that [`ingress.path`](spec.md#sitespecingress) sets. When `ingress.path` is not set, the
path is `/` for a Site that sets `ingress.host`, as in the first example, and `/site/<name>` for any other Site. A Site
that sets no host answers, in the `default` namespace, on any host that no other namespace uses as its own, and in
another namespace, on the namespace's host, `<namespace>.<server.baseDomain>`. The Site `handbook` serves its bundle
at `/handbook`:

::include[../../../../../api/types/v1alpha1/examples/site/site.yaml#site]

- `image` names a bundle that `funcdctl push --site <folder> <ref>` pushed to a registry that funcd can pull from. The
  command prints the bundle's reference with its digest; the
  [reference of `funcdctl push`](../../tools/funcdctl/spec.md#funcdctl-push) describes it.
- Outside an App, a Site declares exactly one Bucket under [`buckets`](spec.md#sitespecbuckets), the Bucket that
  `bindings.blob` names. By `name`, as here, the Site creates the Bucket, with the prefix and no owner for it. By
  `ref`, the Site uses a Bucket that exists, which must hold the prefix with no owner, and it never changes that
  Bucket. Either way, the Bucket outlives the Site: the Site's `buckets` entry cannot set `deletion: delete`.
- `baseHref: true` serves the index with `<base href="/handbook/">`, so a bundle built with relative URLs runs at this
  path, deep links such as `/handbook/chapters/3` included. Without `baseHref`, the edge serves the index as it is,
  so the bundle's URLs must already hold the path.

A Route can also serve a Site's bundle: a rule whose [`backend.site`](../../security-and-networking/networking/spec.md#routespecrulesbackend)
names the Site serves the Site's current bundle, and its `config.json`, at the rule's path, and follows each new
bundle. Such a Route can serve a Function beside the bundle on one host. With `ingress.enabled: false`, the
Site's own address is off, and only such a Route serves the bundle. The
[Networking](../../../platform/security-and-networking/networking.md) page explains how the edge matches a host and a
path.

## Serve other files beside the bundle

With [`ingress.rules`](spec.md#sitespecingressrules), a Site also serves other prefixes of a Bucket, each at a path of
its own. A rule serves the objects of its prefix as the Bucket holds them: funcd copies nothing, so a file written into
the prefix is served with no new deployment. The Site `reports` serves its bundle at `/reports`, and the prefix
`exports` of its Bucket at `/reports/data`. This excerpt shows the Site; the complete file,
[api/types/v1alpha1/examples/site/rules.yaml](https://github.com/pyvvo/funcd/blob/main/api/types/v1alpha1/examples/site/rules.yaml),
also holds the Bucket `reports` and the Function `export`, which owns the prefix `exports`:

::include[../../../../../api/types/v1alpha1/examples/site/rules.yaml#site]

- A request for `/reports/data/<key>` reads the object `exports/<key>`. A rule's path matches itself and every path
  below it. It may sit below the bundle's path, as here, since the longest matching path wins.
- A path under a rule that matches no object gets the HTTP status 404, even when the Site sets `spa`: a rule never
  serves the bundle's index.
- A rule serves a prefix of the Site's own Bucket, or of the Bucket that its `bucket` names, which `buckets` need not
  declare. While that Bucket does not exist, the Site's Route serves nothing, the bundle included, and the Site is not
  `Ready`.
- A rule takes the host, the authentication and the `enabled` setting of the Site's own address.

Files reach the prefix of a rule as any objects of a Bucket do: from the Function that owns the prefix, through
`ctx.blob` or an S3 client, or from a principal that a role allows to write it. The [Bucket](../../services/bucket/index.md)
page shows both clients and the roles.

## Access

A Site's [`ingress.auth.mode`](spec.md#sitespecingressauth) decides who may fetch its files, and its rules follow the
same mode:

- `open`, as in the first example, lets every caller fetch them, with no credential.
- `authenticated` asks each caller for a credential, and what the edge accepts depends on the server setting
  [`server.auth.edge`](../../running-funcd/spec.md#funcdconfigyaml). With its default, `false`, the edge has no
  authenticator, so every request to the Site gets the HTTP status 401, with a message that contains
  `authentication required but no authenticator is configured`, whether it carries a token or not; `funcdctl dev`
  behaves the same way. With `server.auth.edge` set to `true`, a request needs the token of one of the server's
  credentials, in an `Authorization: Bearer <token>` header or an `X-Api-Key` header, and the credential's role must
  allow it to get the Functions of the Site's namespace. A request without a valid token gets 401, and a request whose
  credential may not get those Functions gets 403.
- When the mode is not set, the Namespace's
  [`spec.edgeDefaults.auth`](../../tenancy-and-limits/namespaces/spec.md#namespacespecedgedefaultsauth) decides, else
  the mode is `open`.

Every file of an `open` Site is public, `config.json` included. The Networking page's
[Ask callers to authenticate](../../security-and-networking/networking/index.md#ask-callers-to-authenticate) section
explains the credentials, and its [Access](../../security-and-networking/networking/index.md#access) section the roles
that they need.

## Limits and quotas

- `config.json` holds at most 32 KiB (32768 bytes). `funcdctl apply` refuses `values` that alone pass that size. When
  the ConfigMap takes the file past it, or when the file is larger than the Bucket's `maxObjectBytes`, the Site is not
  `Ready`, with the reason `RuntimeConfigTooLarge`, and its last `config.json` keeps serving.
- A Site's bundles count against the [quota](../../services/bucket/spec.md#bucketspecquota) of its Bucket and of its
  prefix. A file larger than the Bucket's `maxObjectBytes` stops the deployment with the reason `MaterializeFailed`. A
  bundle that does not fit in the quota, or that would take the free space of funcd's data directory below
  `storage.minFreeSize`, stops it with the reason `QuotaExceeded`. funcd retries such a deployment only when the
  Site, its Bucket, the Site's Route or the ConfigMap that `runtimeConfig` names changes, or when funcd restarts; the
  previous bundle keeps serving meanwhile.

The [Spec](spec.md) tab gives the range of each field, and the funcd
[Spec](../../running-funcd/spec.md#funcdconfigyaml) tab describes `storage.minFreeSize`.

## Data storage

A Site keeps its bundles in its Bucket: each bundle in the folder `<prefix>/sha256-<hex>/` of its digest, and its
`config.json` in `<prefix>/_runtime/<site>/`. funcd removes no bundle while the Bucket exists, so the Bucket holds
every bundle that the Site has deployed, and the Bucket's quota counts them all. The Bucket page's [Data storage](../../services/bucket/index.md#data-storage)
section says where funcd keeps a Bucket's objects and whether they survive a restart.

## Status

`funcdctl get site <name> -n <namespace> -o json` prints a Site's status, as the first example shows. The phase is
`Ready` while the condition `Ready` is `True`, and `Pending` otherwise; a Site has no other phase.

The condition `Ready` is `True`, with the reason `Materialized`, once the bundle is in the Bucket and the Site's Route
is set to serve it. `Ready` does not mean that the Site's own address answers: a Site whose address is off, through
`ingress.enabled: false`, or with `ingress.enabled` unset in a namespace whose `defaultExposure` is `explicit`, is
`Ready` too, and its address answers 404. When the condition is `False`, its reason says why:

- `ArtifactNotFound`: funcd cannot find the image, or cannot fetch the bundle at its digest.
- `DigestUnresolved`: the image names no tag and no digest, or names an artifact that is not a Site's bundle, such as
  a Function's.
- `IndexMissing`: the bundle holds no index file: the file that [`index`](spec.md#sitespec) names, else the file that
  the server setting `site.defaultIndex` names, `index.html` by default.
- `MaterializeFailed`: the Bucket refused a file of the bundle, such as a file larger than its `maxObjectBytes`, or the
  bundle holds a `_runtime` entry at its root, a name that funcd keeps for the files that it generates.
- `QuotaExceeded`: the bundle or `config.json` does not fit, as [Limits and quotas](#limits-and-quotas) explains.
- `BucketNotFound`: the Bucket that the Site declares by `ref` does not exist, or does not hold the prefix.
- `StoreNotOwned`: a Bucket of the name that the Site declares by `name` exists, and the Site did not create it. To
  use that Bucket, declare it by `ref`.
- `PrefixOwned`: the Bucket declares the Site's prefix with an owner. A Site writes its bundles into a prefix that has
  no owner.
- `RouteNotOwned`: a Route of the Site's name exists that this Site does not own, such as a Route applied on its
  own.
- `RouteNotReady`: the Site's Route does not serve yet, or cannot serve, such as when a rule's Bucket does not exist or
  another Route already serves the same host and path. The message holds the Route's own reason.
- `ConfigMapNotFound`: the ConfigMap that `runtimeConfig.configMap` names does not exist. The bundle still deploys.
- `RuntimeConfigTooLarge`: `config.json` would be larger than 32 KiB, or than the Bucket's `maxObjectBytes`.

`status.digest` names the bundle that the Site's Route is set to serve, `status.servingPrefix` names its folder in the
Bucket, and `status.objects` and `status.bytes` count its files and their total size, without the files that funcd
generates. Before the first deployment, `digest` is empty; after a deployment that failed, it still names the previous
bundle.

## Changes and deletion

What a change to a Site does depends on the field that changes:

- **A new bundle.** When `image` names another bundle, funcd resolves a tag to its digest, copies the bundle into its
  folder, the index last, and then points the Site's Route at that folder. Until then, the previous bundle serves every
  request. A bundle that fails to deploy, such as a bundle without its index, never serves: the previous bundle keeps
  serving, and the condition `Ready` is `False`.
- **A tag that moves.** funcd resolves a tag when the spec changes, and keeps that digest. A bundle pushed later under
  the same tag is not deployed until the spec changes again, so push each bundle under a new tag, or name its digest in
  `image`.
- **An earlier bundle.** Each bundle that a Site deployed stays in its Bucket, so pointing `image` back at an earlier
  bundle serves it again, with no new copy.
- **A new path.** A change of `ingress.path` moves the Site's Route to the new path, with no new copy of the bundle;
  with `baseHref`, funcd writes a copy of the index whose base is the new path.
- **The configuration.** A change of `runtimeConfig`, or of the ConfigMap that it names, rewrites only `config.json`.
- **The prefix.** The prefix of `bindings.blob` cannot change: a new prefix needs a new Site.

After a new bundle, a browser loads the new index at its next request: the edge answers the index with
`Cache-Control: no-cache`, as it answers `config.json` and the few files that keep their name across bundles, such as
`favicon.ico`, `robots.txt`, `manifest.json` and `sw.js`. A browser may keep any other file for up to 5 minutes, and a
file whose name holds a hexadecimal hash of 8 or more characters, such as `app.3f9a2c1b.js`, for a year, since a
bundler gives a changed file a new hash. The edge answers a conditional request with 304 when the file has not changed.

Deleting a Site deletes its Route soon after, and the Site's address then answers 404. Deleting a Site never deletes
its Bucket: the Bucket stays, with every bundle that the Site deployed and its `config.json`. A Site created again under
the same name that declares that Bucket by `name` is not `Ready`, with the reason `StoreNotOwned`, since the Bucket
still belongs to the deleted Site: declare it by `ref` instead. A Site of an App goes when the App is deleted, and the
App's `deletion` setting decides whether its Bucket goes too: with `delete`, as in the first example, funcd deletes the
Bucket and every bundle in it. The Bucket page's
[Changes and deletion](../../services/bucket/index.md#changes-and-deletion) section explains how to delete a Bucket.

## Errors

A browser or another client of a Site meets these HTTP statuses:

- **404** with a message that contains `no route for` when no Site or Route answers on that host and path, such as
  before the Site's first bundle serves, while its own address is off, after the Site is deleted, or on a host that the
  Site does not answer.
- **404** with a message that contains `no such object` for a path that matches no file, under a Site without `spa` or
  under a rule.
- **405** for a method other than GET and HEAD, with the header `Allow: GET, HEAD`.
- **401** or **403** when the Site asks for authentication, as [Access](#access) explains.

`funcdctl apply` refuses a Site:

- whose `buckets` does not hold exactly one entry, the Bucket that `bindings.blob` names, with a message that contains
  `spec.buckets must declare exactly the one Bucket spec.bindings.blob names`;
- whose Bucket entry sets `deletion: delete`, with a message that contains
  `spec.buckets[0].deletion "delete" is not implemented`;
- whose Bucket entry by `ref` lists `prefixes`, since a Site never changes a Bucket that it refers to;
- whose rule has the path `/`, the bundle's path, the path of another rule or the path of `config.json`;
- whose `runtimeConfig.values` alone make `config.json` larger than 32 KiB;
- whose prefix differs from the prefix that the Site was created with, with a message that contains
  `spec.bindings.blob[0].prefix is immutable`.

## Run it locally

`funcdctl dev` differs from a funcd server in these ways for a Site:

- It needs Node.js, or Python 3.12 or later as `python3`, on the `PATH`, or the interpreter that `FUNCD_NODE` or
  `FUNCD_PYTHON` names, even when it runs no Function. Without one, it stops at its start with a message that contains
  `no runtime found on PATH`.
- It packs a Site's bundle only for a Site of an App template, from the folder that `--build <key>=<folder>` names.
  When a file of that folder changes, it packs and pushes the folder again, and the Site deploys the new bundle. When
  the folder is empty or holds no index at that moment, as while a build tool writes it, the Site keeps its last
  bundle, and `funcdctl dev` prints a line that ends with `keeping its last build`.
- For an App template, with `dev.liveReload: true` in the `funcdctl.config.yaml` file of the current folder, it reloads
  the open pages of a Site after each new bundle.
- It runs a folder of resource files only when the folder holds a Function or a Workflow image step, so a folder that
  holds only a Site does not run: `funcdctl dev` stops with a message that contains
  `holds no Function or Workflow image step to run`. Beside a Function, it applies a Site as it is written and packs
  nothing for it, so the Site's `image` must name an artifact that funcd can pull. The Sites `bi` and `docs` of the
  s3-roundtrip example name a registry of the test platform that runs them, so under `funcdctl dev` they stay
  `Pending`, with the reason `ArtifactNotFound`.
- It runs the namespace `default` only.
- Its edge has no authenticator, as on a server whose `server.auth.edge` is `false`, so a Site whose
  `ingress.auth.mode` is `authenticated` answers 401 to every request, with a message that contains
  `authentication required but no authenticator is configured`.

Without the `--persist` flag, `funcdctl dev` keeps the Bucket's files in memory, so the bundles that the Site deployed
are lost when it stops. It keeps the bundles that it packed, and a record of the processes that it starts, in a state
folder of its own, `funcd/dev/<id>` in the user's cache folder, such as `~/.cache` on Linux or `~/Library/Caches` on
macOS, where `<id>` stands for the template folder. With `--persist`, it keeps its state and the Bucket's files between
runs in `.funcd-dev`, a folder that it creates in the current folder, or in the folder that `--persist-to` names. To
start again from nothing, delete `.funcd-dev` and the state folder after `funcdctl dev` stops. The
[reference of `funcdctl dev`](../../tools/funcdctl/spec.md#funcdctl-dev) lists its flags and the keys of
`funcdctl.config.yaml`.

## Examples

- [site-config](https://github.com/pyvvo/funcd-functions/tree/main/apps/site-config): one bundle of a web app,
  deployed with its own `config.json` in each environment, and served at a path with its base.
- [s3-roundtrip](https://github.com/pyvvo/funcd-typescript/tree/main/examples/s3-roundtrip): two Sites over the
  prefixes of one Bucket, one on a host with a rule that serves the prefix that a Function writes, the other at its
  default path, `/site/docs`.
