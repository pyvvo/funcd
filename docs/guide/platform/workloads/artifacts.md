---
title: Artifacts
description: How a Function's code reaches funcd as an OCI artifact, pinned by its digest.
---

![funcdctl push packages the code and its contract file as an OCI artifact in a registry or an OCI layout. A Function names it in spec.image, its Revision pins the digest, and funcd pulls the artifact by that digest into its cache before a worker runs the code](../../assets/artifacts.drawio.svg)

A Function's code reaches funcd as an OCI artifact. `funcdctl push` packages a handler file or a bundle directory
and pushes it to an OCI registry or to an OCI layout on disk. The artifact holds the code as one layer and the
contract as a second layer, and it records the runtime. The push reads the runtime and the contract from the
build's contract file, such as `hello-world.contract.yaml` beside `hello-world.mjs`, and it prints
`<ref>@<digest>`:

```sh
funcdctl push hello-world.mjs registry.example.com/team/hello-world:1.0.0
```

A Function names its artifact in `spec.image`, by a registry reference or by an OCI layout reference such as
`oci-layout:///srv/layout:hello-world`, with a tag or a digest:

```yaml
apiVersion: funcd.io/v1alpha1
kind: Function
metadata:
  name: hello-world
  namespace: default
spec:
  runtime: nodejs22
  handler: handle
  image: registry.example.com/team/hello-world:1.0.0
```

When the Function's spec changes, its reconciler creates a Revision for the new generation. It resolves the tag to
a digest at that moment, once, and pins the digest in the Revision, which the API does not let anyone edit. A tag
that moves later changes nothing that runs until the next change of the Function's spec. A reference with a digest
is pinned as it stands.

funcd pulls the artifact by the pinned digest and checks the content against it before a worker starts. It keeps
the code in its artifact cache under `storage.dataDir`, one entry for each digest and platform, so a worker that
starts again reads the cache. A tag that funcd cannot resolve leaves the Function `Failed` with the reason
`ArtifactUnresolved`, and funcd tries again every `runtime.supervisionPeriod`. An artifact that the runtime cannot
start, such as one without a contract, fails when its first worker starts, with `ShapeInvalid`, `ContractMissing`
or `ContractInvalid`.

## Several platforms

An artifact can carry one build for each platform. `funcdctl push --platform` records the platform of a build,
`linux/amd64` or `linux/arm64`, and `funcdctl index` combines the builds into one OCI image index, which the Function
then names:

```sh
funcdctl push dist/amd64 registry.example.com/team/fetch:amd64 --platform linux/amd64
funcdctl push dist/arm64 registry.example.com/team/fetch:arm64 --platform linux/arm64
funcdctl index registry.example.com/team/fetch:1.0.0 \
  registry.example.com/team/fetch:amd64 registry.example.com/team/fetch:arm64
```

funcd pulls the build for its node's platform. When the artifact lists platforms and none of them is the node's,
funcd starts no worker, and the Function is `Failed` with the reason `NoMatchingPlatform`.

## Registries

Any OCI registry holds artifacts, and an OCI layout on the server's disk holds them with no registry at all.
`funcdctl login <registry> -u <user> -p <password>` stores a registry's credentials in the Docker credential store of
the user who runs it, and `funcdctl logout <registry>` removes them. funcd reads the same store when it pulls, so on
a funcd server the login is made as the user that runs funcd: root, for the systemd unit that `funcd install`
writes. An OCI layout needs no login.

`funcdctl inspect <ref>` prints an artifact's contract from its metadata, without pulling the code.
`funcdctl pull <ref> <digest> [dir]` fetches the code by its digest, checks it, and writes it to a directory.

## Sites and App templates

A Site's bundle and an App template are OCI artifacts too. `funcdctl push --site <dir> <ref>` packages a built
static web app as one tar and gzip layer, with no contract and no runtime, for a [Site](site.md).
`funcdctl push --template <dir> <ref>` pushes an [App](app.md) template, tagged with its version, once its
`app.lock` is fresh. `funcdctl app lock` resolves each image that the template names to a digest and writes
`app.lock`, so a rendered App names each image by its digest.
