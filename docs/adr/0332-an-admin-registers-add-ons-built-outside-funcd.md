# ADR-0332: An admin registers add-ons built outside funcd, and users declare their objects

- **Status**: Accepted (2026-11-19), by the decider's standing go-ahead for FEAT-0018 ("go ahead, don't wait for
  me", "until we ship entirely the new feature"), after three rounds of an independent judge (a subagent), every
  Blocker and Major folded
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, provider, api, edge, cedar
- **Realizes**: [FEAT-0018/F258](../feat/0018-feat-addons.md) (add-ons built outside funcd)
- **Relates to**: [ADR-0087](0087-add-on-provider-runtime.md) (the add-on runtime it runs engines on),
  [ADR-0231](0231-durable-storage-for-add-on-engines.md) (engine storage),
  [ADR-0095](0095-reference-engine-typed-paths-predicates.md) (`${{ }}` expressions),
  [ADR-0121](0121-declarative-referential-integrity-admission.md) (an object waits for its referent),
  [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) (upstream edge entries),
  [ADR-0149](0149-runtime-availability.md) (`RuntimeUnavailable`),
  [ADR-0269](0269-a-provider-engine-reaches-only-its-one-internal-endpoint.md) (the engine's fence, whose
  `FenceNetns` this ADR extends with an input chain),
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
- `scenario: addon-image-pinned`. *When* an admin applies an Addon whose image is a tag
  (`ghcr.io/pyvvo/funcd-rauthy:0.1.1`) or on a registry `addons.registries` does not list, *Then* it is refused 422.
- `scenario: addon-env-checked`. *When* an admin applies an Addon whose `env` names a property its schema lacks,
  *Then* it is refused 422.
- `scenario: addon-object-runs`. *Given* the Addon and a Secret `rauthy-keys`, *When* a user applies the
  IdentityProvider `login`, *Then* it turns `Ready` once the engine's readiness path answers 200, and `GET
  https://login.example.com/auth/v1/ping` reaches the engine with its path kept.
- `scenario: addon-kind-unregistered`. *When* a user applies an object of a kind no Addon registers, *Then* it is
  refused 404.
- `scenario: addon-config-schema`. *When* a user applies an object whose `spec.config` breaks the schema, *Then* it is
  refused 422 naming the property.
- `scenario: addon-config-invalid`. *Given* `login` `Ready`, *When* an admin applies the Addon with a schema that
  `login`'s config breaks, *Then* the change is stored, and `login` waits `Pending`, `ConfigInvalid`, with no engine.
- `scenario: addon-removed`. *Given* `login` `Ready`, *When* an admin deletes the Addon, *Then* `login`'s engine stops,
  `login` waits `Pending`, `AddonNotFound`, and `funcdctl get` and `delete` still serve it; *When* an admin registers
  an Addon of kind `IdentityProvider` again, under any name, *Then* `login` turns `Ready`, and an Addon of another kind
  under the old name takes no object.
- `scenario: addon-private-path`. *When* a caller sends `POST /auth/v1/clients_dyn`, also spelled with `//` or `/./`,
  to the object's host, *Then* the edge answers 404, and funcd still reaches the path at the engine's own address;
  *When* a path holds a `..` segment, or its cleaned form leaves `/auth/v1`, *Then* the edge answers 404.
- `scenario: addon-runtime-unavailable`. *Given* `runtime.mode: process`, *When* a user applies `login`, *Then* it
  waits `Pending`, `RuntimeUnavailable`, naming both modes; *When* an admin applies an Addon whose `runtimes` lists
  `process`, *Then* it is refused 422.
- `scenario: addon-secret-keys`. *When* `login` runs and `rauthy-keys` holds a key `secretKeys` does not list, *Then*
  the engine's environment holds only the listed keys; *When* the Secret changes, *Then* the engine is replaced.
- `scenario: addon-secret-missing`. *When* a user applies `login` bound to a Secret that does not exist, *Then* it
  waits `Pending`, `SecretNotFound`, with no engine; *When* the Secret is applied, *Then* it turns `Ready`.
