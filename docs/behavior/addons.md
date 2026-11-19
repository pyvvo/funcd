# Addons

Status words and how to read this: [the index](README.md).

Kinds: Addon, AddonObject.

```mermaid
stateDiagram-v2
    [*] --> Pending : applied
    Pending --> Pending : held, or the engine does not answer its readiness path yet
    Pending --> Ready : the engine answers 200
    Ready --> Pending : its Addon goes or no longer fits, a Secret goes or is forbidden, or a probe fails
    Ready --> Stopping : deleted or held, or a changed Secret, Addon or object, or an in-place Restore, replaces the engine
    Pending --> Stopping : deleted or held
    Stopping --> Pending : a new engine starts
    Stopping --> [*] : deleted, or funcd stops
```

An admin's Addon registers a kind and the engine image of its objects; each object of that kind runs one engine on the add-on
runtime, as a Lake's does, and is stored as an AddonObject. A held object has no engine and no edge entry. `Stopping` is no phase: a
status keeps its last one until the pass ends. An in-place Restore's two engine starts:
[restore-start](../design/addon/restore-start.md). New words: Stopping.

| Where | What happens | Time | You see | Status | Source |
|---|---|---|---|---|---|
| An Addon's kind | Only an admin reads or writes an Addon. A kind of funcd's, or whose plural funcd serves or another Addon has, is refused without case; the kind is kept | none | 403; 400 `is a kind of funcd's own`, `cannot change`; 409 | **differs** | ADR-0332 Decision 1 says 422 for a kept kind, ADR-0148 Decision 1 400; `TestScenarioAddonRegisterAdminOnly`, `TestAddonKindRules` |
| An Addon's image | Pinned by digest, from a registry that `addons.registries` lists, the image's host; the empty default refuses every Addon. A tag fails the schema | none | 422 at `body.spec.image`; 400 `does not come from a registry that addons.registries lists` | **differs** | the scenario addon-image-pinned says 422 for both, ADR-0148 Decision 1 400; `TestScenarioAddonImagePinned` |
| An Addon's config and env | The schema passes the contract profile and compiles; each env value is a literal or one `${{ }}` expression over config that the schema types | none | 400 naming `spec.config` or the env key | **differs** | ADR-0332 Decisions 1 and 4 say 422; `Invalid` is 400 by ADR-0148 Decision 1; `TestScenarioAddonEnvChecked` |
| An Addon's runtimes | containerd alone, the default: the process runtime runs no image | none | 422 at `body.spec.runtimes` | decided | ADR-0332 Decision 1, `TestScenarioAddonRuntimeUnavailable` |
| An Addon changed or deleted | Takes no lock and checks no object: a new schema applies to the next writes, each object of its kind gets one new engine, and its objects outlive it | the next pass | an object that no longer fits waits | decided | ADR-0332 Decisions 1 and 3, `TestScenarioAddonConfigInvalid`, `TestScenarioAddonRemoved` |
| An object of an add-on's kind | At its kind's plural, a namespaced kind's verbs; one name is one object per namespace, whatever its kind. With no Addon of the kind, create and replace are 404; list, get and delete serve what is stored | none | 404; 409 for a name of another kind | decided | ADR-0332 Decision 2, `TestScenarioAddonKindUnregistered`, `TestScenarioAddonSecondKind` |
| An object's config | Checked against its Addon's schema when it is written | none | 422 naming the property under `body.spec.config` | decided | ADR-0332 Decision 2, `TestScenarioAddonConfigSchema` |
| An object that cannot run | Pending, with no engine or edge entry, while its Addon is gone or does not fit, its env fails, or a Secret is missing or forbidden | a lifted forbid, `controller.referentPollInterval` 2 s | `AddonNotFound`, `ConfigInvalid`, `EnvInvalid`, `SecretNotFound`, `BindingResolveFailed` | decided | ADR-0332 Decisions 3 and 4, `TestAddonSecretForbidden` |
| An object on a process server | Its Addon runs on containerd only, which the process runtime does not run; under funcdctl dev the add-on runtime runs containerd's | none | `Pending`, `RuntimeUnavailable`, naming both modes | decided | ADR-0332 Decision 3, `TestScenarioAddonRuntimeUnavailable` |
| An object's engine | One replica of its Addon's image, port, probe and storage, with the object's resources, fenced with no endpoint | until its readiness path answers 200 | `Ready`; else `Pending`, `EngineNotReady`, or `EngineConvergeFailed` when it cannot start | decided | ADR-0332 Decision 3, `TestScenarioAddonObjectRuns` |
| Who reaches an engine | funcd alone: the fence admits new connections from loopback and the bridge address, and the egress gateway refuses an engine's address whatever rule permits it, from before the engine starts | none | a refused connection | decided | ADR-0332 Decision 3, `TestDecideRefusesAnAddonEngine`, `TestAddonRuntimeIndexesEachEngineBeforeItStarts` |
| An object's environment | Its Addon's env over its config, a non-string as its JSON, and only the `secretKeys` of its bound Secrets, read by Cedar as the object; a changed Secret, Addon or object replaces the engine | the next pass | the engine's environment | decided | ADR-0332 Decision 4, `TestScenarioAddonSecretKeys` |
| An object's edge entry | On `ingress.host` under its Addon's basePath, kept, ranked after a Lake's; its stance is its own `ingress.auth`, open when unset, never the namespace's default. A path or the authenticated mode is refused | once Ready | `IngressReady`; 400 | **differs** | ADR-0332 Decision 5 says 422 for authenticated; `Invalid` is 400 by ADR-0148 Decision 1; `TestAddonEntryStance` |
| An object behind an `oidc` stance | Its own `ingress.auth` signs callers in through an OIDCProvider of its namespace, with `ingress.host` in `default`; while the provider is missing, the entry keeps its claim, refused | until the provider exists | `IngressReady=False`, `ProviderNotFound`; 503 | decided | ADR-0335 Decision 10, `TestAddonObjectProviderNotFound` |
| An Addon's basePath at or under `/.auth` | Refused, since the edge answers `/.auth/` on every host | none | 400 naming `spec.basePath` | **differs** | ADR-0335 Decision 3 says 422, `TestAddonValidate` |
| A private path at the edge | 404 however it is spelled, and so is a path with a `..` segment or whose cleaned form leaves basePath; funcd reaches a private path at the engine's own address | none | 404 | decided | ADR-0332 Decision 5, `TestScenarioAddonPrivatePath` |
| Deleting an add-on object | Once its engine is gone, `deletion: delete` removes its storage, and `retain`, the default, moves it into the quarantine, from which it comes back by hand with funcd stopped; neither while held | `storage.quarantineRetention` 168 h | `funcdctl hold status` lists the entry | decided | ADR-0333 Decision 5, `TestScenarioAddonDeletion` |
| A start | Not held, it quarantines the storage of each add-on object no longer stored, whatever its deletion, so a new one of that name starts empty; held, it lists them | the start | `moved the data of an AddonObject with no object into the quarantine`; `addonOrphans` | decided | ADR-0333 Decision 5, ADR-0231 Decision 4, `TestEveryReclaimHasHoldEvidence` |
| A crash before a restore start | hiqlite cannot remove its crash marker, so the engine never starts with `restore.env`: the Restore times out, and its clear restarts the engine without it | `restore.timeout` | `RestoreTimedOut`; the namespace stays held | accepted | ADR-0333 Consequences, `TestScenarioAddonRestoreTimeout` |
| A registry taken out of `addons.registries` | Stops no engine: the reconciler reads no registry list, and the Addon's next change is refused | none | 400 on the next change | decided | ADR-0332 Decision 6, `TestAddonRegistryRemovedStopsNoEngine` |

<!-- generated values, do not edit: just behavior-values -->
<details>
<summary>Addons: values</summary>

| Value | Default | From |
|---|---|---|
| `addons.maxObjectsPerNamespace` | 20 | config |
| `addons.registries` | [] | config |
| `defaultPollInterval` | 2s | `internal/services/addon/addon.go` |

</details>
<!-- end of generated values -->
