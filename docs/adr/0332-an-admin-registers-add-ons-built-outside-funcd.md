# ADR-0332: An admin registers add-ons built outside funcd, and users declare their objects

- **Status**: Proposed
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, provider, api, edge, cedar
- **Realizes**: [FEAT-0018/F258](../feat/0018-feat-addons.md) (add-ons built outside funcd)
- **Relates to**: [ADR-0087](0087-add-on-provider-runtime.md) (the add-on runtime it runs engines on),
  [ADR-0231](0231-durable-storage-for-add-on-engines.md) (engine storage),
  [ADR-0095](0095-reference-engine-typed-paths-predicates.md) (`${{ }}` expressions),
  [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) (upstream edge entries),
  [ADR-0149](0149-runtime-availability.md) (`RuntimeUnavailable`),
  [ADR-0269](0269-a-provider-engine-reaches-only-its-one-internal-endpoint.md) (the engine's egress fence),
  [ADR-0321](0321-cedar-decides-each-secret-a-workload-reads.md) (Cedar decides each Secret read)

## Context & Need

funcd's add-ons are built into its repository: the Lake and the SQL database each have a kind, a reconciler and an
image here, and `funcd.New` fixes the provider list (`pkg/funcd/funcd.go`). An engine funcd does not ship, such as
rauthy, cannot become an add-on without a funcd release. This ADR lets an **admin register an add-on** on a running
funcd: an engine image built elsewhere and the namespaced kind it adds. Users then declare objects of that kind, which
funcd checks against the add-on's schema and runs as it runs its own engines. Its callers: an admin with `funcdctl
apply`, users of the namespaces, and the edge.

## Scenarios

- `scenario: addon-register-admin-only`. *Given* the Addon `rauthy`, *When* a developer applies it, *Then* it is
  refused 403; *When* an admin applies it, *Then* `funcdctl get identityproviders` works in every namespace.
- `scenario: addon-image-pinned`. *Given* an Addon whose image is `ghcr.io/pyvvo/funcd-rauthy:0.1.1`, or is pinned by
  digest in a registry `addons.registries` does not list, *When* an admin applies it, *Then* it is refused 422 naming
  the rule.
- `scenario: addon-object-runs`. *Given* the Addon and a Secret `rauthy-keys`, *When* a user applies the
  IdentityProvider `login`, *Then* it turns `Ready` once the engine's readiness path answers 200, and `GET
  https://login.example.com/auth/v1/ping` reaches the engine with its path unchanged.
- `scenario: addon-config-schema`. *When* a user applies an object whose `spec.config` breaks the Addon's schema,
  *Then* it is refused 422 naming the property.
- `scenario: addon-private-path`. *When* a caller sends `POST /auth/v1/clients_dyn` to the object's host, *Then* the
  edge answers 404, and funcd still reaches that path at the engine's own address.
- `scenario: addon-runtime-unavailable`. *Given* an Addon with `runtimes: [process]` on a containerd server, *Then* its
  objects stay `Pending`, `RuntimeUnavailable`, naming both modes.
- `scenario: addon-secret-keys`. *Given* the Secret `rauthy-keys` with a key the Addon's `secretKeys` does not list,
  *Then* the engine's environment holds only the listed keys; *When* the Secret changes, *Then* the engine is replaced.
- `scenario: addon-second-kind`. *Given* the Addon `whoami` (kind `EchoService`, no storage, no config), *Then* its
  objects run with no code for them, beside the IdentityProvider.

## Scope

In: the `Addon` kind; objects of an add-on's kind (API, store, SDK, funcdctl); running their engines; their Secrets;
their edge entry; the image rule. Out: an add-on's backups (F259, [ADR-0333](0333-an-add-on-objects-data-in-funcds-backups.md)),
`funcdctl dev` (F260, [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md)), the edge sign-in (F261, F262), the
`pyvvo/funcd-addons` repository's layout, an add-on's egress list (Open questions), and add-ons whose code runs inside
funcd (FEAT-0018 Out of scope).

## Constraints & Decision drivers

- One provider runtime: an add-on engine runs exactly as a Lake engine does (ADR-0087, ADR-0231), so every runtime
  mode, the stop path and the boot sweep apply unchanged.
- funcd's set of kinds stays fixed: the code, the OpenAPI spec and the Cedar schema name no add-on's kind.
- Only an admin adds code to a server, and only from registries the operator lists.
- A user's object is checked at apply, as a contract is (ADR-0058's JSON Schema profile).

## Alternatives considered