- `scenario: addon-second-kind`. *Given* the Addon `whoami` (kind `EchoService`, no storage, no config), *When* a user
  applies an EchoService beside `login`, *Then* it runs with no code for its kind.

## Scope

In: the `Addon` kind; objects of an add-on's kind (API, store, SDK, funcdctl) and their count per namespace; running
their engines; their Secrets; their edge entry; the image rule. Out: an add-on's backups (F259,
[ADR-0333](0333-an-add-on-objects-data-in-funcds-backups.md)), `funcdctl dev` (F260,
[ADR-0334](0334-funcdctl-dev-runs-add-on-images.md)), the edge sign-in (F261, F262), the `pyvvo/funcd-addons`
repository's layout, an add-on's egress list (Open questions), and add-ons whose code runs inside funcd (FEAT-0018
Out of scope).

## Constraints & Decision drivers

- One provider runtime: an add-on engine runs exactly as a Lake engine does (ADR-0087, ADR-0231), so the stop path,
  the boot sweep and the storage apply unchanged.
- funcd's set of kinds stays fixed: the code, the OpenAPI spec and the Cedar schema name no add-on's kind.
- Only an admin adds code to a server, and only from registries the operator lists.
- A user's object is checked at apply, as a contract is (ADR-0058's JSON Schema profile).
- ADR-0087 rejected a generic user-facing kind for its lack of per-kind validation and as "a raw 'run this image'
  resource". This conforms: only an admin's Addon names an image, with the schema it lacked, and users get typed kinds.

## Alternatives considered

