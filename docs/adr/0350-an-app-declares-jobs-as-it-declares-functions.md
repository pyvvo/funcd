# ADR-0350: An App declares Jobs as it declares Functions

- **Status**: Implemented (2026-11-24), review passed with one Minor; Accepted (2026-11-23) on the decider's go, after two judge rounds with every Blocker and Major folded
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: apps, jobs, templates
- **Realizes**: [FEAT-0012/F271](../feat/0012-feat-infrastack.md) (Jobs in Apps)
- **Relates to**: [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) (the Job) ·
  [ADR-0199](0199-app-resource.md) (the App and its parts) · [ADR-0200](0200-app-revisions.md) ·
  [ADR-0212](0212-app-self-heal-and-pause.md) · [ADR-0217](0217-app-templates-values-and-rendering.md) ·
  [ADR-0218](0218-app-template-images-lock-and-push.md) · [ADR-0337](0337-an-app-template-carries-its-add-on.md) ·
  [ADR-0345](0345-a-function-reconciles-an-engineless-kind-and-a-deletion-waits-for-its-destroy.md) ·
  ADR-0349 (a Function part binds a Job part)

## Context & Need

An App is the unit an owner installs, upgrades and removes, and the InfraStack add-on's template holds one Job per
stack (F141). F271 decided that an App declares Jobs next to its Functions, so a template carries the Jobs its parts
need. #1015's input (2026-11-23): an App's `jobs` section holds Jobs as its `functions` section holds Functions.

## Scenarios

- `scenario: app-declares-job`: Given an App whose `jobs` declares `migrate` and whose `buckets` declares `state`;
  when it is applied; then the Job `migrate` exists, binds `state`, and the App is `Ready` once the Job is.
- `scenario: app-job-runs-keep-their-revision`: Given a `Running` run of `migrate`; when an upgrade changes its image;
  then the run ends on its revision, the next run takes the new one, and the upgrade waits for no run.
- `scenario: app-job-self-heals`: Given the App is `Ready`; when someone edits the Job `migrate` by hand; then the App
  writes its entry back.
- `scenario: app-delete-removes-job`: When the App is deleted; then `migrate` is deleted, a run of it not ended ends
  `Cancelled` with `JobDeleted`, and its runs stay listed until `job.retention`.
- `scenario: app-dev-builds-job`: Given `funcdctl dev` on an App whose `migrate` entry builds from local source; when
  the source changes; then dev rebuilds the image and the next run takes the new revision.
- `scenario: app-template-pins-job-image`: Given a template whose `jobs` entry names an image by tag; when `funcdctl
  app lock` runs; then `app.lock` pins that image's digest, and the rendered Job carries it.

## Scope

In: the `jobs` section, its entries' rules, the App's rollout, self-heal, readiness and deletion of Job parts,
templates' `jobs`, and `funcdctl dev`. Out: a run as an App part (runs are started by people and Functions); an App
hook that starts a run (a hook's Function may, through ADR-0349); stacks in Apps (F145); a Job part's local volumes in
the App's backup, since a backup member is a Function's volume, archived as `<function>/<volume>` (ADR-0262 Decision
11), and a Job's would restore into a same-named Function's.

## Constraints & Decision drivers

- A Job part behaves as a Function part wherever the two share a rule (#1015; ADR-0347's "reuse what Functions have").
- A waiting or long run never holds or fails an App's rollout (F145's rule, which this ADR must not break).

## Alternatives considered

- **Jobs only through templates' plain manifests**, outside the App: no self-heal, no cascade, no lock. Rejected.
- **Runs as App parts**: a run is an execution, not desired state, and would run again at each App revision. Rejected.

## Decision

1. **The section.** `App.spec.jobs` holds entries of `name` (a Job the App declares) or `ref` (an existing one),
   inline with a Job's spec, as `functions` holds Functions, and comes right after `functions` in the App's section
   order. No two entries name the same Job. An entry leaves `kv`, `buckets`, `sql`, `lakes`, `configMaps` and
   `secrets` empty, and each store, Lake, ConfigMap and Secret it binds is one the App's lists declare, as for a
   Function entry.
2. **The parts.** The App reconciler applies, writes back (ADR-0212) and prunes a Job part as it does a Function part:
   this ADR adds the pair `{App, Job}` to `gc.Pairs`, from which `sectionKinds` takes the kind. A Job part is `Ready`
   when its Job is (ADR-0347 Decision 2); its runs count for nothing in the App's readiness or its upgrade, which
   never waits for one.
3. **An upgrade.** A changed entry updates the Job, which stamps a new revision; runs not ended keep their pin
   (ADR-0347 Decision 2), so an upgrade never cuts a run.
4. **A deletion.** Deleting the App deletes its Job parts, with ADR-0347 Decision 6's rule for their runs. An App with
   engineless parts keeps its Jobs until those parts are gone (ADR-0345), as it keeps its Functions.
5. **Templates.** A template's `jobs` renders as its `functions` does (ADR-0217); `funcdctl app lock` and `push` pin
   and carry a Job entry's image as a Function entry's (ADR-0218). A Function part's `bindings.job` names a Job part
   by its name (ADR-0349).
6. **Dev.** `funcdctl dev` builds, runs and rebuilds a Job entry's local-source image as it does a Function entry's
   (ADR-0253, ADR-0254).

## Design model

None. A Job part takes the App's existing part lifecycle with no lock, timer or stop path of its own; a run's own
lifecycle is ADR-0347's model.

## Behavior rows

- **apps.md**: the `jobs` section, a Job part's readiness and its runs' independence from the rollout.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/app.go: AppSpec gains Jobs []AppJob `json:"jobs,omitempty"`.
type AppJob struct {
	Name    ObjectName `json:"name,omitempty"`
	Ref     ObjectName `json:"ref,omitempty"`
	JobSpec `json:",inline"`
}
```

| Dependency & I/O | Direction | Shape |
|---|---|---|
| `spec.jobs` | consumes | entries as `functions`' |
| `{App, Job}` | gc pair | the cascade of an App's Job parts |
| `app.lock` | writes | a Job entry's image digest |

## Implementation plan

1. `api/types/v1alpha1/app.go`: `AppJob`, `Jobs` and `App.Parts`, their checks beside `functions`'.
2. `internal/app`: `entries` (after `functions`), the readiness verdict of a Job part, admission's rules for an entry
   and `undeclaredSecret`'s walk; `internal/gc`: the pair `{App, Job}`. `internal/app/template`:
   render, lock and push a Job entry's image. `cmd/funcdctl/dev_app.go`: a Job entry's local image.
3. Docs: the row; the App guide page.

Tests, each written to pass: the entry checks; one `TestScenario…` per scenario in `pkg/funcd`'s e2e suite; the
template's lock test. Done when `just ci-full` passes with ADR-0347 implemented.

## Review checklist

- [ ] Every scenario has its passing `TestScenario…`.
- [ ] A Job part follows a Function part's rules for entries, self-heal, pruning and the lock.
- [ ] No run counts in an App's readiness or upgrade.

## Consequences

- An App template can carry the Jobs its parts start, so the InfraStack add-on's template holds a Job per stack.
- An App's deletion cancels its Jobs' runs not ended, so a reinstalled App of the same name never runs them (ADR-0347
  Decision 6); an owner who wants one to finish waits before deleting.

## Open questions

None.

## References

- Feature issue #1534 (FEAT-0012 F271), under tracker #1028; #1015's inputs of 2026-11-23.
- ADR-0347, ADR-0349, ADR-0199, ADR-0200, ADR-0212, ADR-0217, ADR-0218, ADR-0337, ADR-0345.