- **A kind per add-on compiled into funcd** (today's Lake and SQL). Rejected: it needs a funcd release per engine,
  which is the problem.
- **A generic `AddonInstance` kind with `spec.addon`**. Simpler to route, but a user writes `kind: AddonInstance` and
  `funcdctl get` cannot list identity providers by name; the decider chose the add-on's own kind (card 1, B).
- **Kubernetes-style CRDs with a validating webhook**. Rejected: a webhook is code outside funcd that admission must
  call, and the schema alone covers what rauthy needs.
- **Images by tag, or from any registry**. Rejected: a tag can move under a running server, and an admin's typo would
  pull from a registry nobody vetted (card 2).

## Decision

1. **The `Addon` kind**, cluster-scoped; only an admin creates, changes or deletes one. Its spec names the kind it
   adds (`^[A-Z][A-Za-z0-9]{1,62}$`, not a funcd kind, registered by no other Addon) and its engine: `image`, pinned by
   digest (`<repository>@sha256:<64 hex>`) in a registry of `addons.registries`; `runtimes`, the `runtime.mode`
   values it runs on (default `[containerd]`); `port`; `basePath`; `privatePaths`; `readiness.path`;
   `storage` (path, uid, gid, as ADR-0231); `config`, the JSON Schema of its objects' `spec.config` (ADR-0058's
   profile); `env`; and `secretKeys`. While objects use an Addon, its `kind` cannot change, a `config` that one of
   them breaks is refused, and a delete is refused: 409 names them.
2. **Its objects** are namespaced, at `/apis/funcd.io/v1alpha1/namespaces/{ns}/{plural}`, the plural being the kind
   in lower case with an `s`. funcd stores each as the internal kind `AddonObject` (`spec.kind`, `spec.addon`), so
   one name is one object per namespace across add-on kinds. The spec holds `config`, `secrets`, `ingress`,
   `resources` and `deletion` (F259). The API refuses an object whose config breaks the schema, 422, and one of a
   kind no Addon registers, 404. funcdctl and the SDK resolve an unknown kind to its plural, with no code per kind.
3. **The engine.** One reconciler runs every object through the add-on runtime (ADR-0087), one replica, with the
   Addon's image, port, readiness and storage and the object's resources, and ADR-0269's fence with no endpoint: the
   engine reaches nothing. An object whose Addon's `runtimes` exclude the server's mode stays `Pending`,
   `RuntimeUnavailable` (ADR-0149's reason). `status.address` is the engine's own address, which only funcd uses.
4. **The environment.** Each `env` value is a literal or one `${{ }}` expression over `config` (ADR-0095), checked
   against the schema when the Addon is applied and evaluated per object: a failure is `EnvInvalid`. The keys of the
   object's Secrets that `secretKeys` lists are laid over it; no other key reaches the engine. Cedar decides each read
   with the object as the principal (`funcd::AddonObject`), as ADR-0321 decides a Lake's. A changed Secret, Addon or
   object replaces the engine.
5. **The edge.** With `spec.ingress`, the edge serves the engine at the object's host under `basePath`, the path
   kept, as an upstream entry (ADR-0138) owned by the object. `privatePaths` under it answer 404 at the edge; funcd
   reaches them at `status.address`.
6. **The image rule.** `addons.registries`, a new funcdconfig.yaml key, lists the registries an Addon's image may come
   from; empty refuses every Addon. funcd pulls a public image, and a private one with the server's existing Docker
   credential store; an Addon names no pull secret.

## Design model

None: an object's engine follows the Lake engine's lifecycle (ADR-0087: converge, replace on a spec change, teardown
on delete), which this ADR does not change, and nothing new races.

## Behavior rows

- **A new chapter, Add-ons** (`docs/behavior/addons.md`), takes `internal/services/addon`, the `addons` config section
  and the kinds `Addon` and `AddonObject`: registering an Addon (admins only, the image rule, 409 while used), applying
  an object (422 on the schema, 404 on an unregistered kind), `Pending` with `AddonNotFound`, `RuntimeUnavailable` and
  `EnvInvalid`, `Ready` on the readiness path, a replaced engine on a change, a private path's 404, `decided`.

## Temporary workarounds

None.

## Contracts

```go
package v1alpha1 // api/types/v1alpha1/addon.go

type Addon struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AddonSpec   `json:"spec"`
	Status     AddonStatus `json:"status,omitempty"`
}

type AddonSpec struct {
	Kind         string            `json:"kind"`
	Image        string            `json:"image"`
	Runtimes     []string          `json:"runtimes,omitempty"`
	Port         int               `json:"port"`
	BasePath     string            `json:"basePath,omitempty"`
	PrivatePaths []string          `json:"privatePaths,omitempty"`
	Readiness    AddonReadiness    `json:"readiness"`
	Storage      *AddonStorage     `json:"storage,omitempty"`
	Config       json.RawMessage   `json:"config,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	SecretKeys   []string          `json:"secretKeys,omitempty"`
	Snapshot     *AddonSnapshot    `json:"snapshot,omitempty"` // ADR-0333
	Restore      *AddonRestore     `json:"restore,omitempty"`  // ADR-0333
}

type AddonObject struct { // stored kind; the API shows it under Spec.Kind
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AddonObjectSpec   `json:"spec"`
	Status     AddonObjectStatus `json:"status,omitempty"`
}