- **A kind per add-on compiled into funcd** (today's Lake and SQL). Rejected: it needs a funcd release per engine,
  which is the problem.
- **A generic `AddonInstance` kind with `spec.addon`**. Simpler to route, but a user writes `kind: AddonInstance` and
  `funcdctl get` cannot list identity providers by name; the decider chose the add-on's own kind (card 1, B).
- **A `RuntimeClass` per add-on** to name its runtime. Rejected: a RuntimeClass maps a workload runtime to its image
  (ADR-0003, ADR-0032, ADR-0149), while the driver is the server-wide `runtime.mode` (card 1).
- **Kubernetes-style CRDs with a validating webhook**. Rejected: a webhook is code outside funcd that admission must
  call, and the schema alone covers what rauthy needs.
- **A lock per Addon, and a 409 while objects use it**. Rejected: an Addon and its objects live in different
  namespaces, which ADR-0147's admission never locks together; funcd holds an object whose referent is gone instead.
- **Images by tag, or from any registry**. Rejected: a tag can move under a running server, and an admin's typo would
  pull from a registry nobody vetted (card 2).

## Decision

1. **The `Addon` kind**, cluster-scoped; only an admin creates, changes, deletes or reads one. Its spec names the kind
   it adds (`^[A-Z][A-Za-z0-9]{1,62}$`) and its engine: `image`, pinned by digest (`<repository>@sha256:<64 hex>`);
   `runtimes`, the `runtime.mode` values it runs on, of which only `containerd` (the default) is admitted, since the
   process driver runs no image; `port`; `basePath`; `privatePaths`, each under `basePath` (checked at apply);
   `readiness.path`; `storage` (path, uid, gid, as ADR-0231); `config`, its objects' JSON Schema, which must pass
   `internal/contract`'s profile gate (`Check`) and compile (`Compile`, ADR-0058); `env`; and `secretKeys`. An Addon is
   refused when its kind is funcd's, or its plural (the kind in lower case with an `s`) is funcd's (`pkg/sdk/kinds.go`)
   or another Addon's, kinds compared without case. Its `kind` cannot change (422). A change or a delete takes no lock
   and checks no object: a new schema applies to the next writes, and Decision 3 holds an object its Addon no longer
   fits.
2. **Its objects** are namespaced, at `/apis/funcd.io/v1alpha1/namespaces/{ns}/{plural}[/{name}]` with a namespaced
   kind's verbs (list, get, create, replace, delete; `dryRun`, `If-Match`), and stored as the internal kind
   `AddonObject` (`spec.kind`): one name is one object per namespace across add-on kinds. An object belongs to the Addon
   that registers its `spec.kind`, whatever that Addon's name. The spec holds
   `config`, `bindings`, `ingress`, `resources` and `deletion` (ADR-0333). The API refuses a kind no Addon registers
   (404) and a config that breaks the schema (422). A create past `addons.maxObjectsPerNamespace` (new; 0 means 20, a
   negative value no cap) gets 400, as SQLDatabases, each also an engine, are capped by `sql.maxDatabasesPerNamespace`
   (ADR-0232 Decision 8, a marked admission under ADR-0147 Decision 1). While no Addon registers a kind, list, get and
   delete still serve its stored objects, and create and replace get 404. funcdctl and the SDK resolve an unknown kind
   to its plural, with no code per kind.
3. **The engine.** One reconciler runs every object through the add-on runtime (ADR-0087), one replica, with the
   Addon's image, port, readiness and storage and the object's resources. It reads the Addon of the object's kind on each pass and
   watches Addons (`controller.Controller.Watches`). An object waits `Pending` with no engine, as a Lake waits for its
   Bucket (ADR-0121 Decision 2): `AddonNotFound` while its Addon is gone, `ConfigInvalid` while its config breaks the
   schema, and `RuntimeUnavailable` (ADR-0149's reason, with which a Function ends `Failed`) while `runtimes` lacks
   the add-on runtime's mode: `containerd` on a containerd server and under `funcdctl dev` (ADR-0334), none on a
   process server. The engine is fenced with no endpoint (ADR-0269, `provider.EgressTo("")`) until an egress-list
   ADR. On containerd, its `funcd_fence` table gains an `input` chain that admits only loopback and funcd's bridge
   address ([ADR-0238](0238-container-bridge-address.md)): only funcd reaches the engine's port, and no funcd proxy that
   acts for a workload, the egress gateway included (ADR-0317), connects to an add-on engine's address. The fence is
   containerd's; ADR-0334 states what dev does not fence. `status.address` is the engine's own address.
4. **The environment.** Each `env` value is a literal or one `${{ }}` expression over `config` (ADR-0095),
   type-checked against the schema when the Addon is applied (422) and evaluated per object, a non-string result as
   its JSON: a failure holds the object `Pending`, `EnvInvalid`. An object binds Secrets by name in
   `spec.bindings.secret`, as a Lake does (ADR-0246), and only the keys `secretKeys` lists reach the environment. Cedar
   decides each read with the object as the principal (`funcd::AddonObject`), as for a Lake: a missing Secret holds the
   object `Pending`, `SecretNotFound`, and a forbidden one `BindingResolveFailed` (Decision 5 of ADR-0251 and of
   ADR-0321). A changed Secret, Addon or object replaces the engine.
5. **The edge.** With `spec.ingress`, the edge serves the engine at the object's host under `basePath`, as an upstream
   entry (ADR-0138) owned by the object and ranked after the Lake's (ADR-0176 Decision 2); `ingress.path` is refused,
   since the entry serves `basePath`. Its stance is its own `ingress.auth`, `open` when unset: an upstream entry takes
   no namespace default and runs no edge PEP (ADR-0138, `internal/dataplane/dataplane.go`), so `ingress.auth` takes
   `open` or `oidc` (ADR-0335) and `authenticated` is refused (422). The edge answers 404 to a path with a `..` segment,
   as the static backend does (`internal/edge/static/static.go`), cleans the rest (`path.Clean`), answers 404 when the
   cleaned path falls outside `basePath` or under a private path (`pathx.HasPrefix`), and forwards the cleaned path;
   funcd reaches a private path at `status.address`.
6. **The image rule.** `addons.registries`, a new funcdconfig.yaml key, lists the registries an Addon's image may come
   from: a registry is the image's host after `docker.io` normalization (`internal/runtime/containerd/image_ref.go`),
   and the empty default refuses every Addon. It is checked when an Addon is applied, so a registry removed from it
   stops no engine and refuses that Addon's next change. funcd pulls a public image, and a private one with the
   server's Docker credential store; an Addon names no pull secret.

## Design model

None: no lock and no rule spans an Addon and its objects at admission. The reconciler reads each object's Addon on
every pass and holds what no longer fits (ADR-0121 Decision 2), so any order of Addon and object writes ends in the
state the last writes describe. The engine's lifecycle is ADR-0087's, unchanged, and a change replaces the engine
through the spec fingerprint of #1195.

## Behavior rows

- **A new chapter, Add-ons** (`docs/behavior/addons.md`: `internal/services/addon`, the `addons` config section, the
  kinds `Addon` and `AddonObject`), `decided`: the refusals and waits of Decisions 1 to 6, a replaced engine, a
  private path's 404, the entry's stance from its own `ingress.auth` only, and only funcd reaching the engine's port.
- **limits.md**: the add-on objects in a namespace (`addons.maxObjectsPerNamespace` 20, 400), `decided`.

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

type AddonReadiness struct {
	Path string `json:"path"`
}

type AddonStorage struct { // ADR-0231
	Path string `json:"path"`
	UID  int    `json:"uid,omitempty"`
	GID  int    `json:"gid,omitempty"`
}

// AddonStatus is the shared Status only, as RuntimeClassStatus is: each object reports its own state.
type AddonStatus struct {
	Status `json:",inline"`
}

type AddonObject struct { // stored kind; the API shows it under Spec.Kind
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AddonObjectSpec   `json:"spec"`
	Status     AddonObjectStatus `json:"status,omitempty"`
}

type AddonObjectSpec struct {
	Kind      string              `json:"kind,omitempty"`  // set by the API
	Config    json.RawMessage     `json:"config,omitempty"`
	Bindings  AddonObjectBindings `json:"bindings,omitempty"`
	Ingress   *Ingress            `json:"ingress,omitempty"`
	Resources ResourceSpec        `json:"resources,omitempty"`
	Deletion  DeletionPolicy      `json:"deletion,omitempty"` // ADR-0333
}

type AddonObjectBindings struct {
	Secret []ObjectName `json:"secret,omitempty"` // as LakeBindings.Secret
}

type AddonObjectStatus struct {
	Status  `json:",inline"`
	Address string `json:"address,omitempty"`
}

func AddonPlural(kind string) string // strings.ToLower(kind) + "s"
```

```go
package provider // internal/provider/spec.go: the inbound fence beside ADR-0269's EgressOnly

type ProviderSpec struct {
	// ...the existing fields
	// HostOnly admits new connections to the engine only from loopback and funcd's bridge address; copied to the
	// WorkerSpec, as EgressOnly is.
	HostOnly bool
}
```

```go
package network // internal/network/fence_linux.go: funcd_fence gains the input chain when admit is non-nil

func FenceNetns(netnsPath string, allow []netip.AddrPort, admit []netip.Addr) error
```

rauthy's Addon, the worked example, is [`rauthy/addon.yaml`](https://github.com/pyvvo/funcd-addons/blob/main/rauthy/addon.yaml)
in pyvvo/funcd-addons.


| Consumes | Exposes |
|---|---|
| funcdconfig.yaml: `addons.registries` (list, default empty), `addons.maxObjectsPerNamespace` (default 20) | `/apis/funcd.io/v1alpha1/addons[/{name}]` (admins) |
| the add-on runtime (ADR-0087), the edge aggregator (ADR-0138, ADR-0176), `internal/contract` (ADR-0058) | `/apis/funcd.io/v1alpha1/namespaces/{ns}/{plural}[/{name}]` |
| Cedar's `secret::read` with principal `funcd::AddonObject` | reasons `AddonNotFound`, `ConfigInvalid`, `RuntimeUnavailable`, `EnvInvalid`, `SecretNotFound`, `BindingResolveFailed`, `EngineNotReady` |

## Implementation plan

- `api/types/v1alpha1/addon.go` (the types, Validate), `metadata.go` (`Addon` cluster-scoped),
  `internal/controlplane/addon.go` (the generic routes, the kind rules, the schema through `internal/contract`, the
  cap as a marked admission), `internal/services/addon` (the reconciler), `pkg/funcd/addon_provider.go`,
  `internal/platform/config` (the two keys), the Cedar schema, `pkg/sdk` (the plural fallback),
  `internal/edge/router/aggregator.go` (the rank), `internal/dataplane` (the paths), `internal/network` (the `input`
  chain), the OpenAPI golden.
- `internal/runtime/containerd`: `Config.Pullable` admits an Addon's image, and the pull reads the Docker credential
  store that `internal/artifact` reads (`containerd_linux.go` pulls with none today).
- Start from experiment 0025's spike (`funcd.patch`), rewritten to ADR-0002's conventions.
- Tests, one per Scenario, named after it, in `pkg/funcd` (e2e, `t.Parallel()`, a fake `provider.Runtime` over an
  httptest engine, as `funcd.WithSQLProviderRuntime` takes fakesql's) and `internal/services/addon`; unit tests for
  Validate, the schema check, the env rendering, the plural rules, the cap and the private-path match.
- Real platform: a Lima lane case (`addon-rauthy`) registers rauthy from its published image, applies an
  IdentityProvider and gets `/auth/v1/ping` through the edge on containerd.
- Done: `just ci` green, the lane case passes, `just check-behavior` passes.

## Review checklist

- [ ] Each Scenario has a passing test named after it.
- [ ] A kind that is funcd's, or whose plural is funcd's or another Addon's, is refused without case; `kind` is kept.
- [ ] A create past `addons.maxObjectsPerNamespace` gets 400, through a marked admission.
- [ ] `ingress.auth: authenticated` and `ingress.path` are refused; a namespace's `edgeDefaults.auth` does not reach
      the entry.
- [ ] Only the `secretKeys` of `spec.bindings.secret` reach the engine, read by Cedar as `funcd::AddonObject`.
- [ ] The engine runs with ADR-0269's fence, no endpoint, and an `input` chain that admits only loopback and funcd's
      bridge address.
- [ ] A registry removed from `addons.registries` stops no engine.
- [ ] No add-on kind appears in the code, the OpenAPI spec or the Cedar schema.

## Consequences

- Any engine an admin trusts becomes an add-on with a manifest; `pyvvo/funcd-addons` holds the ones funcd ships.
- One name is one add-on object per namespace, whatever its kind.
- An Addon change replaces each of its objects' engines once, in every namespace, with no staged rollout; a schema
  change stops every engine whose object's config breaks it, until the object or the schema changes.
- An add-on engine reaches nothing outward, so rauthy sends no email until the egress list exists.
- Addons are admin-only to read, like every cluster-scoped kind: a developer learns a kind's schema from the 422.
- Experiment 0025 ran under `funcdctl dev` on Docker only, so the lane case first shows the containerd path.
- At acceptance, the blueprint's *Add-on providers*, "reused by per-provider CRDs", gains the Addon.
- Risk accepted: a registered image runs with the namespace's Secrets that `secretKeys` lists; registering one is
  trusting it, as installing a binary is.

## Open questions

- **An Addon's egress list** (rauthy's SMTP, upstream identity providers): a follow-up ADR of FEAT-0018.

## References

- Feature issue [#1497](https://github.com/pyvvo/funcd/issues/1497) (FEAT-0018 F258), tracker
  [#1496](https://github.com/pyvvo/funcd/issues/1496).
- Experiment 0025 (pyvvo/experiment at a0643b3c, folder `0025-addons-rauthy`): rauthy and whoami registered and run
  from manifests alone, the schema refusal, the private path and `RuntimeUnavailable`.
- The decider's cards 1 and 2 (2026-11-19).
