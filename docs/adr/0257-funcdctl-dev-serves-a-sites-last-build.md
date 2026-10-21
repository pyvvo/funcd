# ADR-0257: `funcdctl dev` serves a Site's last build while its build cannot be pushed

- **Status**: Accepted (2026-10-21), after two judge rounds, under the decider's "just file it, then ADR, and ship it"
  and choice A recorded on #1137; the line's wording and the index check in reloads without `watch` are the drafting
  agent's choices, briefed before acceptance. The judge kept the first deploy out, declared the change for runs without
  `watch`, and kept the Site's last pushed image as its last.
- **Date**: 2026-10-21
- **Deciders**: green-0-rabbit
- **Tags**: dx, cli, dev, site
- **Realizes**: [FEAT-0001/F168](../feat/0001-feat-v1.1.md) (#1137, tracker #1062)
- **Supersedes (in part)**: [ADR-0253](0253-funcdctl-dev-runs-an-app-template.md) Decision 3, where a reload tries to
  push an empty Site folder or pushes an index-less one, and Decision 9, where a reload whose Site build cannot be
  pushed fails as a whole, so the other parts' changes wait for the Site.
- **Relates to**: [ADR-0255](0255-funcdctl-dev-runs-the-dev-loop.md) (the wait at start under `watch`),
  [ADR-0256](0256-funcdctl-dev-prints-output-that-led-to-no-build.md) (a watch build's error in the terminal)

## Context & Need

Vite 5 empties a Site's output folder before a rebuild, also one that then fails (experiment 0001); Vite 8 fails
before it empties. Dev's next reload then pushes an empty folder, `artifact.PushSite` refuses it
(`internal/artifact/site.go:42-44`), and under ADR-0253 Decision 9 the whole reload fails: dev prints `reload failed`,
and a function changed meanwhile waits until the Site builds again. The decider chose, on 2026-10-21, that dev keeps
the last good Site and still reloads the other parts, rather than waiting (#1137).

## Scenarios

Fixture: the todo App with `--build web=web/dist` and `liveReload: true`, its Site built, dev running without `watch`.

- **scenario: dev-site-keeps-last-build** — *When* `web/dist` is emptied and a function's build changes, *Then* dev
  prints `images.web: <dir> is empty; keeping its last build`, the function's change serves, the Site serves its last
  build, and open pages get no reload event.
- **scenario: dev-site-no-index** — *When* `web/dist` holds files but no index, *Then* dev prints `images.web: <dir>
  holds no index.html; keeping its last build` and the Site serves its last build.
- **scenario: dev-site-builds-again** — *When* `web/dist` holds a build again, *Then* `pushed images.web` appears and
  the new build serves.

## Scope

- **In**: a Site's build during a reload.
- **Out**: function and step builds, whose tools keep the last good bundle (ADR-0244); the start; folder mode.

## Constraints & Decision drivers

- A broken Site must not block another part's change (the decider).
- Dev never serves a Site with nothing in it or without its index.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| Chosen: keep the Site's last pushed image | The other parts reload; the Site serves what last worked | The Site lags its sources until it builds again; a line says so | Chosen by the decider |
| A. Wait until the Site builds again | Simple | The other parts' changes wait; saves look lost | Rejected by the decider |
| B. Fail the reload, as today | No change | Blocks the other parts; `reload failed` on each save | Rejected |

## Decision

1. **A Site build that cannot be pushed.** In a reload after the first deploy, with or without `watch`, a Site's
   build cannot be pushed when `--build` names no folder for it, or its folder is absent, empty or holds no index file
   (the checks ADR-0255 Decision 2 makes at start under `watch`), or when its push fails (`artifact.PushSite`, or the
   copy that live reload tags). Dev no longer pushes an empty or index-less Site folder in a reload.
2. **Its last build serves.** When dev pushed an image of that Site earlier in the run, the reload locks that image's
   digest, leaves it as the Site's last pushed image, prints `images.<key>: <reason>; keeping its last build`, and goes
   on with the other parts. Nothing is pushed for the Site, so its part of the revision is unchanged and live reload
   sends no event.
3. **No earlier image.** Without an earlier image of the Site in the run, a Site part the template added after the
   start included, the reload fails as ADR-0253 Decision 9 says. The first deploy keeps ADR-0253's push and ADR-0255
   Decision 2's wait.
4. **Builds again.** A Site build that can be pushed is pushed as before, printing `pushed images.<key> …`.

## Temporary workarounds

None.

## Contracts

- **Line**: `images.<key>: <reason>; keeping its last build`, on each reload that keeps it. `<reason>` is worded as the
  start's waiting line words it (`<dir> is absent`, `<dir> is empty`, `<dir> holds no <index>`), or is the refusal's
  message.
- No key, no flag, no state file.

## Implementation plan

- **funcd**, one PR closing #1137 (`Fixes #1137`):
  - `cmd/funcdctl/dev_app.go`: in a reload, `prepare` checks a Site's folder with `siteHoldsIndex`; on that miss, a
    refused folder or a failed push of a Site with an image in `s.images`, it locks that image's digest, leaves
    `s.images[key]` as it was and prints the line.
  - Tests (`-tags dev`): one per scenario, named after it; a unit test of Decision 3, a Site the template adds after
    the start with a broken folder.
- **Done when** `go build`, `go vet`, `golangci-lint` and `go test` pass with `-tags dev` and without.

## Review checklist

- [ ] In a reload, with or without `watch`, a Site build that cannot be pushed keeps the last pushed image, which
      stays the last, prints the line, and the other parts reload; no live reload event goes out.
- [ ] A Site with no earlier image fails the reload as before; the first deploy is unchanged.
- [ ] No reload pushes a Site that is empty or holds no index; one that builds again pushes as before.
- [ ] One test per scenario, named after it.

## Consequences

- **Positive**: a broken Site no longer blocks a function's change, and the Site keeps serving what last worked.
- **Negative**: the served Site lags its sources until it builds again, and the terminal says so on each reload; a
  good rebuild that leaves its folder without an index for longer than the reload's settle (two polls) prints the line
  once before its push; a template change that renames the Site image's repository in that reload fails the
  revision.
- **Risks accepted**: a Site whose index is gone for good serves its last build until dev restarts; the build kept is
  the last one pushed, so when that one never served (an index-less first build without `watch`, or one its
  reconciler refused), the Site still does not serve and the revision still fails on it until the Site builds.

## Open questions

None.

## References

- ADR-0253 Decisions 3 and 9; ADR-0255 Decision 2; `cmd/funcdctl/dev_app.go` (`prepare`, `push`, `siteHoldsIndex`);
  `internal/artifact/site.go:42-44`; experiment 0001 (Vite 5 empties `dist` before a failing rebuild).