type AddonObjectSpec struct {
	Kind               string              `json:"kind,omitempty"`  // set by the API
	Addon              ObjectName          `json:"addon,omitempty"` // set by the API
	Config             json.RawMessage     `json:"config,omitempty"`
	SecretDeclarations []SecretDeclaration `json:"secrets,omitempty"`
	Ingress            *Ingress            `json:"ingress,omitempty"`
	Resources          ResourceSpec        `json:"resources,omitempty"`
	Deletion           DeletionPolicy      `json:"deletion,omitempty"` // ADR-0333
}

type AddonObjectStatus struct {
	Status  `json:",inline"`
	Address string `json:"address,omitempty"`
}

func AddonPlural(kind string) string // strings.ToLower(kind) + "s"
```

```yaml
apiVersion: funcd.io/v1alpha1
kind: Addon
metadata:
  name: rauthy
spec:
  kind: IdentityProvider
  image: ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5edcb27399e23f8386fecb03b13f909966d2c3c4708095e2c31ccfe2d
  port: 8080
  basePath: /auth/v1
  privatePaths:
    - /auth/v1/clients_dyn
  readiness:
    path: /auth/v1/ping
  storage:
    path: /app/data
    uid: 10001
    gid: 10001
  config:
    type: object
    additionalProperties: false
    properties:
      publicHost:
        type: string
    required:
      - publicHost
  env:
    PUB_URL: ${{ config.publicHost }}
  secretKeys:
    - ENC_KEYS
    - ENC_KEY_ACTIVE
```

| Consumes | Exposes |
|---|---|
| `addons.registries` (funcdconfig.yaml, list, default empty) | `/apis/funcd.io/v1alpha1/addons[/{name}]` (admins) |
| the add-on runtime (ADR-0087), the edge aggregator (ADR-0138) | `/apis/funcd.io/v1alpha1/namespaces/{ns}/{plural}[/{name}]` |
| Cedar's `secret::read` with principal `funcd::AddonObject` | status reasons `AddonNotFound`, `RuntimeUnavailable`, `EnvInvalid`, `EngineNotReady` |

## Implementation plan

- `api/types/v1alpha1/addon.go` (the types above, Validate), `metadata.go` (the two kinds; `Addon`
  cluster-scoped), `internal/controlplane/addon.go` (CRUD, the schema check with `santhosh-tekuri/jsonschema/v6`,
  already a dependency; the generic routes), `internal/services/addon` (the reconciler), `pkg/funcd/addon_provider.go`
  (the provider, beside the Lake's), `internal/platform/config` (`addons.registries`), the Cedar schema
  (`funcd::AddonObject`), `pkg/sdk` (the plural fallback), `internal/edge/router` and `internal/dataplane` (kept path,
  private paths), the OpenAPI golden.
- Start from experiment 0025's spike (`funcd.patch`), rewritten to ADR-0002's conventions.
- Tests, one per Scenario, named after it, in `pkg/funcd` (e2e, `t.Parallel()`, a fake engine image from
  `internal/testkit`) and `internal/services/addon`; unit tests for Validate, the schema check, the env rendering,
  the plural and the private-path match.
- Real platform: a Lima lane case (`addon-rauthy`) registers rauthy from its published image, applies an
  IdentityProvider and gets `/auth/v1/ping` through the edge on containerd.
- Done: `just ci` green, the lane case passes, `just check-behavior` passes.

## Review checklist

- [ ] A developer's Addon is refused 403; an admin's is accepted.
- [ ] An image by tag, or from an unlisted registry, is refused 422.
- [ ] An object breaking the schema is refused 422; an unregistered kind 404.
- [ ] While objects use an Addon, a change of its `kind`, a schema they break and its delete are refused 409.
- [ ] Only `secretKeys` reach the engine, read by Cedar as `funcd::AddonObject`.
- [ ] A private path answers 404 at the edge.
- [ ] The engine runs with ADR-0269's fence and no endpoint.
- [ ] No add-on kind appears in the code, the OpenAPI spec or the Cedar schema.

## Consequences

- Any engine an admin trusts becomes an add-on with a manifest; `pyvvo/funcd-addons` holds the ones funcd ships.
- One name is one add-on object per namespace, whatever its kind.
- An add-on engine reaches nothing outward, so rauthy sends no email until the egress list exists.
- Risk accepted: a registered image runs with the namespace's Secrets that `secretKeys` lists; registering one is
  trusting it, as installing a binary is.

## Open questions

- **An Addon's egress list** (rauthy's SMTP, upstream identity providers): a follow-up ADR of FEAT-0018.
- **The default of `addons.registries`**: empty (nothing allowed until listed) or `ghcr.io/pyvvo`: the decider, at
  acceptance.

## References

- Feature issue [#1497](https://github.com/pyvvo/funcd/issues/1497) (FEAT-0018 F258), tracker
  [#1496](https://github.com/pyvvo/funcd/issues/1496).
- Experiment 0025 (pyvvo/experiment, folder `0025-addons-rauthy`): rauthy and whoami registered and run from
  manifests alone, the schema refusal, the private path and `RuntimeUnavailable`.
- The decider's cards 1 and 2 (2026-11-19).
