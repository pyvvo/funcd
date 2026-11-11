---
title: Site
description: A static web app, served from a Bucket through its own Route.
---

![funcdctl applies a Site; the Site reconciler resolves its bundle image to a digest, copies the bundle into the Site's Bucket with the index last and writes the Site's Route; a browser's request goes through the edge and the Route to the Bucket's files](site.drawio.svg)

A Site serves a static web app. Its spec names the bundle, an OCI image, and the Route the Site owns. The Site
reconciler resolves the image to a digest once for each change of the spec, copies the bundle into the Site's Bucket
under a prefix for that digest, writing the index last so that its presence means the copy is complete, and writes the
Site's Route.

The edge serves the files straight from the Bucket, with no worker: it resolves the index, sets the content types and
answers conditional requests. A deploy that fails leaves the previous digest serving, and the Site's status comes from
its Route. With `spa`, an unmatched path serves the index, so a client-side router owns routing, and `runtimeConfig`
adds a generated `config.json` beside the bundle, so each deployment sets its values without a rebuild.
